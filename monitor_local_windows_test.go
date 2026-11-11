package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestLocalMonitorSamplesAndEndsProcess(t *testing.T) {
	if err := startMonitor(Connection{Local: true}); err != nil {
		t.Fatal(err)
	}
	defer stopMonitor()
	s := waitLocalMonitor(t, func(s MonitorStatus) bool { return s.Seq >= 2 })
	var hello struct{ Platform, Privilege, User, System, CPUModel string }
	if err := json.Unmarshal(s.Hello, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Platform != "windows" || hello.User == "" || !strings.Contains(hello.System, "Windows") {
		t.Fatalf("unexpected hello: %s", s.Hello)
	}
	if want := map[bool]string{true: "admin", false: "none"}[windows.GetCurrentProcessToken().IsElevated()]; hello.Privilege != want {
		t.Fatalf("privilege %q, want %q", hello.Privilege, want)
	}
	var sample struct {
		CPU struct {
			Total float64   `json:"total"`
			Cores []float64 `json:"cores"`
		} `json:"cpu"`
		Memory struct {
			Total, Used, Available, CommitLimit int64
		} `json:"memory"`
		GPUs []struct {
			Name string   `json:"name"`
			Util *float64 `json:"util"`
		} `json:"gpus"`
		Network   []struct{ Name string } `json:"network"`
		Processes []struct {
			PID       int    `json:"pid"`
			Name      string `json:"name"`
			Own       bool   `json:"own"`
			Protected bool   `json:"protected"`
		} `json:"processes"`
		ProcessCount int `json:"processCount"`
	}
	if err := json.Unmarshal(s.Sample, &sample); err != nil {
		t.Fatal(err)
	}
	if len(sample.CPU.Cores) == 0 || sample.CPU.Total < 0 || sample.CPU.Total > 100 {
		t.Fatalf("bad CPU sample: %+v", sample.CPU)
	}
	if sample.Memory.Total <= 0 || sample.Memory.Used <= 0 || sample.Memory.Used > sample.Memory.Total || sample.Memory.CommitLimit <= 0 {
		t.Fatalf("bad memory sample: %+v", sample.Memory)
	}
	if sample.ProcessCount < 10 || len(sample.Processes) == 0 {
		t.Fatalf("too few processes: %d", sample.ProcessCount)
	}
	for _, g := range sample.GPUs {
		if g.Util != nil {
			t.Logf("GPU %q use %.1f%%", g.Name, *g.Util)
		}
	}
	t.Logf("%d cores, %d network interfaces, %d processes", len(sample.CPU.Cores), len(sample.Network), sample.ProcessCount)

	// The system process is refused, and so is a PID whose start time no longer matches.
	if err := monitorAction(MonitorAction{ID: 1, Type: "kill", PID: 4, Start: 0, Signal: "KILL"}); err != nil {
		t.Fatal(err)
	}
	victim := exec.Command("ping", "-n", "60", "127.0.0.1")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer victim.Process.Kill()
	start := processStart(t, victim.Process.Pid)
	actions := []MonitorAction{
		{ID: 2, Type: "kill", PID: victim.Process.Pid, Start: start + 1, Signal: "KILL"},
		{ID: 3, Type: "kill", PID: victim.Process.Pid, Start: start, Signal: "TERM"},
		{ID: 4, Type: "kill", PID: victim.Process.Pid, Start: start, Signal: "KILL"},
	}
	for _, a := range actions {
		if err := monitorAction(a); err != nil {
			t.Fatal(err)
		}
		waitLocalMonitor(t, func(s MonitorStatus) bool { _, _, found := monitorResult(s, a.ID); return found })
	}
	s = monitorStatus()
	expect := map[int]struct {
		ok   bool
		text string
	}{1: {false, "part of Windows"}, 2: {true, "already exited"}, 3: {false, "no open window"}, 4: {true, "Force-ended"}}
	for id, want := range expect {
		ok, message, _ := monitorResult(s, id)
		if ok != want.ok || !strings.Contains(message, want.text) {
			t.Errorf("action %d: ok=%v %q, want ok=%v containing %q", id, ok, message, want.ok, want.text)
		}
	}
	done := make(chan error, 1)
	go func() { done <- victim.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("process still running after Force end")
	}
}

func processStart(t *testing.T, pid int) int64 {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	return (int64(created.HighDateTime)<<32 | int64(created.LowDateTime)) / 10000
}

func waitLocalMonitor(t *testing.T, done func(MonitorStatus) bool) MonitorStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
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

func TestListDrives(t *testing.T) {
	drives := listDrives()
	system := strings.ToUpper(os.Getenv("SystemDrive")) + `\`
	for _, d := range drives {
		if strings.EqualFold(d.Path, system) {
			if d.Total <= 0 || d.Free < 0 || d.Free > d.Total || d.Kind != "fixed" {
				t.Fatalf("bad system drive: %+v", d)
			}
			return
		}
	}
	t.Fatalf("system drive %s missing from %+v", system, drives)
}
