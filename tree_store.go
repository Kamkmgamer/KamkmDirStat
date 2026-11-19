package main

import (
	"errors"
	"slices"

	"github.com/Kamkmgamer/KamkmDirStat/internal/dirview"
	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// treeStore is where a scan's folders are kept. A tree has one at a time: its nodes in
// memory while the scan runs (memStore), the cache file once a scan of this computer has
// finished (diskTree), or the report kept on the server (remoteReport).
type treeStore interface {
	// view returns one folder and a size-limited treemap of what is beneath it.
	view(id int) (DirView, error)
	// item finds folder id, or the entry called name in it when name is not empty.
	item(id int, name string) (storeItem, error)
	close()
}

// searcher is a store that searches itself, as the report on a server does. The others
// are searched through the tree's index.
type searcher interface {
	search(q search.Query) (search.Result, error)
}

type storeItem struct {
	trail []string // names from the scan root down to the item
	dir   bool
	size  int64
	files int // for an entry of a folder in a local scan: the files it holds, at least 1
}

// lineage is a store that knows which folders lie above another.
type lineage interface {
	// ancestors returns folder id and the folders above it, nearest first.
	ancestors(id int) []int
}

// current returns the store holding the results now. The in-memory store reads nodes the
// scanner may still be adding, so t.mu stays held for reading until unlock is called; the
// other stores guard themselves.
func (t *Tree) current() (s treeStore, unlock func()) {
	t.mu.RLock()
	if t.store != nil {
		s = t.store
		t.mu.RUnlock()
		return s, func() {}
	}
	return memStore{t}, t.mu.RUnlock
}

// remoteStore returns the report kept on the server, or nil for other scans.
func (t *Tree) remoteStore() *remoteReport {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, _ := t.store.(*remoteReport)
	return r
}

// diskStore returns the cache file holding the results, or nil if they are elsewhere.
func (t *Tree) diskStore() *diskTree {
	t.mu.RLock()
	defer t.mu.RUnlock()
	d, _ := t.store.(*diskTree)
	return d
}

// view is one folder of the scan, without what was deleted since.
func (t *Tree) view(id int) (DirView, error) {
	v, err := t.viewScanned(id)
	if err == nil {
		t.applyEdits(&v)
	}
	return v, err
}

func (t *Tree) viewScanned(id int) (DirView, error) {
	s, unlock := t.current()
	defer unlock()
	return s.view(id)
}

// memStore reads the tree's nodes. Its methods must be called with t.mu held for reading.
type memStore struct{ t *Tree }

func (m memStore) close() {}

func (m memStore) view(id int) (DirView, error) {
	t := m.t
	if id < 0 || id >= len(t.dirs) {
		return DirView{}, errors.New("That folder is not in the scan results.")
	}
	n := t.dirs[id]
	var trail []Crumb
	for p := n; p != nil; p = p.Parent {
		trail = append(trail, Crumb{ID: p.id, Name: p.Name})
	}
	slices.Reverse(trail)
	return dirview.Build[*Node](m, trail, n, sortedChildren(n))
}

func (m memStore) Item(n *Node) dirview.Item {
	return dirview.Item{Name: n.Name, Size: n.Size, Files: n.Files, Directory: n.Directory, ID: n.id, Incomplete: n.Incomplete}
}

func (m memStore) Children(n *Node) ([]*Node, error) { return sortedChildren(n), nil }

func (m memStore) item(id int, name string) (storeItem, error) {
	t := m.t
	if id < 0 || id >= len(t.dirs) {
		return storeItem{}, errItemGone
	}
	n := t.dirs[id]
	it := storeItem{dir: true, size: n.Size}
	for p := n; p != nil; p = p.Parent {
		it.trail = append(it.trail, p.Name)
	}
	slices.Reverse(it.trail)
	if name != "" {
		i := slices.IndexFunc(n.Children, func(c *Node) bool { return c.Name == name })
		if i < 0 {
			return storeItem{}, errItemGone
		}
		it.dir, it.size, it.files = n.Children[i].Directory, n.Children[i].Size, max(1, n.Children[i].Files)
		it.trail = append(it.trail, name)
	}
	return it, nil
}

func (m memStore) ancestors(id int) []int {
	var ids []int
	if id >= 0 && id < len(m.t.dirs) {
		for n := m.t.dirs[id]; n != nil; n = n.Parent {
			ids = append(ids, n.id)
		}
	}
	return ids
}
