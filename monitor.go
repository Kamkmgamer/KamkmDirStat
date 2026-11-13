package main

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
)

//go:embed remote_monitor.py
var remoteMonitorScript string

// The monitor runs remote_monitor.py over its own SSH connection. The script sends a hello
// message, then a sample about once a second, and replies to actions with result messages.
// Samples pass through to the page unchanged.

type MonitorStatus struct {
	Running    bool `json:"running"`
	Connecting bool `json:"connecting,omitempty"`
	// Reconnecting is set while the monitor replaces a connection that stalled or dropped.
	Reconnecting bool              `json:"reconnecting,omitempty"`
	Error        string            `json:"error,omitempty"`
	Hello        json.RawMessage   `json:"hello,omitempty"`
	Sample       json.RawMessage   `json:"sample,omitempty"`
	Seq          int64             `json:"seq"`
	Results      []json.RawMessage `json:"results,omitempty"`
}

type MonitorAction struct {
	ID       int    `json:"id"`
	Type     string `json:"type"`
	PID      int    `json:"pid,omitempty"`
	Start    int64  `json:"start,omitempty"`
	Signal   string `json:"signal,omitempty"`
	Password string `json:"password,omitempty"`
}

var monitorState struct {
	sync.Mutex
	status     MonitorStatus
	generation int
	cancel     context.CancelFunc
	writes     chan []byte
}

const maxMonitorResults = 20

// firstMonitorSample is how long the samplers measure before their first sample. It is
// shorter than the usual second so the page shows figures almost as soon as it opens.
const firstMonitorSample = 250 * time.Millisecond

func startMonitor(c Connection) error {
	monitorState.Lock()
	if monitorState.cancel != nil {
		monitorState.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	monitorState.generation++
	gen := monitorState.generation
	writes := make(chan []byte, 16)
	monitorState.cancel = cancel
	monitorState.writes = writes
	monitorState.status = MonitorStatus{Running: true, Connecting: true}
	monitorState.Unlock()
	go func() {
		update := func(update func(*MonitorStatus)) bool {
			monitorState.Lock()
			defer monitorState.Unlock()
			if monitorState.generation != gen {
				return false
			}
			update(&monitorState.status)
			return true
		}
		run := func(ctx context.Context, update func(func(*MonitorStatus)) bool) error {
			if c.Local {
				return runLocalMonitor(ctx, writes, update)
			}
			return runMonitor(ctx, c, writes, update)
		}
		err := superviseMonitor(ctx, run, update)
		cancel()
		monitorState.Lock()
		defer monitorState.Unlock()
		if monitorState.generation != gen {
			return
		}
		monitorState.cancel = nil
		monitorState.writes = nil
		monitorState.status.Running = false
		monitorState.status.Connecting = false
		monitorState.status.Reconnecting = false
		if err != nil && !errors.Is(err, context.Canceled) {
			monitorState.status.Error = err.Error()
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
func superviseMonitor(ctx context.Context, run func(context.Context, func(func(*MonitorStatus)) bool) error,
	update func(func(*MonitorStatus)) bool) error {
	connected := false
	for failures := 0; ; failures++ {
		attempt, cancel := context.WithCancel(ctx)
		var last atomic.Int64
		var hello, stalled atomic.Bool
		last.Store(time.Now().UnixNano())
		// Updates from an abandoned attempt, which may still be finishing a sample, are dropped.
		attemptUpdate := func(f func(*MonitorStatus)) bool {
			if attempt.Err() != nil {
				return false
			}
			last.Store(time.Now().UnixNano())
			return update(func(s *MonitorStatus) {
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
		if !update(func(s *MonitorStatus) { s.Connecting = true; s.Reconnecting = true }) {
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

func stopMonitor() error {
	monitorState.Lock()
	defer monitorState.Unlock()
	if monitorState.cancel != nil {
		monitorState.cancel()
	}
	monitorState.generation++
	monitorState.cancel = nil
	monitorState.writes = nil
	monitorState.status.Running = false
	monitorState.status.Connecting = false
	monitorState.status.Reconnecting = false
	return nil
}

func monitorStatus() MonitorStatus {
	monitorState.Lock()
	defer monitorState.Unlock()
	s := monitorState.status
	s.Results = append([]json.RawMessage(nil), s.Results...)
	return s
}

func addMonitorResult(s *MonitorStatus, raw json.RawMessage) {
	s.Results = append(s.Results, raw)
	if len(s.Results) > maxMonitorResults {
		s.Results = s.Results[len(s.Results)-maxMonitorResults:]
	}
}

func validateMonitorAction(a MonitorAction) error {
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

// monitorAction queues a request for the server. Its outcome arrives later as a result with
// the same ID in monitorStatus.
func monitorAction(a MonitorAction) error {
	if err := validateMonitorAction(a); err != nil {
		return err
	}
	line, err := json.Marshal(a)
	if err != nil {
		return err
	}
	monitorState.Lock()
	defer monitorState.Unlock()
	if monitorState.writes == nil || monitorState.status.Connecting {
		return errors.New("The monitor is not connected.")
	}
	select {
	case monitorState.writes <- append(line, '\n'):
		return nil
	default:
		return errors.New("The server is still handling earlier requests. Try again shortly.")
	}
}

func runMonitor(ctx context.Context, c Connection, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
	client, err := connectVerified(ctx, c)
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
	command := "if command -v python3 >/dev/null 2>&1; then exec python3 -u -c " + shellQuote(remoteMonitorScript) + "; else printf 'SSM1 unavailable\\n'; fi"
	if err := session.Start(command); err != nil {
		return fmt.Errorf("The server does not allow running commands, which monitoring needs: %w", err)
	}
	return monitorProtocol(ctx, input, stdout, func() { _ = client.Close() }, "the server", writes, update)
}

// monitorProtocol talks to remote_monitor.py over its standard input and output, whether
// it runs on a server or, on Linux, on this computer. abort unblocks stalled reads.
func monitorProtocol(ctx context.Context, input io.Writer, stdout io.Reader, abort func(), where string,
	writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
	output := bufio.NewReaderSize(stdout, 64<<10)
	header := make(chan string, 1)
	go func() { header <- readScriptHeader(output, "SSM1 ") }()
	timer := time.NewTimer(shellHeaderTimeout)
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
		data, _, err := readFrame(output)
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
			current = update(func(s *MonitorStatus) { s.Hello = raw; s.Connecting = false })
		case "sample":
			current = update(func(s *MonitorStatus) { s.Sample = raw; s.Seq++ })
		case "result":
			current = update(func(s *MonitorStatus) { addMonitorResult(s, raw) })
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
