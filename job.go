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

var (
	scanState       scanSession // this computer
	remoteScanState scanSession // the SSH server
)

func scanFor(local bool) *scanSession {
	if local {
		return &scanState
	}
	return &remoteScanState
}

func scanForTarget(target string) *scanSession { return scanFor(target != "remote") }

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
	state := scanFor(c.Local)
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

// clearResults forgets this computer's scan results and returns their memory to the system.
func clearResults() error { return clearResultsFor("local") }

func clearResultsFor(target string) error {
	state := scanForTarget(target)
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

func cancelScan() error { return cancelScanFor("local") }

func cancelScanFor(target string) error {
	state := scanForTarget(target)
	state.Lock()
	defer state.Unlock()
	if state.cancel != nil {
		state.status.Cancelling = true
		state.cancel()
	}
	return nil
}

func scanStatus() ScanStatus { return scanStatusFor("local") }

func scanStatusFor(target string) ScanStatus {
	state := scanForTarget(target)
	state.Lock()
	defer state.Unlock()
	return state.status
}

// viewDir returns one folder and a size-limited treemap of what is beneath it.
func viewDir(id int) (DirView, error) { return viewDirFor("local", id) }

func viewDirFor(target string, id int) (DirView, error) {
	state := scanForTarget(target)
	state.Lock()
	tree := state.tree
	state.Unlock()
	if tree == nil {
		return DirView{}, errors.New("No scan results yet.")
	}
	return tree.view(id)
}

type FingerprintStatus struct {
	Running     bool   `json:"running"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Trust is hostKnown, hostNew or hostChanged; a known server needs no confirmation.
	Trust string `json:"trust,omitempty"`
	Error string `json:"error,omitempty"`
}

var fingerprintState struct {
	sync.Mutex
	status FingerprintStatus
	key    hostKey
}

func startFingerprint(c Connection) error {
	fingerprintState.Lock()
	if fingerprintState.status.Running {
		fingerprintState.Unlock()
		return errors.New("A connection is already in progress.")
	}
	fingerprintState.status = FingerprintStatus{Running: true}
	fingerprintState.key = hostKey{}
	fingerprintState.Unlock()
	go func() {
		key, err := fingerprint(c)
		fingerprintState.Lock()
		defer fingerprintState.Unlock()
		fingerprintState.key = key
		fingerprintState.status = FingerprintStatus{Fingerprint: key.Fingerprint, Trust: key.Trust}
		if err != nil {
			fingerprintState.status.Error = err.Error()
		}
	}()
	return nil
}
func fingerprintStatus() FingerprintStatus {
	fingerprintState.Lock()
	defer fingerprintState.Unlock()
	return fingerprintState.status
}

// trustHostKey always trusts the key the last check found, so later connections to that
// server do not ask. The fingerprint must be the one the user was shown.
func trustHostKey(fingerprint string) error {
	fingerprintState.Lock()
	key := fingerprintState.key
	fingerprintState.Unlock()
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

var resolveState struct {
	sync.Mutex
	status ResolveStatus
}

func startResolve(c Connection) error {
	resolveState.Lock()
	if resolveState.status.Running {
		resolveState.Unlock()
		return errors.New("SSH config lookup is already in progress.")
	}
	resolveState.status = ResolveStatus{Running: true}
	resolveState.Unlock()
	go func() {
		value, err := resolveSSH(c)
		resolveState.Lock()
		defer resolveState.Unlock()
		resolveState.status = ResolveStatus{Connection: &value}
		if err != nil {
			resolveState.status = ResolveStatus{Error: err.Error()}
		}
	}()
	return nil
}
func resolveStatus() ResolveStatus {
	resolveState.Lock()
	defer resolveState.Unlock()
	return resolveState.status
}

type ViewStatus struct {
	Running bool     `json:"running"`
	View    *DirView `json:"view,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type viewSession struct {
	sync.Mutex
	status ViewStatus
}

var (
	viewState       viewSession
	remoteViewState viewSession
)

func viewForTarget(target string) *viewSession {
	if target == "remote" {
		return &remoteViewState
	}
	return &viewState
}

func startView(id int) error { return startViewFor("local", id) }

func startViewFor(target string, id int) error {
	state := viewForTarget(target)
	state.Lock()
	if state.status.Running {
		state.Unlock()
		return errors.New("A folder report is already loading.")
	}
	state.status = ViewStatus{Running: true}
	state.Unlock()
	go func() {
		v, err := viewDirFor(target, id)
		s := ViewStatus{View: &v}
		if err != nil {
			s = ViewStatus{Error: err.Error()}
		}
		state.Lock()
		state.status = s
		state.Unlock()
	}()
	return nil
}

func viewStatus() ViewStatus { return viewStatusFor("local") }

func viewStatusFor(target string) ViewStatus {
	state := viewForTarget(target)
	state.Lock()
	defer state.Unlock()
	return state.status
}

func closeResults() {
	for _, target := range []string{"local", "remote"} {
		_ = cancelScanFor(target)
		state := scanForTarget(target)
		state.Lock()
		tree := state.tree
		state.Unlock()
		tree.close()
	}
}
