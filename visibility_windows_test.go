//go:build windows

package main

import (
	"os"
	"strconv"
	"testing"
)

func TestRectsCover(t *testing.T) {
	target := rect{0, 0, 100, 100}
	cases := []struct {
		name  string
		above []rect
		want  bool
	}{
		{"nothing above", nil, false},
		{"one window over all of it", []rect{{-10, -10, 200, 200}}, true},
		{"one window over part of it", []rect{{0, 0, 100, 60}}, false},
		{"two windows together", []rect{{0, 0, 100, 60}, {0, 50, 100, 100}}, true},
		{"a one-pixel gap", []rect{{0, 0, 100, 50}, {0, 51, 100, 100}}, false},
		{"windows elsewhere", []rect{{200, 200, 300, 300}}, false},
		{"four quarters", []rect{{0, 0, 50, 50}, {50, 0, 100, 50}, {0, 50, 50, 100}, {50, 50, 100, 100}}, true},
	}
	for _, c := range cases {
		if got := rectsCover(target, c.above); got != c.want {
			t.Errorf("%s: rectsCover = %v, want %v", c.name, got, c.want)
		}
	}
	if !rectsCover(rect{}, nil) {
		t.Error("an empty window, such as one off every screen, should count as covered")
	}
	// Negative coordinates, as on a screen left of the main one.
	if !rectsCover(rect{-1920, 0, -10, 1000}, []rect{{-1930, -8, 0, 1010}}) {
		t.Error("a window on a screen left of the main one should be coverable")
	}
}

func TestIntersectRect(t *testing.T) {
	if got := intersectRect(rect{0, 0, 100, 100}, rect{50, 50, 150, 150}); got != (rect{50, 50, 100, 100}) {
		t.Errorf("overlap = %v", got)
	}
	if got := intersectRect(rect{0, 0, 10, 10}, rect{20, 20, 30, 30}); got != (rect{}) {
		t.Errorf("no overlap = %v", got)
	}
}

// KAMKM_COVER_HWND=<decimal window handle> reports whether that window is covered now.
func TestWindowCoveredLive(t *testing.T) {
	s := os.Getenv("KAMKM_COVER_HWND")
	if s == "" {
		t.Skip("set KAMKM_COVER_HWND to a window handle")
	}
	h, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := windowBounds(uintptr(h))
	t.Logf("bounds %v covered=%v", r, windowCovered(uintptr(h)))
}
