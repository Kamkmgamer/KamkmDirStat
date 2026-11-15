package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	monitorHistoryRetention = 72 * time.Hour
	historySnapshotInterval = 10 * time.Second
	maxHistoryEvents        = 5000
)

type HistoryPoint struct {
	Time       float64  `json:"time"`
	CPU        float64  `json:"cpu"`
	Memory     float64  `json:"memory"`
	GPU        *float64 `json:"gpu,omitempty"`
	Rx         float64  `json:"rx"`
	Tx         float64  `json:"tx"`
	DiskRead   float64  `json:"diskRead,omitempty"`
	DiskWrite  float64  `json:"diskWrite,omitempty"`
	DiskActive *float64 `json:"diskActive,omitempty"`
	CPUTemp    *float64 `json:"cpuTemp,omitempty"`
}

type HistoryProcess struct {
	PID       int     `json:"pid"`
	PPID      int     `json:"ppid,omitempty"`
	Start     int64   `json:"start"`
	Name      string  `json:"name"`
	User      string  `json:"user,omitempty"`
	Cmd       string  `json:"cmd,omitempty"`
	CPU       float64 `json:"cpu"`
	RSS       int64   `json:"rss"`
	Memory    float64 `json:"mem"`
	Publisher string  `json:"publisher,omitempty"`
	Signed    *bool   `json:"signed,omitempty"`
	Elevated  *bool   `json:"elevated,omitempty"`
}

func (p HistoryProcess) identity() string { return fmt.Sprintf("%d:%d", p.PID, p.Start) }
func (p HistoryProcess) executable() string {
	if p.Cmd != "" {
		return strings.ToLower(filepath.Clean(p.Cmd))
	}
	return strings.ToLower(p.Name)
}

type HistorySnapshot struct {
	Time      float64          `json:"time"`
	Processes []HistoryProcess `json:"processes"`
}

type MonitorEvent struct {
	ID        string          `json:"id"`
	Time      float64         `json:"time"`
	Type      string          `json:"type"`
	Severity  string          `json:"severity"`
	Title     string          `json:"title"`
	Message   string          `json:"message"`
	Process   *HistoryProcess `json:"process,omitempty"`
	Metric    string          `json:"metric,omitempty"`
	Value     float64         `json:"value,omitempty"`
	Threshold float64         `json:"threshold,omitempty"`
}

type historyRecord struct {
	Kind     string           `json:"kind"`
	Point    *HistoryPoint    `json:"point,omitempty"`
	Snapshot *HistorySnapshot `json:"snapshot,omitempty"`
	Event    *MonitorEvent    `json:"event,omitempty"`
}

type HistoryQuery struct {
	From        float64 `json:"from"`
	To          float64 `json:"to"`
	Points      int     `json:"points"`
	ProcessTime float64 `json:"processTime"`
	EventType   string  `json:"eventType"`
	EventLimit  int     `json:"eventLimit"`
}

type MonitorHistory struct {
	From           float64          `json:"from"`
	To             float64          `json:"to"`
	Oldest         float64          `json:"oldest"`
	Newest         float64          `json:"newest"`
	Points         []HistoryPoint   `json:"points"`
	Events         []MonitorEvent   `json:"events"`
	Processes      []HistoryProcess `json:"processes,omitempty"`
	ProcessTime    float64          `json:"processTime,omitempty"`
	StorageWarning string           `json:"storageWarning,omitempty"`
}

type resourceState struct {
	since  float64
	active bool
}

type historyStore struct {
	mu             sync.Mutex
	path           string
	file           *os.File
	points         []HistoryPoint
	snapshots      []HistorySnapshot
	events         []MonitorEvent
	known          map[string]HistoryProcess
	executables    map[string]bool
	lastActivity   float64
	lastSnapshot   float64
	resources      map[string]*resourceState
	nextEventID    uint64
	storageWarning string
	identify       func(HistoryProcess) HistoryProcess
	lastCompact    float64
}

