//go:build windows

package main

import (
	"context"
	"github.com/Microsoft/go-winio"
	"net"
	"os"
	"strings"
	"time"
)

func dialAgent(address string) (net.Conn, error) {
	if address == "" || address == "SSH_AUTH_SOCK" || address == "$SSH_AUTH_SOCK" {
		address = os.Getenv("SSH_AUTH_SOCK")
	}
	if address == "" {
		address = `\\.\pipe\openssh-ssh-agent`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if strings.HasPrefix(strings.ToLower(address), `\\.\pipe\`) {
		return winio.DialPipeContext(ctx, address)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", address)
}
