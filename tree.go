package main

import (
	"cmp"
	"slices"
	"sync"

	"github.com/Kamkmgamer/KamkmDirStat/internal/dirview"
)

// Node is one scanned entry. A directory's Size and Files cover everything beneath it.
type Node struct {
	Name       string
	Parent     *Node `json:"-"`
	Size       int64
	Files      int
	Directory  bool
	Children   []*Node
	Incomplete bool
	id         int // directories only; index into Tree.dirs
}

// Tree is shared by the scanner, which adds entries, and the window, which reads one folder
// at a time while the scan is still running. Never send the whole tree to the window: a full
// disk has hundreds of thousands of entries.
type Tree struct {
	mu       sync.RWMutex
	RootPath string
	Root     *Node
	dirs     []*Node
	store    treeStore // nil while the results are the nodes above
	local    bool      // a scan of this computer, whose files can be opened
	index    treeIndex // for searching a finished scan
	closed   bool
	roots    []string     // the folders or drives a local scan started from
	clutter  *clutterPlan // what Clean up can offer, found when a local scan finishes
	edits    treeEdits    // what was deleted since the scan finished
}

func newTree(rootPath string) *Tree {
	root := &Node{Name: rootPath, Directory: true}
	return &Tree{RootPath: rootPath, Root: root, dirs: []*Node{root}}
}

// addChild must be called with t.mu held for writing.
func (t *Tree) addChild(parent *Node, name string, directory bool, size int64) *Node {
	n := &Node{Name: name, Parent: parent, Directory: directory}
	parent.Children = append(parent.Children, n)
	if directory {
		n.id = len(t.dirs)
		t.dirs = append(t.dirs, n)
		return n
	}
	n.Size = size
	n.Files = 1
	for p := parent; p != nil; p = p.Parent {
		p.Size += size
		p.Files++
	}
	return n
}

// addEntries adds one folder's listing under parent in a single step: one allocation for
// all the new nodes and one walk up the ancestors for the totals, instead of one per file.
// It returns the new folder nodes in listing order. It must be called with t.mu held for
// writing.
func (t *Tree) addEntries(parent *Node, entries []localEntry) (subs []*Node, files, size int64) {
	nodes := make([]Node, len(entries))
	parent.Children = slices.Grow(parent.Children, len(entries))
	for i, e := range entries {
		n := &nodes[i]
		n.Name, n.Parent = e.name, parent
		if e.kind == localDir {
			n.Directory = true
			n.id = len(t.dirs)
			t.dirs = append(t.dirs, n)
			subs = append(subs, n)
		} else {
			n.Size, n.Files = e.size, 1
			files++
			size += e.size
		}
		parent.Children = append(parent.Children, n)
	}
	for p := parent; p != nil; p = p.Parent {
		p.Size += size
		p.Files += int(files)
	}
	return subs, files, size
}

// addOwnSize counts the space a directory entry itself occupies, as du does. It must be
// called with t.mu held for writing.
func (t *Tree) addOwnSize(dir *Node, size int64) {
	for p := dir; p != nil; p = p.Parent {
		p.Size += size
	}
}

// markIncomplete must be called with t.mu held for writing. Ancestors of an incomplete
// node are always incomplete, so the walk can stop at the first one already marked.
func markIncomplete(n *Node) {
	for ; n != nil && !n.Incomplete; n = n.Parent {
		n.Incomplete = true
	}
}

func bySize(a, b *Node) int {
	if c := cmp.Compare(b.Size, a.Size); c != 0 {
		return c
	}
	return cmp.Compare(a.Name, b.Name)
}

// finalize sorts every directory's children by size, largest first.
func (t *Tree) finalize() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range t.dirs {
		slices.SortStableFunc(d.Children, bySize)
	}
}

// sortedChildren returns a sorted copy so readers never reorder a slice the scanner is appending to.
func sortedChildren(n *Node) []*Node {
	kids := slices.Clone(n.Children)
	slices.SortStableFunc(kids, bySize)
	return kids
}

// The folder report types are shared with the native server scanner.
type (
	Crumb    = dirview.Crumb
	DirEntry = dirview.Entry
	MapNode  = dirview.MapNode
	DirView  = dirview.View
)

const (
	viewEntryLimit = dirview.EntryLimit
	mapNodeLimit   = dirview.MapNodeLimit
	mapDetail      = dirview.MapDetail
)

// close releases the remote report, the cache file and the nodes when the results are
// replaced, cleared or the window closes.
func (t *Tree) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	s := t.store
	t.store, t.Root, t.dirs = nil, nil, nil
	t.closed = true
	t.mu.Unlock()
	if s != nil {
		s.close()
	}
	t.closeIndex()
}