func openHistoryStore(path string, now time.Time) (*historyStore, error) {
	h := &historyStore{path: path, executables: map[string]bool{}, resources: map[string]*resourceState{}, nextEventID: uint64(now.UnixNano())}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := h.load(now); err != nil {
		return nil, err
	}
	if err := h.rewrite(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	h.file = f
	return h, nil
}

func (h *historyStore) load(now time.Time) error {
	f, err := os.Open(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	cutoff := float64(now.Add(-monitorHistoryRetention).UnixNano()) / 1e9
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64<<10), 4<<20)
	for s.Scan() {
		var r historyRecord
		if json.Unmarshal(s.Bytes(), &r) != nil {
			continue
		}
		switch r.Kind {
		case "point":
			if r.Point != nil && r.Point.Time >= cutoff && r.Point.Time <= float64(now.UnixNano())/1e9+60 {
				h.points = append(h.points, *r.Point)
			}
		case "snapshot":
			if r.Snapshot != nil && r.Snapshot.Time >= cutoff {
				h.snapshots = append(h.snapshots, *r.Snapshot)
				for _, p := range r.Snapshot.Processes {
					h.executables[p.executable()] = true
				}
			}
		case "event":
			if r.Event != nil && r.Event.Time >= cutoff {
				h.events = append(h.events, *r.Event)
				if r.Event.Process != nil {
					h.executables[r.Event.Process.executable()] = true
				}
			}
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	h.prune(float64(now.UnixNano()) / 1e9)
	return nil
}

func (h *historyStore) prune(now float64) {
	cutoff := now - monitorHistoryRetention.Seconds()
	h.points = sliceAfter(h.points, cutoff, func(p HistoryPoint) float64 { return p.Time })
	h.snapshots = sliceAfter(h.snapshots, cutoff, func(s HistorySnapshot) float64 { return s.Time })
	h.events = sliceAfter(h.events, cutoff, func(e MonitorEvent) float64 { return e.Time })
	if len(h.events) > maxHistoryEvents {
		h.events = append([]MonitorEvent(nil), h.events[len(h.events)-maxHistoryEvents:]...)
	}
}

func sliceAfter[T any](items []T, cutoff float64, at func(T) float64) []T {
	i := sort.Search(len(items), func(i int) bool { return at(items[i]) >= cutoff })
	if i == 0 {
		return items
	}
	return append(items[:0], items[i:]...)
}

func (h *historyStore) rewrite() error {
	f, err := os.CreateTemp(filepath.Dir(h.path), "history-*.tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	write := func(r historyRecord) {
		if err == nil {
			err = enc.Encode(r)
		}
	}
	for i := range h.points {
		write(historyRecord{Kind: "point", Point: &h.points[i]})
	}
	for i := range h.snapshots {
		write(historyRecord{Kind: "snapshot", Snapshot: &h.snapshots[i]})
	}
	for i := range h.events {
		write(historyRecord{Kind: "event", Event: &h.events[i]})
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), h.path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

func (h *historyStore) append(r historyRecord) {
	if h.file == nil {
		return
	}
	b, err := json.Marshal(r)
	if err == nil {
		_, err = h.file.Write(append(b, '\n'))
	}
	if err != nil {
		h.storageWarning = "History could not be saved: " + err.Error()
	}
}

func (h *historyStore) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		_ = h.file.Close()
		h.file = nil
	}
}

type sampleData struct {
	Time float64 `json:"time"`
	CPU  struct {
		Total float64 `json:"total"`
	} `json:"cpu"`
	Memory struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	} `json:"memory"`
	GPUs []struct {
		Util *float64 `json:"util"`
	} `json:"gpus"`
	Network []struct {
		Rx float64 `json:"rx"`
		Tx float64 `json:"tx"`
	} `json:"network"`
	Disk struct {
		Read   float64  `json:"read"`
		Write  float64  `json:"write"`
		Active *float64 `json:"active"`
	} `json:"disk"`
	CPUTemp         *float64         `json:"cpuTemp"`
	Processes       []HistoryProcess `json:"processes"`
	ProcessActivity []HistoryProcess `json:"processActivity"`
}

