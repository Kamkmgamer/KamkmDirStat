package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Bound functions run on the window's UI thread, so each one must return quickly. Long
// work runs in goroutines and the page polls for its state.

var errScanRunning = errors.New("A scan is already running.")

type ScanStatus struct {
	Running    bool         `json:"running"`
	Cancelling bool         `json:"cancelling,omitempty"`
	Cancelled  bool         `json:"cancelled,omitempty"`
	Error      string       `json:"error,omitempty"`
	Progress   ScanProgress `json:"progress"`
	RootPath   string       `json:"rootPath,omitempty"`
	Mode       string       `json:"mode,omitempty"`
	Files      int          `json:"files"`
	Size       int64        `json:"size"`
	Errors     []string     `json:"errors,omitempty"`
	ErrorCount int          `json:"errorCount,omitempty"`
	Skipped    []string     `json:"skipped,omitempty"`
	Notes      []string     `json:"notes,omitempty"`
	Seconds    float64      `json:"seconds,omitempty"`
	// OnDisk is set once the results have moved from memory to the cache file.
	OnDisk bool `json:"onDisk,omitempty"`
	// Cleared is set when the user cleared the results.
	Cleared bool `json:"cleared,omitempty"`
}

type scanSession struct {
	sync.Mutex
	status ScanStatus
	cancel context.CancelFunc
	tree   *Tree
}

// scanTree returns the machine's scan results, or nil, and whether the scan is still running.
func (m *machine) scanTree() (*Tree, bool) {
	m.scan.Lock()
	defer m.scan.Unlock()
	return m.scan.tree, m.scan.status.Running
}

var (
	localScanRun  = localScanInto
	remoteScanRun = scanInto
)

func startScan(c Connection) error {
	var tree *Tree
	var localRoots []string
	if c.Local {
		roots, err := cleanLocalPaths(c.Paths)
		if err != nil {
			return err
		}
		localRoots = roots
		tree = newLocalTree(roots)
	} else {
		root, err := cleanRemotePath(c.Path)
		if err != nil {
			return err
		}
		c.Path = root
		tree = newTree(root)
	}
	root := tree.RootPath
	state := &machineOf(c).scan
	state.Lock()
	if state.status.Running {
		state.Unlock()
		tree.close()
		return errScanRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	state.cancel = cancel
	oldTree := state.tree
	state.tree = tree
	state.status = ScanStatus{Running: true, RootPath: root}
	state.Unlock()
	oldTree.close()
	started := time.Now()
	go func() {
		report := func(p ScanProgress) {
			state.Lock()
			if state.tree == tree && state.status.Running {
				state.status.Progress = p
			}
			state.Unlock()
		}
		var result ScanResult
		var err error
		if c.Local {
			result, err = localScanRun(ctx, localRoots, tree, report)
		} else {
			result, err = remoteScanRun(ctx, c, tree, report)
		}
		cancel()
		s := ScanStatus{RootPath: root, Mode: result.Mode, Errors: result.Errors, ErrorCount: result.ErrorCount, Skipped: result.Skipped, Notes: result.Notes, Seconds: time.Since(started).Seconds()}
		if len(s.Errors) > 5 {
			s.Errors = s.Errors[:5]
		}
		tree.mu.Lock()
		if tree.Root != nil { // nil when the results were cleared while cancelling
			if err != nil {
				markIncomplete(tree.Root)
			}
			s.Files, s.Size = tree.Root.Files, tree.Root.Size
		}
		tree.mu.Unlock()
		if err != nil {
			s.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				s.Cancelled = true
				s.Error = "Scan cancelled."
			}
		}
		// Clean up works from the finished tree, so find what it can offer while the
		// nodes are still in memory, before the scan reports that it is done.
		if c.Local && err == nil {
			tree.planClutter()
		}
		state.Lock()
		if state.tree != tree {
			state.Unlock()
			tree.close()
			return
		}
		s.Progress = state.status.Progress
		state.cancel = nil
		state.status = s
		state.Unlock()
		// The finished tree no longer changes, so move it out of memory. The window can
		// already browse it meanwhile; views switch to the file once it is written.
		if onDisk, _ := tree.releaseMemory(); !onDisk {
			freeMemory()
			return
		}
		state.Lock()
		if state.tree == tree && !state.status.Running {
			state.status.OnDisk = true
		}
		state.Unlock()
	}()
	return nil
}

func clearResultsFor(target string) error { return machineFor(target).clearResults() }

