//go:build windows

package main

import (
	"fmt"
	"github.com/Microsoft/go-winio"
	"net"
	"os"
	"sync/atomic"
	"testing"
)

var testPipeID atomic.Uint64

func listenTestAgent(t *testing.T) (net.Listener, string) {
	t.Helper()
	name := fmt.Sprintf(`\\.\pipe\ssh-storage-test-%d-%d`, os.Getpid(), testPipeID.Add(1))
	l, err := winio.ListenPipe(name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return l, name
}
