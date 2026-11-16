package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func historyTestStore(t *testing.T) *historyStore {
	t.Helper()
	h := &historyStore{path: filepath.Join(t.TempDir(), "history.jsonl"), executables: map[string]bool{}, resources: map[string]*resourceState{}}
	t.Cleanup(h.close)
	return h
}

func historySample(at float64, activity ...HistoryProcess) sampleData {
	var s sampleData
	s.Time = at
	s.CPU.Total = 20
	s.Memory.Total = 1000
	s.Memory.Used = 400
	s.ProcessActivity = activity
	return s
}

func historyTypes(events []MonitorEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func TestHistoryPoint(t *testing.T) {
	s := historySample(123)
	s.Network = []struct {
		Rx float64 `json:"rx"`
		Tx float64 `json:"tx"`
	}{{Rx: 10, Tx: 2}, {Rx: 4, Tx: 8}}
	gpu, active, temp := 40.0, 70.0, 55.0
	s.GPUs = []struct {
		Util *float64 `json:"util"`
	}{{Util: &gpu}, {Util: nil}}
	s.Disk.Read, s.Disk.Write, s.Disk.Active, s.CPUTemp = 100, 200, &active, &temp
	p := historyPoint(s)
	if p.Time != 123 || p.CPU != 20 || p.Memory != 40 || p.Rx != 14 || p.Tx != 10 || p.DiskRead != 100 || p.DiskWrite != 200 {
		t.Fatalf("unexpected point: %+v", p)
	}
	if p.GPU == nil || *p.GPU != 40 || p.DiskActive != &active || p.CPUTemp != &temp {
		t.Fatalf("optional values were not kept: %+v", p)
	}
	if historyPoint(historySample(1)).GPU != nil {
		t.Fatal("GPU should be absent when no adapter reports use")
	}
}

func TestHistoryProcessEventsAndIdentity(t *testing.T) {
	h := historyTestStore(t)
	unsigned := false
	h.identify = func(p HistoryProcess) HistoryProcess { p.Signed = &unsigned; p.Publisher = ""; return p }
	old := HistoryProcess{PID: 10, Start: 100, Name: "old.exe", Cmd: `C:\Apps\old.exe`}
	h.recordProcesses(1000, []HistoryProcess{old})
	if len(h.events) != 0 {
		t.Fatalf("baseline created events: %v", historyTypes(h.events))
	}
	started := HistoryProcess{PID: 10, Start: 200, Name: "new.exe", Cmd: `C:\Apps\new.exe`}
	h.recordProcesses(1001, []HistoryProcess{old, started})
	if got, want := fmt.Sprint(historyTypes(h.events)), "[process_start process_new process_unsigned]"; got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	if h.events[0].Process.identity() != "10:200" {
		t.Fatalf("PID reuse was not distinguished by start time: %+v", h.events[0].Process)
	}
	// A known unsigned executable alerts on a later start too, without another first-run alert.
	h.recordProcesses(1002, []HistoryProcess{old})
	h.recordProcesses(1003, []HistoryProcess{old, started})
	if got, want := fmt.Sprint(historyTypes(h.events)), "[process_start process_new process_unsigned process_exit process_start process_unsigned]"; got != want {
		t.Fatalf("events %s, want %s", got, want)
	}
	if eventGroup("process_new") != "alerts" || eventGroup("process_unsigned") != "alerts" || eventGroup("process_start") != "processes" || eventGroup("resource_cpu") != "alerts" {
		t.Fatal("event groups are incorrect")
	}
}

func TestHistoryUnknownIdentityIsNotUnsigned(t *testing.T) {
	h := historyTestStore(t)
	h.recordProcesses(1000, []HistoryProcess{{PID: 1, Start: 1, Name: "one"}})
	h.recordProcesses(1001, []HistoryProcess{{PID: 2, Start: 2, Name: "two", Cmd: filepath.Join(t.TempDir(), "missing.exe")}})
	for _, e := range h.events {
		if e.Type == "process_unsigned" {
			t.Fatal("an inaccessible executable was called unsigned")
		}
	}
	p := identifyHistoryProcess(HistoryProcess{Cmd: filepath.Join(t.TempDir(), "missing.exe")})
	if p.Signed != nil || p.Publisher != "" {
		t.Fatalf("inaccessible identity should be unknown: %+v", p)
	}
}

func TestHistoryResourceWarningTimingAndReset(t *testing.T) {
	h := historyTestStore(t)
	for i := 0; i < 29; i++ {
		h.resource(1000+float64(i), "cpu", "CPU use", 95, 90)
	}
	if len(h.events) != 0 {
		t.Fatal("resource warning fired before 30 seconds")
	}
	h.resource(1029.5, "cpu", "CPU use", 95, 90)
	h.resource(1030, "cpu", "CPU use", 95, 90)
	if len(h.events) != 1 || h.events[0].Type != "resource_cpu" {
		t.Fatalf("resource warning did not fire: %+v", h.events)
	}
	h.resource(1031, "cpu", "CPU use", 99, 90)
	if len(h.events) != 1 {
		t.Fatal("active resource warning repeated")
	}
	h.resource(1032, "cpu", "CPU use", 80, 90) // hysteresis keeps the incident active
	h.resource(1040, "cpu", "CPU use", 99, 90)
	if len(h.events) != 1 {
		t.Fatal("resource warning reset inside the margin")
	}
	h.resource(1041, "cpu", "CPU use", 70, 90)
	for i := 0; i < 31; i++ {
		h.resource(1042+float64(i), "cpu", "CPU use", 95, 90)
	}
	if len(h.events) != 2 {
		t.Fatalf("resource warning did not rearm: %+v", h.events)
	}
}

func TestHistoryPruneAndCompaction(t *testing.T) {
	now := time.Now()
	h := historyTestStore(t)
	h.record(mustHistoryJSON(t, historySample(float64(now.Add(-73*time.Hour).Unix())+0.5)))
	h.record(mustHistoryJSON(t, historySample(float64(now.Add(-time.Hour).Unix())+0.5)))
	h.addEvent(MonitorEvent{Time: epoch(now.Add(-73 * time.Hour)), Type: "process_start"})
	h.prune(float64(now.Unix()))
	if len(h.points) != 1 || len(h.events) != 0 {
		t.Fatalf("retention was not applied: %d points, %d events", len(h.points), len(h.events))
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	store, err := openHistoryStore(path, now)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	old := now.Add(-73 * time.Hour)
	store.record(mustHistoryJSON(t, historySample(float64(old.Unix()))))
	store.record(mustHistoryJSON(t, historySample(float64(now.Add(-2*time.Hour).Unix()))))
	store.lastCompact = float64(old.Add(-time.Hour).Unix())
	store.record(mustHistoryJSON(t, historySample(float64(now.Unix()))))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded historyStore
	loaded.executables = map[string]bool{}
	loaded.resources = map[string]*resourceState{}
	loaded.path = path
	if err := loaded.load(now); err != nil {
		t.Fatal(err)
	}
	if len(store.points) != 2 || len(loaded.points) != 2 || len(data) == 0 {
		t.Fatalf("compacted file has wrong data: %d points, %d bytes", len(loaded.points), len(data))
	}
}

func TestHistoryQueryAndDownsample(t *testing.T) {
	h := historyTestStore(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 10; i++ {
		at := epoch(base.Add(time.Duration(i) * time.Second))
		s := historySample(at)
		s.CPU.Total = float64(i)
		gpu := float64(i * 2)
		if i%2 == 0 {
			s.GPUs = []struct {
				Util *float64 `json:"util"`
			}{{Util: &gpu}}
		}
		h.record(mustHistoryJSON(t, s))
	}
	h.addEvent(MonitorEvent{Time: epoch(base.Add(2 * time.Second)), Type: "process_start"})
	h.addEvent(MonitorEvent{Time: epoch(base.Add(3 * time.Second)), Type: "resource_cpu"})
	out := h.query(HistoryQuery{From: epoch(base), To: epoch(base.Add(9 * time.Second)), Points: 5, EventType: "alerts"})
	if len(out.Points) != 5 || len(out.Events) != 1 || out.Events[0].Type != "resource_cpu" {
		t.Fatalf("unexpected query: %+v", out)
	}
	if out.Points[0].CPU != 0.5 || out.Points[0].GPU == nil || *out.Points[0].GPU != 0 {
		t.Fatalf("downsample did not average present optional values: CPU=%v GPU=%v", out.Points[0].CPU, *out.Points[0].GPU)
	}
	if out.Points[1].GPU == nil || *out.Points[1].GPU != 4 {
		t.Fatalf("optional values were not averaged over reported samples: %+v", out.Points[1])
	}
	swapped := h.query(HistoryQuery{From: epoch(base.Add(9 * time.Second)), To: epoch(base), EventLimit: 1})
	if len(swapped.Points) == 0 || len(swapped.Events) != 1 || swapped.Events[0].Type != "resource_cpu" {
		t.Fatalf("range swap or newest-first limit failed: %+v", swapped)
	}
}

func TestHistorySnapshotsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Second)
	h, err := openHistoryStore(path, now)
	if err != nil {
		t.Fatal(err)
	}
	base := float64(now.Add(-time.Minute).Unix())
	s := historySample(base)
	s.ProcessActivity = []HistoryProcess{{PID: 100, Start: 1, Name: "app"}}
	for j := 0; j < 35; j++ {
		s.Processes = append(s.Processes, HistoryProcess{PID: j + 1, Start: 1, Name: fmt.Sprintf("p%d", j), CPU: float64(j), RSS: int64(j)})
	}
	h.record(mustHistoryJSON(t, s))
	if len(h.snapshots) != 1 {
		t.Fatalf("snapshot was not recorded: %+v", h.snapshots)
	}
	if snap, err := h.readSnapshot(h.snapshots[0]); err != nil || len(snap.Processes) != 30 || snap.Processes[0].PID != 35 {
		t.Fatalf("snapshot was not capped and sorted: %+v %v", snap, err)
	}
	h.addEvent(MonitorEvent{Time: base, Type: "process_start"})
	h.close()
	reopened, err := openHistoryStore(path, now)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	out := reopened.query(HistoryQuery{From: base - 1, To: base + 1, ProcessTime: base + 2})
	if len(out.Points) != 1 || len(out.Processes) != 30 || out.ProcessTime != base || len(out.Events) != 1 {
		t.Fatalf("saved history did not reload: %+v", out)
	}
	far := reopened.query(HistoryQuery{From: base - 1, To: base + 100, ProcessTime: base + 31})
	if len(far.Processes) != 0 {
		t.Fatal("selected a process snapshot too far from the requested time")
	}
	if err := reopened.clear(); err != nil {
		t.Fatal(err)
	}
	cleared := reopened.query(HistoryQuery{From: base - 1, To: base + 1})
	if len(cleared.Points) != 0 || len(cleared.Events) != 0 || len(reopened.snapshots) != 0 {
		t.Fatal("clear left history behind")
	}
}

func TestHistoryLoadSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Second)
	point := HistoryPoint{Time: epoch(now.Add(-time.Minute)), CPU: 12}
	line, _ := json.Marshal(historyRecord{Kind: "point", Point: &point})
	data := append([]byte("not json\n"), line...)
	data = append(data, []byte("\n{\"kind\":\"point\",\"point\":null}\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := openHistoryStore(path, now)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if len(h.points) != 1 || h.points[0].CPU != 12 {
		t.Fatalf("valid records around corrupt lines were lost: %+v", h.points)
	}
}

