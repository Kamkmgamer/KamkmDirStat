//go:build linux

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// useTestHelper builds cmd/kamkm-helper and makes the app use it for this machine.
func useTestHelper(t *testing.T) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "kamkm-helper")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/kamkm-helper")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	program, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	z.Write(program)
	z.Close()
	build, err := newHelperBuild(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	old := loadHelper
	loadHelper = func(arch string) (helperBuild, bool) { return build, arch == runtime.GOARCH }
	t.Cleanup(func() { loadHelper = old })
}

// helperTools are the commands storing and running the helper needs on the server.
var helperTools = []string{"sh", "find", "python3", "uname", "mkdir", "chmod", "mv", "rm", "gzip", "sha256sum", "cut"}

func TestHelperReportMatchesLocalAggregation(t *testing.T) {
	useTestHelper(t)
	root, want, files := shellFixture(t)
	// Which name of a hard-linked file is counted differs between find and the helper, so
	// compare views without one; TestHelperCountsHardLinksOnce covers them.
	if err := os.Remove(filepath.Join(root, "hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	files--
	home := t.TempDir()
	c := execServer(t, binDir(t, helperTools...), "HOME="+home)
	c.Path = root
	local := newTree(root)
	defer local.close()
	if _, err := scanInto(context.Background(), c, local, nil); err != nil {
		t.Fatal(err)
	}

	c.ServerReport, c.ServerHelper = true, true
	for scan := 1; scan <= 2; scan++ {
		uploads := helperUploads.Load()
		remote := newTree(root)
		result, err := scanInto(context.Background(), c, remote, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Mode != "remote-native" || remote.Root.Size != want || remote.Root.Files != files || result.ErrorCount != 0 {
			t.Fatalf("scan %d: mode=%s size=%d files=%d errors=%v notes=%v", scan, result.Mode, remote.Root.Size, remote.Root.Files, result.Errors, result.Notes)
		}
		// The helper is stored on the first scan and reused afterwards.
		if stored := helperUploads.Load() - uploads; stored != map[int]int64{1: 1, 2: 0}[scan] {
			t.Fatalf("scan %d stored the helper %d times", scan, stored)
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
				t.Fatalf("view %d differs\nhelper: %+v\nlocal:  %+v", id, got, expected)
			}
		}
		remote.close()
	}
	stored, _ := filepath.Glob(filepath.Join(home, ".cache", "kamkmdirstat", "helper-*"))
	if len(stored) != 1 {
		t.Fatalf("stored helpers: %v", stored)
	}
	if info, err := os.Stat(stored[0]); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("stored helper permissions: %v %v", info.Mode(), err)
	}
}

func TestHelperCountsHardLinksOnce(t *testing.T) {
	useTestHelper(t)
	root, want, files := shellFixture(t)
	c := execServer(t, binDir(t, helperTools...), "HOME="+t.TempDir())
	c.Path, c.ServerReport, c.ServerHelper = root, true, true
	tree := newTree(root)
	defer tree.close()
	result, err := scanInto(context.Background(), c, tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-native" || tree.Root.Size != want || tree.Root.Files != files {
		t.Fatalf("mode=%s size=%d files=%d, want %d bytes in %d files", result.Mode, tree.Root.Size, tree.Root.Files, want, files)
	}
}

func TestHelperFallsBack(t *testing.T) {
	useTestHelper(t)
	root, want, _ := shellFixture(t)
	// A server the helper was not built for uses the Python report.
	bin := binDir(t, helperTools...)
	os.Remove(filepath.Join(bin, "uname"))
	if err := os.WriteFile(filepath.Join(bin, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -s) echo Linux ;; *) echo riscv64 ;; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	c := execServer(t, bin, "HOME="+home)
	c.Path, c.ServerReport, c.ServerHelper = root, true, true
	tree := newTree(root)
	defer tree.close()
	start := time.Now()
	result, err := scanInto(context.Background(), c, tree, nil)
	t.Logf("unsupported server: %s", time.Since(start))
	if err != nil || result.Mode != "remote" || tree.Root.Size != want {
		t.Fatalf("unsupported server: mode=%s size=%d err=%v", result.Mode, tree.Root.Size, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, ".cache", "kamkmdirstat")); len(entries) != 0 {
		t.Fatal("helper stored on a server it cannot run on")
	}

	// A home folder where programs may not run (a noexec mount) makes the stored helper
	// look missing. It is stored once, found unusable and removed, and later scans in the
	// session go straight to the Python report.
	bin = binDir(t, helperTools...)
	os.Remove(filepath.Join(bin, "chmod"))
	if err := os.WriteFile(filepath.Join(bin, "chmod"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	noexecHome := t.TempDir()
	c = execServer(t, bin, "HOME="+noexecHome, "XDG_CACHE_HOME="+filepath.Join(noexecHome, "cache"))
	c.Path, c.ServerReport, c.ServerHelper = root, true, true
	for scan := 1; scan <= 2; scan++ {
		uploads := helperUploads.Load()
		tree := newTree(root)
		start := time.Now()
		result, err := scanInto(context.Background(), c, tree, nil)
		tree.close()
		t.Logf("noexec home, scan %d: %s, notes %q", scan, time.Since(start), result.Notes)
		if err != nil || result.Mode != "remote" || tree.Root.Size != want {
			t.Fatalf("noexec scan %d: mode=%s size=%d err=%v", scan, result.Mode, tree.Root.Size, err)
		}
		if scan == 2 && (helperUploads.Load() != uploads || len(result.Notes) != 0) {
			t.Fatal("helper stored again on a noexec server")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(noexecHome, "cache", "kamkmdirstat", "helper-*")); len(left) != 0 {
		t.Fatalf("unusable helper left behind: %v", left)
	}

	// Turned off, the helper is not used or stored.
	c.ServerHelper = false
	off := newTree(root)
	defer off.close()
	if result, err := scanInto(context.Background(), c, off, nil); err != nil || result.Mode != "remote" {
		t.Fatalf("helper off: mode=%s err=%v", result.Mode, err)
	}
}
