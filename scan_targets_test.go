package main

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

func waitForTargetState(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestScanTargetsRetainIndependentResults(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	root := t.TempDir()

	oldLocal, oldRemote := localScanRun, remoteScanRun
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseScans := func() { releaseOnce.Do(func() { close(release) }) }
	localScanRun = func(ctx context.Context, _ []string, tree *Tree, _ func(ScanProgress)) (ScanResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return ScanResult{}, ctx.Err()
		}
		tree.mu.Lock()
		tree.addChild(tree.Root, "local.txt", false, 10)
		tree.mu.Unlock()
		tree.finalize()
		return ScanResult{Mode: "local"}, nil
	}
	remoteScanRun = func(ctx context.Context, _ Connection, tree *Tree, _ func(ScanProgress)) (ScanResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return ScanResult{}, ctx.Err()
		}
		tree.mu.Lock()
		tree.addChild(tree.Root, "remote.txt", false, 20)
		tree.mu.Unlock()
		tree.finalize()
		return ScanResult{Mode: "remote"}, nil
	}
	t.Cleanup(func() {
		releaseScans()
		_ = clearResultsFor("local")
		_ = clearResultsFor("remote")
		localScanRun, remoteScanRun = oldLocal, oldRemote
	})

	if err := startScan(Connection{Local: true, Paths: []string{root}}); err != nil {
		t.Fatal(err)
	}
	if err := startScan(Connection{Path: "/srv"}); err != nil {
		t.Fatal(err)
	}
	if !scanStatusFor("local").Running || !scanStatusFor("remote").Running {
		t.Fatal("local and SSH scans did not run at the same time")
	}
	releaseScans()
	waitForTargetState(t, "both scans", func() bool {
		return !scanStatusFor("local").Running && !scanStatusFor("remote").Running
	})
	for _, target := range []string{"local", "remote"} {
		if status := scanStatusFor(target); status.Error != "" || status.Files != 1 {
			t.Fatalf("%s scan status: %+v", target, status)
		}
		if err := startViewFor(target, 0); err != nil {
			t.Fatal(err)
		}
	}
	waitForTargetState(t, "both folder views", func() bool {
		return !viewStatusFor("local").Running && !viewStatusFor("remote").Running
	})
	for _, tc := range []struct{ target, name string }{{"local", "local.txt"}, {"remote", "remote.txt"}} {
		status := viewStatusFor(tc.target)
		if status.Error != "" || status.View == nil || len(status.View.Entries) != 1 || status.View.Entries[0].Name != tc.name {
			t.Fatalf("%s view: %+v", tc.target, status)
		}
	}

	localSeq, err := startSearchFor("local", search.Query{Text: "local"})
	if err != nil {
		t.Fatal(err)
	}
	remoteSeq, err := startSearchFor("remote", search.Query{Text: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTargetState(t, "both searches", func() bool {
		return searchStatusFor("local").Seq >= localSeq && searchStatusFor("remote").Seq >= remoteSeq
	})
	for _, tc := range []struct {
		target, name string
		seq          int
	}{{"local", "local.txt", localSeq}, {"remote", "remote.txt", remoteSeq}} {
		status := searchStatusFor(tc.target)
		if status.Seq != tc.seq || status.Error != "" || status.Result == nil || !reflect.DeepEqual(hitNames(*status.Result), []string{tc.name}) {
			t.Fatalf("%s search: %+v", tc.target, status)
		}
	}

	for _, tc := range []struct{ target, name, want string }{
		{"local", "local.txt", filepath.Join(root, "local.txt")},
		{"remote", "remote.txt", "/srv/remote.txt"},
	} {
		seq, err := startOpen(OpenRequest{Target: tc.target, Name: tc.name, Action: "path"})
		if err != nil {
			t.Fatal(err)
		}
		waitForTargetState(t, tc.target+" open", func() bool {
			status := openStatus()
			return status.Seq == seq && !status.Running
		})
		if status := openStatus(); status.Error != "" || status.Path != tc.want {
			t.Fatalf("%s open: %+v, want path %q", tc.target, status, tc.want)
		}
	}

	if err := clearResultsFor("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := viewDirFor("local", 0); err == nil {
		t.Fatal("cleared local results are still available")
	}
	if view, err := viewDirFor("remote", 0); err != nil || len(view.Entries) != 1 || view.Entries[0].Name != "remote.txt" {
		t.Fatalf("clearing local results dumped the SSH scan: %+v, %v", view, err)
	}
}
