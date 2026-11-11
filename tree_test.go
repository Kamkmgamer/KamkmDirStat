package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func findStream(records ...string) *strings.Reader {
	return strings.NewReader(strings.Join(records, "\x00") + "\x00")
}

func TestFindStreamBuildsTree(t *testing.T) {
	tree := newTree("/data")
	b := newFindBuilder(tree)
	var reports []ScanProgress
	err := consumeFindStream(context.Background(), findStream(
		"d 8 0 data",
		"d 8 1 logs",
		"f 16 2 app.log",
		"h 2049:77 8 2 linked",
		"l 0 2 current", // symlink: skipped
		"d 8 2 archive",
		"f 40 3 old.log",
		"h 2049:77 8 1 linked-again", // same inode: counted once
		"f 4 1 name with spaces",
		"f 2 1 line\nbreak",
		"f 1 3 orphan", // deeper than any open folder
		"garbage",
	), b, func(p ScanProgress) { reports = append(reports, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 || reports[len(reports)-1].Files != 6 || reports[len(reports)-1].Entries != 11 {
		t.Fatalf("progress not reported: %+v", reports)
	}
	tree.finalize()
	const blocks = 512
	if got, want := tree.Root.Files, 6; got != want {
		t.Fatalf("files = %d, want %d", got, want)
	}
	if got, want := tree.Root.Size, int64(8+8+16+8+8+40+0+4+2)*blocks; got != want {
		t.Fatalf("size = %d, want %d", got, want)
	}
	logs := tree.Root.Children[0]
	if logs.Name != "logs" || logs.Size != (8+16+8+8+40)*blocks || len(logs.Children) != 3 {
		t.Fatalf("unexpected logs folder: %+v", logs)
	}
	if archive := logs.Children[0]; archive.Name != "archive" || len(archive.Children) != 1 || archive.Children[0].Name != "old.log" {
		t.Fatalf("nested folder rebuilt wrongly: %+v", archive)
	}
	names := map[string]bool{}
	for _, c := range tree.Root.Children {
		names[c.Name] = true
	}
	for _, want := range []string{"name with spaces", "line\nbreak", "linked-again"} {
		if !names[want] {
			t.Errorf("missing %q in %v", want, names)
		}
	}
	if b.bad != 2 {
		t.Errorf("bad records = %d, want 2", b.bad)
	}
	if p := reports[len(reports)-1].CurrentPath; p != "/data/line\nbreak" {
		t.Errorf("current path = %q", p)
	}
}

func TestFindErrorPathMarksIncomplete(t *testing.T) {
	p, ok := findErrorPath("find: '/data/private/x': Permission denied")
	if !ok || p != "/data/private/x" {
		t.Fatalf("parsed %q, %v", p, ok)
	}
	if _, ok := findErrorPath("bash: warning: something"); ok {
		t.Fatal("parsed a non-find message")
	}
	tree := newTree("/data")
	b := newFindBuilder(tree)
	if err := consumeFindStream(context.Background(), findStream("d 0 0 data", "d 0 1 private", "f 2 1 other"), b, nil); err != nil {
		t.Fatal(err)
	}
	rel, ok := relativeTo("/data", p)
	if !ok {
		t.Fatal("path not under root")
	}
	markIncomplete(b.nearest(rel))
	v, _ := tree.view(0)
	if !v.Incomplete || !v.Entries[1].Incomplete || v.Entries[0].Incomplete {
		t.Fatalf("incomplete flags wrong: %+v", v)
	}
}

func TestRelativePaths(t *testing.T) {
	for _, tc := range []struct{ root, p, rel string }{{"/", "/var/log", "var/log"}, {"/srv", "/srv", ""}, {"/srv", "/srv/a b", "a b"}} {
		if rel, ok := relativeTo(tc.root, tc.p); !ok || rel != tc.rel {
			t.Errorf("relativeTo(%q,%q) = %q,%v", tc.root, tc.p, rel, ok)
		}
		if got := joinRemote(tc.root, tc.rel); got != tc.p {
			t.Errorf("joinRemote(%q,%q) = %q", tc.root, tc.rel, got)
		}
	}
	if _, ok := relativeTo("/srv", "/srv2/x"); ok {
		t.Error("accepted a sibling with a shared prefix")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/it's here"); got != `'/it'\''s here'` {
		t.Fatalf("got %s", got)
	}
	script := findScript("/a'b", true)
	if !strings.Contains(script, `p='/a'\''b'`) || !strings.Contains(script, " -xdev ") {
		t.Fatalf("unexpected script:\n%s", script)
	}
	if strings.Contains(findScript("/", false), "-xdev") {
		t.Fatal("-xdev used when crossing file systems was requested")
	}
}

func TestViewLimitsEntriesAndMap(t *testing.T) {
	tree := newTree("/")
	tree.mu.Lock()
	big := tree.addChild(tree.Root, "big", true, 0)
	tree.addChild(big, "huge.img", false, 1<<30)
	for i := 0; i < 700; i++ {
		tree.addChild(tree.Root, fmt.Sprintf("small-%03d", i), false, 1)
	}
	tree.mu.Unlock()
	v, err := tree.view(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Entries) != viewEntryLimit || v.MoreCount != 701-viewEntryLimit || v.Items != 701 {
		t.Fatalf("entries=%d more=%d items=%d", len(v.Entries), v.MoreCount, v.Items)
	}
	if v.Entries[0].Name != "big" || !v.Entries[0].Directory || v.Entries[0].ID != big.id {
		t.Fatalf("largest entry first: %+v", v.Entries[0])
	}
	if len(v.Map.C) != 1 || v.Map.O != 700 || v.Map.C[0].I != big.id || len(v.Map.C[0].C) != 1 {
		t.Fatalf("map should keep the big folder and merge tiny files: %+v", v.Map)
	}
	sub, err := tree.view(big.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Trail) != 2 || sub.Trail[0].ID != 0 || sub.Trail[1].Name != "big" {
		t.Fatalf("trail = %+v", sub.Trail)
	}
	if _, err := tree.view(99); err == nil {
		t.Fatal("unknown folder accepted")
	}
}

func TestMapNodeBudget(t *testing.T) {
	// 4000 chains of three folders and one file: every entry is exactly at the detail
	// threshold, so only the node budget stops the map from growing to 16000 nodes.
	tree := newTree("/")
	tree.mu.Lock()
	for i := 0; i < mapDetail; i++ {
		a := tree.addChild(tree.Root, fmt.Sprintf("a%04d", i), true, 0)
		b := tree.addChild(a, "b", true, 0)
		c := tree.addChild(b, "c", true, 0)
		tree.addChild(c, "file", false, 1000)
	}
	tree.mu.Unlock()
	v, _ := tree.view(0)
	nodes, other := 0, int64(0)
	var walk func(*MapNode)
	walk = func(m *MapNode) {
		other += m.O
		for _, c := range m.C {
			nodes++
			walk(c)
		}
	}
	walk(v.Map)
	if nodes != mapNodeLimit || other != mapDetail*1000 {
		t.Fatalf("map nodes=%d other=%d", nodes, other)
	}
}
