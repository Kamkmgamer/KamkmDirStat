//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// On Linux the local monitor runs the same sampler as on servers, as a child process.
func runLocalMonitor(ctx context.Context, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
	python, err := exec.LookPath("python3")
	if err != nil {
		return errors.New("Monitoring this computer needs Python 3. Install the python3 package and try again.")
	}
	cmd := exec.CommandContext(ctx, python, "-u", "-c", remoteMonitorScript)
	cmd.Env = append(os.Environ(), "KAMKM_LOCAL=1")
	cmd.WaitDelay = time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = monitorProtocol(ctx, input, output, func() { _ = cmd.Process.Kill() }, "this computer", writes, update)
	_ = input.Close() // the sampler exits when its input closes
	_ = cmd.Wait()
	return err
}