func historyPoint(s sampleData) HistoryPoint {
	p := HistoryPoint{Time: s.Time, CPU: s.CPU.Total, DiskRead: s.Disk.Read, DiskWrite: s.Disk.Write, DiskActive: s.Disk.Active, CPUTemp: s.CPUTemp}
	if s.Memory.Total > 0 {
		p.Memory = 100 * float64(s.Memory.Used) / float64(s.Memory.Total)
	}
	var sum float64
	var count int
	for _, g := range s.GPUs {
		if g.Util != nil {
			sum += *g.Util
			count++
		}
	}
	if count > 0 {
		v := sum / float64(count)
		p.GPU = &v
	}
	for _, n := range s.Network {
		p.Rx += n.Rx
		p.Tx += n.Tx
	}
	return p
}

func (h *historyStore) compactLocked(now float64) error {
	if h.file != nil {
		if err := h.file.Close(); err != nil {
			return err
		}
		h.file = nil
	}
	if err := h.rewrite(); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	h.file = f
	h.lastCompact = now
	return nil
}

func (h *historyStore) record(raw json.RawMessage) {
	var s sampleData
	if json.Unmarshal(raw, &s) != nil || s.Time <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune(s.Time)
	if h.lastCompact == 0 {
		h.lastCompact = s.Time
	} else if s.Time-h.lastCompact >= time.Hour.Seconds() {
		if err := h.compactLocked(s.Time); err != nil {
			h.storageWarning = "History could not be compacted: " + err.Error()
		}
	}
	p := historyPoint(s)
	h.points = append(h.points, p)
	h.append(historyRecord{Kind: "point", Point: &p})
	h.recordProcesses(s.Time, s.ProcessActivity)
	if len(s.Processes) > 0 && s.Time-h.lastSnapshot >= historySnapshotInterval.Seconds() {
		processes := append([]HistoryProcess(nil), s.Processes...)
		sort.Slice(processes, func(i, j int) bool {
			if processes[i].CPU != processes[j].CPU {
				return processes[i].CPU > processes[j].CPU
			}
			return processes[i].RSS > processes[j].RSS
		})
		if len(processes) > 30 {
			processes = processes[:30]
		}
		snap := HistorySnapshot{Time: s.Time, Processes: processes}
		h.snapshots = append(h.snapshots, snap)
		h.append(historyRecord{Kind: "snapshot", Snapshot: &snap})
		h.lastSnapshot = s.Time
		for _, p := range processes {
			h.executables[p.executable()] = true
		}
	}
	h.resource(s.Time, "cpu", "CPU use", p.CPU, 90)
	h.resource(s.Time, "memory", "Memory use", p.Memory, 90)
	if p.GPU != nil {
		h.resource(s.Time, "gpu", "GPU use", *p.GPU, 95)
	}
}

func (h *historyStore) recordProcesses(now float64, current []HistoryProcess) {
	if len(current) == 0 {
		return
	}
	next := make(map[string]HistoryProcess, len(current))
	for _, p := range current {
		next[p.identity()] = p
	}
	if h.known == nil || h.lastActivity == 0 || now-h.lastActivity > 15 {
		h.known = next
		h.lastActivity = now
		for _, p := range current {
			h.executables[p.executable()] = true
		}
		return
	}
	identify := h.identify
	if identify == nil {
		identify = identifyHistoryProcess
	}
	for id, p := range next {
		if _, ok := h.known[id]; ok {
			continue
		}
		p = identify(p)
		next[id] = p
		h.addEvent(MonitorEvent{Time: now, Type: "process_start", Severity: "info", Title: p.Name + " started", Message: fmt.Sprintf("%s started with PID %d.", p.Name, p.PID), Process: processCopy(p)})
		if key := p.executable(); key != "" && !h.executables[key] {
			h.addEvent(MonitorEvent{Time: now, Type: "process_new", Severity: "notice", Title: "New executable: " + p.Name, Message: executableMessage(p), Process: processCopy(p)})
		}
		if p.Signed != nil && !*p.Signed {
			h.addEvent(MonitorEvent{Time: now, Type: "process_unsigned", Severity: "warning", Title: "Unverified executable: " + p.Name, Message: p.executable() + " has no Authenticode signature that Windows can verify.", Process: processCopy(p)})
		}
		h.executables[p.executable()] = true
	}
	for id, p := range h.known {
		if _, ok := next[id]; !ok {
			h.addEvent(MonitorEvent{Time: now, Type: "process_exit", Severity: "info", Title: p.Name + " exited", Message: fmt.Sprintf("%s (PID %d) exited.", p.Name, p.PID), Process: processCopy(p)})
		}
	}
	h.known = next
	h.lastActivity = now
}

