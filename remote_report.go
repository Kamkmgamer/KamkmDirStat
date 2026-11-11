package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

//go:embed remote_report.py
var remoteReportScript string

type reportMessage struct {
	wireBytes  int64
	Type       string       `json:"type"`
	Error      string       `json:"error"`
	View       DirView      `json:"view"`
	Progress   ScanProgress `json:"progress"`
	Errors     []string     `json:"errors"`
	ErrorCount int          `json:"errorCount"`
}

type remoteReport struct {
	mu       sync.Mutex
	once     sync.Once
	closed   atomic.Bool
	client   *ssh.Client
	session  *ssh.Session
	input    io.WriteCloser
	output   *bufio.Reader
	cache    map[int]DirView
	received atomic.Int64
}

func (r *remoteReport) close() {
	r.once.Do(func() { r.closed.Store(true); _ = r.client.Close(); _ = r.session.Close() })
}

func readReportMessage(src io.Reader) (reportMessage, error) {
	var message reportMessage
	var length uint32
	if err := binary.Read(src, binary.BigEndian, &length); err != nil {
		return message, err
	}
	if length == 0 || length > 8<<20 {
		return message, errors.New("The remote report frame is too large.")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(src, payload); err != nil {
		return message, err
	}
	z, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return message, err
	}
	defer z.Close()
	data, err := io.ReadAll(io.LimitReader(z, (32<<20)+1))
	if err != nil {
		return message, err
	}
	if len(data) > 32<<20 {
		return message, errors.New("The remote report is too large.")
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return message, err
	}
	message.wireBytes = int64(length) + 4
	return message, nil
}

func (r *remoteReport) view(id int) (DirView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.cache[id]; ok {
		return v, nil
	}
	if r.closed.Load() {
		return DirView{}, errors.New("The remote report connection closed. Run a new scan to open more folders.")
	}
	request, _ := json.Marshal(struct {
		Type string `json:"type"`
		ID   int    `json:"id"`
	}{"view", id})
	// Closing the SSH transport unblocks both a stalled write and a stalled read.
	timer := time.AfterFunc(20*time.Second, r.close)
	defer timer.Stop()
	if _, err := r.input.Write(append(request, '\n')); err != nil {
		r.close()
		return DirView{}, fmt.Errorf("requesting folder report: %w", err)
	}
	message, err := readReportMessage(r.output)
	r.received.Add(message.wireBytes)
	if err != nil {
		r.close()
		return DirView{}, fmt.Errorf("reading folder report: %w", err)
	}
	if message.Type == "error" {
		return DirView{}, errors.New(message.Error)
	}
	if message.Type != "view" || message.View.ID != id {
		r.close()
		return DirView{}, errors.New("The remote machine returned an invalid folder report.")
	}
	// Keep recently requested folders available if the connection later closes.
	if len(r.cache) >= 32 {
		for key := range r.cache {
			if key != 0 {
				delete(r.cache, key)
				break
			}
		}
	}
	r.cache[id] = message.View
	return message.View, nil
}

func readRemoteReportHeader(r *bufio.Reader) string {
	size := 0
	for size < 64<<10 {
		line, err := r.ReadString('\n')
		size += len(line)
		if rest, ok := strings.CutPrefix(line, "SSE2 "); ok {
			return strings.TrimSpace(rest)
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// remoteReportScan keeps the detailed tree on the server. Only progress, the completed
// root report, and subsequent requested folder views cross the network.
func remoteReportScan(ctx context.Context, client *ssh.Client, c Connection, t *Tree, result *ScanResult, report func(ScanProgress)) (bool, error) {
	session, err := client.NewSession()
	if err != nil {
		return false, nil
	}
	keep := false
	defer func() {
		if !keep {
			_ = session.Close()
		}
	}()
	input, err := session.StdinPipe()
	if err != nil {
		return false, nil
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return false, nil
	}
	session.Stderr = io.Discard
	one := "0"
	if c.SkipVirtual {
		one = "1"
	}
	command := "if command -v python3 >/dev/null 2>&1; then exec python3 -u -c " + shellQuote(remoteReportScript) + " " + shellQuote(t.RootPath) + " " + one + "; else printf 'SSE2 unavailable\\n'; fi"
	if err := session.Start(command); err != nil {
		return false, nil
	}
	output := bufio.NewReaderSize(stdout, 64<<10)
	header := make(chan string, 1)
	go func() { header <- readRemoteReportHeader(output) }()
	timer := time.NewTimer(shellHeaderTimeout)
	defer timer.Stop()
	select {
	case line := <-header:
		if line != "gzip" {
			return false, nil
		}
	case <-ctx.Done():
		return true, ctx.Err()
	case <-timer.C:
		return false, nil
	}
	result.Mode = "remote"
	var received int64
	for {
		message, err := readReportMessage(output)
		received += message.wireBytes
		if err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			return true, fmt.Errorf("reading completed remote report: %w", err)
		}
		switch message.Type {
		case "progress":
			if report != nil {
				report(message.Progress)
			}
		case "error":
			return true, errors.New(message.Error)
		case "done":
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if message.View.ID != 0 || len(message.View.Trail) != 1 {
				return true, errors.New("The remote machine returned an invalid overview.")
			}
			result.Files = message.View.Files
			result.Errors = message.Errors
			result.ErrorCount = message.ErrorCount
			remote := &remoteReport{client: client, session: session, input: input, output: output, cache: map[int]DirView{0: message.View}}
			remote.received.Store(received)
			t.mu.Lock()
			t.Root.Size = message.View.Size
			t.Root.Files = message.View.Files
			t.Root.Incomplete = message.View.Incomplete
			t.remote = remote
			t.mu.Unlock()
			if report != nil {
				report(message.Progress)
			}
			keep = true
			return true, nil
		default:
			return true, errors.New("The remote machine returned an invalid report message.")
		}
	}
}
