package fslist

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testFolder holds a file, a folder and, where the system allows making one, a link to
// the folder, which listings must leave out.
func testFolder(t *testing.T) (dir string, want []Entry) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "inner"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "link")); err != nil {
		t.Logf("no symbolic link in this test: %v", err)
	}
	return dir, []Entry{{Name: "file.txt", Kind: File, Size: 5}, {Name: "sub", Kind: Dir}}
}

func sorted(entries []Entry) []Entry {
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return entries
}

func TestList(t *testing.T) {
	dir, want := testFolder(t)
	got, err := List(dir, DeviceOf(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sorted(got), want) {
		t.Fatalf("listed %+v, want %+v", got, want)
	}
	if _, err := List(filepath.Join(dir, "missing"), DeviceOf(dir)); err == nil {
		t.Fatal("a missing folder was listed")
	}
}
