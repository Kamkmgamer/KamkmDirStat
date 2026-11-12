//go:build linux

// kamkm-helper scans a folder on a Linux server for KamkmDirStat. It speaks the same
// protocol as remote_report.py: a "SSE2 gzip" line, then gzip-compressed JSON frames with
// progress and the finished overview, then one folder view for each request read from
// standard input. It walks folders on every core, which is several times faster than find
// and Python. The app stores it in ~/.cache/kamkmdirstat on the server.
//
// Usage: kamkm-helper report ROOT ONE_FILE_SYSTEM(0 or 1)
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type node struct {
	name       string
	parent     *node
	children   []*node
	size       int64
	files      int
	id         int
	dir        bool
	incomplete bool
}

type progress struct {
	Mode        string `json:"mode"`
	Files       int64  `json:"files"`
	Directories int64  `json:"directories"`
	Entries     int64  `json:"entries"`
	Bytes       int64  `json:"bytes"`
	Expected    int64  `json:"expected,omitempty"`
}

var out = bufio.NewWriterSize(os.Stdout, 64<<10)

func send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var buf bytes.Buffer
	z, _ := gzip.NewWriterLevel(&buf, 3)
	z.Write(data)
	z.Close()
	binary.Write(out, binary.BigEndian, uint32(buf.Len()))
	out.Write(buf.Bytes())
	if err := out.Flush(); err != nil {
		os.Exit(0) // the app went away
	}
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "report" {
		fmt.Fprintln(os.Stderr, "usage: kamkm-helper report ROOT 0|1")
		os.Exit(2)
	}
	root, oneFS := os.Args[2], os.Args[3] == "1"
	fmt.Fprintln(out, "SSE2 gzip")
	out.Flush()
	var st unix.Stat_t
	if unix.Stat(root, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		send(map[string]string{"type": "error", "error": "The remote path must be a directory."})
		return
	}
	w := &walker{oneFS: oneFS, device: st.Dev, links: map[[2]uint64][]linkedName{}}
	w.root = &node{name: root, dir: true, size: st.Blocks * 512}
	w.dirs = []*node{w.root}
	w.entries.Add(1)
	w.bytes.Add(w.root.size)
	if oneFS && isMountPoint(root, st.Dev) {
		var fs unix.Statfs_t
		if unix.Statfs(root, &fs) == nil && fs.Files > fs.Ffree {
			w.expected = int64(fs.Files - fs.Ffree)
		}
	}
	send(map[string]any{"type": "progress", "progress": w.progress()})
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				send(map[string]any{"type": "progress", "progress": w.progress()})
			}
		}
	}()
	w.walk(root)
	close(done)
	<-stopped // no progress may follow the finished report
	w.finish()
	send(map[string]any{"type": "done", "view": w.view(0), "progress": w.progress(), "errors": w.errors, "errorCount": w.errorCount})

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 4096), 4096)
	for in.Scan() {
		var request struct {
			Type string `json:"type"`
			ID   *int   `json:"id"`
		}
		if json.Unmarshal(in.Bytes(), &request) != nil || request.Type == "close" {
			return
		}
		if request.ID == nil || *request.ID < 0 || *request.ID >= len(w.dirs) {
			send(map[string]string{"type": "error", "error": "That folder is not in the scan results."})
			continue
		}
		send(map[string]any{"type": "view", "view": w.view(*request.ID)})
	}
}

func isMountPoint(p string, device uint64) bool {
	if p == "/" {
		return true
	}
	var parent unix.Stat_t
	return unix.Lstat(path.Dir(path.Clean(p)), &parent) == nil && parent.Dev != device
}

type walker struct {
	root     *node
	oneFS    bool
	device   uint64
	expected int64

	mu    sync.Mutex
	dirs  []*node
	links map[[2]uint64][]linkedName // files with several names, counted once in finish

	errMu      sync.Mutex
	errors     []string
	errorCount int

	files, directories, entries, bytes atomic.Int64
}

func (w *walker) progress() progress {
	return progress{Mode: "remote", Files: w.files.Load(), Directories: w.directories.Load(), Entries: w.entries.Load(),
		Bytes: w.bytes.Load(), Expected: w.expected}
}

func (w *walker) fail(n *node, p string, err error) {
	message := err.Error()
	if errno, ok := err.(unix.Errno); ok {
		message = errno.Error()
	}
	if message != "" {
		message = strings.ToUpper(message[:1]) + message[1:]
	}
	w.errMu.Lock()
	w.errorCount++
	if len(w.errors) < 200 {
		w.errors = append(w.errors, fmt.Sprintf("find: '%s': %s", p, message))
	}
	w.errMu.Unlock()
	w.mu.Lock()
	for ; n != nil && !n.incomplete; n = n.parent {
		n.incomplete = true
	}
	w.mu.Unlock()
}

// linkedName is one name of a file with several hard links.
type linkedName struct {
	n    *node
	path string
}

type job struct {
	n    *node
	p    string
	root bool // the scan root may be a symbolic link, as with find -H
}

// walk lists folders with one worker per core, taking the newest folder first.
func (w *walker) walk(root string) {
	workers := min(32, max(4, runtime.NumCPU()))
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stack := []job{{w.root, root, true}}
	active := 0
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(stack) == 0 && active > 0 {
					cond.Wait()
				}
				if len(stack) == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				active++
				mu.Unlock()
				subs := w.visit(j)
				mu.Lock()
				stack = append(stack, subs...)
				active--
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
}

