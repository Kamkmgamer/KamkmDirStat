//go:build !windows

package main

import (
	"errors"
	"net"
	"os"
	"time"
)

func dialAgent(address string) (net.Conn, error) {
	if address == "" || address == "SSH_AUTH_SOCK" || address == "$SSH_AUTH_SOCK" {
		address = os.Getenv("SSH_AUTH_SOCK")
	}
	if address == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set")
	}
	return net.DialTimeout("unix", address, 3*time.Second)
}
