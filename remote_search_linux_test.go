//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// Searches of a scan kept on the server, by the Python report and by the native scanner,
// find the same entries as a search of the same folder scanned into the app, and server
// videos can be copied to this computer.
func TestRemoteSearchAndOpen(t *testing.T) {
	useTestHelper(t)
	root, _, _ := shellFixture(t)
	// find and the native scanner count a different name of a hard-linked file.
	if err := os.Remove(filepath.Join(root, "hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "media", "Holiday Clip.mp4"), 200_000)
	writeFile(t, filepath.Join(root, "media", "cover.JPG"), 3000)
	writeFile(t, filepath.Join(root, "notes", "holiday plan.txt"), 100)
	writeFile(t, filepath.Join(root, "setup.sh"), 50)
	home := t.TempDir()
	c := execServer(t, binDir(t, helperTools...), "HOME="+home)
	c.Path = root
	local := newTree(root)
	defer local.close()
	if _, err := scanInto(context.Background(), c, local, nil); err != nil {
		t.Fatal(err)
	}
	queries := []search.Query{
		{Text: "holiday"},
		{Text: "hol clip"},
		{Text: "data", Sort: "size"},
		{Kind: "videos"},
		{Kind: "images"},
		{Kind: "folders"},
		{Exts: []string{"bin"}},
		{},
	}
	ctx := context.Background()
	for _, mode := range []struct {
		name   string
		helper bool
	}{{"python", false}, {"native", true}} {
		t.Run(mode.name, func(t *testing.T) {
			c := c
			c.ServerReport, c.ServerHelper = true, mode.helper
			remote := newTree(root)
			defer remote.close()
			if _, err := scanInto(ctx, c, remote, nil); err != nil {
				t.Fatal(err)
			}
			for _, q := range queries {
				want, err := local.search(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				got, err := remote.search(ctx, q)
				if err != nil {
					t.Fatalf("%+v: %v", q, err)
				}
				key := func(r search.Result) []string {
					var out []string
					for _, h := range r.Hits {
						out = append(out, h.Path+" | "+h.Name)
					}
					// Equal sizes and scores keep scan order, which differs between scanners.
					slices.Sort(out)
					return out
				}
				if !reflect.DeepEqual(key(got), key(want)) || got.Total != want.Total || got.TotalSize != want.TotalSize {
					t.Fatalf("%+v:\nserver %v (%d, %d)\nlocal  %v (%d, %d)", q, key(got), got.Total, got.TotalSize, key(want), want.Total, want.TotalSize)
				}
			}
			r, err := remote.search(ctx, search.Query{Text: "clip", Types: true})
			if err != nil || len(r.Hits) != 1 || len(r.Types) == 0 {
				t.Fatalf("clip: %+v %v", r, err)
			}
			h := r.Hits[0]

			var launched []string
			old := launch
			launch = func(p string, reveal bool) error { launched = append(launched, p); return nil }
			defer func() { launch = old }()
			s := runOpen(ctx, remote, OpenRequest{Dir: h.Parent, Name: h.Name, Action: "open"})
			if s.Confirm != "download" || len(launched) != 0 || s.Path != filepath.Join(root, "media", "Holiday Clip.mp4") {
				t.Fatalf("server files are copied only after asking: %+v", s)
			}
			s = runOpen(ctx, remote, OpenRequest{Dir: h.Parent, Name: h.Name, Action: "open", Confirmed: true})
			if s.Error != "" || len(launched) != 1 {
				t.Fatalf("copy: %+v", s)
			}
			want, _ := os.ReadFile(filepath.Join(root, "media", "Holiday Clip.mp4"))
			copied, err := os.ReadFile(launched[0])
			if err != nil || !reflect.DeepEqual(copied, want) || filepath.Base(launched[0]) != "Holiday Clip.mp4" {
				t.Fatalf("copied %s: %d bytes, %v", launched[0], len(copied), err)
			}
			os.RemoveAll(filepath.Dir(launched[0]))
			for _, name := range []string{"setup.sh", "small.txt"} {
				if s := runOpen(ctx, remote, OpenRequest{Dir: 0, Name: name, Action: "open", Confirmed: true}); s.Error == "" {
					t.Fatalf("%s: only media files are copied from servers", name)
				}
			}
			if s := runOpen(ctx, remote, OpenRequest{Dir: 0, Name: "small.txt", Action: "reveal"}); s.Error == "" {
				t.Fatal("server files cannot be shown in a local folder window")
			}
		})
	}
}
