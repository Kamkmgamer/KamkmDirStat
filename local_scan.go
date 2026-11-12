package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Local scans read this computer's drives directly. Sizes are file sizes, as in Explorer and
// WinDirStat. Links, junctions and mounted volumes are not followed, so nothing is counted
// twice through them, but hard-linked files are counted at each of their names.

type localKind int

const (
	localFile localKind = iota
	localDir
)

// localEntry is one item from listLocalDir, which leaves out links and other volumes.
type localEntry struct {
	name string
	kind localKind
	size int64
}

// More workers than this mostly contend inside the file system on Windows.
const localScanWorkers = 8

// localScanDirect allows reading NTFS drives directly. Tests turn it off to compare.
var localScanDirect = true

// cleanLocalPaths checks the folders chosen for a local scan and removes any that are inside
// another chosen folder.
func cleanLocalPaths(paths []string) ([]string, error) {
	var roots []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, "\x00\r\n") || !filepath.IsAbs(p) {
			return nil, fmt.Errorf("Enter a full folder path, such as C:\\Users: %q", p)
		}
		p = filepath.Clean(p)
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", p, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a folder.", p)
		}
		roots = append(roots, p)
	}
	if len(roots) == 0 {
		return nil, errors.New("Choose at least one drive or folder to scan.")
	}
	slices.SortFunc(roots, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	var kept []string
	for _, r := range roots {
		inside := false
		for _, k := range kept {
			if strings.EqualFold(r, k) || isInside(r, k) {
				inside = true
				break
			}
		}
		if !inside {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

func isInside(p, parent string) bool {
	rel, err := filepath.Rel(parent, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// newLocalTree makes the tree for a local scan. Several roots sit under one "This PC" node.
func newLocalTree(roots []string) *Tree {
	if len(roots) == 1 {
		return newTree(roots[0])
	}
	t := newTree(localRootName())
	for _, r := range roots {
		t.addChild(t.Root, r, true, 0)
	}
	return t
}

func localScanInto(ctx context.Context, roots []string, t *Tree, report func(ScanProgress)) (ScanResult, error) {
	result := ScanResult{Root: t.Root, Mode: "local"}
	defer t.finalize()
	type start struct {
		node *Node
		path string
	}
	var starts []start
	if len(roots) == 1 {
		starts = []start{{t.Root, roots[0]}}
	} else {
		t.mu.RLock()
		for i, r := range roots {
			starts = append(starts, start{t.Root.Children[i], r})
		}
		t.mu.RUnlock()
	}
	var expected int64
	for _, r := range roots {
		expected += volumeUsedBytes(r)
	}

	var files, dirs, bytes atomic.Int64
	var current atomic.Value
	current.Store("")
	var errMu sync.Mutex
	addError := func(message string) {
		errMu.Lock()
		defer errMu.Unlock()
		result.ErrorCount++
		if len(result.Errors) < maxReportedErrors {
			result.Errors = append(result.Errors, message)
		}
	}
	progress := func() ScanProgress {
		return ScanProgress{Mode: "local", Files: int(files.Load()), Directories: int(dirs.Load()), Bytes: bytes.Load(),
			CurrentPath: current.Load().(string), ExpectedBytes: expected}
	}
	stopReports := make(chan struct{})
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopReports:
				return
			case <-ticker.C:
				if report != nil {
					report(progress())
				}
			}
		}
	}()

	w := &localWalk{ctx: ctx, t: t, files: &files, dirs: &dirs, bytes: &bytes, current: &current, addError: addError}
	usedNTFS, needAdmin := false, false
	for _, s := range starts {
		if ctx.Err() != nil {
			break
		}
		handled, damaged, err := false, 0, errNotNTFS
		if localScanDirect {
			handled, damaged, err = scanNTFSRoot(ctx, s.path, s.node, t, &files, &dirs, &bytes, &current)
		}
		if handled {
			if err != nil {
				break // cancelled
			}
			usedNTFS = true
			if damaged > 0 {
				result.Notes = append(result.Notes, fmt.Sprintf("%d damaged file records on %s were skipped.", damaged, s.path))
			}
			continue
		}
		switch {
		case errors.Is(err, errNeedsAdmin):
			needAdmin = true
		case err != nil && !errors.Is(err, errNotNTFS):
			result.Notes = append(result.Notes, fmt.Sprintf("Could not read the NTFS file table of %s (%v), so its folders were listed instead.", s.path, err))
		}
		w.walk(s.node, s.path)
	}
	if usedNTFS {
		result.Mode = "local-ntfs"
	}
	if needAdmin {
		result.Notes = append(result.Notes, "Run KamkmDirStat as administrator to read NTFS drives directly, which is much faster.")
	}
	close(stopReports)
	<-reported
	result.Files = int(files.Load())
	if report != nil {
		report(progress())
	}
	return result, ctx.Err()
}

// scanNTFSRoot reads an NTFS drive's file table into node. It reports handled=false, with
// the reason, when the drive cannot be read directly and its folders should be listed.
func scanNTFSRoot(ctx context.Context, path string, node *Node, t *Tree, files, dirs, bytes *atomic.Int64, current *atomic.Value) (handled bool, damaged int, err error) {
	r, closeVolume, err := openNTFSVolume(path)
	if err != nil {
		return false, 0, err
	}
	defer closeVolume()
	v, err := openNTFS(r)
	if err != nil {
		return false, 0, err
	}
	current.Store("Reading the NTFS file table of " + path)
	var mu sync.Mutex
	var seenFiles, seenBytes int64
	idx, err := v.read(ctx, func(_, f, b int64) {
		mu.Lock()
		defer mu.Unlock()
		if f > seenFiles {
			files.Add(f - seenFiles)
			seenFiles = f
		}
		if b > seenBytes {
			bytes.Add(b - seenBytes)
			seenBytes = b
		}
	})
	if err != nil {
		files.Add(-seenFiles)
		bytes.Add(-seenBytes)
		if ctx.Err() != nil {
			return true, 0, ctx.Err()
		}
		return false, 0, err
	}
	t.mu.Lock()
	beforeDirs, beforeSize := len(t.dirs), node.Size
	added := idx.addToTree(t, node, ntfsRootRecord)
	newDirs, size := len(t.dirs)-beforeDirs, node.Size-beforeSize
	t.mu.Unlock()
	// Replace the running totals, which counted the whole table, with what was added.
	files.Add(int64(added) - seenFiles)
	bytes.Add(size - seenBytes)
	dirs.Add(int64(newDirs) + 1)
	return true, idx.damaged, nil
}

// localWalk is what a folder walk updates. Each platform provides walk, which lists the
// folder at path into node and everything beneath it, and returns when it is done.
type localWalk struct {
	ctx                context.Context
	t                  *Tree
	files, dirs, bytes *atomic.Int64
	current            *atomic.Value
	addError           func(string)
}

// add records one folder's entries under node and returns the new subfolder nodes in the
// order of their entries. listErr marks the folder incomplete.
func (w *localWalk) add(node *Node, dir string, entries []localEntry, listErr error) []*Node {
	if listErr != nil {
		w.addError(fmt.Sprintf("%s: %v", dir, listErr))
	}
	w.t.mu.Lock()
	if listErr != nil {
		markIncomplete(node)
	}
	subs, added, size := w.t.addEntries(node, entries)
	w.t.mu.Unlock()
	w.dirs.Add(1)
	w.files.Add(added)
	w.bytes.Add(size)
	return subs
}
