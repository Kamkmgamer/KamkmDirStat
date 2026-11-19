// Package monitor samples the activity of this computer or of a server over SSH, about
// once a second, and carries out requests to end processes and free the cache.
package monitor

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/history"
	"github.com/Kamkmgamer/KamkmDirStat/internal/wire"
	"golang.org/x/crypto/ssh"
)

//go:embed remote_monitor.py
var remoteMonitorScript string

// The monitor runs remote_monitor.py over its own SSH connection. The script sends a hello
// message, then a sample about once a second, and replies to actions with result messages.
// Samples pass through to the page unchanged.

type Status struct {
	Running bool `json:"running"`
	// Machine is the history key of the monitored computer: "local", or user@host:port.
	Machine    string `json:"machine,omitempty"`
	Connecting bool   `json:"connecting,omitempty"`
	// Reconnecting is set while the monitor replaces a connection that stalled or dropped.
	Reconnecting bool              `json:"reconnecting,omitempty"`
	Error        string            `json:"error,omitempty"`
	Hello        json.RawMessage   `json:"hello,omitempty"`
	Sample       json.RawMessage   `json:"sample,omitempty"`
	Seq          int64             `json:"seq"`
	Results      []json.RawMessage `json:"results,omitempty"`
}

type Action struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	PID      int    `json:"pid,omitempty"`
	Start    int64  `json:"start,omitempty"`
	Signal   string `json:"signal,omitempty"`
	Password string `json:"password,omitempty"`
}

// Session monitors one computer at a time. The zero value is ready to use.
type Session struct {
	mu         sync.Mutex
	status     Status
	generation int
	cancel     context.CancelFunc
	writes     chan []byte
}

// Each computer has its own monitor. Selecting another computer in the page never stops
// the first one, and samples continue into each computer's own status and history.

var (
	localMonitorRun  = runLocalMonitor
	remoteMonitorRun = runMonitor
)

const maxMonitorResults = 20

// firstMonitorSample is how long the samplers measure before their first sample. It is
// shorter than the usual second so the page shows figures almost as soon as it opens.
const firstMonitorSample = 250 * time.Millisecond

// Target is the computer to monitor.
type Target struct {
	// Machine is the computer's history key: history.Local, or history.Remote for a server.
	Machine string
	// Connect opens the SSH connection to a server. It is nil for this computer.
	Connect func(ctx context.Context) (*ssh.Client, error)
}

// Start monitors t, replacing whatever the session was monitoring.
func (state *Session) Start(t Target) error {
	state.mu.Lock()
	if state.cancel != nil {
		state.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.generation++
	gen := state.generation
	writes := make(chan []byte, 16)
	state.cancel = cancel
	state.writes = writes
	state.status = Status{Running: true, Connecting: true, Machine: t.Machine}
	state.mu.Unlock()
	go func() {
		update := func(update func(*Status)) bool {
			state.mu.Lock()
			defer state.mu.Unlock()
			if state.generation != gen {
				return false
			}
			update(&state.status)
			return true
		}
		run := func(ctx context.Context, update func(func(*Status)) bool) error {
			if t.Connect == nil {
				return localMonitorRun(ctx, writes, update)
			}
			return remoteMonitorRun(ctx, t, writes, update)
		}
		err := superviseMonitor(ctx, run, update)
		cancel()
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.generation != gen {
			return
		}
		state.cancel = nil
		state.writes = nil
		state.status.Running = false
		state.status.Connecting = false
		state.status.Reconnecting = false
		if err != nil && !errors.Is(err, context.Canceled) {
			state.status.Error = err.Error()
		}
	}()
	return nil
}

// A busy machine can delay samples, or a connection can stop delivering them without
// failing. These limits decide when a silent monitor is replaced by a new connection.
var (
	monitorStallTimeout   = 15 * time.Second // after the hello, samples arrive about once a second
	monitorConnectTimeout = 45 * time.Second // connecting, authenticating and starting the sampler
	monitorRetryDelays    = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second}
)

// superviseMonitor runs the monitor and, once it has connected, starts it again whenever it
// stalls or its connection drops, so a heavily loaded machine does not leave the view frozen.
// Errors before the first connection, such as missing Python, are returned as before.
func superviseMonitor(ctx context.Context, run func(context.Context, func(func(*Status)) bool) error,
	update func(func(*Status)) bool) error {
	connected := false
	for failures := 0; ; failures++ {
		attempt, cancel := context.WithCancel(ctx)
		var last atomic.Int64
		var hello, stalled atomic.Bool
		last.Store(time.Now().UnixNano())
		// Updates from an abandoned attempt, which may still be finishing a sample, are dropped.
		attemptUpdate := func(f func(*Status)) bool {
			if attempt.Err() != nil {
				return false
			}
			last.Store(time.Now().UnixNano())
			return update(func(s *Status) {
				f(s)
				if !s.Connecting {
					hello.Store(true)
					s.Reconnecting = false
				}
			})
		}
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-attempt.Done():
					return
				case <-ticker.C:
					limit := monitorConnectTimeout
					if hello.Load() {
						limit = monitorStallTimeout
					}
					if time.Since(time.Unix(0, last.Load())) > limit {
						stalled.Store(true)
						cancel()
						return
					}
				}
			}
		}()
		err := run(attempt, attemptUpdate)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if hello.Load() {
			connected = true
			failures = 0
		}
		if !connected {
			if stalled.Load() {
				return errors.New("The monitor did not respond in time.")
			}
			return err
		}
		if errors.Is(err, context.Canceled) && !stalled.Load() {
			return err // superseded by a newer monitor
		}
		if !update(func(s *Status) { s.Connecting = true; s.Reconnecting = true }) {
			return context.Canceled
		}
		delay := monitorRetryDelays[min(failures, len(monitorRetryDelays)-1)]
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// Stop ends the monitoring.
func (state *Session) Stop() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cancel != nil {
		state.cancel()
	}
	state.generation++
	state.cancel = nil
	state.writes = nil
	state.status.Running = false
	state.status.Connecting = false
	state.status.Reconnecting = false
	return nil
}

