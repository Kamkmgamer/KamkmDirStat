package search

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"reflect"
	"testing"
)

// sample builds:
//
//	/data (0)
//	  Videos (1): Holiday 2024.mp4, holiday-notes.txt, clip.MKV
//	  Photos (2): IMG_0001.JPG, Hollow Knight.png
//	  src (3): HolidayPlanner.go, main.go
//	  report.pdf
//	  holiday (4)
func sample() *Index {
	b := NewBuilder(0, 0)
	b.Add("/data", -1, 0, true, 0)
	b.Add("Videos", 0, 3_300, true, 1)
	b.Add("Holiday 2024.mp4", 1, 3_000, false, 0)
	b.Add("holiday-notes.txt", 1, 100, false, 0)
	b.Add("clip.MKV", 1, 200, false, 0)
	b.Add("Photos", 0, 700, true, 2)
	b.Add("IMG_0001.JPG", 2, 500, false, 0)
	b.Add("Hollow Knight.png", 2, 200, false, 0)
	b.Add("src", 0, 90, true, 3)
	b.Add("HolidayPlanner.go", 3, 60, false, 0)
	b.Add("main.go", 3, 30, false, 0)
	b.Add("report.pdf", 0, 400, false, 0)
	b.Add("holiday", 0, 0, true, 4)
	return b.Finish()
}

func run(t *testing.T, x *Index, q Query) Result {
	t.Helper()
	r, err := x.Search(context.Background(), q, func(n []string) string { return path.Join(n...) })
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func names(r Result) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Name)
	}
	return out
}

func TestFuzzyRanking(t *testing.T) {
	x := sample()
	r := run(t, x, Query{Text: "holiday"})
	got := names(r)
	// Whole-word matches come before scattered ones such as "Hollow Knight" (h-o-l-...-d-a-y?).
	if len(got) < 4 || got[0] != "holiday" {
		t.Fatalf("best match first: %v", got)
	}
	for _, n := range got {
		if n == "main.go" || n == "report.pdf" {
			t.Fatalf("%s should not match holiday: %v", n, got)
		}
	}
	if r.Total != len(got) {
		t.Fatalf("total %d for %d hits", r.Total, len(got))
	}
	// Letters in order, with gaps, still match; word starts score higher than middles.
	got = names(run(t, x, Query{Text: "hp"}))
	if len(got) == 0 || got[0] != "HolidayPlanner.go" {
		t.Fatalf("camel case initials: %v", got)
	}
	// Every word must match.
	got = names(run(t, x, Query{Text: "holiday txt"}))
	if !reflect.DeepEqual(got, []string{"holiday-notes.txt"}) {
		t.Fatalf("two words: %v", got)
	}
	if got := names(run(t, x, Query{Text: "zzz"})); len(got) != 0 {
		t.Fatalf("no match expected: %v", got)
	}
}

func TestPathsAndMatches(t *testing.T) {
	x := sample()
	r := run(t, x, Query{Text: "img"})
	if len(r.Hits) != 1 {
		t.Fatalf("hits %v", names(r))
	}
	h := r.Hits[0]
	if h.Path != "/data/Photos" || h.Parent != 2 || h.Dir || h.Size != 500 {
		t.Fatalf("hit %+v", h)
	}
	if !reflect.DeepEqual(h.Match, []int{0, 1, 2}) {
		t.Fatalf("match positions %v", h.Match)
	}
	r = run(t, x, Query{Text: "videos", Kind: "folders"})
	if len(r.Hits) != 1 || !r.Hits[0].Dir || r.Hits[0].ID != 1 || r.Hits[0].Path != "/data" {
		t.Fatalf("folder hit %+v", r.Hits)
	}
}

func TestFilters(t *testing.T) {
	x := sample()
	cases := []struct {
		q    Query
		want []string
	}{
		// With nothing typed, the largest files are listed, not folders.
		{Query{}, []string{"Holiday 2024.mp4", "IMG_0001.JPG", "report.pdf", "clip.MKV", "Hollow Knight.png", "holiday-notes.txt", "HolidayPlanner.go", "main.go"}},
		{Query{Kind: "videos"}, []string{"Holiday 2024.mp4", "clip.MKV"}},
		{Query{Kind: "images"}, []string{"IMG_0001.JPG", "Hollow Knight.png"}},
		{Query{Kind: "documents"}, []string{"report.pdf", "holiday-notes.txt"}},
		{Query{Kind: "folders"}, []string{"Videos", "Photos", "src", "holiday"}},
		{Query{Exts: []string{".GO"}}, []string{"HolidayPlanner.go", "main.go"}},
		{Query{Exts: []string{"mkv", "pdf"}}, []string{"report.pdf", "clip.MKV"}},
		{Query{Text: "holi", Kind: "videos"}, []string{"Holiday 2024.mp4"}},
		{Query{Text: "holi", Sort: "size"}, []string{"Holiday 2024.mp4", "Hollow Knight.png", "holiday-notes.txt", "HolidayPlanner.go", "holiday"}},
		{Query{Kind: "nonsense"}, nil},
	}
	for _, c := range cases {
		if got := names(run(t, x, c.q)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v:\n got %v\nwant %v", c.q, got, c.want)
		}
	}
	r := run(t, x, Query{Kind: "videos", Limit: 1})
	if len(r.Hits) != 1 || r.Total != 2 || r.TotalSize != 3_200 {
		t.Fatalf("limit keeps totals: %+v", r)
	}
}

