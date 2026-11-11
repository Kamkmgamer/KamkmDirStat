//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// testProcessStart is the start time the Linux sampler reports: clock ticks after boot.
func testProcessStart(t *testing.T, pid int) int64 {
	t.Helper()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return start
}