// recordSample records a sample in its machine's history, then publishes it, so the
// history already holds a sample by the time the page polls it.
func recordSample(machine string, raw json.RawMessage) func(*Status) {
	history.Record(machine, raw)
	return func(s *Status) { s.Sample = raw; s.Seq++ }
}

// Status is the latest the session knows: its connection, the newest sample and the
// results of recent actions.
func (state *Session) Status() Status {
	state.mu.Lock()
	defer state.mu.Unlock()
	s := state.status
	s.Results = append([]json.RawMessage(nil), s.Results...)
	return s
}

func addMonitorResult(s *Status, raw json.RawMessage) {
	s.Results = append(s.Results, raw)
	if len(s.Results) > maxMonitorResults {
		s.Results = s.Results[len(s.Results)-maxMonitorResults:]
	}
}

func validateMonitorAction(a Action) error {
	switch a.Type {
	case "kill":
		if a.PID <= 1 || a.Start < 0 || (a.Signal != "TERM" && a.Signal != "KILL") {
			return errors.New("Invalid request to end a process.")
		}
	case "dropcaches":
		if a.PID != 0 || a.Signal != "" {
			return errors.New("Invalid request to free the cache.")
		}
	default:
		return errors.New("Unknown monitor action.")
	}
	if len(a.Password) > 1024 {
		return errors.New("The password is too long.")
	}
	return nil
}

// Action queues a request for the monitored computer. Its outcome arrives later as a
// result with the same ID in Status.
func (state *Session) Action(a Action) error {
	if err := validateMonitorAction(a); err != nil {
		return err
	}
	line, err := json.Marshal(a)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.writes == nil || state.status.Connecting {
		return errors.New("The monitor is not connected.")
	}
	select {
	case state.writes <- append(line, '\n'):
		return nil
	default:
		return errors.New("The server is still handling earlier requests. Try again shortly.")
	}
}

func runMonitor(ctx context.Context, t Target, writes <-chan []byte, update func(func(*Status)) bool) error {
	client, err := t.Connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClose()
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open monitor session: %w", err)
	}
	defer session.Close()
	input, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	session.Stderr = io.Discard
	command := "if command -v python3 >/dev/null 2>&1; then exec python3 -u -c " + wire.ShellQuote(remoteMonitorScript) + "; else printf 'SSM1 unavailable\\n'; fi"
	if err := session.Start(command); err != nil {
		return fmt.Errorf("The server does not allow running commands, which monitoring needs: %w", err)
	}
	return monitorProtocolMode(ctx, input, stdout, func() { _ = client.Close() }, "the server", writes, update, t.Machine)
}

// monitorProtocol talks to remote_monitor.py over its standard input and output, whether
// it runs on a server or, on Linux, on this computer. abort unblocks stalled reads.
func monitorProtocol(ctx context.Context, input io.Writer, stdout io.Reader, abort func(), where string,
	writes <-chan []byte, update func(func(*Status)) bool) error {
	return monitorProtocolMode(ctx, input, stdout, abort, where, writes, update, "")
}

// monitorProtocolMode records samples in the history of machine, unless machine is empty.
func monitorProtocolMode(ctx context.Context, input io.Writer, stdout io.Reader, abort func(), where string,
	writes <-chan []byte, update func(func(*Status)) bool, machine string) error {
	output := bufio.NewReaderSize(stdout, 64<<10)
	header := make(chan string, 1)
	go func() { header <- wire.Header(output, "SSM1 ") }()
	timer := time.NewTimer(wire.HeaderTimeout)
	defer timer.Stop()
	select {
	case line := <-header:
		switch line {
		case "gzip":
		case "unsupported":
			return fmt.Errorf("Monitoring needs Linux; %s has no /proc file system.", where)
		default:
			return fmt.Errorf("Monitoring needs Python 3 on %s.", where)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("The monitor did not start on %s in time.", where)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-writes:
				if _, err := input.Write(line); err != nil {
					abort()
					return
				}
			}
		}
	}()

	for {
		data, _, err := wire.Read(output)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("The monitor stopped unexpectedly: %w", err)
		}
		var head struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			return errors.New("The monitor sent an invalid message.")
		}
		raw := json.RawMessage(data)
		current := true
		switch head.Type {
		case "hello":
			current = update(func(s *Status) { s.Hello = raw; s.Connecting = false })
		case "sample":
			publish := func(s *Status) { s.Sample = raw; s.Seq++ }
			if machine != "" {
				publish = recordSample(machine, raw)
			}
			current = update(publish)
		case "result":
			current = update(func(s *Status) { addMonitorResult(s, raw) })
		case "error":
			return errors.New(head.Error)
		default:
			return errors.New("The monitor sent an unknown message.")
		}
		if !current {
			return context.Canceled
		}
	}
}
