package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func waitForTargetState(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func targetMonitorRunner(label string) func(context.Context, <-chan []byte, func(func(*MonitorStatus)) bool) error {
	return func(ctx context.Context, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
		hello, _ := json.Marshal(map[string]string{"host": label})
		update(func(s *MonitorStatus) {
			s.Connecting = false
			s.Hello = hello
			s.Sample = json.RawMessage("{}")
			s.Seq = 1
		})
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case line := <-writes:
				var action MonitorAction
				if json.Unmarshal(line, &action) != nil {
					continue
				}
				result, _ := json.Marshal(map[string]interface{}{"id": action.ID, "ok": true, "message": label})
				update(func(s *MonitorStatus) { s.Results = append(s.Results, result) })
			}
		}
	}
}

func TestMonitorTargetsRunIndependently(t *testing.T) {
	oldLocal, oldRemote := localMonitorRun, remoteMonitorRun
	localMonitorRun = targetMonitorRunner("local")
	remoteMonitorRun = func(ctx context.Context, _ Connection, _ string, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
		return targetMonitorRunner("remote")(ctx, writes, update)
	}
	t.Cleanup(func() {
		stopAllMonitors()
		localMonitorRun, remoteMonitorRun = oldLocal, oldRemote
	})

	if err := startMonitor(Connection{Local: true}); err != nil {
		t.Fatal(err)
	}
	if err := startMonitor(Connection{Host: "server", User: "root", Port: "22"}); err != nil {
		t.Fatal(err)
	}
	waitForTargetState(t, "both monitors", func() bool {
		local, remote := monitorStatusFor("local"), monitorStatusFor("remote")
		return local.Running && remote.Running && !local.Connecting && !remote.Connecting && local.Seq == 1 && remote.Seq == 1
	})

	if err := monitorActionFor("local", MonitorAction{ID: 11, Type: "dropcaches"}); err != nil {
		t.Fatal(err)
	}
	waitForTargetState(t, "the local action", func() bool {
		_, _, found := monitorResult(monitorStatusFor("local"), 11)
		return found
	})
	if _, _, found := monitorResult(monitorStatusFor("remote"), 11); found {
		t.Fatal("a local action was delivered to the SSH monitor")
	}
	if err := stopMonitorFor("local"); err != nil {
		t.Fatal(err)
	}
	if monitorStatusFor("local").Running || !monitorStatusFor("remote").Running {
		t.Fatal("stopping the local monitor changed the SSH monitor")
	}
	if err := monitorActionFor("local", MonitorAction{ID: 12, Type: "dropcaches"}); err == nil {
		t.Fatal("a stopped local monitor accepted an action")
	}
	if err := monitorActionFor("remote", MonitorAction{ID: 21, Type: "dropcaches"}); err != nil {
		t.Fatal(err)
	}
	waitForTargetState(t, "the SSH action", func() bool {
		_, _, found := monitorResult(monitorStatusFor("remote"), 21)
		return found
	})
}
