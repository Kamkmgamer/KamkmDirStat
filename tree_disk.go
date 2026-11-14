package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// A finished scan of a full disk holds millions of nodes, which costs gigabytes of RAM.
// Once the scan stops nothing changes any more, so the tree is written to a cache file
// and dropped from memory. The window reads one folder at a time, so each view reads
// only the few records it needs; the operating system's file cache keeps recent ones fast.
//
// The file holds one record per folder, in folder id order:
//
//	parent id + 1 (0 for the root), size, files, incomplete flag, name,
//	child count, then for each child (already sorted by size):
//	flags (1 = folder, 2 = incomplete), size, files, folder id if a folder, name.
type diskTree struct {
	file    *os.File
	path    string
	offsets []int64 // offsets[id] .. offsets[id+1] is folder id's record
	size    int64
	files   int
}

type diskEntry struct {
	name       string
	size       int64
	files      int
	directory  bool
	incomplete bool
	id         int
}

type diskDir struct {
	diskEntry
	parent int // -1 for the root
	kids   []diskEntry
}

var diskTreeSeq atomic.Int64

func diskCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "KamkmDirStat", "scans")
	return dir, os.MkdirAll(dir, 0o700)
}

// removeStaleScanFiles deletes cache files left behind by an earlier run that did not exit
// cleanly. Files another running window may still use are recent, so only old ones go.
func removeStaleScanFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && (strings.HasSuffix(e.Name(), ".scan") || strings.HasSuffix(e.Name(), ".idx")) && time.Since(info.ModTime()) > 7*24*time.Hour {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendEntry(b []byte, n *Node) []byte {
	var flags byte
	if n.Directory {
		flags |= 1
	}
	if n.Incomplete {
		flags |= 2
	}
	b = append(b, flags)
	b = binary.AppendVarint(b, n.Size)
	b = binary.AppendUvarint(b, uint64(n.Files))
	if n.Directory {
		b = binary.AppendUvarint(b, uint64(n.id))
	}
	return appendString(b, n.Name)
}

// writeDiskTree writes the tree's folders to a new cache file. The caller must hold t.mu
// for reading and the tree must be finalized, so children are already sorted.
func writeDiskTree(t *Tree) (*diskTree, error) {
	dir, err := diskCacheDir()
	if err != nil {
		return nil, err
	}
	removeStaleScanFiles(dir)
	path := filepath.Join(dir, fmt.Sprintf("%d-%d-%d.scan", os.Getpid(), time.Now().UnixNano(), diskTreeSeq.Add(1)))
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	d := &diskTree{file: f, path: path, offsets: make([]int64, 0, len(t.dirs)+1), size: t.Root.Size, files: t.Root.Files}
	w := bufio.NewWriterSize(f, 1<<20)
	var off int64
	var buf []byte
	for _, n := range t.dirs {
		d.offsets = append(d.offsets, off)
		buf = buf[:0]
		parent := 0
		if n.Parent != nil {
			parent = n.Parent.id + 1
		}
		buf = binary.AppendUvarint(buf, uint64(parent))
		buf = appendEntry(buf, n)
		buf = binary.AppendUvarint(buf, uint64(len(n.Children)))
		for _, c := range n.Children {
			buf = appendEntry(buf, c)
		}
		if _, err := w.Write(buf); err != nil {
			d.close()
			return nil, err
		}
		off += int64(len(buf))
	}
	d.offsets = append(d.offsets, off)
	if err := w.Flush(); err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

func (d *diskTree) close() {
	if d == nil {
		return
	}
	_ = d.file.Close()
	_ = os.Remove(d.path)
}

type recordReader struct {
	b   []byte
	err error
}

func (r *recordReader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.err, r.b = errors.New("The scan cache file is damaged."), nil
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *recordReader) varint() int64 {
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.err, r.b = errors.New("The scan cache file is damaged."), nil
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *recordReader) entry() diskEntry {
	if len(r.b) == 0 {
		r.err = errors.New("The scan cache file is damaged.")
		return diskEntry{}
	}
	flags := r.b[0]
	r.b = r.b[1:]
	e := diskEntry{directory: flags&1 != 0, incomplete: flags&2 != 0}
	e.size = r.varint()
	e.files = int(r.uvarint())
	if e.directory {
		e.id = int(r.uvarint())
	}
	l := r.uvarint()
	if uint64(len(r.b)) < l {
		r.err, r.b = errors.New("The scan cache file is damaged."), nil
		return diskEntry{}
	}
	e.name = string(r.b[:l])
	r.b = r.b[l:]
	return e
}

func (d *diskTree) read(id int, withKids bool) (diskDir, error) {
	if id < 0 || id+1 >= len(d.offsets) {
		return diskDir{}, errors.New("That folder is not in the scan results.")
	}
	b := make([]byte, d.offsets[id+1]-d.offsets[id])
	if _, err := d.file.ReadAt(b, d.offsets[id]); err != nil && !errors.Is(err, io.EOF) {
		return diskDir{}, err
	}
	r := &recordReader{b: b}
	dd := diskDir{parent: int(r.uvarint()) - 1}
	dd.diskEntry = r.entry()
	if withKids {
		count := r.uvarint()
		if r.err == nil && count <= uint64(len(r.b)) {
			dd.kids = make([]diskEntry, 0, count)
			for i := uint64(0); i < count && r.err == nil; i++ {
				dd.kids = append(dd.kids, r.entry())
			}
		}
	}
	return dd, r.err
}

func (d *diskTree) view(id int) (DirView, error) {
	n, err := d.read(id, true)
	if err != nil {
		return DirView{}, err
	}
	v := DirView{ID: id, Size: n.size, Files: n.files, Items: len(n.kids), Incomplete: n.incomplete, Entries: []DirEntry{}}
	v.Trail = append(v.Trail, Crumb{ID: id, Name: n.name})
	for p := n.parent; p >= 0; {
		pd, err := d.read(p, false)
		if err != nil {
			return DirView{}, err
		}
		v.Trail = append(v.Trail, Crumb{ID: p, Name: pd.name})
		p = pd.parent
	}
	slices.Reverse(v.Trail)
	for i, c := range n.kids {
		if i >= viewEntryLimit {
			v.MoreCount++
			v.MoreSize += c.size
			continue
		}
		e := DirEntry{Name: c.name, Size: c.size, Directory: c.directory, Files: c.files, Incomplete: c.incomplete}
		if c.directory {
			e.ID = c.id
		}
		v.Entries = append(v.Entries, e)
	}
	v.Map, err = d.buildMap(id, n)
	return v, err
}

// buildMap matches Tree's buildMap, reading folders from the cache file as it goes.
func (d *diskTree) buildMap(id int, n diskDir) (*MapNode, error) {
	root := &MapNode{N: n.name, S: n.size, D: true, I: id}
	threshold := max(n.size/mapDetail, 1)
	type item struct {
		id   int
		m    *MapNode
		kids []diskEntry
		read bool
	}
	queue := []item{{id, root, n.kids, true}}
	budget := mapNodeLimit
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		ks := it.kids
		if !it.read {
			dd, err := d.read(it.id, true)
			if err != nil {
				return nil, err
			}
			ks = dd.kids
		}
		for _, c := range ks {
			if c.size < threshold || budget <= 0 {
				it.m.O += c.size
				continue
			}
			budget--
			cm := &MapNode{N: c.name, S: c.size, D: c.directory}
			if c.directory {
				cm.I = c.id
				queue = append(queue, item{id: c.id, m: cm})
			}
			it.m.C = append(it.m.C, cm)
		}
	}
	return root, nil
}

// releaseMemory moves a finished local scan to the cache file and frees its nodes. It
// leaves the tree in memory when the file cannot be written, and reports whether the tree
// now lives on disk.
func (t *Tree) releaseMemory() (bool, error) {
	t.mu.RLock()
	if t.disk != nil {
		t.mu.RUnlock()
		return true, nil
	}
	if t.remote != nil || t.Root == nil {
		t.mu.RUnlock()
		return false, nil
	}
	d, err := writeDiskTree(t)
	var idx *search.Index
	if err == nil {
		idx = buildIndex(t)
	}
	t.mu.RUnlock()
	if err != nil {
		return false, err
	}
	// The search index goes to its own file, so it too can leave memory when unused.
	indexFile, err := writeIndexFile(idx)
	if err != nil {
		d.close()
		return false, err
	}
	t.mu.Lock()
	if t.Root == nil { // closed while writing
		t.mu.Unlock()
		d.close()
		_ = os.Remove(indexFile)
		return false, nil
	}
	t.disk, t.Root, t.dirs = d, nil, nil
	t.mu.Unlock()
	t.keepIndex(idx, indexFile)
	freeMemory()
	return true, nil
}

// freeMemory collects garbage now and hands the freed pages back to the operating system
// instead of keeping them for later allocations.
func freeMemory() { debug.FreeOSMemory() }
