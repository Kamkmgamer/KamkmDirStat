package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

// TestRealDrives indexes this computer's drives and searches them. It reads every folder
// on every drive, so it runs only when KAMKM_DRIVE_TEST is set; KAMKM_DRIVE_TEST=C:\ picks
// one drive.
func TestRealDrives(t *testing.T) {
	only := os.Getenv("KAMKM_DRIVE_TEST")
	if only == "" {
		t.Skip("set KAMKM_DRIVE_TEST=1 to index this computer's drives")
	}
	for _, d := range listDrives() {
		if !indexable(d) || only != "1" && !strings.EqualFold(only, d.Path) {
			continue
		}
		var read atomic.Int64
		idx := search.NewLive()
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- walkDrive(context.Background(), d.Path, idx, &read) }()
		tick := time.NewTicker(10 * time.Second)
	wait:
		for {
			select {
			case err := <-done:
				if err != nil {
					t.Logf("%s: %v", d.Path, err)
				}
				break wait
			case <-tick.C:
				t.Logf("%s (%s, %s): %d entries after %s", d.Path, d.Kind, d.FileSystem, read.Load(), time.Since(start).Round(time.Second))
			}
		}
		tick.Stop()
		s := time.Now()
		r, _ := idx.Search(context.Background(), search.Query{Text: "notepad"}, joinDrivePath)
		t.Logf("%s: %d entries walked in %s; search %s, %d matches", d.Path, idx.Len(), time.Since(start).Round(time.Millisecond), time.Since(s), r.Total)
	}
}

// TestLiveDriveIndex reads a drive's file table, then creates, renames, grows and deletes
// a file and checks each change reaches the index through the change journal. It uses the
// system drive when run as administrator with KAMKM_DRIVE_TEST set, or the NTFS drive in
// KAMKM_LIVE_ROOT, such as a USB drive, which Windows lets users read directly.
func TestLiveDriveIndex(t *testing.T) {
	root := os.Getenv("KAMKM_LIVE_ROOT")
	if root == "" {
		if os.Getenv("KAMKM_DRIVE_TEST") == "" || !isElevated() {
			t.Skip("run as administrator with KAMKM_DRIVE_TEST=1, or set KAMKM_LIVE_ROOT")
		}
		root = filepath.VolumeName(os.TempDir()) + `\`
	}
	ix := &driveIndexer{dir: t.TempDir()}
	s := &driveStore{ix: ix, root: root, key: "live-test", done: make(chan struct{}), loaded: make(chan struct{}), refresh: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go s.run(ctx)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Minute)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %+v", what, s.status())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor("the index", func() bool { st := s.status(); return st.State == "ready" && st.Mode == "live" })
	t.Logf("%s: %d entries from the file table in %s", root, s.status().Entries, time.Since(start))
	find := func(text string) []search.Hit {
		s.mu.Lock()
		idx := s.idx
		s.mu.Unlock()
		r, err := idx.Search(context.Background(), search.Query{Text: text, Kind: "files"}, joinDrivePath)
		if err != nil {
			t.Fatal(err)
		}
		return r.Hits
	}
	dir, err := os.MkdirTemp(root, "kamkm-live-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	name := fmt.Sprintf("kamkm-live-%d.txt", time.Now().UnixNano())
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := time.Now()
	waitFor("the new file", func() bool { h := find(name); return len(h) == 1 && h[0].Path == dir && h[0].Size == 5 })
	t.Logf("created file found after %s", time.Since(changed))
	renamed := strings.Replace(name, "live", "moved", 1)
	if err := os.Rename(p, filepath.Join(dir, renamed)); err != nil {
		t.Fatal(err)
	}
	waitFor("the rename", func() bool { return len(find(name)) == 0 && len(find(renamed)) == 1 })
	if err := os.WriteFile(filepath.Join(dir, renamed), make([]byte, 70000), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor("the new size", func() bool { h := find(renamed); return len(h) == 1 && h[0].Size == 70000 })
	os.Remove(filepath.Join(dir, renamed))
	waitFor("the delete", func() bool { return len(find(renamed)) == 0 })
	cancel()
	<-s.done
	// The saved index resumes from the journal.
	again := &driveStore{ix: ix, root: root, key: "live-test", done: make(chan struct{}), loaded: make(chan struct{}), refresh: make(chan struct{}, 1)}
	again.load()
	if again.idx == nil || again.mode != "live" || again.journal == 0 {
		t.Fatalf("not saved: %+v", again.status())
	}
}
