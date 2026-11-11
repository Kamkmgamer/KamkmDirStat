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

	sem := make(chan struct{}, localScanWorkers)
	var wg sync.WaitGroup
	var walk func(node *Node, dir string, device uint64)
	walk = func(node *Node, dir string, device uint64) {
		if ctx.Err() != nil {
			return
		}
		current.Store(dir)
		entries, err := listLocalDir(dir, device)
		if err != nil {
			addError(fmt.Sprintf("%s: %v", dir, err))
		}
		type sub struct {
			node *Node
			path string
		}
		var subs []sub
		var added, size int64
		t.mu.Lock()
		if err != nil {
			markIncomplete(node)
		}
		for _, e := range entries {
			if e.kind == localDir {
				subs = append(subs, sub{t.addChild(node, e.name, true, 0), filepath.Join(dir, e.name)})
			} else {
				t.addChild(node, e.name, false, e.size)
				added++
				size += e.size
			}
		}
		t.mu.Unlock()
		dirs.Add(1)
		files.Add(added)
		bytes.Add(size)
		for _, s := range subs {
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sem }()
					walk(s.node, s.path, device)
				}()
			default:
				walk(s.node, s.path, device)
			}
		}
	}
	for _, s := range starts {
		walk(s.node, s.path, deviceOf(s.path))
	}
	wg.Wait()
	close(stopReports)
	<-reported
	result.Files = int(files.Load())
	if report != nil {
		report(progress())
	}
	return result, ctx.Err()
}
