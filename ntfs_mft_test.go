package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestDecodeRuns(t *testing.T) {
	// 0x10 clusters at 0x1000; 8 sparse clusters; 0x20 clusters 0x200 back (at 0xE00).
	runs, err := decodeRuns([]byte{0x21, 0x10, 0x00, 0x10, 0x01, 0x08, 0x21, 0x20, 0x00, 0xFE, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	want := []ntfsRun{{0x1000, 0x10}, {-1, 8}, {0xE00, 0x20}}
	if len(runs) != len(want) {
		t.Fatalf("runs %v", runs)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Fatalf("run %d = %v, want %v", i, runs[i], want[i])
		}
	}
	for _, bad := range [][]byte{{0x21, 0x10}, {0x91, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, {0x11, 0x05, 0x80}} {
		if _, err := decodeRuns(bad); err == nil {
			t.Errorf("% x accepted", bad)
		}
	}
}

// testRecord builds a 1024-byte MFT record from attributes and protects it with fixups.
func testRecord(flags uint16, base uint64, attrs ...[]byte) []byte {
	r := make([]byte, 1024)
	copy(r, "FILE")
	binary.LittleEndian.PutUint16(r[4:], 0x30) // update sequence at 0x30, 3 entries
	binary.LittleEndian.PutUint16(r[6:], 3)
	binary.LittleEndian.PutUint16(r[0x10:], 7) // sequence number
	binary.LittleEndian.PutUint16(r[0x14:], 0x38)
	binary.LittleEndian.PutUint16(r[0x16:], flags)
	binary.LittleEndian.PutUint64(r[0x20:], base)
	offset := 0x38
	for _, a := range attrs {
		copy(r[offset:], a)
		offset += len(a)
	}
	binary.LittleEndian.PutUint32(r[offset:], ntfsAttrEnd)
	binary.LittleEndian.PutUint32(r[0x18:], uint32(offset+8))
	binary.LittleEndian.PutUint16(r[0x30:], 0xBEEF)
	for i := 1; i <= 2; i++ {
		end := i*512 - 2
		copy(r[0x30+2*i:], r[end:end+2])
		binary.LittleEndian.PutUint16(r[end:], 0xBEEF)
	}
	return r
}

func residentAttr(kind uint32, value []byte) []byte {
	length := (0x18 + len(value) + 7) &^ 7
	a := make([]byte, length)
	binary.LittleEndian.PutUint32(a, kind)
	binary.LittleEndian.PutUint32(a[4:], uint32(length))
	binary.LittleEndian.PutUint32(a[0x10:], uint32(len(value)))
	binary.LittleEndian.PutUint16(a[0x14:], 0x18)
	copy(a[0x18:], value)
	return a
}

func fileNameValue(parent uint64, seq uint16, namespace byte, name string, reparseTag uint32) []byte {
	units := utf16.Encode([]rune(name))
	v := make([]byte, 0x42+2*len(units))
	binary.LittleEndian.PutUint64(v, parent|uint64(seq)<<48)
	if reparseTag != 0 {
		binary.LittleEndian.PutUint32(v[0x38:], ntfsAttributeReparsePoint)
		binary.LittleEndian.PutUint32(v[0x3C:], reparseTag)
	}
	v[0x40], v[0x41] = byte(len(units)), namespace
	for i, u := range units {
		binary.LittleEndian.PutUint16(v[0x42+2*i:], u)
	}
	return v
}

func TestParseRecord(t *testing.T) {
	// A file with a short name, a long name and a second hard link elsewhere.
	r := testRecord(ntfsRecordInUse, 0,
		residentAttr(ntfsAttrFileName, fileNameValue(40, 3, ntfsNamespaceDOS, "REPORT~1.TXT", 0)),
		residentAttr(ntfsAttrFileName, fileNameValue(40, 3, 1, "Report for March.txt", 0)),
		residentAttr(ntfsAttrFileName, fileNameValue(41, 9, 0, "other name.txt", 0)),
		residentAttr(ntfsAttrData, []byte("hello, world")))
	var e mftEntry
	if _, kind := parseRecord(r, &e); kind != recordFile {
		t.Fatalf("kind %v", kind)
	}
	if e.name != "Report for March.txt" || e.parent != 40 || e.pseq != 3 || e.size != 12 || e.seq != 7 || e.flags&mftLink != 0 || e.links != 1 {
		t.Fatalf("entry %+v", e)
	}

	// A symbolic link is marked so it is not listed.
	r = testRecord(ntfsRecordInUse|ntfsRecordDirectory, 0,
		residentAttr(ntfsAttrFileName, fileNameValue(5, 5, 1, "link", ntfsTagSymlink)))
	if _, kind := parseRecord(r, &e); kind != recordDirectory || e.flags&mftLink == 0 {
		t.Fatalf("symbolic link: kind %v entry %+v", kind, e)
	}

	// An extension record's data belongs to its base record.
	r = testRecord(ntfsRecordInUse, 77|uint64(2)<<48, residentAttr(ntfsAttrData, make([]byte, 99)))
	if x, kind := parseRecord(r, &e); kind != recordExtension || x.base != 77 || !x.hasDat || x.size != 99 {
		t.Fatalf("extension: kind %v extra %+v", kind, x)
	}

	// A folder whose long name sits in an extension record, with only its 8.3 name in the
	// base record, must be shown by its long name.
	idx := &mftIndex{entries: make([]mftEntry, 100)}
	base := testRecord(ntfsRecordInUse|ntfsRecordDirectory, 0,
		residentAttr(ntfsAttrFileName, fileNameValue(5, 5, ntfsNamespaceDOS, "PACKAG~1", 0)))
	if _, kind := parseRecord(base, &idx.entries[60]); kind != recordDirectory || idx.entries[60].flags&mftShortName == 0 {
		t.Fatalf("short-named folder: kind %v entry %+v", kind, idx.entries[60])
	}
	ext := testRecord(ntfsRecordInUse, 60|uint64(7)<<48,
		residentAttr(ntfsAttrFileName, fileNameValue(5, 5, 1, "Package_for_RollupFix", 0)))
	x, kind := parseRecord(ext, &idx.entries[61])
	if kind != recordExtension || x.short || x.name != "Package_for_RollupFix" {
		t.Fatalf("extension name: kind %v extra %+v", kind, x)
	}
	idx.applyExtras([]mftExtra{x})
	if e := idx.entries[60]; e.name != "Package_for_RollupFix" || e.flags&mftShortName != 0 {
		t.Fatalf("long name did not replace the short name: %+v", e)
	}

	// A torn write leaves a sector that does not end with the check value.
	r = testRecord(ntfsRecordInUse, 0, residentAttr(ntfsAttrData, nil))
	r[1022] ^= 0xFF
	if _, kind := parseRecord(r, &e); kind != recordDamaged {
		t.Fatalf("damaged record parsed as %v", kind)
	}
	// Unused records are skipped.
	if _, kind := parseRecord(testRecord(0, 0), &e); kind != recordUnused {
		t.Fatalf("unused record parsed as %v", kind)
	}
}

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
	v, err := openNTFS(f)
	if err != nil {
		t.Fatal(err)
	}
	var lastRecords int64
	idx, err := v.read(context.Background(), func(records, _, _ int64) { lastRecords = max(lastRecords, records) })
	if err != nil {
		t.Fatal(err)
	}
	tree := newTree("volume")
	tree.mu.Lock()
	added := idx.addToTree(tree, tree.Root, ntfsRootRecord)
	tree.mu.Unlock()
	t.Logf("%d records, %d files, %d damaged, %d-byte clusters, %d MFT runs", lastRecords, added, idx.damaged, v.clusterSize, len(v.mftRuns))

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
