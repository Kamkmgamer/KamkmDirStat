//go:build darwin && cgo

package main

import "testing"

// testProcessStart is the start time the Mac sampler reports, in milliseconds.
func testProcessStart(t *testing.T, pid int) int64 {
	t.Helper()
	start, ok := macProcessStart(pid)
	if !ok {
		t.Fatalf("no process information for %d", pid)
	}
	return start
}
