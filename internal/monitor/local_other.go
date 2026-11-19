//go:build !windows && !linux && !(darwin && cgo)

package monitor

import (
	"context"
	"errors"
)

func runLocalMonitor(context.Context, <-chan []byte, func(func(*Status)) bool) error {
	return errors.New("Monitoring this computer is not available on this system.")
}
