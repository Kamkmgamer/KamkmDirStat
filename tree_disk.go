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
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/dirview"
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
	mu      sync.RWMutex // held for reading while a folder is read, for writing to close
	closed  bool
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
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
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
	if d.closed || id < 0 || id+1 >= len(d.offsets) {
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

// trail returns folder id, whose record is n, and its ancestors, from the scan root down.
// The caller holds d.mu.
func (d *diskTree) trail(id int, n diskDir) ([]Crumb, error) {
	trail := []Crumb{{ID: id, Name: n.name}}
	for p := n.parent; p >= 0; {
		pd, err := d.read(p, false)
		if err != nil {
			return nil, err
		}
		trail = append(trail, Crumb{ID: p, Name: pd.name})
		p = pd.parent
	}
	slices.Reverse(trail)
	return trail, nil
}

func (d *diskTree) item(id int, name string) (storeItem, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	n, err := d.read(id, name != "")
	if err != nil {
		return storeItem{}, err
	}
	trail, err := d.trail(id, n)
	if err != nil {
		return storeItem{}, err
	}
	it := storeItem{dir: true, size: n.size}
	for _, c := range trail {
		it.trail = append(it.trail, c.Name)
	}
	if name != "" {
		i := slices.IndexFunc(n.kids, func(e diskEntry) bool { return e.name == name })
		if i < 0 {
			return storeItem{}, errItemGone
		}
		it.dir, it.size, it.files = n.kids[i].directory, n.kids[i].size, max(1, n.kids[i].files)
		it.trail = append(it.trail, name)
	}
	return it, nil
}

func (d *diskTree) ancestors(id int) []int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var ids []int
	for id >= 0 {
		ids = append(ids, id)
		n, err := d.read(id, false)
		if err != nil {
			break
		}
		id = n.parent
	}
	return ids
}

func (d *diskTree) view(id int) (DirView, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	n, err := d.read(id, true)
	if err != nil {
		return DirView{}, err
	}
	trail, err := d.trail(id, n)
	if err != nil {
		return DirView{}, err
	}
	return dirview.Build[diskEntry](diskSource{d}, trail, n.diskEntry, n.kids)
}

// diskSource reads folders from the cache file as a view needs them. The caller holds d.mu.
type diskSource struct{ d *diskTree }

func (s diskSource) Item(e diskEntry) dirview.Item {
	return dirview.Item{Name: e.name, Size: e.size, Files: e.files, Directory: e.directory, ID: e.id, Incomplete: e.incomplete}
}

func (s diskSource) Children(e diskEntry) ([]diskEntry, error) {
	dd, err := s.d.read(e.id, true)
	return dd.kids, err
}

// releaseMemory moves a finished local scan to the cache file and frees its nodes. It
// leaves the tree in memory when the file cannot be written, and reports whether the tree
// now lives on disk.
func (t *Tree) releaseMemory() (bool, error) {
	t.mu.RLock()
	if _, ok := t.store.(*diskTree); ok {
		t.mu.RUnlock()
		return true, nil
	}
	if t.store != nil || t.Root == nil {
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
	t.store, t.Root, t.dirs = d, nil, nil
	t.mu.Unlock()
	t.keepIndex(idx, indexFile)
	freeMemory()
	return true, nil
}

// freeMemory collects garbage now and hands the freed pages back to the operating system
// instead of keeping them for later allocations.
func freeMemory() { debug.FreeOSMemory() }
