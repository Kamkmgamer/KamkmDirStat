package dirview

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/view.golden.json")

type testNode struct {
	Item
	kids []*testNode
	fail bool
}

type testSource struct{}

func (testSource) Item(n *testNode) Item { return n.Item }

func (testSource) Children(n *testNode) ([]*testNode, error) {
	if n.fail {
		return nil, errors.New("unreadable")
	}
	return n.kids, nil
}

func file(name string, size int64) *testNode {
	return &testNode{Item: Item{Name: name, Size: size, Files: 1}}
}

func dir(id int, name string, kids ...*testNode) *testNode {
	d := &testNode{Item: Item{Name: name, Directory: true, ID: id}, kids: kids}
	for _, k := range kids {
		d.Size += k.Size
		d.Files += k.Files
	}
	return d
}

// The window, the native server scanner and remote_report.py all exchange this JSON, so
// its field names and which fields are left out when empty must not change by accident.
func TestViewJSON(t *testing.T) {
	photos := dir(2, "photos", file("a.jpg", 3000), file("b.jpg", 1000))
	empty := dir(3, "empty")
	docs := dir(1, "docs", photos, file("notes.txt", 500), empty)
	docs.Incomplete = true
	root := dir(0, "/data", file("big.iso", 9000), docs, file("tiny", 1))
	root.Incomplete = true

	v, err := Build[*testNode](testSource{}, []Crumb{{ID: 0, Name: "/data"}, {ID: 1, Name: "docs"}}, docs, docs.kids)
	if err != nil {
		t.Fatal(err)
	}
	top, err := Build[*testNode](testSource{}, []Crumb{{ID: 0, Name: "/data"}}, root, root.kids)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(map[string]View{"root": top, "docs": v}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/view.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("the folder report changed; if that is intended, run go test ./internal/dirview -update and bring remote_report.py in step.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestViewLimits(t *testing.T) {
	var kids []*testNode
	for i := range EntryLimit + 200 {
		kids = append(kids, file(fmt.Sprintf("f%d", i), int64(1000-i)))
	}
	root := dir(0, "/", kids...)
	v, err := Build[*testNode](testSource{}, []Crumb{{Name: "/"}}, root, kids)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Entries) != EntryLimit || v.MoreCount != 200 || v.Items != EntryLimit+200 {
		t.Fatalf("entries %d, more %d, items %d", len(v.Entries), v.MoreCount, v.Items)
	}
	var more int64
	for _, k := range kids[EntryLimit:] {
		more += k.Size
	}
	if v.MoreSize != more {
		t.Fatalf("more size %d, want %d", v.MoreSize, more)
	}

	// Entries below 1/MapDetail of the folder are merged into their folder's remainder.
	root = dir(0, "/", file("big", 40000), file("kept", 10), file("dust", 9))
	v, err = Build[*testNode](testSource{}, []Crumb{{Name: "/"}}, root, root.kids)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Map.C) != 2 || v.Map.O != 9 {
		t.Fatalf("map blocks %d, other %d", len(v.Map.C), v.Map.O)
	}

	// Blocks stop at the budget, shallow levels first: three levels of folders use it up,
	// so the files beneath them are merged.
	kids = kids[:0]
	id := 0
	for range MapNodeLimit / 3 {
		d := file("f", 1000)
		for range 3 {
			id++
			d = dir(id, "d", d)
		}
		kids = append(kids, d)
	}
	root = dir(0, "/", kids...)
	v, err = Build[*testNode](testSource{}, []Crumb{{Name: "/"}}, root, kids)
	if err != nil {
		t.Fatal(err)
	}
	blocks, other := 0, int64(0)
	var count func(m *MapNode)
	count = func(m *MapNode) {
		other += m.O
		for _, c := range m.C {
			blocks++
			count(c)
		}
	}
	count(v.Map)
	if blocks != MapNodeLimit || other != root.Size {
		t.Fatalf("map blocks %d, other %d, want %d and %d", blocks, other, MapNodeLimit, root.Size)
	}
}

func TestViewReportsUnreadableFolder(t *testing.T) {
	bad := dir(1, "bad", file("x", 10))
	bad.fail = true
	root := dir(0, "/", bad)
	if _, err := Build[*testNode](testSource{}, []Crumb{{Name: "/"}}, root, root.kids); err == nil {
		t.Fatal("expected the read error")
	}
}
