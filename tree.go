package main

import (
	"cmp"
	"errors"
	"slices"
	"sync"
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
	remote   *remoteReport
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

type Crumb struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type DirEntry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Directory  bool   `json:"dir,omitempty"`
	ID         int    `json:"id,omitempty"`
	Files      int    `json:"files,omitempty"`
	Incomplete bool   `json:"incomplete,omitempty"`
}

// MapNode is the treemap data. Short keys keep the payload small; O is the combined size of
// children too small to draw.
type MapNode struct {
	N string     `json:"n"`
	S int64      `json:"s"`
	D bool       `json:"d,omitempty"`
	I int        `json:"i,omitempty"`
	C []*MapNode `json:"c,omitempty"`
	O int64      `json:"o,omitempty"`
}

type DirView struct {
	ID         int        `json:"id"`
	Trail      []Crumb    `json:"trail"`
	Size       int64      `json:"size"`
	Files      int        `json:"files"`
	Items      int        `json:"items"`
	Incomplete bool       `json:"incomplete,omitempty"`
	Entries    []DirEntry `json:"entries"`
	MoreCount  int        `json:"moreCount,omitempty"`
	MoreSize   int64      `json:"moreSize,omitempty"`
	Map        *MapNode   `json:"map"`
}

const (
	viewEntryLimit = 500
	mapNodeLimit   = 12000
	mapDetail      = 4000 // entries smaller than 1/mapDetail of the folder are merged
)

func (t *Tree) view(id int) (DirView, error) {
	t.mu.RLock()
	if t.remote != nil {
		r := t.remote
		t.mu.RUnlock()
		return r.view(id)
	}
	defer t.mu.RUnlock()
	if id < 0 || id >= len(t.dirs) {
		return DirView{}, errors.New("That folder is not in the scan results.")
	}
	n := t.dirs[id]
	v := DirView{ID: id, Size: n.Size, Files: n.Files, Items: len(n.Children), Incomplete: n.Incomplete, Entries: []DirEntry{}}
	for p := n; p != nil; p = p.Parent {
		v.Trail = append(v.Trail, Crumb{ID: p.id, Name: p.Name})
	}
	slices.Reverse(v.Trail)
	kids := sortedChildren(n)
	for i, c := range kids {
		if i >= viewEntryLimit {
			v.MoreCount++
			v.MoreSize += c.Size
			continue
		}
		e := DirEntry{Name: c.Name, Size: c.Size, Directory: c.Directory, Files: c.Files, Incomplete: c.Incomplete}
		if c.Directory {
			e.ID = c.id
		}
		v.Entries = append(v.Entries, e)
	}
	v.Map = buildMap(n, kids)
	return v, nil
}

// buildMap walks breadth first so shallow levels get detail before deep ones when the node
// budget runs out.
func buildMap(n *Node, kids []*Node) *MapNode {
	root := &MapNode{N: n.Name, S: n.Size, D: true, I: n.id}
	threshold := max(n.Size/mapDetail, 1)
	type item struct {
		node *Node
		m    *MapNode
		kids []*Node
	}
	queue := []item{{n, root, kids}}
	budget := mapNodeLimit
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		ks := it.kids
		if ks == nil {
			ks = sortedChildren(it.node)
		}
		for _, c := range ks {
			if c.Size < threshold || budget <= 0 {
				it.m.O += c.Size
				continue
			}
			budget--
			cm := &MapNode{N: c.Name, S: c.Size, D: c.Directory}
			if c.Directory {
				cm.I = c.id
				if len(c.Children) > 0 {
					queue = append(queue, item{node: c, m: cm})
				}
			}
			it.m.C = append(it.m.C, cm)
		}
	}
	return root
}

// close releases the remote in-memory report when it is replaced or the window closes.
func (t *Tree) close() {
	if t == nil {
		return
	}
	t.mu.RLock()
	r := t.remote
	t.mu.RUnlock()
	if r != nil {
		r.close()
	}
}
