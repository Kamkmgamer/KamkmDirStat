package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func withFastMonitorTimeouts(t *testing.T) {
	stall, connect, delays := monitorStallTimeout, monitorConnectTimeout, monitorRetryDelays
	monitorStallTimeout, monitorConnectTimeout, monitorRetryDelays = 1500*time.Millisecond, 2*time.Second, []time.Duration{10 * time.Millisecond}
	t.Cleanup(func() { monitorStallTimeout, monitorConnectTimeout, monitorRetryDelays = stall, connect, delays })
}

func TestSuperviseMonitorReconnectsAfterStall(t *testing.T) {
	withFastMonitorTimeouts(t)
	var mu sync.Mutex
	status := MonitorStatus{Running: true, Connecting: true}
	update := func(f func(*MonitorStatus)) bool { mu.Lock(); defer mu.Unlock(); f(&status); return true }
	attempts := 0
	sawReconnecting := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := func(ctx context.Context, up func(func(*MonitorStatus)) bool) error {
		attempts++
		mu.Lock()
		sawReconnecting = sawReconnecting || status.Reconnecting
		mu.Unlock()
		up(func(s *MonitorStatus) { s.Connecting = false })
		if attempts == 1 {
			<-ctx.Done() // a stalled connection: no samples, no error
			return ctx.Err()
		}
		up(func(s *MonitorStatus) { s.Seq++ })
		cancel()
		return ctx.Err()
	}
	err := superviseMonitor(ctx, run, update)
	if !errors.Is(err, context.Canceled) || attempts != 2 || !sawReconnecting {
		t.Fatalf("err=%v attempts=%d reconnecting=%v", err, attempts, sawReconnecting)
	}
	if status.Reconnecting || status.Seq != 1 {
		t.Fatalf("status after reconnect: %+v", status)
	}
}

func TestSuperviseMonitorReturnsErrorsBeforeConnecting(t *testing.T) {
	withFastMonitorTimeouts(t)
	update := func(f func(*MonitorStatus)) bool { var s MonitorStatus; f(&s); return true }
	want := errors.New("no python")
	attempts := 0
	err := superviseMonitor(context.Background(), func(context.Context, func(func(*MonitorStatus)) bool) error {
		attempts++
		return want
	}, update)
	if err != want || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestSuperviseMonitorReconnectsAfterDrop(t *testing.T) {
	withFastMonitorTimeouts(t)
	update := func(f func(*MonitorStatus)) bool { var s MonitorStatus; f(&s); return true }
	attempts := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = superviseMonitor(ctx, func(ctx context.Context, up func(func(*MonitorStatus)) bool) error {
		attempts++
		up(func(s *MonitorStatus) { s.Connecting = false })
		if attempts < 3 {
			return errors.New("The monitor stopped unexpectedly: EOF")
		}
		cancel()
		return ctx.Err()
	}, update)
	if attempts != 3 {
		t.Fatalf("attempts=%d", attempts)
	}
}
