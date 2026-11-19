package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"github.com/Kamkmgamer/KamkmDirStat/internal/wire"
	"golang.org/x/crypto/ssh"
)

//go:embed remote_report.py
var remoteReportScript string

type reportMessage struct {
	wireBytes  int64
	Type       string        `json:"type"`
	Error      string        `json:"error"`
	View       DirView       `json:"view"`
	Search     search.Result `json:"search"`
	Progress   ScanProgress  `json:"progress"`
	Errors     []string      `json:"errors"`
	ErrorCount int           `json:"errorCount"`
	Features   []string      `json:"features"`
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
	features []string // what the scanner on the server can do besides folder views
	received atomic.Int64
}

func (r *remoteReport) close() {
	r.once.Do(func() { r.closed.Store(true); _ = r.client.Close(); _ = r.session.Close() })
}

func readReportMessage(src io.Reader) (reportMessage, error) {
	var message reportMessage
	data, wire, err := wire.Read(src)
	if err != nil {
		return message, err
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return message, err
	}
	message.wireBytes = wire
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

func (r *remoteReport) item(id int, name string) (storeItem, error) {
	v, err := r.view(id)
	if err != nil {
		return storeItem{}, err
	}
	it := storeItem{dir: true, size: v.Size}
	for _, c := range v.Trail {
		it.trail = append(it.trail, c.Name)
	}
	if name != "" {
		it.trail = append(it.trail, name)
		// Folders with more than 500 entries list only the largest, so a missing one
		// is taken to be a file.
		it.dir, it.size = false, 0
		if i := slices.IndexFunc(v.Entries, func(e DirEntry) bool { return e.Name == name }); i >= 0 {
			it.dir, it.size = v.Entries[i].Directory, v.Entries[i].Size
		}
	}
	return it, nil
}

// search asks the server to search its scan. The server indexes the scan the first time,
// which takes a few seconds for millions of files with the Python report.
func (r *remoteReport) search(q search.Query) (search.Result, error) {
	if !slices.Contains(r.features, "search") {
		// Only a build whose embedded fast scanner was not rebuilt has one without search.
		return search.Result{}, errors.New("The fast server scanner built into this copy of the app is out of date and cannot search. Rebuild it with go run ./tools/buildhelper and rebuild the app, or turn off Fast server scan and scan again.")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() {
		return search.Result{}, errors.New("The remote report connection closed. Run a new scan to search it.")
	}
	request, err := json.Marshal(struct {
		Type  string       `json:"type"`
		Query search.Query `json:"query"`
	}{"search", q})
	if err != nil {
		return search.Result{}, err
	}
	if len(request) > 3500 {
		return search.Result{}, errors.New("The search is too long.")
	}
	timer := time.AfterFunc(2*time.Minute, r.close)
	defer timer.Stop()
	if _, err := r.input.Write(append(request, '\n')); err != nil {
		r.close()
		return search.Result{}, fmt.Errorf("sending search: %w", err)
	}
	message, err := readReportMessage(r.output)
	r.received.Add(message.wireBytes)
	if err != nil {
		r.close()
		return search.Result{}, fmt.Errorf("reading search results: %w", err)
	}
	if message.Type == "error" {
		return search.Result{}, errors.New(message.Error)
	}
	if message.Type != "search" {
		r.close()
		return search.Result{}, errors.New("The remote machine returned invalid search results.")
	}
	if message.Search.Hits == nil {
		message.Search.Hits = []search.Hit{}
	}
	return message.Search, nil
}

// remoteReportScan keeps the detailed tree on the server. Only progress, the completed
// root report, and subsequent requested folder views cross the network.
func remoteReportScan(ctx context.Context, client *ssh.Client, c Connection, t *Tree, result *ScanResult, report func(ScanProgress)) (bool, error) {
	one := "0"
	if c.SkipVirtual {
		one = "1"
	}
	command := "if command -v python3 >/dev/null 2>&1; then exec python3 -u -c " + shellQuote(remoteReportScript) + " " + shellQuote(t.RootPath) + " " + one + "; else printf 'SSE2 unavailable\\n'; fi"
	_, handled, err := runRemoteReport(ctx, client, command, t, result, report)
	return handled, err
}

// runRemoteReport runs a report program, the Python script or the native helper, and
// reads its report. It returns the program's header line, and handled=false if the header
// was not "gzip", which means this program cannot scan here.
func runRemoteReport(ctx context.Context, client *ssh.Client, command string, t *Tree, result *ScanResult, report func(ScanProgress)) (string, bool, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", false, nil
	}
	keep := false
	defer func() {
		if !keep {
			_ = session.Close()
		}
	}()
	input, err := session.StdinPipe()
	if err != nil {
		return "", false, nil
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", false, nil
	}
	session.Stderr = io.Discard
	if err := session.Start(command); err != nil {
		return "", false, nil
	}
	output := bufio.NewReaderSize(stdout, 64<<10)
	header := make(chan string, 1)
	go func() { header <- wire.Header(output, "SSE2 ") }()
	timer := time.NewTimer(shellHeaderTimeout)
	defer timer.Stop()
	var line string
	select {
	case line = <-header:
		if line != "gzip" {
			return line, false, nil
		}
	case <-ctx.Done():
		return "", true, ctx.Err()
	case <-timer.C:
		return "", false, nil
	}
	result.Mode = "remote"
	var received int64
	for {
		message, err := readReportMessage(output)
		received += message.wireBytes
		if err != nil {
			if ctx.Err() != nil {
				return line, true, ctx.Err()
			}
			return line, true, fmt.Errorf("reading completed remote report: %w", err)
		}
		switch message.Type {
		case "progress":
			if report != nil {
				report(message.Progress)
			}
		case "error":
			return line, true, errors.New(message.Error)
		case "done":
			if ctx.Err() != nil {
				return line, true, ctx.Err()
			}
			if message.View.ID != 0 || len(message.View.Trail) != 1 {
				return line, true, errors.New("The remote machine returned an invalid overview.")
			}
			result.Files = message.View.Files
			result.Errors = message.Errors
			result.ErrorCount = message.ErrorCount
			remote := &remoteReport{client: client, session: session, input: input, output: output, cache: map[int]DirView{0: message.View}, features: message.Features}
			remote.received.Store(received)
			t.mu.Lock()
			t.Root.Size = message.View.Size
			t.Root.Files = message.View.Files
			t.Root.Incomplete = message.View.Incomplete
			t.store = remote
			t.mu.Unlock()
			if report != nil {
				report(message.Progress)
			}
			keep = true
			return line, true, nil
		default:
			return line, true, errors.New("The remote machine returned an invalid report message.")
		}
	}
}