// clearResults forgets the machine's scan results and returns their memory to the system.
func (m *machine) clearResults() error {
	state := &m.scan
	state.Lock()
	if state.status.Running {
		state.Unlock()
		return errors.New("Cancel the scan before clearing its results.")
	}
	tree := state.tree
	state.tree = nil
	state.status = ScanStatus{Cleared: true}
	state.Unlock()
	tree.close()
	freeMemory()
	return nil
}

func cancelScanFor(target string) error {
	machineFor(target).cancelScan()
	return nil
}

func (m *machine) cancelScan() {
	state := &m.scan
	state.Lock()
	defer state.Unlock()
	if state.cancel != nil {
		state.status.Cancelling = true
		state.cancel()
	}
}

func scanStatusFor(target string) ScanStatus { return machineFor(target).scanStatus() }

func (m *machine) scanStatus() ScanStatus {
	m.scan.Lock()
	defer m.scan.Unlock()
	return m.scan.status
}

func viewDirFor(target string, id int) (DirView, error) { return machineFor(target).viewDir(id) }

// viewDir returns one folder and a size-limited treemap of what is beneath it.
func (m *machine) viewDir(id int) (DirView, error) {
	tree, _ := m.scanTree()
	if tree == nil {
		return DirView{}, errors.New("No scan results yet.")
	}
	return tree.view(id)
}

// job runs one piece of background work at a time and keeps its latest status, of type S,
// for the page to poll.
type job[S any] struct {
	mu      sync.Mutex
	running bool
	status  S
}

// start shows the status running and replaces it with what work returns. It fails with
// the message busy while earlier work has not finished.
func (j *job[S]) start(busy string, running S, work func() S) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return errors.New(busy)
	}
	j.running, j.status = true, running
	go func() {
		s := work()
		j.mu.Lock()
		defer j.mu.Unlock()
		j.running, j.status = false, s
	}()
	return nil
}

func (j *job[S]) get() S {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

type FingerprintStatus struct {
	Running     bool   `json:"running"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Trust is hostKnown, hostNew or hostChanged; a known server needs no confirmation.
	Trust string `json:"trust,omitempty"`
	Error string `json:"error,omitempty"`
	// key is what the check found, kept with the status so both change together. It is
	// not sent to the page.
	key hostKey
}

var fingerprintJob job[FingerprintStatus]

func startFingerprint(c Connection) error {
	return fingerprintJob.start("A connection is already in progress.", FingerprintStatus{Running: true}, func() FingerprintStatus {
		key, err := fingerprint(c)
		s := FingerprintStatus{Fingerprint: key.Fingerprint, Trust: key.Trust, key: key}
		if err != nil {
			s.Error = err.Error()
		}
		return s
	})
}

func fingerprintStatus() FingerprintStatus { return fingerprintJob.get() }

// trustHostKey always trusts the key the last check found, so later connections to that
// server do not ask. The fingerprint must be the one the user was shown.
func trustHostKey(fingerprint string) error {
	key := fingerprintJob.get().key
	if key.Key == nil || fingerprint == "" || key.Fingerprint != fingerprint {
		return errors.New("Connect again to trust this server.")
	}
	return rememberHostKey(key.Hostname, key.Key)
}

type ResolveStatus struct {
	Running    bool        `json:"running"`
	Connection *Connection `json:"connection,omitempty"`
	Error      string      `json:"error,omitempty"`
}

var resolveJob job[ResolveStatus]

func startResolve(c Connection) error {
	return resolveJob.start("SSH config lookup is already in progress.", ResolveStatus{Running: true}, func() ResolveStatus {
		value, err := resolveSSH(c)
		if err != nil {
			return ResolveStatus{Error: err.Error()}
		}
		return ResolveStatus{Connection: &value}
	})
}

func resolveStatus() ResolveStatus { return resolveJob.get() }

type ViewStatus struct {
	Running bool     `json:"running"`
	View    *DirView `json:"view,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func startViewFor(target string, id int) error { return machineFor(target).startView(id) }

func (m *machine) startView(id int) error {
	return m.view.start("A folder report is already loading.", ViewStatus{Running: true}, func() ViewStatus {
		v, err := m.viewDir(id)
		if err != nil {
			return ViewStatus{Error: err.Error()}
		}
		return ViewStatus{View: &v}
	})
}

func viewStatusFor(target string) ViewStatus { return machineFor(target).viewStatus() }

func (m *machine) viewStatus() ViewStatus { return m.view.get() }

func closeResults() {
	for _, m := range machines {
		m.cancelScan()
		tree, _ := m.scanTree()
		tree.close()
	}
}
