package search

import (
	"bytes"
	"context"
	"path"
	"reflect"
	"strings"
	"testing"
)

// liveSample is a drive as an NTFS index holds it, with record numbers as slots:
//
//	C:\ (5): Users (40): Ann (41): song.mp3, Holiday.mp4
//	         notes.txt (60)
func liveSample() *Index {
	x := NewLive()
	x.Update(func(u Updater) {
		u.Set(5, `C:\`, -1, 0, true)
		u.Set(40, "Users", 5, 0, true)
		u.Set(41, "Ann", 40, 0, true)
		u.Set(50, "song.mp3", 41, 300, false)
		u.Set(51, "Holiday.mp4", 41, 900, false)
		u.Set(60, "notes.txt", 5, 10, false)
	})
	return x
}

func winJoin(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], `\`) + `\` + strings.Join(names[1:], `\`)
}

func TestLiveIndexChanges(t *testing.T) {
	x := liveSample()
	if x.Len() != 6 {
		t.Fatalf("len %d", x.Len())
	}
	r, err := x.Search(context.Background(), Query{Text: "holiday"}, winJoin)
	if err != nil || len(r.Hits) != 1 || r.Hits[0].Path != `C:\Users\Ann` || r.Hits[0].Parent != 41 {
		t.Fatalf("search %+v %v", r, err)
	}
	r, _ = x.Search(context.Background(), Query{Text: "ann", Kind: "folders"}, winJoin)
	if len(r.Hits) != 1 || r.Hits[0].ID != 41 || r.Hits[0].Path != `C:\Users` {
		t.Fatalf("folder %+v", r.Hits)
	}
	// A rename, a move, a size change and a delete.
	x.Update(func(u Updater) {
		u.Set(51, "Beach.mp4", 5, 900, false)
		u.SetSize(50, 1234)
		u.Remove(60)
		if p, ok := u.Path(50); !ok || strings.Join(p, "|") != `C:\|Users|Ann|song.mp3` {
			t.Errorf("path %q %v", p, ok)
		}
	})
	if r, _ := x.Search(context.Background(), Query{Text: "holiday"}, winJoin); len(r.Hits) != 0 {
		t.Fatalf("old name still found: %+v", r.Hits)
	}
	r, _ = x.Search(context.Background(), Query{Text: "beach"}, winJoin)
	if len(r.Hits) != 1 || r.Hits[0].Path != `C:\` {
		t.Fatalf("moved %+v", r.Hits)
	}
	r, _ = x.Search(context.Background(), Query{}, winJoin)
	if got := names(r); !reflect.DeepEqual(got, []string{"song.mp3", "Beach.mp4"}) || r.Hits[0].Size != 1234 {
		t.Fatalf("files %v %+v", got, r.Hits)
	}
	if ty := x.Types(0); len(ty) != 2 || ty[0].Ext != "mp3" || ty[0].Size != 1234 {
		t.Fatalf("types are recounted: %+v", ty)
	}
	if x.Len() != 5 {
		t.Fatalf("len after remove %d", x.Len())
	}
	// A removed folder leaves what is inside it without a full path, which must not panic.
	x.Update(func(u Updater) { u.Remove(40) })
	if _, err := x.Search(context.Background(), Query{Text: "song"}, winJoin); err != nil {
		t.Fatal(err)
	}
}

func TestLiveIndexCompactsAndSaves(t *testing.T) {
	x := liveSample()
	long := strings.Repeat("x", 60000)
	for i := range 40 {
		x.Update(func(u Updater) { u.Set(70, long+string(rune('a'+i%26)), 5, int64(i), false) })
	}
	x.mu.RLock()
	size := len(x.names)
	x.mu.RUnlock()
	if size > 4<<20 {
		t.Fatalf("names grew to %d bytes", size)
	}
	var buf bytes.Buffer
	if err := x.Save(&buf); err != nil {
		t.Fatal(err)
	}
	y, err := ReadIndex(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !y.Live() || y.Len() != x.Len() {
		t.Fatalf("read back live=%v len=%d", y.Live(), y.Len())
	}
	for _, q := range []Query{{Text: "song"}, {Kind: "videos"}, {}, {Text: "users", Kind: "folders"}} {
		a, _ := x.Search(context.Background(), q, winJoin)
		b, _ := y.Search(context.Background(), q, winJoin)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%+v differs after reading back:\n%+v\n%+v", q, a, b)
		}
	}
	// Changes go on after reading back.
	y.Update(func(u Updater) { u.Set(80, "new.pdf", 41, 5, false) })
	if r, _ := y.Search(context.Background(), Query{Text: "new"}, winJoin); len(r.Hits) != 1 || r.Hits[0].Path != `C:\Users\Ann` {
		t.Fatalf("after read %+v", r.Hits)
	}
	for cut := 0; cut < buf.Len(); cut += 101 {
		if _, err := ReadIndex(bytes.NewReader(buf.Bytes()[:cut])); err == nil {
			t.Fatalf("truncated at %d: no error", cut)
		}
	}
}

func TestSearchAllMergesDrives(t *testing.T) {
	c := liveSample()
	d := NewLive()
	d.Update(func(u Updater) {
		u.Set(5, `D:\`, -1, 0, true)
		u.Set(20, "Holiday 2.mp4", 5, 5000, false)
		u.Set(21, "doc.txt", 5, 1, false)
	})
	scan := sample()
	sources := []Source{{c, winJoin}, {d, winJoin}, {scan, func(n []string) string { return path.Join(n...) }}}
	r, err := SearchAll(context.Background(), Query{Kind: "videos", Types: true}, sources)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(r); !reflect.DeepEqual(got, []string{"Holiday 2.mp4", "Holiday 2024.mp4", "Holiday.mp4", "clip.MKV"}) {
		t.Fatalf("merged %v", got)
	}
	if r.Total != 4 || r.TotalSize != 5000+3000+900+200 || r.Hits[0].Path != `D:\` {
		t.Fatalf("totals %+v", r)
	}
	if r.Types[0].Ext != "mp4" || r.Types[0].Files != 3 || r.Types[0].Size != 8900 {
		t.Fatalf("types %+v", r.Types[:2])
	}
	r, _ = SearchAll(context.Background(), Query{Text: "holiday", Limit: 2}, sources)
	if len(r.Hits) != 2 || r.Total < 5 {
		t.Fatalf("limit %+v", r)
	}
}
