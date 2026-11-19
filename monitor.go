package main

import (
	"context"

	"github.com/Kamkmgamer/KamkmDirStat/internal/history"
	"github.com/Kamkmgamer/KamkmDirStat/internal/monitor"
	"golang.org/x/crypto/ssh"
)

// Each computer has its own monitor. Selecting another computer in the page never stops
// the first one, and samples continue into each computer's own status and history.

func startMonitor(c Connection) error {
	target := monitor.Target{Machine: history.Local}
	if !c.Local {
		target.Machine = history.Remote(c.User, c.Host, c.Port)
		target.Connect = func(ctx context.Context) (*ssh.Client, error) { return connectVerified(ctx, c) }
	}
	return machineOf(c).monitor.Start(target)
}

func stopMonitorFor(target string) error { return machineFor(target).monitor.Stop() }

func stopAllMonitors() {
	for _, m := range machines {
		_ = m.monitor.Stop()
	}
}

func monitorStatusFor(target string) monitor.Status { return machineFor(target).monitor.Status() }

func monitorActionFor(target string, a monitor.Action) error {
	return machineFor(target).monitor.Action(a)
}