func processCopy(p HistoryProcess) *HistoryProcess { return &p }
func executableMessage(p HistoryProcess) string {
	if p.Cmd != "" {
		return p.Cmd + " ran for the first time in the recorded history."
	}
	return p.Name + " ran for the first time in the recorded history."
}

func (h *historyStore) resource(now float64, metric, label string, value, threshold float64) {
	r := h.resources[metric]
	if r == nil {
		r = &resourceState{}
		h.resources[metric] = r
	}
	if value >= threshold {
		if r.since == 0 {
			r.since = now
		}
		if !r.active && now-r.since >= 30 {
			r.active = true
			h.addEvent(MonitorEvent{Time: now, Type: "resource_" + metric, Severity: "warning", Title: label + " is high", Message: fmt.Sprintf("%s stayed at or above %.0f%% for 30 seconds (now %.0f%%).", label, threshold, value), Metric: metric, Value: value, Threshold: threshold})
		}
	} else if value < threshold-15 {
		r.since = 0
		r.active = false
	}
}

func (h *historyStore) addEvent(e MonitorEvent) {
	h.nextEventID++
	e.ID = fmt.Sprintf("%d-%s-%d", int64(e.Time*1000), e.Type, h.nextEventID)
	h.events = append(h.events, e)
	h.append(historyRecord{Kind: "event", Event: &e})
	if len(h.events) > maxHistoryEvents {
		h.events = append([]MonitorEvent(nil), h.events[len(h.events)-maxHistoryEvents:]...)
	}
}

func (h *historyStore) query(q HistoryQuery) MonitorHistory {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := float64(time.Now().UnixNano()) / 1e9
	if q.To <= 0 {
		q.To = now
	}
	if q.From <= 0 {
		q.From = q.To - 3600
	}
	if q.From > q.To {
		q.From, q.To = q.To, q.From
	}
	q.From = math.Max(q.From, now-monitorHistoryRetention.Seconds())
	if q.Points <= 0 {
		q.Points = 600
	}
	q.Points = min(2000, q.Points)
	out := MonitorHistory{From: q.From, To: q.To, Points: []HistoryPoint{}, Events: []MonitorEvent{}, StorageWarning: h.storageWarning}
	if len(h.points) > 0 {
		out.Oldest = h.points[0].Time
		out.Newest = h.points[len(h.points)-1].Time
	}
	var selected []HistoryPoint
	for _, p := range h.points {
		if p.Time >= q.From && p.Time <= q.To {
			selected = append(selected, p)
		}
	}
	out.Points = downsampleHistory(selected, q.From, q.To, q.Points)
	limit := q.EventLimit
	if limit <= 0 {
		limit = 1000
	}
	limit = min(limit, maxHistoryEvents)
	for i := len(h.events) - 1; i >= 0 && len(out.Events) < limit; i-- {
		e := h.events[i]
		if e.Time < q.From || e.Time > q.To || q.EventType != "" && eventGroup(e.Type) != q.EventType && e.Type != q.EventType {
			continue
		}
		out.Events = append(out.Events, e)
	}
	if q.ProcessTime > 0 && len(h.snapshots) > 0 {
		i := sort.Search(len(h.snapshots), func(i int) bool { return h.snapshots[i].Time >= q.ProcessTime })
		if i == len(h.snapshots) || i > 0 && math.Abs(h.snapshots[i-1].Time-q.ProcessTime) <= math.Abs(h.snapshots[i].Time-q.ProcessTime) {
			i--
		}
		if i >= 0 && i < len(h.snapshots) && math.Abs(h.snapshots[i].Time-q.ProcessTime) <= 30 {
			out.ProcessTime = h.snapshots[i].Time
			out.Processes = append([]HistoryProcess(nil), h.snapshots[i].Processes...)
		}
	}
	return out
}

