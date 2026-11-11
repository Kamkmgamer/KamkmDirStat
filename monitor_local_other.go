//go:build !windows

package main

import (
	"context"
	"errors"
)

func runLocalMonitor(context.Context, <-chan []byte, func(func(*MonitorStatus)) bool) error {
	return errors.New("Monitoring this computer is only available on Windows.")
}
