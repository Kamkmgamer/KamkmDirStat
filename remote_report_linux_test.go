//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRemoteReportMatchesLocalAggregation(t *testing.T) {
	root, want, files := shellFixture(t)
	c := execServer(t, binDir(t, "sh", "find", "python3"))
	c.Path = root
	local := newTree(root)
	defer local.close()
	if _, err := scanInto(context.Background(), c, local, nil); err != nil {
		t.Fatal(err)
	}
	c.ServerReport = true
	remote := newTree(root)
	defer remote.close()
	var last ScanProgress
	result, err := scanInto(context.Background(), c, remote, func(p ScanProgress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote" || remote.Root.Size != want || remote.Root.Files != files || result.ErrorCount != 0 {
		t.Fatalf("unexpected remote report mode=%s size=%d files=%d errors=%v", result.Mode, remote.Root.Size, remote.Root.Files, result.Errors)
	}
	if last.Mode != "remote" || last.Files != files {
		t.Fatalf("unexpected progress: %+v", last)
	}
	if len(remote.Root.Children) != 0 {
		t.Fatal("complete file listing was downloaded instead of remote report views")
	}
	for id := range local.dirs {
		expected, err := local.view(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := remote.view(id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("view %d differs\nremote: %+v\nlocal: %+v", id, got, expected)
		}
	}
	remote.close()
	if _, err := remote.view(0); err != nil {
		t.Fatalf("cached overview unavailable after connection closes: %v", err)
	}
	if _, err := remote.view(9999); err == nil {
		t.Fatal("uncached folder succeeded after report connection closed")
	}
}

func TestRemoteReportFallbackWithoutPython(t *testing.T) {
	root, want, _ := shellFixture(t)
	c := execServer(t, binDir(t, "sh", "find", "gzip"))
	c.Path = root
	c.ServerReport = true
	tree := newTree(root)
	defer tree.close()
	result, err := scanInto(context.Background(), c, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "shell" || tree.Root.Size != want {
		t.Fatalf("fallback failed: mode=%s size=%d", result.Mode, tree.Root.Size)
	}
}

func TestRemoteReportCancellationBeforeCompletion(t *testing.T) {
	bin := binDir(t, "sh", "python3", "sleep")
	fake := "#!/bin/sh\ncase \"$*\" in *-maxdepth*) exit 0;; esac\nprintf 'd 0 0 x\\000f 8 1 first\\000'\nexec sleep 2\n"
	if err := os.WriteFile(filepath.Join(bin, "find"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	c := execServer(t, bin)
	c.Path = t.TempDir()
	c.ServerReport = true
	tree := newTree(c.Path)
	defer tree.close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := scanInto(ctx, c, tree, func(p ScanProgress) {
			if p.Mode == "remote" {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("remote scan did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote scan did not cancel")
	}
	if len(tree.Root.Children) > 0 || tree.remote != nil {
		t.Fatal("unfinished remote report was published")
	}
}

func TestRemoteReportPermissionErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "locked", "file"), 5000)
	writeFile(t, filepath.Join(root, "open"), 5000)
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "locked"), 0700) })
	c := execServer(t, binDir(t, "sh", "find", "python3"))
	c.Path = root
	c.ServerReport = true
	tree := newTree(root)
	defer tree.close()
	result, err := scanInto(context.Background(), c, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := tree.view(0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote" || result.ErrorCount != 1 || !view.Incomplete {
		t.Fatalf("permissions were not reported: %+v %+v", result, view)
	}
	for _, entry := range view.Entries {
		if entry.Incomplete != (entry.Name == "locked") {
			t.Fatalf("wrong incomplete flag: %+v", entry)
		}
	}
}
