package main

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// A finished scan lives in a cache file that is never rewritten. Files and folders deleted
// from the app afterwards are recorded here instead, and every view, map and search of the
// scan leaves them out and shrinks the folders that held them.

type treeEdits struct {
	mu       sync.Mutex
	gone     map[int]map[string]bool // folder id to the names deleted from it
	emptied  map[int]bool            // folders whose whole contents were deleted
	shrink   map[int]sizeFiles       // folder id to what was deleted beneath it
	paths    []string                // deleted paths, for search results
	contents []string                // emptied folders, for search results
}

type sizeFiles struct {
	size  int64
	files int
}

// ancestors returns dir and the folders above it, nearest first.
func (t *Tree) ancestors(dir int) []int {
	s, unlock := t.current()
	defer unlock()
	if s, ok := s.(lineage); ok {
		return s.ancestors(dir)
	}
	return nil
}

func (e *treeEdits) shrinkAll(ids []int, size int64, files int) {
	if e.shrink == nil {
		e.shrink = map[int]sizeFiles{}
	}
	for _, id := range ids {
		s := e.shrink[id]
		s.size += size
		s.files += files
		e.shrink[id] = s
	}
}

// forget records that name, in folder dir, was deleted.
func (t *Tree) forget(dir int, name, path string, size int64, files int) {
	ids := t.ancestors(dir)
	e := &t.edits
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gone == nil {
		e.gone = map[int]map[string]bool{}
	}
	if e.gone[dir] == nil {
		e.gone[dir] = map[string]bool{}
	}
	if e.gone[dir][name] {
		return
	}
	e.gone[dir][name] = true
	e.shrinkAll(ids, size, files)
	e.paths = append(e.paths, path)
}

// forgetContents records that what was inside folder id was deleted, freeing size bytes.
func (t *Tree) forgetContents(id int, path string, size int64, files int) {
	ids := t.ancestors(id)
	e := &t.edits
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.emptied == nil {
		e.emptied = map[int]bool{}
	}
	e.emptied[id] = true
	e.shrinkAll(ids, size, files)
	e.contents = append(e.contents, path)
}

func (t *Tree) isForgotten(dir int, name string) bool {
	e := &t.edits
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gone[dir][name] || e.emptied[dir]
}

func (e *treeEdits) empty() bool { return len(e.gone) == 0 && len(e.emptied) == 0 }

func (e *treeEdits) dropped(dir int, name string) bool { return e.emptied[dir] || e.gone[dir][name] }

// applyEdits removes deleted entries from a folder view and its map.
func (t *Tree) applyEdits(v *DirView) {
	e := &t.edits
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.empty() {
		return
	}
	s := e.shrink[v.ID]
	v.Size, v.Files = max(0, v.Size-s.size), max(0, v.Files-s.files)
	kept := v.Entries[:0]
	for _, en := range v.Entries {
		if e.dropped(v.ID, en.Name) {
			v.Items--
			continue
		}
		if en.Directory {
			s := e.shrink[en.ID]
			en.Size, en.Files = max(0, en.Size-s.size), max(0, en.Files-s.files)
		}
		kept = append(kept, en)
	}
	v.Entries = kept
	slices.SortStableFunc(v.Entries, func(a, b DirEntry) int { return int(min(1, max(-1, b.Size-a.Size))) })
	if e.emptied[v.ID] {
		v.MoreCount, v.MoreSize = 0, 0
	}
	if v.Map != nil {
		v.Map.S = v.Size
		e.applyMap(v.Map)
	}
}

func (e *treeEdits) applyMap(m *MapNode) {
	if !m.D {
		return
	}
	if e.emptied[m.I] {
		m.C, m.O = nil, 0
		return
	}
	kept := m.C[:0]
	for _, c := range m.C {
		if e.dropped(m.I, c.N) {
			continue
		}
		if c.D {
			c.S = max(0, c.S-e.shrink[c.I].size)
			e.applyMap(c)
		}
		if c.S > 0 {
			kept = append(kept, c)
		}
	}
	m.C = kept
}

// filterHits leaves deleted items out of search results.
func (t *Tree) filterHits(r *search.Result) {
	e := &t.edits
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.empty() {
		return
	}
	gone := func(p string) bool {
		for _, d := range e.paths {
			if samePath(p, d) || isInside(p, d) {
				return true
			}
		}
		for _, d := range e.contents {
			if isInside(p, d) {
				return true
			}
		}
		return false
	}
	kept := r.Hits[:0]
	for _, h := range r.Hits {
		// A hit's path is the folder holding it.
		if gone(filepath.Join(h.Path, h.Name)) {
			r.Total--
			r.TotalSize -= h.Size
			continue
		}
		kept = append(kept, h)
	}
	r.Hits = kept
	r.Total, r.TotalSize = max(0, r.Total), max(0, r.TotalSize)
}

// scanRoots is where a local scan started: its one folder, or each of its drives.
func (t *Tree) scanRoots() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.roots) > 0 {
		return slices.Clone(t.roots)
	}
	if filepath.IsAbs(t.RootPath) && !strings.ContainsRune(t.RootPath, 0) {
		return []string{t.RootPath}
	}
	return nil
}
