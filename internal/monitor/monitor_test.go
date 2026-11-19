package monitor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/history"
	"golang.org/x/crypto/ssh"
)

// The tests of the samplers monitor this computer through one session.
var testSession Session

func startLocalMonitor() error                          { return testSession.Start(Target{Machine: history.Local}) }
func stopMonitor() error                                { return testSession.Stop() }
func monitorStatus() Status                             { return testSession.Status() }
func monitorAction(a Action) error                      { return testSession.Action(a) }
func noConnection(context.Context) (*ssh.Client, error) { return nil, nil }

func monitorResult(s Status, id int) (ok bool, message string, found bool) {
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

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestValidateMonitorAction(t *testing.T) {
	valid := []Action{
		{Type: "kill", PID: 1234, Start: 99, Signal: "TERM"},
		{Type: "kill", PID: 1234, Start: 99, Signal: "KILL", Password: "secret"},
		{Type: "dropcaches"},
	}
	for _, a := range valid {
		if err := validateMonitorAction(a); err != nil {
			t.Errorf("%+v rejected: %v", a, err)
		}
	}
	invalid := []Action{
		{Type: "kill", PID: 1, Signal: "TERM"},
		{Type: "kill", PID: 0, Signal: "TERM"},
		{Type: "kill", PID: 1234, Signal: "HUP"},
		{Type: "kill", PID: 1234, Start: -1, Signal: "TERM"},
		{Type: "dropcaches", PID: 1234},
		{Type: "reboot"},
		{Type: "dropcaches", Password: string(make([]byte, 2000))},
	}
	for _, a := range invalid {
		if err := validateMonitorAction(a); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}

func TestMonitorActionNeedsConnection(t *testing.T) {
	_ = stopMonitor()
	if err := monitorAction(Action{Type: "dropcaches"}); err == nil {
		t.Fatal("action accepted without a monitor connection")
	}
}

func fakeRunner(label string) func(context.Context, <-chan []byte, func(func(*Status)) bool) error {
	return func(ctx context.Context, writes <-chan []byte, update func(func(*Status)) bool) error {
		hello, _ := json.Marshal(map[string]string{"host": label})
		update(func(s *Status) {
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
				var action Action
				if json.Unmarshal(line, &action) != nil {
					continue
				}
				result, _ := json.Marshal(map[string]interface{}{"id": action.ID, "ok": true, "message": label})
				update(func(s *Status) { s.Results = append(s.Results, result) })
			}
		}
	}
}

// Two sessions, one for this computer and one for a server, never disturb each other.
func TestSessionsRunIndependently(t *testing.T) {
	oldLocal, oldRemote := localMonitorRun, remoteMonitorRun
	localMonitorRun = fakeRunner("local")
	remoteMonitorRun = func(ctx context.Context, _ Target, writes <-chan []byte, update func(func(*Status)) bool) error {
		return fakeRunner("remote")(ctx, writes, update)
	}
	var local, remote Session
	t.Cleanup(func() {
		_ = local.Stop()
		_ = remote.Stop()
		localMonitorRun, remoteMonitorRun = oldLocal, oldRemote
	})

	if err := local.Start(Target{Machine: history.Local}); err != nil {
		t.Fatal(err)
	}
	server := history.Remote("root", "server", "22")
	if err := remote.Start(Target{Machine: server, Connect: noConnection}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both monitors", func() bool {
		l, r := local.Status(), remote.Status()
		return l.Running && r.Running && !l.Connecting && !r.Connecting && l.Seq == 1 && r.Seq == 1
	})
	if l, r := local.Status(), remote.Status(); l.Machine != history.Local || r.Machine != server {
		t.Fatalf("machines %q and %q", l.Machine, r.Machine)
	}

	if err := local.Action(Action{ID: 11, Type: "dropcaches"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the local action", func() bool {
		_, _, found := monitorResult(local.Status(), 11)
		return found
	})
	if _, _, found := monitorResult(remote.Status(), 11); found {
		t.Fatal("a local action was delivered to the SSH monitor")
	}
	if err := local.Stop(); err != nil {
		t.Fatal(err)
	}
	if local.Status().Running || !remote.Status().Running {
		t.Fatal("stopping the local monitor changed the SSH monitor")
	}
	if err := local.Action(Action{ID: 12, Type: "dropcaches"}); err == nil {
		t.Fatal("a stopped local monitor accepted an action")
	}
	if err := remote.Action(Action{ID: 21, Type: "dropcaches"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the SSH action", func() bool {
		_, _, found := monitorResult(remote.Status(), 21)
		return found
	})
}
