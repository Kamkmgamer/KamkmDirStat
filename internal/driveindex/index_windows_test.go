package driveindex

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/Kamkmgamer/KamkmDirStat/internal/ntfs"
	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"golang.org/x/sys/windows"
)

func driveSearch(t *testing.T, idx *search.Index, q search.Query) search.Result {
	t.Helper()
	r, err := idx.Search(context.Background(), q, joinDrivePath)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hitPaths(r search.Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, filepath.Join(h.Path, h.Name))
	}
	slices.Sort(out)
	return out
}

// The table's entries become slots by record number; system files, links, orphans and
// anything not beneath the root are left out.
func TestMFTToIndex(t *testing.T) {
	m := &ntfs.Index{Entries: make([]ntfs.Entry, 32)}
	set := func(r int, name string, parent uint32, pseq uint16, flags uint8, size int64) {
		m.Entries[r] = ntfs.Entry{Name: name, Parent: parent, ParentSeq: pseq, Seq: uint16(r), Flags: ntfs.Used | ntfs.Named | flags, Size: size}
	}
	set(5, ".", 5, 5, ntfs.Dir, 0)
	set(11, "$Extend", 5, 5, ntfs.Dir, 0)
	set(16, "Users", 5, 5, ntfs.Dir, 0)
	set(17, "a.txt", 16, 16, 0, 10)
	set(18, "orphan.txt", 16, 99, 0, 10) // its folder's record was reused
	set(19, "Junction", 16, 16, ntfs.Dir|ntfs.Link, 0)
	set(20, "inside-link.txt", 19, 19, 0, 10)
	set(21, "$UsnJrnl", 11, 11, 0, 10)
	set(22, "Deep", 16, 16, ntfs.Dir, 0)
	set(23, "movie.mp4", 22, 22, 0, 500)
	idx := mftToIndex(m, `C:\`)
	r := driveSearch(t, idx, search.Query{})
	if got := hitPaths(r); !reflect.DeepEqual(got, []string{`C:\Users\Deep\movie.mp4`, `C:\Users\a.txt`}) {
		t.Fatalf("files %v", got)
	}
	if r := driveSearch(t, idx, search.Query{Kind: "folders", Text: "e"}); !reflect.DeepEqual(hitPaths(r), []string{`C:\Users`, `C:\Users\Deep`}) {
		t.Fatalf("folders %v", hitPaths(r))
	}
}

func usnBytes(ref, parent uint64, reason, attrs uint32, name string) []byte {
	units := utf16.Encode([]rune(name))
	b := make([]byte, 60+2*len(units))
	binary.LittleEndian.PutUint32(b, uint32(len(b)))
	binary.LittleEndian.PutUint16(b[4:], 2)
	binary.LittleEndian.PutUint64(b[8:], ref|7<<48)
	binary.LittleEndian.PutUint64(b[16:], parent|3<<48)
	binary.LittleEndian.PutUint32(b[40:], reason)
	binary.LittleEndian.PutUint32(b[52:], attrs)
	binary.LittleEndian.PutUint16(b[56:], uint16(2*len(units)))
	binary.LittleEndian.PutUint16(b[58:], 60)
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[60+2*i:], u)
	}
	return b
}

// Journal records create, rename, move and delete entries, and mark files whose size must
// be read again.
func TestApplyUsnRecords(t *testing.T) {
	var raw []byte
	add := func(ref, parent uint64, reason, attrs uint32, name string) {
		raw = append(raw, usnBytes(ref, parent, reason, attrs, name)...)
	}
	add(40, 5, usnFileCreate, windows.FILE_ATTRIBUTE_DIRECTORY, "Projects")
	add(41, 40, usnFileCreate|usnDataExtend, windows.FILE_ATTRIBUTE_ARCHIVE, "draft.docx")
	add(42, 40, usnFileCreate, 0, "temp.tmp")
	add(41, 40, usnRenameOldName, 0, "draft.docx")
	add(41, 5, usnRenameNewName, 0, "final.docx") // renamed and moved up
	add(42, 40, usnFileDelete|usnClose, 0, "temp.tmp")
	add(43, 999, usnFileCreate, 0, "elsewhere.txt") // in a folder the index does not hold
	add(3, 5, usnFileCreate, 0, "$System")
	add(44, 40, usnFileCreate, windows.FILE_ATTRIBUTE_REPARSE_POINT, "link")
	records := parseUsnRecords(raw)
	if len(records) != 9 || records[4].name != "final.docx" || records[4].ref != 41 || records[4].parent != 5 {
		t.Fatalf("parsed %+v", records)
	}

	idx := search.NewLive()
	idx.Update(func(u search.Updater) {
		u.Set(5, `C:\`, -1, 0, true)
		u.Set(30, "old.txt", 5, 7, false)
	})
	need := map[int]bool{}
	if !applyUsnRecords(idx, records, need) {
		t.Fatal("no change reported")
	}
	if got := hitPaths(driveSearch(t, idx, search.Query{})); !reflect.DeepEqual(got, []string{`C:\Projects\link`, `C:\final.docx`, `C:\old.txt`}) {
		t.Fatalf("after changes %v", got)
	}
	if !reflect.DeepEqual(need, map[int]bool{41: false, 44: true}) {
		t.Fatalf("need %v", need)
	}
	// Writing to a listed file asks for its size; its name stays.
	need = map[int]bool{}
	applyUsnRecords(idx, parseUsnRecords(usnBytes(30, 5, usnDataOverwrite|usnClose, 0, "old.txt")), need)
	if !reflect.DeepEqual(need, map[int]bool{30: false}) {
		t.Fatalf("size need %v", need)
	}
}

const usnClose = 0x80000000

// A folder walk finds everything, and the saved index comes back on the next run.
func TestDriveWalkSaveAndLoad(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{`Music\song.mp3`, `Music\Live\set.flac`, `Docs\cv.pdf`, `top.txt`} {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix := &driveIndexer{dir: t.TempDir()}
	newStore := func() *driveStore {
		return &driveStore{ix: ix, root: root, key: "test", done: make(chan struct{}), loaded: make(chan struct{}), refresh: make(chan struct{}, 1)}
	}
	s := newStore()
	if err := s.walk(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, `Docs\cv.pdf`), filepath.Join(root, `Music\Live\set.flac`), filepath.Join(root, `Music\song.mp3`), filepath.Join(root, `top.txt`)}
	if got := hitPaths(driveSearch(t, s.idx, search.Query{})); !reflect.DeepEqual(got, want) {
		t.Fatalf("walk %v", got)
	}
	if st := s.status(); st.State != "ready" || st.Mode != "snapshot" || st.Entries != 8 || st.Built == 0 {
		t.Fatalf("status %+v", st)
	}

	again := newStore()
	again.load()
	if again.idx == nil || again.mode != "snapshot" || again.built.UnixMilli() != s.built.UnixMilli() {
		t.Fatalf("not loaded: %+v", again.status())
	}
	if got := hitPaths(driveSearch(t, again.idx, search.Query{Text: "set"})); !reflect.DeepEqual(got, []string{filepath.Join(root, `Music\Live\set.flac`)}) {
		t.Fatalf("loaded search %v", got)
	}

	// The same drive under another letter keeps its index, with the new root.
	moved := newStore()
	moved.root = `Z:\`
	moved.load()
	if got := hitPaths(driveSearch(t, moved.idx, search.Query{Text: "cv"})); !reflect.DeepEqual(got, []string{`Z:\Docs\cv.pdf`}) {
		t.Fatalf("new letter %v", got)
	}
}

func TestPipeSecurity(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	sd := pipeSecurity(sid)
	if _, err := windows.SecurityDescriptorFromString(sd); err != nil {
		t.Fatalf("%s: %v", sd, err)
	}
	if want := "(A;;GRGW;;;" + sid + ")"; sd[len(sd)-len(want):] != want {
		t.Fatalf("user not granted: %s", sd)
	}
	if sd := pipeSecurity("S-1-5-21-bad;(A;;GA;;;WD)"); sd != "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)" {
		t.Fatalf("bad account accepted: %s", sd)
	}
}
