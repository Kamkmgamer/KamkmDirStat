package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

func searchTree(t *testing.T, tree *Tree, q search.Query) search.Result {
	t.Helper()
	r, err := tree.search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hitNames(r search.Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Name)
	}
	return out
}

// A local tree is searchable in memory, after it moves to its cache file, and after the
// idle index leaves memory; items resolve to real paths throughout.
func TestTreeSearchAndItems(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	root := t.TempDir()
	for _, p := range []string{"Videos/Holiday 2024.mp4", "Videos/clip.mkv", "Docs/report.pdf", "setup.exe"} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tree := newLocalTree([]string{root})
	if _, err := localScanInto(context.Background(), []string{root}, tree, nil); err != nil {
		t.Fatal(err)
	}

	check := func(stage string) {
		t.Helper()
		r := searchTree(t, tree, search.Query{Text: "holiday"})
		if len(r.Hits) != 1 || r.Hits[0].Path != filepath.Join(root, "Videos") {
			t.Fatalf("%s: search %+v", stage, r.Hits)
		}
		h := r.Hits[0]
		it, err := tree.item(h.Parent, h.Name)
		if err != nil || it.path != filepath.Join(root, "Videos", "Holiday 2024.mp4") || it.dir || it.size != int64(len("Videos/Holiday 2024.mp4")) {
			t.Fatalf("%s: item %+v, %v", stage, it, err)
		}
		if got := hitNames(searchTree(t, tree, search.Query{Kind: "videos"})); !reflect.DeepEqual(got, []string{"Holiday 2024.mp4", "clip.mkv"}) {
			t.Fatalf("%s: videos %v", stage, got)
		}
		r = searchTree(t, tree, search.Query{Text: "docs", Kind: "folders"})
		if len(r.Hits) != 1 || !r.Hits[0].Dir {
			t.Fatalf("%s: folder %+v", stage, r.Hits)
		}
		if it, err := tree.item(r.Hits[0].ID, ""); err != nil || it.path != filepath.Join(root, "Docs") || !it.dir {
			t.Fatalf("%s: folder item %+v, %v", stage, it, err)
		}
		if _, err := tree.item(0, "missing.txt"); err == nil {
			t.Fatalf("%s: unknown names must not resolve", stage)
		}
		if _, err := tree.item(0, ".."); err == nil {
			t.Fatalf("%s: .. must not resolve", stage)
		}
	}
	check("in memory")

	indexIdle = 50 * time.Millisecond
	defer func() { indexIdle = 90 * time.Second }()
	if onDisk, err := tree.releaseMemory(); err != nil || !onDisk {
		t.Fatalf("releaseMemory = %v, %v", onDisk, err)
	}
	check("on disk")
	file := tree.index.file
	if file == "" {
		t.Fatal("index not saved")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tree.index.mu.Lock()
		gone := tree.index.idx == nil
		tree.index.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle index stayed in memory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	check("read back")

	tree.close()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("index file kept: %v", err)
	}
	if _, err := tree.search(context.Background(), search.Query{Text: "a"}); err == nil {
		t.Fatal("closed tree searched")
	}
}

func TestJoinPath(t *testing.T) {
	remote := newTree("/srv")
	if got := remote.joinPath([]string{"/srv", "a", "b.txt"}); got != "/srv/a/b.txt" {
		t.Fatal(got)
	}
	if got := remote.joinPath([]string{"/", "etc"}); got != "/etc" {
		t.Fatal(got)
	}
	if runtime.GOOS != "windows" {
		return
	}
	many := newLocalTree([]string{`C:\`, `D:\`})
	if got := many.joinPath([]string{"This PC", `D:\`, "Games", "x.iso"}); got != `D:\Games\x.iso` {
		t.Fatal(got)
	}
	if got := many.joinPath([]string{"This PC", `C:\`}); got != `C:\` {
		t.Fatal(got)
	}
}

func TestProgramsNeedConfirmation(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, mode os.FileMode) os.FileInfo {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, mode); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(p)
		return info
	}
	for _, name := range []string{"setup.EXE", "run.bat", "deploy.ps1", "x.lnk", "a.sh"} {
		if !isProgram(filepath.Join(dir, name), write(name, 0o644)) {
			t.Errorf("%s should ask first", name)
		}
	}
	for _, name := range []string{"movie.mp4", "notes.txt", "photo.JPG", "Makefile"} {
		if isProgram(filepath.Join(dir, name), write(name, 0o644)) {
			t.Errorf("%s should open directly", name)
		}
	}
	if got := localFileName(`a:b*c?.mp4`); got != "a_b_c_.mp4" {
		t.Errorf("local name %q", got)
	}
	if got := localFileName(" . "); got != "file" {
		t.Errorf("local name %q", got)
	}
}

func TestSearchJobKeepsLatest(t *testing.T) {
	tree := newTree("/data")
	tree.mu.Lock()
	tree.addChild(tree.Root, "alpha.txt", false, 5)
	tree.addChild(tree.Root, "beta.txt", false, 7)
	tree.mu.Unlock()
	tree.finalize()
	scanState.Lock()
	old := scanState.tree
	scanState.tree, scanState.status = tree, ScanStatus{}
	scanState.Unlock()
	defer func() {
		scanState.Lock()
		scanState.tree = old
		scanState.Unlock()
	}()
	var last int
	for _, q := range []string{"a", "al", "bet"} {
		seq, err := startSearch(search.Query{Text: q})
		if err != nil {
			t.Fatal(err)
		}
		last = seq
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := searchStatus()
		if s.Seq == last {
			if s.Error != "" || s.Result == nil || !reflect.DeepEqual(hitNames(*s.Result), []string{"beta.txt"}) {
				t.Fatalf("latest search: %+v", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no result for search %d: %+v", last, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Results of a search of every drive are opened by their folder's path, with no scan.
func TestOpenByPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	it, err := pathItem(dir, "a.txt")
	if err != nil || it.path != filepath.Join(dir, "a.txt") || it.size != 5 || it.dir || it.name != "a.txt" {
		t.Fatalf("item %+v %v", it, err)
	}
	if it, err := pathItem(dir, ""); err != nil || !it.dir || it.path != dir {
		t.Fatalf("folder %+v %v", it, err)
	}
	for _, bad := range [][2]string{{"relative", "a.txt"}, {dir, ".."}, {dir, "x/../../a.txt"}, {dir, "missing.txt"}} {
		if _, err := pathItem(bad[0], bad[1]); err == nil {
			t.Errorf("%q %q resolved", bad[0], bad[1])
		}
	}
	scanState.Lock()
	old := scanState.tree
	scanState.tree = nil
	scanState.Unlock()
	defer func() {
		scanState.Lock()
		scanState.tree = old
		scanState.Unlock()
	}()
	seq, err := startOpen(OpenRequest{Path: dir, Name: "a.txt", Action: "path"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := openStatus()
		if s.Seq == seq && !s.Running {
			if s.Error != "" || s.Path != filepath.Join(dir, "a.txt") {
				t.Fatalf("status %+v", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("open did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := startOpen(OpenRequest{Name: "a.txt", Action: "path"}); err == nil {
		t.Fatal("opened by scan with no scan")
	}
}

// A server scanner from before search gets a clear message, and no request is sent to it.
func TestOldServerScannerCannotSearch(t *testing.T) {
	r := &remoteReport{} // no connection: sending anything would panic
	if _, err := r.search(search.Query{Text: "a"}); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("err = %v", err)
	}
}
