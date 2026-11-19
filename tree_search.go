package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// A finished scan is searched through a compact index of every name. It is built when the
// scan moves to its cache file, kept in memory while the user searches, and written to its
// own cache file so it can be dropped when searching stops and read back in well under a
// second.

// indexIdle is how long an unused index stays in memory once it is also on disk.
var indexIdle = 90 * time.Second

type treeIndex struct {
	mu    sync.Mutex
	idx   *search.Index
	file  string
	timer *time.Timer
}

// buildIndex indexes every entry of an in-memory tree. The caller holds t.mu for reading.
func buildIndex(t *Tree) *search.Index {
	b := search.NewBuilder(len(t.dirs)*8, len(t.dirs)*8*16)
	root := t.Root
	b.Add(root.Name, -1, root.Size, true, root.id)
	for _, d := range t.dirs {
		for _, c := range d.Children {
			b.Add(c.Name, d.id, c.Size, c.Directory, c.id)
		}
	}
	return b.Finish()
}

func writeIndexFile(idx *search.Index) (string, error) {
	dir, err := diskCacheDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, fmt.Sprintf("%d-%d-%d.idx", os.Getpid(), time.Now().UnixNano(), diskTreeSeq.Add(1)))
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	err = idx.Save(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(p)
		return "", err
	}
	return p, nil
}

func readIndexFile(p string) (*search.Index, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return search.ReadIndex(f)
}

// keepIndex holds idx in memory and, if it could be saved, remembers its file. The caller
// must not hold t.mu.
func (t *Tree) keepIndex(idx *search.Index, file string) {
	ix := &t.index
	ix.mu.Lock()
	defer ix.mu.Unlock()
	t.mu.RLock()
	closed := t.closed
	t.mu.RUnlock()
	if closed {
		if file != "" {
			_ = os.Remove(file)
		}
		return
	}
	if ix.idx == nil {
		ix.idx = idx
	}
	if file != "" {
		if ix.file != "" {
			_ = os.Remove(ix.file)
		}
		ix.file = file
	}
	t.touchIndexLocked()
}

// touchIndexLocked restarts the idle timer. The caller holds t.index.mu.
func (t *Tree) touchIndexLocked() {
	ix := &t.index
	if ix.file == "" {
		return // nowhere to read it back from, so keep it
	}
	if ix.timer == nil {
		ix.timer = time.AfterFunc(indexIdle, func() {
			ix.mu.Lock()
			dropped := ix.idx != nil && ix.file != ""
			if dropped {
				ix.idx = nil
			}
			ix.mu.Unlock()
			if dropped {
				freeMemory()
			}
		})
		return
	}
	ix.timer.Reset(indexIdle)
}

// searchIndex returns the tree's index, reading or building it if needed.
func (t *Tree) searchIndex() (*search.Index, error) {
	ix := &t.index
	ix.mu.Lock()
	if ix.idx != nil {
		idx := ix.idx
		t.touchIndexLocked()
		ix.mu.Unlock()
		return idx, nil
	}
	if ix.file != "" {
		idx, err := readIndexFile(ix.file)
		if err == nil {
			ix.idx = idx
			t.touchIndexLocked()
			ix.mu.Unlock()
			return idx, nil
		}
	}
	ix.mu.Unlock()
	t.mu.RLock()
	if t.Root == nil {
		t.mu.RUnlock()
		return nil, errors.New("These scan results are no longer available. Scan again to search them.")
	}
	idx := buildIndex(t)
	t.mu.RUnlock()
	t.keepIndex(idx, "")
	return idx, nil
}

// closeIndex forgets the index and deletes its file.
func (t *Tree) closeIndex() {
	ix := &t.index
	ix.mu.Lock()
	if ix.timer != nil {
		ix.timer.Stop()
	}
	if ix.file != "" {
		_ = os.Remove(ix.file)
	}
	ix.idx, ix.file, ix.timer = nil, "", nil
	ix.mu.Unlock()
}

// joinPath turns a trail of names from the scan root into a path on the scanned machine.
func (t *Tree) joinPath(names []string) string {
	if !t.local {
		return path.Join(names...)
	}
	// Several drives sit under one "This PC" folder whose name is not a path.
	if len(names) > 1 && !filepath.IsAbs(names[0]) {
		names = names[1:]
	}
	return filepath.Join(names...)
}

func (t *Tree) search(ctx context.Context, q search.Query) (search.Result, error) {
	t.mu.RLock()
	s := t.store
	t.mu.RUnlock()
	if s, ok := s.(searcher); ok {
		return s.search(q)
	}
	idx, err := t.searchIndex()
	if err != nil {
		return search.Result{}, err
	}
	return idx.Search(ctx, q, t.joinPath)
}
