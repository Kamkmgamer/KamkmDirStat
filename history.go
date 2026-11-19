package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	monitorHistoryRetention = 72 * time.Hour
	historySnapshotInterval = 10 * time.Second
	maxHistoryEvents        = 5000
	// historyEventSlack lets the event list grow a little past its limit before the oldest
	// are dropped, so a busy computer does not move the whole list on every new event.
	historyEventSlack = 500
	// Samples arrive about once a second. The last historyDetailWindow keeps every one;
	// older samples are averaged into historyCoarseStep buckets, which is finer than the
	// chart shows for any range that reaches that far back.
	historyDetailWindow = 6 * time.Hour
	historyCoarseStep   = 10.0
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

func (p HistoryProcess) identity() string   { return fmt.Sprintf("%d:%d", p.PID, p.Start) }
func (p HistoryProcess) executable() string { return executableKey(p.Name, p.Cmd) }

func executableKey(name, cmd string) string {
	if cmd != "" {
		return strings.ToLower(filepath.Clean(cmd))
	}
	return strings.ToLower(name)
}

type HistorySnapshot struct {
	Time      float64          `json:"time"`
	Processes []HistoryProcess `json:"processes"`
}

// storedPoint is a HistoryPoint as kept in memory. Values are float32 and a missing optional
// value is NaN, so three days of samples need no allocations beyond one slice.
type storedPoint struct {
	Time                                                               float64
	CPU, Memory, GPU, Rx, Tx, DiskRead, DiskWrite, DiskActive, CPUTemp float32
}

var nan32 = float32(math.NaN())

func optional32(v *float64) float32 {
	if v == nil {
		return nan32
	}
	return float32(*v)
}

func optional64(v float32) *float64 {
	if v != v {
		return nil
	}
	f := float64(v)
	return &f
}

func storePoint(p HistoryPoint) storedPoint {
	return storedPoint{Time: p.Time, CPU: float32(p.CPU), Memory: float32(p.Memory), GPU: optional32(p.GPU),
		Rx: float32(p.Rx), Tx: float32(p.Tx), DiskRead: float32(p.DiskRead), DiskWrite: float32(p.DiskWrite),
		DiskActive: optional32(p.DiskActive), CPUTemp: optional32(p.CPUTemp)}
}

func (p storedPoint) point() HistoryPoint {
	return HistoryPoint{Time: p.Time, CPU: float64(p.CPU), Memory: float64(p.Memory), GPU: optional64(p.GPU),
		Rx: float64(p.Rx), Tx: float64(p.Tx), DiskRead: float64(p.DiskRead), DiskWrite: float64(p.DiskWrite),
		DiskActive: optional64(p.DiskActive), CPUTemp: optional64(p.CPUTemp)}
}

// pointSum averages points. Optional values are averaged over the points that report them.
type pointSum struct {
	n                                      int
	time, cpu, memory, rx, tx, read, write float64
	gpu, active, temp                      float64
	gpuN, activeN, tempN                   int
}

func (s *pointSum) add(p storedPoint) {
	s.n++
	s.time += p.Time
	s.cpu += float64(p.CPU)
	s.memory += float64(p.Memory)
	s.rx += float64(p.Rx)
	s.tx += float64(p.Tx)
	s.read += float64(p.DiskRead)
	s.write += float64(p.DiskWrite)
	if p.GPU == p.GPU {
		s.gpu += float64(p.GPU)
		s.gpuN++
	}
	if p.DiskActive == p.DiskActive {
		s.active += float64(p.DiskActive)
		s.activeN++
	}
	if p.CPUTemp == p.CPUTemp {
		s.temp += float64(p.CPUTemp)
		s.tempN++
	}
}

func (s *pointSum) mean() storedPoint {
	n := float64(s.n)
	avg := func(sum float64, count int) float32 {
		if count == 0 {
			return nan32
		}
		return float32(sum / float64(count))
	}
	return storedPoint{Time: s.time / n, CPU: float32(s.cpu / n), Memory: float32(s.memory / n), GPU: avg(s.gpu, s.gpuN),
		Rx: float32(s.rx / n), Tx: float32(s.tx / n), DiskRead: float32(s.read / n), DiskWrite: float32(s.write / n),
		DiskActive: avg(s.active, s.activeN), CPUTemp: avg(s.temp, s.tempN)}
}

