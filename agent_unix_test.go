//go:build !windows

package main

import (
	"net"
	"path/filepath"
	"testing"
)

func listenTestAgent(t *testing.T) (net.Listener, string) {
	t.Helper()
	name := filepath.Join(t.TempDir(), "agent.sock")
	l, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	return l, name
}
