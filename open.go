package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"github.com/pkg/sftp"
)

// Double-clicking a file opens it with the program the system uses for that type. Files on
// an SSH server are copied to this computer first, which the app does only for pictures,
// videos and music, after asking. Programs and scripts are run only after asking too.

type OpenRequest struct {
	Dir       int    `json:"dir"`
	Name      string `json:"name"`   // empty for the folder itself
	Action    string `json:"action"` // "open", "reveal" (show in the file manager) or "path"
	Confirmed bool   `json:"confirmed,omitempty"`
	// Path, when set, is the folder on this computer holding Name, for results of a search
	// of every drive, which are not part of a scan.
	Path string `json:"path,omitempty"`
}

type OpenStatus struct {
	Seq     int    `json:"seq"`
	Running bool   `json:"running"`
	Action  string `json:"action,omitempty"`
	Name    string `json:"name,omitempty"`
	Path    string `json:"path,omitempty"`
	// Confirm is "run" for a program or "download" for a server file: the page asks, then
	// starts the same request again with Confirmed set.
	Confirm     string `json:"confirm,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Downloading bool   `json:"downloading,omitempty"`
	Done        int64  `json:"done,omitempty"`
	Total       int64  `json:"total,omitempty"`
	Cancelled   bool   `json:"cancelled,omitempty"`
	Error       string `json:"error,omitempty"`
}

var openState struct {
	sync.Mutex
	seq    int
	status OpenStatus
	cancel context.CancelFunc
	done   atomic.Int64
}

func startOpen(req OpenRequest) (int, error) {
	switch req.Action {
	case "open", "reveal", "path":
	default:
		return 0, errors.New("Unknown action.")
	}
	scanState.Lock()
	tree := scanState.tree
	scanState.Unlock()
	if tree == nil && req.Path == "" {
		return 0, errors.New("No scan results yet.")
	}
	openState.Lock()
	defer openState.Unlock()
	if openState.status.Running {
		if openState.status.Downloading {
			return 0, fmt.Errorf("%s is still downloading. Cancel it first.", openState.status.Name)
		}
		return 0, errors.New("Still opening the previous item.")
	}
	openState.seq++
	seq := openState.seq
	ctx, cancel := context.WithCancel(context.Background())
	openState.cancel = cancel
	openState.status = OpenStatus{Seq: seq, Running: true, Action: req.Action, Name: req.Name}
	openState.done.Store(0)
	go func() {
		defer cancel()
		s := runOpen(ctx, tree, req)
		s.Seq, s.Action, s.Running = seq, req.Action, false
		if s.Name == "" {
			s.Name = req.Name
		}
		if errors.Is(ctx.Err(), context.Canceled) && s.Error != "" {
			s.Error, s.Cancelled = "", true
		}
		openState.Lock()
		if s.Downloading || openState.status.Downloading {
			s.Done, s.Total = openState.done.Load(), openState.status.Total
		}
		s.Downloading = false
		openState.status = s
		openState.cancel = nil
		openState.Unlock()
	}()
	return seq, nil
}

func openStatus() OpenStatus {
	openState.Lock()
	defer openState.Unlock()
	s := openState.status
	if s.Downloading {
		s.Done = openState.done.Load()
	}
	return s
}

func cancelOpen() {
	openState.Lock()
	defer openState.Unlock()
	if openState.cancel != nil {
		openState.cancel()
	}
}

func runOpen(ctx context.Context, tree *Tree, req OpenRequest) OpenStatus {
	var it treeItem
	var err error
	if req.Path != "" {
		it, err = pathItem(req.Path, req.Name)
	} else {
		it, err = tree.item(req.Dir, req.Name)
	}
	if err != nil {
		return OpenStatus{Error: err.Error()}
	}
	s := OpenStatus{Name: it.name, Path: it.path, Size: it.size}
	fail := func(err error) OpenStatus { s.Error = err.Error(); return s }
	if req.Action == "path" {
		return s
	}
	if it.remote == nil && req.Path == "" && !tree.local {
		return fail(errors.New("This scan did not keep its connection to the server, so its files cannot be opened. Scan again with Python 3 or the fast server scan available on the server."))
	}
	if it.remote != nil {
		if req.Action == "reveal" {
			return fail(errors.New("Only files on this computer can be shown in a folder window."))
		}
		if it.dir {
			return fail(errors.New("Folders on a server cannot be opened here. Click the folder to browse it."))
		}
		switch search.KindOf(it.name) {
		case search.Image, search.Video, search.Audio:
		default:
			return fail(errors.New("Only pictures, videos and music on a server can be opened. They are copied to this computer first."))
		}
		if !req.Confirmed {
			s.Confirm = "download"
			return s
		}
		openState.Lock()
		openState.status.Downloading, openState.status.Total, openState.status.Path = true, it.size, it.path
		openState.Unlock()
		s.Downloading = true
		local, err := downloadRemote(ctx, it.remote, it.path)
		if err != nil {
			return fail(err)
		}
		if err := launch(local, false); err != nil {
			return fail(fmt.Errorf("The file was copied to %s but could not be opened: %w", local, err))
		}
		s.Path = local
		return s
	}
	info, err := os.Stat(it.path)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(fmt.Errorf("%s no longer exists.", it.path))
		}
		return fail(err)
	}
	if req.Action == "open" && !req.Confirmed && isProgram(it.path, info) {
		s.Confirm = "run"
		return s
	}
	if err := launch(it.path, req.Action == "reveal"); err != nil {
		return fail(err)
	}
	return s
}

// programTypes are opened by running them, so the app asks first.
// launch is launchPath; tests replace it so nothing is opened.
var launch = launchPath

var programTypes = map[string]bool{}

func init() {
	for _, e := range strings.Fields("exe com bat cmd msi msp msc ps1 psm1 vbs vbe js jse wsf wsh scr pif lnk url reg hta cpl jar inf application appref-ms gadget scf sh bash zsh csh ksh command tool app appimage run desktop bin out py pyw pl rb") {
		programTypes[e] = true
	}
}

func isProgram(p string, info os.FileInfo) bool {
	ext := search.Ext(filepath.Base(p))
	if info.IsDir() {
		return runtime.GOOS == "darwin" && ext == "app" // an app bundle runs when opened
	}
	if programTypes[ext] {
		return true
	}
	// On Linux and macOS an executable file without a document type runs when opened.
	return runtime.GOOS != "windows" && info.Mode()&0o111 != 0 && search.KindOf(filepath.Base(p)) == search.Other
}

type treeItem struct {
	name   string
	path   string
	dir    bool
	size   int64
	remote *remoteReport
}

var errItemGone = errors.New("That item is not in the scan results.")

// item finds a folder, or a named entry in it, and its full path on the scanned machine.
func (t *Tree) item(dir int, name string) (treeItem, error) {
	if name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || (t.local && runtime.GOOS == "windows" && strings.Contains(name, `\`)) {
		return treeItem{}, errItemGone
	}
	t.mu.RLock()
	r, d := t.remote, t.disk
	if r != nil {
		t.mu.RUnlock()
		v, err := r.view(dir)
		if err != nil {
			return treeItem{}, err
		}
		var trail []string
		for _, c := range v.Trail {
			trail = append(trail, c.Name)
		}
		it := treeItem{name: name, dir: name == "", size: v.Size, remote: r}
		if name == "" {
			it.name = trail[len(trail)-1]
		} else {
			it.size = 0
			trail = append(trail, name)
			// Folders with more than 500 entries list only the largest, so a missing one
			// is taken to be a file.
			if i := slices.IndexFunc(v.Entries, func(e DirEntry) bool { return e.Name == name }); i >= 0 {
				it.dir, it.size = v.Entries[i].Directory, v.Entries[i].Size
			}
		}
		it.path = t.joinPath(trail)
		return it, nil
	}
	defer t.mu.RUnlock()
	var trail []string
	it := treeItem{name: name}
	if d != nil {
		dd, err := d.read(dir, name != "")
		if err != nil {
			return treeItem{}, err
		}
		trail = append(trail, dd.name)
		for p := dd.parent; p >= 0; {
			pd, err := d.read(p, false)
			if err != nil {
				return treeItem{}, err
			}
			trail = append(trail, pd.name)
			p = pd.parent
		}
		slices.Reverse(trail)
		if name == "" {
			it.name, it.dir, it.size = dd.name, true, dd.size
		} else {
			i := slices.IndexFunc(dd.kids, func(e diskEntry) bool { return e.name == name })
			if i < 0 {
				return treeItem{}, errItemGone
			}
			it.dir, it.size = dd.kids[i].directory, dd.kids[i].size
		}
	} else {
		if dir < 0 || dir >= len(t.dirs) {
			return treeItem{}, errItemGone
		}
		n := t.dirs[dir]
		for p := n; p != nil; p = p.Parent {
			trail = append(trail, p.Name)
		}
		slices.Reverse(trail)
		if name == "" {
			it.name, it.dir, it.size = n.Name, true, n.Size
		} else {
			i := slices.IndexFunc(n.Children, func(c *Node) bool { return c.Name == name })
			if i < 0 {
				return treeItem{}, errItemGone
			}
			it.dir, it.size = n.Children[i].Directory, n.Children[i].Size
		}
	}
	if name != "" {
		trail = append(trail, name)
	}
	it.path = t.joinPath(trail)
	return it, nil
}

// pathItem is an item on this computer given by the folder holding it and its name.
func pathItem(dir, name string) (treeItem, error) {
	if !filepath.IsAbs(dir) || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") || runtime.GOOS == "windows" && strings.Contains(name, `\`) {
		return treeItem{}, errItemGone
	}
	p := filepath.Clean(dir)
	if name != "" {
		p = filepath.Join(p, name)
	}
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return treeItem{}, fmt.Errorf("%s no longer exists.", p)
		}
		return treeItem{}, err
	}
	it := treeItem{name: name, path: p, dir: info.IsDir()}
	if it.name == "" {
		it.name = filepath.Base(p)
	}
	if !it.dir {
		it.size = info.Size()
	}
	return it, nil
}

// openedDir holds copies of server files opened on this computer. Each copy has a folder
// of its own so it keeps its name, which programs use to recognise it.
func openedDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "KamkmDirStat", "opened")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// Copies from earlier days are no longer in use.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
	return dir, nil
}

// localFileName makes a server file name safe to use on this computer.
func localFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		name = "file"
	}
	return name
}

type countingWriter struct {
	w    io.Writer
	done *atomic.Int64
}

func (c countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.done.Add(int64(n))
	return n, err
}

// downloadRemote copies a server file over SFTP, on the scan's SSH connection, with many
// reads in flight so that distant servers still copy quickly.
func downloadRemote(ctx context.Context, r *remoteReport, remotePath string) (string, error) {
	if r.closed.Load() {
		return "", errors.New("The connection to the server closed. Run a new scan to open its files.")
	}
	client, err := sftp.NewClient(r.client, sftp.UseConcurrentReads(true), sftp.MaxConcurrentRequestsPerFile(64))
	if err != nil {
		return "", fmt.Errorf("The server does not allow copying files (SFTP): %w", err)
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	src, err := client.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if info, err := src.Stat(); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("Only regular files can be opened.")
		}
		openState.Lock()
		openState.status.Total = info.Size()
		openState.Unlock()
	}
	base, err := openedDir()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, "copy-")
	if err != nil {
		return "", err
	}
	local := filepath.Join(dir, localFileName(path.Base(remotePath)))
	dst, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	_, err = src.WriteTo(countingWriter{dst, &openState.done})
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("copying %s: %w", path.Base(remotePath), err)
	}
	return local, nil
}
