package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Kamkmgamer/KamkmDirStat/internal/ntfs"
)

// TestNTFSImage reads an image made by testdata/make_ntfs_image.sh and checks that the
// scan finds exactly what the mounted file system showed, with hard links counted once.
func TestNTFSImage(t *testing.T) {
	dir := os.Getenv("KAMKM_NTFS_IMAGE_DIR")
	if dir == "" {
		t.Skip("set KAMKM_NTFS_IMAGE_DIR to a folder made by testdata/make_ntfs_image.sh")
	}
	f, err := os.Open(filepath.Join(dir, "volume.img"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := ntfs.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	var lastRecords atomic.Int64
	idx, err := v.Read(context.Background(), func(records, _, _ int64) {
		// progress runs on several workers at once.
		if records > lastRecords.Load() {
			lastRecords.Store(records)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := newTree("volume")
	tree.mu.Lock()
	added := addMFTToTree(idx, tree, tree.Root, ntfs.RootRecord)
	tree.mu.Unlock()
	t.Logf("%d records, %d files, %d damaged", lastRecords.Load(), added, idx.Damaged)

	gotFiles, gotDirs := map[string]int64{}, map[string]bool{}
	var walk func(n *Node, prefix string)
	walk = func(n *Node, prefix string) {
		for _, c := range n.Children {
			p := prefix + c.Name
			if c.Directory {
				gotDirs[p] = true
				walk(c, p+"/")
			} else {
				gotFiles[p] = c.Size
			}
		}
	}
	walk(tree.Root, "")

	type file struct {
		size  int64
		paths []string
	}
	byInode := map[string]*file{}
	lines := readLines(t, filepath.Join(dir, "files.txt"))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		if byInode[parts[0]] == nil {
			byInode[parts[0]] = &file{size: size}
		}
		byInode[parts[0]].paths = append(byInode[parts[0]].paths, parts[2])
	}
	var wantSize int64
	for inode, f := range byInode {
		wantSize += f.size
		found := 0
		for _, p := range f.paths {
			if size, ok := gotFiles[p]; ok {
				found++
				if size != f.size {
					t.Errorf("%s: size %d, want %d", p, size, f.size)
				}
			}
		}
		if found != 1 {
			t.Errorf("file %s (%v) found %d times, want once", inode, f.paths, found)
		}
	}
	if len(gotFiles) != len(byInode) || added != len(byInode) {
		t.Errorf("found %d files (%d added), want %d", len(gotFiles), added, len(byInode))
	}
	if tree.Root.Size != wantSize {
		t.Errorf("total size %d, want %d", tree.Root.Size, wantSize)
	}
	wantDirs := readLines(t, filepath.Join(dir, "dirs.txt"))
	for _, d := range wantDirs {
		if !gotDirs[d] {
			t.Errorf("folder %q missing", d)
		}
	}
	if len(gotDirs) != len(wantDirs) {
		var extra []string
		for d := range gotDirs {
			extra = append(extra, d)
		}
		sort.Strings(extra)
		t.Errorf("found %d folders, want %d: %v", len(gotDirs), len(wantDirs), extra)
	}
	for _, name := range []string{"junction", "symlink"} {
		if gotDirs[name] || gotFiles[name] > 0 {
			t.Errorf("link %s was listed", name)
		}
		if _, ok := gotFiles[name]; ok {
			t.Errorf("link %s was listed", name)
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		if s.Text() != "" {
			lines = append(lines, s.Text())
		}
	}
	return lines
}
