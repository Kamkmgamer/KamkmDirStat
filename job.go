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
}

var scanState struct {
	sync.Mutex
	status ScanStatus
	cancel context.CancelFunc
	tree   *Tree
}

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
	scanState.Lock()
	if scanState.status.Running {
		scanState.Unlock()
		return errScanRunning
	}
	ctx, cancel := context.WithCancel(context.Background())
	scanState.cancel = cancel
	oldTree := scanState.tree
	scanState.tree = tree
	scanState.status = ScanStatus{Running: true, RootPath: root}
	scanState.Unlock()
	oldTree.close()
	started := time.Now()
	go func() {
		report := func(p ScanProgress) {
			scanState.Lock()
			scanState.status.Progress = p
			scanState.Unlock()
		}
		var result ScanResult
		var err error
		if c.Local {
			result, err = localScanInto(ctx, localRoots, tree, report)
		} else {
			result, err = scanInto(ctx, c, tree, report)
		}
		cancel()
		s := ScanStatus{RootPath: root, Mode: result.Mode, Errors: result.Errors, ErrorCount: result.ErrorCount, Skipped: result.Skipped, Notes: result.Notes, Seconds: time.Since(started).Seconds()}
		if len(s.Errors) > 5 {
			s.Errors = s.Errors[:5]
		}
		tree.mu.Lock()
		if err != nil {
			markIncomplete(tree.Root)
		}
		s.Files, s.Size = tree.Root.Files, tree.Root.Size
		tree.mu.Unlock()
		if err != nil {
			s.Error = err.Error()
			if errors.Is(err, context.Canceled) {
				s.Cancelled = true
				s.Error = "Scan cancelled."
			}
		}
		scanState.Lock()
		defer scanState.Unlock()
		s.Progress = scanState.status.Progress
		scanState.cancel = nil
		scanState.status = s
	}()
	return nil
}
func cancelScan() error {
	scanState.Lock()
	defer scanState.Unlock()
	if scanState.cancel != nil {
		scanState.status.Cancelling = true
		scanState.cancel()
	}
	return nil
}
func scanStatus() ScanStatus { scanState.Lock(); defer scanState.Unlock(); return scanState.status }

// viewDir returns one folder and a size-limited treemap of what is beneath it.
func viewDir(id int) (DirView, error) {
	scanState.Lock()
	tree := scanState.tree
	scanState.Unlock()
	if tree == nil {
		return DirView{}, errors.New("No scan results yet.")
	}
	return tree.view(id)
}

type FingerprintStatus struct {
	Running     bool   `json:"running"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

var fingerprintState struct {
	sync.Mutex
	status FingerprintStatus
}

func startFingerprint(c Connection) error {
	fingerprintState.Lock()
	if fingerprintState.status.Running {
		fingerprintState.Unlock()
		return errors.New("A connection is already in progress.")
	}
	fingerprintState.status = FingerprintStatus{Running: true}
	fingerprintState.Unlock()
	go func() {
		value, err := fingerprint(c)
		fingerprintState.Lock()
		defer fingerprintState.Unlock()
		fingerprintState.status = FingerprintStatus{Fingerprint: value}
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

var viewState struct {
	sync.Mutex
	status ViewStatus
}

func startView(id int) error {
	viewState.Lock()
	if viewState.status.Running {
		viewState.Unlock()
		return errors.New("A folder report is already loading.")
	}
	viewState.status = ViewStatus{Running: true}
	viewState.Unlock()
	go func() {
		v, err := viewDir(id)
		s := ViewStatus{View: &v}
		if err != nil {
			s = ViewStatus{Error: err.Error()}
		}
		viewState.Lock()
		viewState.status = s
		viewState.Unlock()
	}()
	return nil
}
func viewStatus() ViewStatus { viewState.Lock(); defer viewState.Unlock(); return viewState.status }
func closeResults() {
	_ = cancelScan()
	scanState.Lock()
	tree := scanState.tree
	scanState.Unlock()
	tree.close()
}
