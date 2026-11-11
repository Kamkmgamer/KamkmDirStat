package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func localFixture(t *testing.T) (string, int64, int) {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, size int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("big", "data.bin"), 300000)
	write(filepath.Join("big", "nested", "more.bin"), 70000)
	write(filepath.Join("big", "nested", "deeper", "x", "y.bin"), 1234)
	write("small.txt", 10)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A link back to the top must not be followed, or everything would be counted twice.
	_ = os.Symlink(root, filepath.Join(root, "loop"))
	return root, 300000 + 70000 + 1234 + 10, 4
}

func TestLocalScanSingleFolder(t *testing.T) {
	root, want, files := localFixture(t)
	roots, err := cleanLocalPaths([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	tree := newLocalTree(roots)
	var last ScanProgress
	result, err := localScanInto(context.Background(), roots, tree, func(p ScanProgress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if tree.Root.Size != want || tree.Root.Files != files || result.Files != files || result.Mode != "local" || result.ErrorCount != 0 {
		t.Fatalf("size=%d files=%d result=%+v, want size=%d files=%d", tree.Root.Size, tree.Root.Files, result, want, files)
	}
	if last.Files != files || last.Bytes != want || last.Mode != "local" {
		t.Fatalf("unexpected final progress %+v", last)
	}
	v, err := tree.view(0)
	if err != nil {
		t.Fatal(err)
	}
	if v.Entries[0].Name != "big" || v.Entries[0].Size != 371234 {
		t.Fatalf("largest entry %+v", v.Entries[0])
	}
	for _, e := range v.Entries {
		if e.Name == "loop" {
			t.Fatal("symbolic link was listed")
		}
	}
}

func TestLocalScanSeveralRoots(t *testing.T) {
	a, sizeA, filesA := localFixture(t)
	b, sizeB, filesB := localFixture(t)
	// A folder inside one already chosen is dropped rather than counted twice.
	roots, err := cleanLocalPaths([]string{b, a, filepath.Join(a, "big")})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("nested folder not removed: %v", roots)
	}
	tree := newLocalTree(roots)
	if _, err := localScanInto(context.Background(), roots, tree, nil); err != nil {
		t.Fatal(err)
	}
	if tree.Root.Name != localRootName || len(tree.Root.Children) != 2 || tree.Root.Size != sizeA+sizeB || tree.Root.Files != filesA+filesB {
		t.Fatalf("root %q children=%d size=%d files=%d", tree.Root.Name, len(tree.Root.Children), tree.Root.Size, tree.Root.Files)
	}
	v, err := tree.view(tree.Root.Children[0].id)
	if err != nil || len(v.Trail) != 2 || v.Trail[0].Name != localRootName {
		t.Fatalf("drive view trail %+v err %v", v.Trail, err)
	}
}

func TestLocalScanCancelled(t *testing.T) {
	root, _, _ := localFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := localScanInto(ctx, []string{root}, newTree(root), nil); err == nil {
		t.Fatal("cancelled scan reported success")
	}
}

func TestCleanLocalPathsRejectsBadInput(t *testing.T) {
	for _, paths := range [][]string{nil, {""}, {"relative/path"}, {filepath.Join(t.TempDir(), "missing")}} {
		if _, err := cleanLocalPaths(paths); err == nil {
			t.Errorf("%q accepted", paths)
		}
	}
}
