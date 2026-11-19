package fslist

import (
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLongPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Users`:       `\\?\C:\Users`,
		`\\server\share`: `\\?\UNC\server\share`,
		`\\?\C:\already`: `\\?\C:\already`,
	} {
		if got := LongPath(in); got != want {
			t.Errorf("LongPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NTRoot(`C:\Users`); got != `\??\C:\Users` {
		t.Errorf("NTRoot = %q", got)
	}
}

// Reading a folder through its handle must find what List finds, and a subfolder opens by
// its name alone inside its parent.
func TestOpenRelativeAndRead(t *testing.T) {
	dir, want := testFolder(t)
	h, err := OpenRelative(0, NTRoot(dir))
	if err != nil {
		t.Fatal(err)
	}
	buf := NewBuffer()
	got, err := Read(h, buf)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sorted(got), want) {
		t.Fatalf("read %+v, want %+v", got, want)
	}
	parent := Hold(h, 1)
	sub, err := OpenRelative(parent.H, "sub")
	parent.Release() // closes the parent: its one subfolder is open
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sub)
	inner, err := Read(sub, buf)
	if err != nil || len(inner) != 1 || inner[0] != (Entry{Name: "inner", Kind: File, Size: 100}) {
		t.Fatalf("subfolder read %+v, %v", inner, err)
	}
	if _, err := Read(h, buf); err == nil {
		t.Fatal("the parent handle was still open after its last release")
	}
	var none *Parent
	none.Release()
	if _, err := Read(sub, make([]byte, 16)); err == nil {
		t.Fatal("a buffer that is too small was accepted")
	}
}
