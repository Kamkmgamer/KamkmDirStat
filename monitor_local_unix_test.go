//go:build linux || (darwin && cgo)

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalMonitorSamplesAndEndsProcess(t *testing.T) {
	if err := startMonitor(Connection{Local: true}); err != nil {
		t.Fatal(err)
	}
	defer stopMonitor()
	s := waitUnixMonitor(t, func(s MonitorStatus) bool { return s.Seq >= 2 })
	var hello struct{ Platform, Privilege, User, System string }
	if err := json.Unmarshal(s.Hello, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Platform != runtime.GOOS || hello.User == "" || hello.Privilege == "" {
		t.Fatalf("unexpected hello: %s", s.Hello)
	}
	var sample struct {
		CPU struct {
			Total float64   `json:"total"`
			Cores []float64 `json:"cores"`
		} `json:"cpu"`
		Memory struct {
			Total, Used int64
		} `json:"memory"`
		GPUs      []struct{ Name string } `json:"gpus"`
		GPUNote   string                  `json:"gpuNote"`
		Network   []struct{ Name string } `json:"network"`
		Processes []struct {
			PID       int  `json:"pid"`
			Protected bool `json:"protected"`
		} `json:"processes"`
		ProcessCount int `json:"processCount"`
	}
	if err := json.Unmarshal(s.Sample, &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.CPU.Cores) == 0 || sample.CPU.Total < 0 || sample.CPU.Total > 100 {
		t.Fatalf("bad CPU sample: %+v", sample.CPU)
	}
	if sample.Memory.Total <= 0 || sample.Memory.Used <= 0 || sample.Memory.Used > sample.Memory.Total {
		t.Fatalf("bad memory sample: %+v", sample.Memory)
	}
	if sample.ProcessCount < 3 || len(sample.Processes) == 0 {
		t.Fatalf("too few processes: %d", sample.ProcessCount)
	}
	t.Logf("%s, %d cores, memory %d MiB used of %d MiB, %d interfaces, %d processes, GPUs %v %s", hello.System,
		len(sample.CPU.Cores), sample.Memory.Used>>20, sample.Memory.Total>>20, len(sample.Network), sample.ProcessCount, sample.GPUs, sample.GPUNote)

	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer victim.Process.Kill()
	start := testProcessStart(t, victim.Process.Pid)
	for _, a := range []MonitorAction{
		{ID: 1, Type: "kill", PID: victim.Process.Pid, Start: start + 1, Signal: "TERM"},
		{ID: 2, Type: "kill", PID: victim.Process.Pid, Start: start, Signal: "TERM"},
	} {
		if err := monitorAction(a); err != nil {
			t.Fatal(err)
		}
		waitUnixMonitor(t, func(s MonitorStatus) bool { _, _, found := monitorResult(s, a.ID); return found })
	}
	s = monitorStatus()
	if _, message, _ := monitorResult(s, 1); !strings.Contains(message, "already exited") {
		t.Errorf("stale start time was not refused: %q", message)
	}
	if ok, message, _ := monitorResult(s, 2); !ok {
		t.Errorf("ending own process failed: %q", message)
	}
	done := make(chan error, 1)
	go func() { done <- victim.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("process still running after End")
	}
}

func waitUnixMonitor(t *testing.T, done func(MonitorStatus) bool) MonitorStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s := monitorStatus()
		if done(s) {
			return s
		}
		if !s.Running {
			t.Fatalf("monitor stopped: %q", s.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %+v", monitorStatus())
	return MonitorStatus{}
}

func TestListDrivesFindsSystemDisk(t *testing.T) {
	drives := listDrives()
	if len(drives) == 0 {
		t.Fatal("no drives found")
	}
	for _, d := range drives {
		if d.Total <= 0 || d.Free < 0 || d.Free > d.Total || d.Path == "" {
			t.Errorf("bad drive %+v", d)
		}
		if _, err := os.Stat(d.Path); err != nil {
			t.Errorf("drive path %s: %v", d.Path, err)
		}
	}
	t.Logf("drives: %+v", drives)
}
