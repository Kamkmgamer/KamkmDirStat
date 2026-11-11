//go:build !windows && !linux && !(darwin && cgo)

package main

import (
	"context"
	"errors"
)

func runLocalMonitor(context.Context, <-chan []byte, func(func(*MonitorStatus)) bool) error {
	return errors.New("Monitoring this computer is not available on this system.")
}
