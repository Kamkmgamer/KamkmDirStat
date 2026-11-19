//go:build linux || (darwin && cgo)

package main

import (
	"os"
	"testing"
)

func TestListDrivesFindsSystemDisk(t *testing.T) {
	drives := listDrives()
	if len(drives) == 0 {
		t.Fatal("no drives found")
	}
	for _, d := range drives {
		if d.Total <= 0 || d.Free < 0 || d.Free > d.Total || d.Path == "" {
			t.Errorf("bad drive %+v", d)
		}
		if _, err := os.Stat(d.Path); err != nil {
			t.Errorf("drive path %s: %v", d.Path, err)
		}
	}
	t.Logf("drives: %+v", drives)
}