// visit lists one folder and returns its subfolders to visit.
func (w *walker) visit(j job) []job {
	n, p := j.n, j.p
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	if !j.root {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Open(p, flags, 0)
	if err != nil {
		w.fail(n, p, err)
		return nil
	}
	f := os.NewFile(uintptr(fd), p)
	names, err := f.Readdirnames(-1)
	if err != nil {
		w.fail(n, p, err)
	}
	var subs []job
	var newDirs, linked []*node
	var linkKeys [][2]uint64
	var linkPaths []string
	var files, size int64
	for _, name := range names {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			// A folder that may be listed but not entered: its entries cannot be measured,
			// which find reports for each one.
			if err != unix.ENOENT {
				w.fail(n, joinPath(p, name), err)
			}
			continue
		}
		w.entries.Add(1)
		kind := st.Mode & unix.S_IFMT
		if kind == unix.S_IFLNK {
			continue // links are not followed or counted
		}
		child := &node{name: cleanName(name), parent: n, size: st.Blocks * 512}
		n.children = append(n.children, child)
		if kind == unix.S_IFDIR {
			child.dir = true
			newDirs = append(newDirs, child)
			if !w.oneFS || st.Dev == w.device {
				subs = append(subs, job{n: child, p: joinPath(p, name)})
			}
			w.bytes.Add(child.size)
			continue
		}
		child.files = 1
		if kind == unix.S_IFREG && st.Nlink > 1 {
			linked = append(linked, child)
			linkKeys = append(linkKeys, [2]uint64{st.Dev, st.Ino})
			linkPaths = append(linkPaths, joinPath(p, name))
			continue
		}
		files++
		size += child.size
	}
	f.Close()
	w.mu.Lock()
	for _, d := range newDirs {
		d.id = len(w.dirs)
		w.dirs = append(w.dirs, d)
	}
	for i, l := range linked {
		w.links[linkKeys[i]] = append(w.links[linkKeys[i]], linkedName{l, linkPaths[i]})
	}
	w.mu.Unlock()
	files += int64(len(linked)) // their sizes are added in finish
	n.files += int(files)
	n.size += size
	w.files.Add(files)
	w.directories.Add(int64(len(newDirs)))
	w.bytes.Add(size)
	return subs
}

func joinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// cleanName replaces each byte that is not valid UTF-8, as the Python report does.
func cleanName(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// finish sorts every folder by size and adds each folder's totals to its parent. Folders
// are numbered after their parents, so a reverse pass sums each subtree once.
func (w *walker) finish() {
	if w.errorCount > 0 {
		w.root.incomplete = true
	}
	// A file with several names is counted once, at its alphabetically first path, so the
	// result does not depend on the order the folders were read in.
	for _, names := range w.links {
		first := 0
		for i := range names {
			if names[i].path < names[first].path {
				first = i
			}
		}
		for i, l := range names {
			if i != first {
				l.n.size = 0
				continue
			}
			l.n.parent.size += l.n.size
			w.bytes.Add(l.n.size)
		}
	}
	for i := len(w.dirs) - 1; i >= 0; i-- {
		d := w.dirs[i]
		slices.SortFunc(d.children, func(a, b *node) int {
			if c := cmp.Compare(b.size, a.size); c != 0 {
				return c
			}
			return strings.Compare(a.name, b.name)
		})
		if d.parent != nil {
			d.parent.size += d.size
			d.parent.files += d.files
		}
	}
}

type crumb struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type entry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Dir        bool   `json:"dir"`
	ID         int    `json:"id"`
	Files      int    `json:"files"`
	Incomplete bool   `json:"incomplete"`
}

type mapNode struct {
	N string     `json:"n"`
	S int64      `json:"s"`
	D bool       `json:"d,omitempty"`
	I int        `json:"i,omitempty"`
	C []*mapNode `json:"c,omitempty"`
	O int64      `json:"o,omitempty"`
}

type dirView struct {
	ID         int      `json:"id"`
	Trail      []crumb  `json:"trail"`
	Size       int64    `json:"size"`
	Files      int      `json:"files"`
	Items      int      `json:"items"`
	Incomplete bool     `json:"incomplete"`
	Entries    []entry  `json:"entries"`
	MoreCount  int      `json:"moreCount"`
	MoreSize   int64    `json:"moreSize"`
	Map        *mapNode `json:"map"`
}

// view is one folder's report: its largest 500 entries and a treemap of what is beneath
// it, built breadth first with the same limits as the Python report and the app.
func (w *walker) view(id int) dirView {
	n := w.dirs[id]
	v := dirView{ID: id, Size: n.size, Files: n.files, Items: len(n.children), Incomplete: n.incomplete, Entries: []entry{}}
	for p := n; p != nil; p = p.parent {
		v.Trail = append(v.Trail, crumb{p.id, p.name})
	}
	slices.Reverse(v.Trail)
	for i, c := range n.children {
		if i >= 500 {
			v.MoreCount++
			v.MoreSize += c.size
			continue
		}
		v.Entries = append(v.Entries, entry{Name: c.name, Size: c.size, Dir: c.dir, ID: c.id, Files: c.files, Incomplete: c.incomplete})
	}
	root := &mapNode{N: n.name, S: n.size, D: true, I: n.id}
	threshold, budget := max(n.size/4000, 1), 12000
	type item struct {
		n *node
		m *mapNode
	}
	queue := []item{{n, root}}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		for _, c := range it.n.children {
			if c.size < threshold || budget <= 0 {
				it.m.O += c.size
				continue
			}
			budget--
			m := &mapNode{N: c.name, S: c.size}
			if c.dir {
				m.D, m.I = true, c.id
				if len(c.children) > 0 {
					queue = append(queue, item{c, m})
				}
			}
			it.m.C = append(it.m.C, m)
		}
	}
	v.Map = root
	return v
}
