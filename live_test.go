package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLiveScan scans a real server through the same path the window uses. It is skipped
// unless SSE_LIVE_HOST names an SSH host or config alias. It trusts the host key it is shown,
// so only point it at a server you already trust. SSE_LIVE_DUMP writes the first folder
// views as JSON for checking the page with real data.
func TestLiveScan(t *testing.T) {
	host := os.Getenv("SSE_LIVE_HOST")
	if host == "" {
		t.Skip("set SSE_LIVE_HOST to scan a real server")
	}
	root := os.Getenv("SSE_LIVE_PATH")
	if root == "" {
		root = "/"
	}
	// SSE_LIVE_HELPER=1 uses the native scanner, storing it on the server if needed.
	c, err := resolveSSH(Connection{Host: host, Method: "auto", Path: root, SkipVirtual: true, ServerReport: true, ServerHelper: os.Getenv("SSE_LIVE_HELPER") == "1"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Fingerprint, err = fingerprint(c); err != nil {
		t.Fatal(err)
	}
	tree := newTree(root)
	defer tree.close()
	updates := 0
	var firstUpdate time.Duration
	started := time.Now()
	result, err := scanInto(context.Background(), c, tree, func(ScanProgress) {
		if updates == 0 {
			firstUpdate = time.Since(started)
		}
		updates++
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mode=%s files=%d size=%.2f GiB elapsed=%s firstUpdate=%s updates=%d errors=%d", result.Mode, tree.Root.Files, float64(tree.Root.Size)/(1<<30), time.Since(started).Round(time.Millisecond), firstUpdate.Round(time.Millisecond), updates, result.ErrorCount)
	if tree.remote != nil {
		t.Logf("completed remote overview transfer: %d bytes", tree.remote.received.Load())
	}
	viewStarted := time.Now()
	v, err := tree.view(0)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(v)
	t.Logf("root view: %s, %d KB JSON", time.Since(viewStarted).Round(time.Millisecond), len(payload)/1024)
	for i, e := range v.Entries {
		if i == 12 {
			break
		}
		t.Logf("  %-28s %8.2f GiB  %d files", e.Name, float64(e.Size)/(1<<30), e.Files)
	}
	if tree.remote != nil {
		for _, entry := range v.Entries {
			if entry.Directory && entry.Files > 0 {
				before := tree.remote.received.Load()
				start := time.Now()
				sub, err := tree.view(entry.ID)
				if err != nil {
					t.Fatal(err)
				}
				if sub.Size != entry.Size || sub.Files != entry.Files {
					t.Fatal("remote folder report differs from overview")
				}
				t.Logf("on-demand folder report: %s, %d bytes", time.Since(start).Round(time.Millisecond), tree.remote.received.Load()-before)
				break
			}
		}
	}
	if out := os.Getenv("SSE_LIVE_DUMP"); out != "" {
		views := map[int]DirView{0: v}
		for _, e := range v.Entries {
			if e.Directory && len(views) < 6 {
				if sub, err := tree.view(e.ID); err == nil {
					views[e.ID] = sub
				}
			}
		}
		b, _ := json.Marshal(views)
		if err := os.WriteFile(out, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
