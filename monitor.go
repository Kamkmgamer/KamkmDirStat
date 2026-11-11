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
	"time"
)

//go:embed remote_monitor.py
var remoteMonitorScript string

// The monitor runs remote_monitor.py over its own SSH connection. The script sends a hello
// message, then a sample about once a second, and replies to actions with result messages.
// Samples pass through to the page unchanged.

type MonitorStatus struct {
	Running    bool              `json:"running"`
	Connecting bool              `json:"connecting,omitempty"`
	Error      string            `json:"error,omitempty"`
	Hello      json.RawMessage   `json:"hello,omitempty"`
	Sample     json.RawMessage   `json:"sample,omitempty"`
	Seq        int64             `json:"seq"`
	Results    []json.RawMessage `json:"results,omitempty"`
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
		var err error
		if c.Local {
			err = runLocalMonitor(ctx, writes, update)
		} else {
			err = runMonitor(ctx, c, writes, update)
		}
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
		if err != nil && !errors.Is(err, context.Canceled) {
			monitorState.status.Error = err.Error()
		}
	}()
	return nil
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
			return errors.New("Monitoring needs a Linux server; this one has no /proc file system.")
		default:
			return errors.New("Monitoring needs Python 3 on the server.")
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("The server did not start the monitor in time.")
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-writes:
				if _, err := input.Write(line); err != nil {
					_ = client.Close()
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
			return fmt.Errorf("The monitor connection closed: %w", err)
		}
		var head struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			return errors.New("The server sent an invalid monitor message.")
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
			return errors.New("The server sent an unknown monitor message.")
		}
		if !current {
			return context.Canceled
		}
	}
}
