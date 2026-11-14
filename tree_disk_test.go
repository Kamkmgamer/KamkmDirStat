package main

import (
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestReleaseMemoryKeepsViews(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // os.UserCacheDir on Windows
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	tree := newTree("/data")
	tree.mu.Lock()
	for i := range 30 {
		d := tree.addChild(tree.Root, fmt.Sprintf("dir %d", i), true, 0)
		for j := range 40 {
			sub := tree.addChild(d, fmt.Sprintf("sub %d", j), true, 0)
			for k := range 5 {
				tree.addChild(sub, fmt.Sprintf("file\n%d", k), false, int64((i+1)*(j+1)*(k+1)*1000))
			}
		}
		tree.addChild(d, "neg", false, -1)
	}
	markIncomplete(tree.dirs[42])
	tree.mu.Unlock()
	tree.finalize()

	var want []DirView
	for id := range len(tree.dirs) {
		v, err := tree.view(id)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, v)
	}
	onDisk, err := tree.releaseMemory()
	if err != nil || !onDisk {
		t.Fatalf("releaseMemory = %v, %v", onDisk, err)
	}
	if tree.Root != nil || tree.dirs != nil {
		t.Fatal("nodes still held in memory")
	}
	for id, w := range want {
		got, err := tree.view(id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Fatalf("view %d differs after moving to disk:\n got %+v\nwant %+v", id, got, w)
		}
	}
	if _, err := tree.view(len(want)); err == nil {
		t.Fatal("expected an error for an unknown folder")
	}
	path := tree.disk.path
	tree.close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cache file not removed: %v", err)
	}
}
