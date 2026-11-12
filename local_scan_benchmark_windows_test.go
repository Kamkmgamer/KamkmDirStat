package main

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestBenchmarkSystemDriveScan scans the whole system drive and reports the time. It only
// runs when KAMKM_SCAN_BENCHMARK=1. Run it as administrator to use the direct NTFS scan.
func TestBenchmarkSystemDriveScan(t *testing.T) {
	if os.Getenv("KAMKM_SCAN_BENCHMARK") != "1" {
		t.Skip("set KAMKM_SCAN_BENCHMARK=1 to scan the system drive")
	}
	root := os.Getenv("SystemDrive") + `\`
	tree := newLocalTree([]string{root})
	start := time.Now()
	result, err := localScanInto(context.Background(), []string{root}, tree, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("%s mode=%s took=%s files=%d folders=%d size=%.1f GiB used=%.1f GiB errors=%d notes=%q memory=%d MiB",
		root, result.Mode, elapsed.Round(time.Millisecond), result.Files, len(tree.dirs), float64(tree.Root.Size)/(1<<30),
		float64(volumeUsedBytes(root))/(1<<30), result.ErrorCount, result.Notes, mem.Sys>>20)
	if os.Getenv("KAMKM_SCAN_COMPARE") != "1" {
		return
	}
	// Scan again by listing folders and compare each top-level folder.
	localScanDirect = false
	defer func() { localScanDirect = true }()
	listed := newLocalTree([]string{root})
	start = time.Now()
	listResult, err := localScanInto(context.Background(), []string{root}, listed, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("listing mode=%s took=%s files=%d size=%.1f GiB errors=%d", listResult.Mode, time.Since(start).Round(time.Millisecond),
		listResult.Files, float64(listed.Root.Size)/(1<<30), listResult.ErrorCount)
	// Hard links: the listing counts every name, the direct scan each file once.
	extraNames := map[string]int{}
	if r, closeVolume, err := openNTFSVolume(root); err == nil {
		v, err := openNTFS(r)
		if err == nil {
			if idx, err := v.read(context.Background(), nil); err == nil {
				for _, e := range idx.entries {
					if e.links == 0 || e.flags&mftDir != 0 {
						continue
					}
					top, p := "", e
					for depth := 0; depth < 4096 && int(p.parent) < len(idx.entries); depth++ {
						if p.parent == ntfsRootRecord {
							top = p.name
							break
						}
						p = idx.entries[p.parent]
					}
					extraNames[top] += int(e.links)
				}
			}
		}
		closeVolume()
	}
	byName := map[string]*Node{}
	for _, c := range listed.Root.Children {
		byName[c.Name] = c
	}
	t.Logf("  %-34s %12s %12s %10s %10s %10s", "folder", "direct GiB", "listed GiB", "direct", "+links", "listed")
	for _, c := range tree.Root.Children {
		l := byName[c.Name]
		delete(byName, c.Name)
		if l == nil {
			t.Logf("  %-34s %12.2f %12s %10d %10s", c.Name, float64(c.Size)/(1<<30), "-", c.Files, "-")
			continue
		}
		if c.Files != l.Files || c.Size != l.Size {
			t.Logf("  %-34s %12.2f %12.2f %10d %10d %10d", c.Name, float64(c.Size)/(1<<30), float64(l.Size)/(1<<30), c.Files, c.Files+extraNames[c.Name], l.Files)
		}
	}
	for name, l := range byName {
		t.Logf("  %-34s %12s %12.2f %10s %10d (listed only)", name, "-", float64(l.Size)/(1<<30), "-", l.Files)
	}
}
