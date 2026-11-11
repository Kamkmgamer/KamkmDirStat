package main

import (
	"encoding/json"
	"testing"
)

func monitorResult(s MonitorStatus, id int) (ok bool, message string, found bool) {
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

func TestValidateMonitorAction(t *testing.T) {
	valid := []MonitorAction{
		{Type: "kill", PID: 1234, Start: 99, Signal: "TERM"},
		{Type: "kill", PID: 1234, Start: 99, Signal: "KILL", Password: "secret"},
		{Type: "dropcaches"},
	}
	for _, a := range valid {
		if err := validateMonitorAction(a); err != nil {
			t.Errorf("%+v rejected: %v", a, err)
		}
	}
	invalid := []MonitorAction{
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
	if err := monitorAction(MonitorAction{Type: "dropcaches"}); err == nil {
		t.Fatal("action accepted without a monitor connection")
	}
}