func eventGroup(kind string) string {
	if kind == "process_new" || kind == "process_unsigned" || strings.HasPrefix(kind, "resource_") {
		return "alerts"
	}
	if strings.HasPrefix(kind, "process_") {
		return "processes"
	}
	if strings.HasPrefix(kind, "resource_") {
		return "resources"
	}
	return "other"
}

func downsampleHistory(points []HistoryPoint, from, to float64, limit int) []HistoryPoint {
	if len(points) <= limit || limit <= 0 {
		return append([]HistoryPoint(nil), points...)
	}
	buckets := make([][]HistoryPoint, limit)
	width := math.Max((to-from)/float64(limit), 0.001)
	for _, p := range points {
		i := min(limit-1, max(0, int((p.Time-from)/width)))
		buckets[i] = append(buckets[i], p)
	}
	out := make([]HistoryPoint, 0, min(len(points), limit))
	for _, bucket := range buckets {
		if len(bucket) == 0 {
			continue
		}
		var p HistoryPoint
		var gpu, active, temp int
		for _, v := range bucket {
			p.Time += v.Time
			p.CPU += v.CPU
			p.Memory += v.Memory
			p.Rx += v.Rx
			p.Tx += v.Tx
			p.DiskRead += v.DiskRead
			p.DiskWrite += v.DiskWrite
			if v.GPU != nil {
				if p.GPU == nil {
					p.GPU = new(float64)
				}
				*p.GPU += *v.GPU
				gpu++
			}
			if v.DiskActive != nil {
				if p.DiskActive == nil {
					p.DiskActive = new(float64)
				}
				*p.DiskActive += *v.DiskActive
				active++
			}
			if v.CPUTemp != nil {
				if p.CPUTemp == nil {
					p.CPUTemp = new(float64)
				}
				*p.CPUTemp += *v.CPUTemp
				temp++
			}
		}
		n := float64(len(bucket))
		p.Time /= n
		p.CPU /= n
		p.Memory /= n
		p.Rx /= n
		p.Tx /= n
		p.DiskRead /= n
		p.DiskWrite /= n
		if p.GPU != nil {
			*p.GPU /= float64(gpu)
		}
		if p.DiskActive != nil {
			*p.DiskActive /= float64(active)
		}
		if p.CPUTemp != nil {
			*p.CPUTemp /= float64(temp)
		}
		out = append(out, p)
	}
	return out
}

func (h *historyStore) clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.file != nil {
		if err := h.file.Close(); err != nil {
			return err
		}
		h.file = nil
	}
	h.points = nil
	h.snapshots = nil
	h.events = nil
	h.known = nil
	h.executables = map[string]bool{}
	h.resources = map[string]*resourceState{}
	h.lastActivity = 0
	h.lastSnapshot = 0
	if err := h.rewrite(); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	h.file = f
	return nil
}

var appHistory struct {
	sync.Mutex
	store *historyStore
	err   error
}

func defaultHistoryStore() (*historyStore, error) {
	appHistory.Lock()
	defer appHistory.Unlock()
	if appHistory.store != nil || appHistory.err != nil {
		return appHistory.store, appHistory.err
	}
	dir, err := settingsDir()
	if err == nil {
		appHistory.store, err = openHistoryStore(filepath.Join(dir, "monitor-history.jsonl"), time.Now())
	}
	appHistory.err = err
	return appHistory.store, err
}

func recordMonitorSample(raw json.RawMessage) {
	if h, err := defaultHistoryStore(); err == nil {
		h.record(raw)
	}
}
func monitorHistory(q HistoryQuery) (MonitorHistory, error) {
	h, err := defaultHistoryStore()
	if err != nil {
		return MonitorHistory{}, errors.New("Monitoring history is unavailable: " + err.Error())
	}
	return h.query(q), nil
}
func clearMonitorHistory() error {
	h, err := defaultHistoryStore()
	if err != nil {
		return err
	}
	return h.clear()
}

func closeMonitorHistory() {
	appHistory.Lock()
	defer appHistory.Unlock()
	if appHistory.store != nil {
		appHistory.store.close()
		appHistory.store = nil
	}
	appHistory.err = nil
}