// snapshotRef locates a process snapshot in the history file. Snapshots make up most of the
// file, so only their times stay in memory and a snapshot is read back when it is shown.
type snapshotRef struct {
	time float64
	off  int64
	size int32
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
	Machine  string           `json:"machine,omitempty"`
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
	// Machine selects whose history to read: "local" (or empty) for this computer, or the
	// key of a server, as reported by monitorStatus and historyMachines.
	Machine string `json:"machine"`
}

// HistoryMachine is a computer with recorded history.
type HistoryMachine struct {
	Key    string  `json:"key"`
	Local  bool    `json:"local"`
	Newest float64 `json:"newest"`
}

type MonitorHistory struct {
	From           float64          `json:"from"`
	To             float64          `json:"to"`
	Machine        string           `json:"machine"`
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
	machine        string
	file           *os.File
	size           int64
	points         []storedPoint
	snapshots      []snapshotRef
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
	if err := h.openAppend(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *historyStore) openAppend() error {
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	h.file, h.size = f, info.Size()
	return nil
}

// historyLoadRecord reads a history line without keeping a snapshot's processes: loading
// only needs each process's executable.
type historyLoadRecord struct {
	Kind     string        `json:"kind"`
	Machine  string        `json:"machine,omitempty"`
	Point    *HistoryPoint `json:"point,omitempty"`
	Snapshot *struct {
		Time      float64 `json:"time"`
		Processes []struct {
			Name string `json:"name"`
			Cmd  string `json:"cmd"`
		} `json:"processes"`
	} `json:"snapshot,omitempty"`
	Event *MonitorEvent `json:"event,omitempty"`
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
	latest := float64(now.UnixNano())/1e9 + 60
	in := bufio.NewReaderSize(f, 256<<10)
	var off int64
	for {
		raw, readErr := in.ReadBytes('\n')
		start := off
		off += int64(len(raw))
		line := bytes.TrimRight(raw, "\r\n")
		var r historyLoadRecord
		if len(line) > 0 && json.Unmarshal(line, &r) == nil {
			switch r.Kind {
			case "meta":
				if r.Machine != "" {
					h.machine = r.Machine
				}
			case "point":
				if r.Point != nil && r.Point.Time >= cutoff && r.Point.Time <= latest {
					h.points = append(h.points, storePoint(*r.Point))
					if len(h.points)%(1<<15) == 0 {
						h.coarsen(latest - 60) // keep a long file's points from piling up while it loads
					}
				}
			case "snapshot":
				if r.Snapshot != nil && r.Snapshot.Time >= cutoff && len(line) <= math.MaxInt32 {
					h.snapshots = append(h.snapshots, snapshotRef{time: r.Snapshot.Time, off: start, size: int32(len(line))})
					for _, p := range r.Snapshot.Processes {
						h.executables[executableKey(p.Name, p.Cmd)] = true
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
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	at := float64(now.UnixNano()) / 1e9
	h.prune(at)
	h.trimEvents(0)
	h.coarsen(at)
	return nil
}

func (h *historyStore) prune(now float64) {
	cutoff := now - monitorHistoryRetention.Seconds()
	h.points = sliceAfter(h.points, cutoff, func(p storedPoint) float64 { return p.Time })
	h.snapshots = sliceAfter(h.snapshots, cutoff, func(s snapshotRef) float64 { return s.time })
	h.events = sliceAfter(h.events, cutoff, func(e MonitorEvent) float64 { return e.Time })
	h.trimEvents(historyEventSlack)
}

// trimEvents keeps the newest maxHistoryEvents once the list is more than slack over that.
func (h *historyStore) trimEvents(slack int) {
	if len(h.events) <= maxHistoryEvents+slack {
		return
	}
	n := copy(h.events, h.events[len(h.events)-maxHistoryEvents:])
	clear(h.events[n:])
	h.events = h.events[:n]
}

// coarsen averages samples older than historyDetailWindow into historyCoarseStep buckets.
// Only buckets that end before the cutoff are merged, so no bucket is averaged twice.
func (h *historyStore) coarsen(now float64) {
	before := math.Floor((now-historyDetailWindow.Seconds())/historyCoarseStep) * historyCoarseStep
	points := h.points
	end := sort.Search(len(points), func(i int) bool { return points[i].Time >= before })
	out := 0
	for i := 0; i < end; {
		bucket := math.Floor(points[i].Time / historyCoarseStep)
		var sum pointSum
		for ; i < end && math.Floor(points[i].Time/historyCoarseStep) == bucket; i++ {
			sum.add(points[i])
		}
		points[out] = sum.mean()
		out++
	}
	if out == end {
		return
	}
	n := out + copy(points[out:], points[end:])
	if cap(points) > 2*n+1024 {
		h.points = append(make([]storedPoint, 0, n+n/4), points[:n]...)
	} else {
		h.points = points[:n]
	}
}

func sliceAfter[T any](items []T, cutoff float64, at func(T) float64) []T {
	i := sort.Search(len(items), func(i int) bool { return at(items[i]) >= cutoff })
	if i == 0 {
		return items
	}
	return append(items[:0], items[i:]...)
}

// rewrite replaces the history file with what is kept in memory. Snapshots are copied from
// the current file, since only their positions are in memory. The file must not be open for
// appending.
func (h *historyStore) rewrite() error {
	var src *os.File
	if len(h.snapshots) > 0 {
		var err error
		if src, err = os.Open(h.path); err != nil {
			return err
		}
	}
	snapshots, size, err := h.writeTemp(src)
	if src != nil {
		_ = src.Close() // before the rename, which Windows refuses over an open file
	}
	if err == nil {
		err = os.Rename(h.path+".tmp", h.path)
	}
	if err != nil {
		_ = os.Remove(h.path + ".tmp")
		return err
	}
	h.snapshots, h.size = snapshots, size
	return nil
}

func (h *historyStore) writeTemp(src *os.File) ([]snapshotRef, int64, error) {
	f, err := os.OpenFile(h.path+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, 0, err
	}
	w := bufio.NewWriterSize(f, 256<<10)
	var off int64
	put := func(b []byte) {
		if err == nil {
			_, err = w.Write(b)
			off += int64(len(b))
		}
	}
	write := func(r historyRecord) {
		b, merr := json.Marshal(r)
		if merr != nil && err == nil {
			err = merr
		}
		put(append(b, '\n'))
	}
	if h.machine != "" {
		write(historyRecord{Kind: "meta", Machine: h.machine})
	}
	for _, p := range h.points {
		point := p.point()
		write(historyRecord{Kind: "point", Point: &point})
	}
	snapshots := make([]snapshotRef, 0, len(h.snapshots))
	var buf []byte
	for _, ref := range h.snapshots {
		if err != nil {
			break
		}
		if cap(buf) < int(ref.size)+1 {
			buf = make([]byte, ref.size, ref.size+ref.size/2+1)
		}
		buf = buf[:ref.size]
		if _, rerr := src.ReadAt(buf, ref.off); rerr != nil || len(buf) == 0 || buf[0] != '{' {
			continue // the line is gone or was changed outside the app; drop the snapshot
		}
		snapshots = append(snapshots, snapshotRef{time: ref.time, off: off, size: ref.size})
		put(append(buf, '\n'))
	}
	for i := range h.events {
		write(historyRecord{Kind: "event", Event: &h.events[i]})
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return snapshots, off, err
}

func (h *historyStore) readSnapshot(ref snapshotRef) (HistorySnapshot, error) {
	f, err := os.Open(h.path)
	if err != nil {
		return HistorySnapshot{}, err
	}
	defer f.Close()
	buf := make([]byte, ref.size)
	if _, err := f.ReadAt(buf, ref.off); err != nil {
		return HistorySnapshot{}, err
	}
	var r historyRecord
	if err := json.Unmarshal(buf, &r); err != nil {
		return HistorySnapshot{}, err
	}
	if r.Kind != "snapshot" || r.Snapshot == nil {
		return HistorySnapshot{}, errors.New("history snapshot not found")
	}
	return *r.Snapshot, nil
}

// append writes a record at the end of the file and returns where it starts and its length
// without the line break, or -1 when it was not written.
func (h *historyStore) append(r historyRecord) (int64, int) {
	if h.file == nil {
		return -1, 0
	}
	b, err := json.Marshal(r)
	if err == nil {
		_, err = h.file.Write(append(b, '\n'))
	}
	if err != nil {
		h.storageWarning = "History could not be saved: " + err.Error()
		if info, serr := h.file.Stat(); serr == nil {
			h.size = info.Size()
		}
		return -1, 0
	}
	off := h.size
	h.size += int64(len(b)) + 1
	return off, len(b)
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
	h.coarsen(now)
	if err := h.rewrite(); err != nil {
		return err
	}
	if err := h.openAppend(); err != nil {
		return err
	}
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
	h.points = append(h.points, storePoint(p))
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
		if off, n := h.append(historyRecord{Kind: "snapshot", Snapshot: &snap}); off >= 0 && n <= math.MaxInt32 {
			h.snapshots = append(h.snapshots, snapshotRef{time: s.Time, off: off, size: int32(n)})
		}
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
	h.trimEvents(historyEventSlack)
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
	out := MonitorHistory{Machine: h.machine, From: q.From, To: q.To, Points: []HistoryPoint{}, Events: []MonitorEvent{}, StorageWarning: h.storageWarning}
	if len(h.points) > 0 {
		out.Oldest = h.points[0].Time
		out.Newest = h.points[len(h.points)-1].Time
	}
	first := sort.Search(len(h.points), func(i int) bool { return h.points[i].Time >= q.From })
	last := sort.Search(len(h.points), func(i int) bool { return h.points[i].Time > q.To })
	out.Points = downsampleHistory(h.points[first:max(first, last)], q.From, q.To, q.Points)
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
		i := sort.Search(len(h.snapshots), func(i int) bool { return h.snapshots[i].time >= q.ProcessTime })
		if i == len(h.snapshots) || i > 0 && math.Abs(h.snapshots[i-1].time-q.ProcessTime) <= math.Abs(h.snapshots[i].time-q.ProcessTime) {
			i--
		}
		if i >= 0 && i < len(h.snapshots) && math.Abs(h.snapshots[i].time-q.ProcessTime) <= 30 {
			if snap, err := h.readSnapshot(h.snapshots[i]); err == nil {
				out.ProcessTime = snap.Time
				out.Processes = snap.Processes
			}
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

// downsampleHistory averages time-ordered points into at most limit equal slices of from–to.
func downsampleHistory(points []storedPoint, from, to float64, limit int) []HistoryPoint {
	if len(points) <= limit || limit <= 0 {
		out := make([]HistoryPoint, len(points))
		for i, p := range points {
			out[i] = p.point()
		}
		return out
	}
	width := math.Max((to-from)/float64(limit), 0.001)
	out := make([]HistoryPoint, 0, limit)
	var sum pointSum
	bucket := -1
	for _, p := range points {
		i := min(limit-1, max(0, int((p.Time-from)/width)))
		if i != bucket && sum.n > 0 {
			out = append(out, sum.mean().point())
			sum = pointSum{}
		}
		bucket = i
		sum.add(p)
	}
	if sum.n > 0 {
		out = append(out, sum.mean().point())
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
	return h.openAppend()
}

// Each computer has its own history file. This computer keeps the original file name; a
// server's file is named after a hash of its key, which the file also records in a meta line.
const localHistoryMachine = "local"

func normalizeHistoryMachine(machine string) string {
	machine = strings.TrimSpace(machine)
	if machine == "" || strings.EqualFold(machine, localHistoryMachine) {
		return localHistoryMachine
	}
	return machine
}

// remoteHistoryMachine identifies a server by user, host and port, so two accounts on the
// same server, or two servers behind one address, keep separate histories.
func remoteHistoryMachine(c Connection) string {
	port := strings.TrimSpace(c.Port)
	if port == "" {
		port = "22"
	}
	return strings.TrimSpace(c.User) + "@" + strings.ToLower(strings.TrimSpace(c.Host)) + ":" + port
}

func historyFileName(machine string) string {
	if machine == localHistoryMachine {
		return "monitor-history.jsonl"
	}
	sum := sha256.Sum256([]byte(machine))
	return "monitor-history-" + hex.EncodeToString(sum[:8]) + ".jsonl"
}

var appHistory = struct {
	sync.Mutex
	stores map[string]*historyStore
	errs   map[string]error
}{stores: map[string]*historyStore{}, errs: map[string]error{}}

func historyStoreFor(machine string) (*historyStore, error) {
	machine = normalizeHistoryMachine(machine)
	appHistory.Lock()
	defer appHistory.Unlock()
	if h := appHistory.stores[machine]; h != nil {
		return h, nil
	}
	if err := appHistory.errs[machine]; err != nil {
		return nil, err
	}
	dir, err := settingsDir()
	var h *historyStore
	if err == nil {
		h, err = openMachineHistory(filepath.Join(dir, historyFileName(machine)), machine, time.Now())
	}
	if err != nil {
		appHistory.errs[machine] = err
		return nil, err
	}
	appHistory.stores[machine] = h
	// Loading reads the whole file. Hand the memory it used back to the system now rather
	// than over the next few minutes.
	debug.FreeOSMemory()
	return h, nil
}

func openMachineHistory(path, machine string, now time.Time) (*historyStore, error) {
	h, err := openHistoryStore(path, now)
	if err != nil {
		return nil, err
	}
	if machine != localHistoryMachine {
		// Signatures are checked against files on this computer, which says nothing about a server.
		h.identify = func(p HistoryProcess) HistoryProcess { return p }
	}
	if h.machine != machine {
		h.mu.Lock()
		h.machine = machine
		err = h.compactLocked(float64(now.UnixNano()) / 1e9)
		h.mu.Unlock()
		if err != nil {
			h.close()
			return nil, err
		}
	}
	return h, nil
}

func defaultHistoryStore() (*historyStore, error) { return historyStoreFor(localHistoryMachine) }

func recordMonitorSample(machine string, raw json.RawMessage) {
	if h, err := historyStoreFor(machine); err == nil {
		h.record(raw)
	}
}

func monitorHistory(q HistoryQuery) (MonitorHistory, error) {
	h, err := historyStoreFor(q.Machine)
	if err != nil {
		return MonitorHistory{}, errors.New("Monitoring history is unavailable: " + err.Error())
	}
	return h.query(q), nil
}

func clearMonitorHistory(machine string) error {
	h, err := historyStoreFor(machine)
	if err != nil {
		return err
	}
	return h.clear()
}

// historyMachines lists this computer and every server with a history file, newest first
// after this computer. Files that are not open are dated by their last change.
func historyMachines() ([]HistoryMachine, error) {
	dir, err := settingsDir()
	if err != nil {
		return nil, err
	}
	found := map[string]float64{localHistoryMachine: 0}
	appHistory.Lock()
	open := map[string]*historyStore{}
	for k, h := range appHistory.stores {
		open[k] = h
	}
	appHistory.Unlock()
	files, _ := filepath.Glob(filepath.Join(dir, "monitor-history-*.jsonl"))
	for _, path := range files {
		machine := readHistoryMachine(path)
		if machine == "" || machine == localHistoryMachine || historyFileName(machine) != filepath.Base(path) {
			continue
		}
		if info, err := os.Stat(path); err == nil {
			found[machine] = float64(info.ModTime().UnixNano()) / 1e9
		}
	}
	for k, h := range open {
		h.mu.Lock()
		if n := len(h.points); n > 0 {
			found[k] = h.points[n-1].Time
		} else if _, ok := found[k]; !ok {
			found[k] = 0
		}
		h.mu.Unlock()
	}
	out := make([]HistoryMachine, 0, len(found))
	for k, newest := range found {
		out = append(out, HistoryMachine{Key: k, Local: k == localHistoryMachine, Newest: newest})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		if out[i].Newest != out[j].Newest {
			return out[i].Newest > out[j].Newest
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func readHistoryMachine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4<<10), 64<<10)
	if !s.Scan() {
		return ""
	}
	var r historyRecord
	if json.Unmarshal(s.Bytes(), &r) != nil || r.Kind != "meta" {
		return ""
	}
	return r.Machine
}

func closeMonitorHistory() {
	appHistory.Lock()
	defer appHistory.Unlock()
	for k, h := range appHistory.stores {
		h.close()
		delete(appHistory.stores, k)
	}
	appHistory.errs = map[string]error{}
}
