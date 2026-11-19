//go:build linux

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/monitor"
)

func monitorResult(s monitor.Status, id int) (ok bool, message string, found bool) {
	for _, raw := range s.Results {
		var r struct {
			ID      int    `json:"id"`
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &r) == nil && r.ID == id {
			return r.OK, r.Message, true
		}
	}
	return false, "", false
}

func waitMonitor(t *testing.T, done func(monitor.Status) bool) monitor.Status {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		s := monitorStatusFor("remote")
		if done(s) {
			return s
		}
		if !s.Running {
			t.Fatalf("monitor stopped: %q", s.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %+v", monitorStatusFor("remote"))
	return monitor.Status{}
}

func TestMonitorSamplesAndEndsProcess(t *testing.T) {
	c := execServer(t, binDir(t, "sh", "python3"))
	if err := startMonitor(c); err != nil {
		t.Fatal(err)
	}
	defer stopMonitorFor("remote")
	s := waitMonitor(t, func(s monitor.Status) bool { return s.Seq >= 2 })
	var hello struct {
		Privilege string `json:"privilege"`
		User      string `json:"user"`
	}
	if err := json.Unmarshal(s.Hello, &hello); err != nil {
		t.Fatal(err)
	}
	want := "none" // sudo is not on the test server's PATH
	if os.Geteuid() == 0 {
		want = "root"
	}
	if hello.Privilege != want || hello.User == "" {
		t.Fatalf("unexpected hello: %s", s.Hello)
	}
	var sample struct {
		CPU struct {
			Total float64   `json:"total"`
			Cores []float64 `json:"cores"`
		} `json:"cpu"`
		Memory struct {
			Total, Used, Available int64
		} `json:"memory"`
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
	if sample.ProcessCount == 0 || len(sample.Processes) == 0 {
		t.Fatal("no processes reported")
	}
	guarded := false
	for _, p := range sample.Processes {
		guarded = guarded || p.Protected
	}
	if !guarded {
		t.Fatal("the monitor's own process was not marked protected")
	}

	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	defer victim.Process.Kill()
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(victim.Process.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+2:]))
	start, _ := strconv.ParseInt(fields[19], 10, 64)

	// A stale start time means the PID now belongs to a different process: leave it alone.
	if err := monitorActionFor("remote", monitor.Action{ID: 1, Type: "kill", PID: victim.Process.Pid, Start: start + 1, Signal: "TERM"}); err != nil {
		t.Fatal(err)
	}
	s = waitMonitor(t, func(s monitor.Status) bool { _, _, found := monitorResult(s, 1); return found })
	if _, message, _ := monitorResult(s, 1); !strings.Contains(message, "already exited") {
		t.Fatalf("stale PID was not refused: %q", message)
	}
	if err := monitorActionFor("remote", monitor.Action{ID: 2, Type: "kill", PID: victim.Process.Pid, Start: start, Signal: "TERM"}); err != nil {
		t.Fatal(err)
	}
	s = waitMonitor(t, func(s monitor.Status) bool { _, _, found := monitorResult(s, 2); return found })
	if ok, message, _ := monitorResult(s, 2); !ok {
		t.Fatalf("ending own process failed: %s", message)
	}
	if err := victim.Wait(); err == nil || !strings.Contains(err.Error(), "terminated") {
		t.Fatalf("process was not terminated: %v", err)
	}

	if os.Geteuid() != 0 {
		if err := monitorActionFor("remote", monitor.Action{ID: 3, Type: "dropcaches"}); err != nil {
			t.Fatal(err)
		}
		s = waitMonitor(t, func(s monitor.Status) bool { _, _, found := monitorResult(s, 3); return found })
		if ok, message, _ := monitorResult(s, 3); ok || !strings.Contains(message, "root or sudo") {
			t.Fatalf("unprivileged cache drop was not refused: %v %q", ok, message)
		}
	}

	if err := stopMonitorFor("remote"); err != nil {
		t.Fatal(err)
	}
	if monitorStatusFor("remote").Running {
		t.Fatal("monitor still running after stop")
	}
}

func TestMonitorWithoutPython(t *testing.T) {
	c := execServer(t, binDir(t, "sh"))
	if err := startMonitor(c); err != nil {
		t.Fatal(err)
	}
	defer stopMonitorFor("remote")
	deadline := time.Now().Add(20 * time.Second)
	for monitorStatusFor("remote").Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s := monitorStatusFor("remote"); s.Running || !strings.Contains(s.Error, "Python 3") {
		t.Fatalf("unexpected status: %+v", s)
	}
}