func TestHistoryIdentityCacheFileVersion(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Authenticode is Windows-only")
	}
	path := os.Getenv("SystemRoot") + `\System32\kernel32.dll`
	p := identifyHistoryProcess(HistoryProcess{Cmd: path})
	if p.Signed == nil || !*p.Signed {
		t.Fatalf("Windows system library was not verified: %+v", p)
	}
}

func mustHistoryJSON(t *testing.T, s sampleData) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func epoch(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// Each computer keeps its own history: a server's samples, events and file stay apart from
// this computer's, its processes are not checked against local files, and the server is
// listed again after the stores are closed and reopened.
func TestHistoryPerMachine(t *testing.T) {
	dir := useSettingsDir(t)
	closeMonitorHistory()
	t.Cleanup(closeMonitorHistory)
	server := remoteHistoryMachine(Connection{User: "root", Host: "Host-01", Port: ""})
	if server != "root@host-01:22" {
		t.Fatalf("server key = %q", server)
	}
	now := float64(time.Now().Unix())
	recordMonitorSample(localHistoryMachine, mustHistoryJSON(t, historySample(now-2)))
	remote := historySample(now - 1)
	remote.CPU.Total = 77
	recordMonitorSample(server, mustHistoryJSON(t, remote))

	local, err := monitorHistory(HistoryQuery{From: now - 60, To: now + 1})
	if err != nil || len(local.Points) != 1 || local.Points[0].CPU != 20 || local.Machine != localHistoryMachine {
		t.Fatalf("local history = %+v, %v", local, err)
	}
	got, err := monitorHistory(HistoryQuery{From: now - 60, To: now + 1, Machine: server})
	if err != nil || len(got.Points) != 1 || got.Points[0].CPU != 77 || got.Machine != server {
		t.Fatalf("server history = %+v, %v", got, err)
	}
	h, _ := historyStoreFor(server)
	if p := h.identify(HistoryProcess{Name: "sshd", Cmd: "/usr/sbin/sshd"}); p.Signed != nil || p.Publisher != "" {
		t.Fatalf("server process was checked locally: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dir, historyFileName(server))); err != nil {
		t.Fatalf("server history file: %v", err)
	}

	closeMonitorHistory()
	machines, err := historyMachines()
	if err != nil || len(machines) != 2 || !machines[0].Local || machines[1].Key != server || machines[1].Newest == 0 {
		t.Fatalf("machines = %+v, %v", machines, err)
	}
	got, err = monitorHistory(HistoryQuery{From: now - 60, To: now + 1, Machine: server})
	if err != nil || len(got.Points) != 1 || got.Points[0].CPU != 77 {
		t.Fatalf("server history after reopening = %+v, %v", got, err)
	}
	if err := clearMonitorHistory(server); err != nil {
		t.Fatal(err)
	}
	if got, _ = monitorHistory(HistoryQuery{From: now - 60, To: now + 1, Machine: server}); len(got.Points) != 0 {
		t.Fatalf("server history was not cleared: %+v", got.Points)
	}
	if local, _ = monitorHistory(HistoryQuery{From: now - 60, To: now + 1}); len(local.Points) != 1 {
		t.Fatal("clearing a server cleared this computer's history")
	}
	closeMonitorHistory()
	if machines, _ = historyMachines(); len(machines) != 2 {
		t.Fatalf("a cleared server should stay listed: %+v", machines)
	}
}

func TestHistoryCoarsensOldSamples(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	h := historyTestStore(t)
	start := math.Floor(epoch(now.Add(-8*time.Hour))/historyCoarseStep) * historyCoarseStep
	for i := 0; i < 8*3600; i++ {
		s := historySample(start + float64(i))
		s.CPU.Total = float64(i % 10)
		h.record(mustHistoryJSON(t, s))
	}
	h.coarsen(epoch(now))
	cutoff := epoch(now) - historyDetailWindow.Seconds()
	old, recent := 0, 0
	for _, p := range h.points {
		if p.Time < cutoff-historyCoarseStep {
			old++
			if p.CPU < 4 || p.CPU > 5 {
				t.Fatalf("old samples were not averaged: %+v", p)
			}
		} else {
			recent++
		}
	}
	if old > 2*3600/10+1 || recent < 6*3600-20 {
		t.Fatalf("coarsening kept %d old and %d recent points", old, recent)
	}
	before := len(h.points)
	h.coarsen(epoch(now))
	if len(h.points) != before {
		t.Fatalf("coarsening again merged buckets twice: %d -> %d", before, len(h.points))
	}
}

func TestHistoryCompactionKeepsSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	now := time.Now().Truncate(time.Second)
	h, err := openHistoryStore(path, now)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	base := epoch(now.Add(-time.Hour))
	for i := 0; i < 3; i++ {
		s := historySample(base + float64(i*20))
		s.Processes = []HistoryProcess{{PID: 100 + i, Start: 1, Name: fmt.Sprintf("p%d", i), Cmd: `C:\p.exe`}}
		h.record(mustHistoryJSON(t, s))
	}
	h.mu.Lock()
	err = h.compactLocked(base + 60)
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s := historySample(base + 80)
	s.Processes = []HistoryProcess{{PID: 200, Start: 1, Name: "after"}}
	h.record(mustHistoryJSON(t, s))
	for i, want := range []int{100, 101, 102, 200} {
		at := base + []float64{0, 20, 40, 80}[i]
		out := h.query(HistoryQuery{From: base - 1, To: base + 100, ProcessTime: at})
		if out.ProcessTime != at || len(out.Processes) != 1 || out.Processes[0].PID != want {
			t.Fatalf("snapshot %d after compaction: %+v", i, out.Processes)
		}
	}
}

func TestHistoryEventsStayBounded(t *testing.T) {
	h := historyTestStore(t)
	at := epoch(time.Now())
	for i := 0; i < maxHistoryEvents+historyEventSlack+10; i++ {
		h.addEvent(MonitorEvent{Time: at, Type: "process_start"})
	}
	if len(h.events) > maxHistoryEvents+historyEventSlack {
		t.Fatalf("event list was not trimmed: %d", len(h.events))
	}
	out := h.query(HistoryQuery{From: at - 1, To: at + 1, EventLimit: maxHistoryEvents * 2})
	if len(out.Events) != maxHistoryEvents {
		t.Fatalf("query returned %d events", len(out.Events))
	}
}