func TestTypes(t *testing.T) {
	ty := sample().Types(3)
	if len(ty) != 3 || ty[0].Ext != "mp4" || ty[0].Kind != "videos" || ty[0].Files != 1 || ty[0].Size != 3000 {
		t.Fatalf("types %+v", ty)
	}
	b := NewBuilder(0, 0)
	b.Add("/", -1, 0, true, 0)
	b.Add("a.JPG", 0, 1, false, 0)
	b.Add("b.jpg", 0, 2, false, 0)
	if ty := b.Finish().Types(0); len(ty) != 1 || ty[0].Ext != "jpg" || ty[0].Files != 2 || ty[0].Size != 3 {
		t.Fatalf("extensions differing in case are one type: %+v", ty)
	}
}

func TestUnicode(t *testing.T) {
	b := NewBuilder(0, 0)
	b.Add("/", -1, 0, true, 0)
	b.Add("Ölbild ÄRGER.png", 0, 10, false, 0)
	b.Add("données.txt", 0, 5, false, 0)
	x := b.Finish()
	r := run(t, x, Query{Text: "är"})
	if len(r.Hits) != 1 || r.Hits[0].Name != "Ölbild ÄRGER.png" {
		t.Fatalf("non-ASCII case: %v", names(r))
	}
	if !reflect.DeepEqual(r.Hits[0].Match, []int{7, 8}) {
		t.Fatalf("code point positions %v", r.Hits[0].Match)
	}
	if r := run(t, x, Query{Text: "donn"}); len(r.Hits) != 1 || !reflect.DeepEqual(r.Hits[0].Match, []int{0, 1, 2, 3}) {
		t.Fatalf("ASCII query on a non-ASCII name: %+v", r.Hits)
	}
	if r := run(t, x, Query{Text: "ées"}); len(r.Hits) != 1 || !reflect.DeepEqual(r.Hits[0].Match, []int{4, 5, 6}) {
		t.Fatalf("positions after a wide letter: %+v", r.Hits)
	}
}

func TestSaveAndRead(t *testing.T) {
	x := sample()
	var buf bytes.Buffer
	if err := x.Save(&buf); err != nil {
		t.Fatal(err)
	}
	y, err := ReadIndex(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("round trip differs:\n%+v\n%+v", x, y)
	}
	for cut := 0; cut < buf.Len(); cut += 7 {
		if _, err := ReadIndex(bytes.NewReader(buf.Bytes()[:cut])); err == nil {
			t.Fatalf("truncated at %d: no error", cut)
		}
	}
}

func TestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := big(1<<16).Search(ctx, Query{Text: "a"}, func([]string) string { return "" }); err == nil {
		t.Fatal("cancelled search returned results")
	}
}

func big(n int) *Index {
	words := []string{"report", "holiday", "invoice", "node_modules", "package", "index", "photo", "backup", "Screenshot", "readme"}
	exts := []string{".js", ".ts", ".json", ".png", ".mp4", ".pdf", ".dll", ".txt", "", ".go"}
	b := NewBuilder(n, n*24)
	b.Add("C:\\", -1, 0, true, 0)
	dirs := 1
	for i := 1; i < n; i++ {
		parent := (i * 7919) % dirs
		if i%10 == 0 {
			b.Add(fmt.Sprintf("%s-%d", words[i%len(words)], i), parent, 0, true, dirs)
			dirs++
			continue
		}
		b.Add(fmt.Sprintf("%s %s %d%s", words[i%len(words)], words[(i/7)%len(words)], i, exts[(i/3)%len(exts)]), parent, int64(i), false, 0)
	}
	return b.Finish()
}

func BenchmarkSearch2M(b *testing.B) {
	x := big(2_000_000)
	join := func(n []string) string { return path.Join(n...) }
	for _, q := range []Query{{Text: "hol"}, {Text: "screenshot photo 99"}, {Text: "zzzq"}, {Kind: "videos"}, {}} {
		b.Run(fmt.Sprintf("%q/%s", q.Text, q.Kind), func(b *testing.B) {
			for b.Loop() {
				if _, err := x.Search(context.Background(), q, join); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
