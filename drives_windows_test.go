package main

import (
	"os"
	"strings"
	"testing"
)

func TestListDrives(t *testing.T) {
	drives := listDrives()
	system := strings.ToUpper(os.Getenv("SystemDrive")) + `\`
	for _, d := range drives {
		if strings.EqualFold(d.Path, system) {
			if d.Total <= 0 || d.Free < 0 || d.Free > d.Total || d.Kind != "fixed" {
				t.Fatalf("bad system drive: %+v", d)
			}
			return
		}
	}
	t.Fatalf("system drive %s missing from %+v", system, drives)
}
