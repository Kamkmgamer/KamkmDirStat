package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The local monitor samples this PC with the same system queries Task Manager uses and
// produces the same messages as remote_monitor.py, so the page shows both the same way.
// Running as administrator stands in for root: it allows ending other users' processes and
// clearing the standby cache.

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	user32                      = windows.NewLazySystemDLL("user32.dll")
	pdhDLL                      = windows.NewLazySystemDLL("pdh.dll")
	dxgiDLL                     = windows.NewLazySystemDLL("dxgi.dll")
	procGlobalMemoryStatusEx    = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetPerformanceInfo      = kernel32.NewProc("K32GetPerformanceInfo")
	procGetTickCount64          = kernel32.NewProc("GetTickCount64")
	procPostMessageW            = user32.NewProc("PostMessageW")
	procPdhOpenQueryW           = pdhDLL.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW   = pdhDLL.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData     = pdhDLL.NewProc("PdhCollectQueryData")
	procPdhGetFormattedArrayW   = pdhDLL.NewProc("PdhGetFormattedCounterArrayW")
	procPdhCloseQuery           = pdhDLL.NewProc("PdhCloseQuery")
	procCreateDXGIFactory1      = dxgiDLL.NewProc("CreateDXGIFactory1")
	errLocalProcessExited       = errors.New("The process had already exited.")
	windowsCriticalProcessNames = map[string]bool{"system": true, "registry": true, "memory compression": true, "secure system": true,
		"smss.exe": true, "csrss.exe": true, "wininit.exe": true, "services.exe": true, "lsass.exe": true, "winlogon.exe": true, "lsaiso.exe": true}
)

const (
	systemMemoryListInformation = 80
	memoryFlushModifiedList     = 3
	memoryPurgeStandbyList      = 4
	wmClose                     = 0x0010
)

type memoryStatusEx struct {
	Length, MemoryLoad                                                                       uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile, TotalVirtual, AvailVirtual, AvailExt uint64
}

type performanceInformation struct {
	Size                                                                                uint32
	CommitTotal, CommitLimit, CommitPeak, PhysicalTotal, PhysicalAvailable, SystemCache uintptr
	KernelTotal, KernelPaged, KernelNonpaged, PageSize                                  uintptr
	HandleCount, ProcessCount, ThreadCount                                              uint32
}

type memoryListInformation struct {
	Zero, Free, Modified, ModifiedNoWrite, Bad uintptr
	ByPriority, Repurposed                     [8]uintptr
	ModifiedPageFile                           uintptr
}

type processorTimes struct {
	Idle, Kernel, User, Dpc, Interrupt int64
	Count                              uint32
	_                                  uint32
}

type processKey struct {
	pid     uint32
	created int64
}

type processOwner struct {
	user string
	own  bool
	path string
}

type localSampler struct {
	elevated  bool
	userSID   string
	userName  string
	last      time.Time
	cores     []processorTimes
	procTimes map[processKey]int64
	owners    map[processKey]processOwner
	sidNames  map[string]string
	network   map[uint64][2]uint64
	procBuf   []byte
	gpu       *gpuSampler
}

func runLocalMonitor(ctx context.Context, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
	s := newLocalSampler()
	defer s.gpu.close()
	hello, _ := json.Marshal(s.hello())
	if !update(func(st *MonitorStatus) { st.Hello = hello; st.Connecting = false }) {
		return context.Canceled
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case line := <-writes:
				result, _ := json.Marshal(s.handle(line))
				update(func(st *MonitorStatus) { addMonitorResult(st, result) })
			}
		}
	}()
	s.sample() // the first sample only sets the starting counters
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(firstMonitorSample):
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		data, err := json.Marshal(s.sample())
		if err != nil {
			return err
		}
		if !update(func(st *MonitorStatus) { st.Sample = data; st.Seq++ }) {
			return context.Canceled
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func newLocalSampler() *localSampler {
	s := &localSampler{elevated: windows.GetCurrentProcessToken().IsElevated(), procTimes: map[processKey]int64{},
		owners: map[processKey]processOwner{}, sidNames: map[string]string{}, network: map[uint64][2]uint64{}}
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		s.userSID = u.User.Sid.String()
		s.userName = s.accountName(u.User.Sid)
	}
	if s.elevated {
		enablePrivilege("SeDebugPrivilege")
		enablePrivilege("SeProfileSingleProcessPrivilege")
	}
	s.gpu = newGPUSampler()
	return s
}

func (s *localSampler) hello() map[string]any {
	privilege := "none"
	if s.elevated {
		privilege = "admin"
	}
	host, _ := os.Hostname()
	return map[string]any{"type": "hello", "privilege": privilege, "user": s.userName, "host": host, "platform": "windows",
		"system": windowsVersion(), "machine": runtime.GOARCH, "cpuModel": cpuModel()}
}

func windowsVersion() string {
	v := windows.RtlGetVersion()
	name := "Windows"
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE); err == nil {
		if product, _, err := k.GetStringValue("ProductName"); err == nil {
			name = product
		}
		if display, _, err := k.GetStringValue("DisplayVersion"); err == nil {
			name += " " + display
		}
		k.Close()
	}
	// Windows 11 still reports "Windows 10" as its product name.
	if v.BuildNumber >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1)
	}
	return fmt.Sprintf("%s (build %d)", name, v.BuildNumber)
}

func cpuModel() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	name, _, _ := k.GetStringValue("ProcessorNameString")
	return strings.TrimSpace(name)
}

func enablePrivilege(name string) {
	var token windows.Token
	if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token) != nil {
		return
	}
	defer token.Close()
	var luid windows.LUID
	p, _ := windows.UTF16PtrFromString(name)
	if windows.LookupPrivilegeValue(nil, p, &luid) != nil {
		return
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	_ = windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}

func (s *localSampler) accountName(sid *windows.SID) string {
	key := sid.String()
	if name, ok := s.sidNames[key]; ok {
		return name
	}
	name, _, _, err := sid.LookupAccount("")
	if err != nil {
		name = key
	}
	s.sidNames[key] = name
	return name
}

func percent(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return min(100, max(0, 100*float64(part)/float64(whole)))
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

func readProcessorTimes() []processorTimes {
	n := runtime.NumCPU()
	buf := make([]processorTimes, n)
	var size uint32
	if windows.NtQuerySystemInformation(windows.SystemProcessorPerformanceInformation, unsafe.Pointer(&buf[0]),
		uint32(n)*uint32(unsafe.Sizeof(buf[0])), &size) != nil {
		return nil
	}
	return buf[:min(n, int(size/uint32(unsafe.Sizeof(buf[0]))))]
}

func (s *localSampler) sample() map[string]any {
	now := time.Now()
	elapsed := now.Sub(s.last).Seconds()
	first := s.last.IsZero()
	s.last = now

	// CPU. Kernel time includes idle time.
	cores := readProcessorTimes()
	var coreUse []float64
	var dIdle, dKernel, dUser int64
	for i, c := range cores {
		if first || i >= len(s.cores) {
			coreUse = append(coreUse, 0)
			continue
		}
		p := s.cores[i]
		idle, kernel, user := c.Idle-p.Idle, c.Kernel-p.Kernel, c.User-p.User
		dIdle, dKernel, dUser = dIdle+idle, dKernel+kernel, dUser+user
		coreUse = append(coreUse, round1(100-percent(idle, kernel+user)))
	}
	s.cores = cores
	total := dKernel + dUser
	cpu := map[string]any{"total": round1(100 - percent(dIdle, total)), "user": round1(percent(dUser, total)),
		"system": round1(percent(dKernel-dIdle, total)), "cores": coreUse}
	if total == 0 {
		cpu["total"] = 0.0
	}

	memory := readLocalMemory()
	processes, count := s.processes(elapsed, max(1, len(cores)), memory["total"].(int64))
	var uptime float64
	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		uptime = float64(r) / 1000
	}
	gpus, note := s.gpu.sample()
	return map[string]any{"type": "sample", "time": float64(now.UnixNano()) / 1e9, "uptime": uptime, "load": []float64{},
		"cpu": cpu, "memory": memory, "gpus": gpus, "gpuNote": note, "network": s.readNetwork(elapsed),
		"processes": processes, "processCount": count}
}

// readLocalMemory reports memory the way Task Manager does: cached is the standby list,
// free is the free and zeroed lists, and dirty is the modified list waiting to be written.
func readLocalMemory() map[string]any {
	status := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	perf := performanceInformation{Size: uint32(unsafe.Sizeof(performanceInformation{}))}
	procGetPerformanceInfo.Call(uintptr(unsafe.Pointer(&perf)), uintptr(perf.Size))
	page := int64(perf.PageSize)
	if page == 0 {
		page = 4096
	}
	total, available := int64(status.TotalPhys), int64(status.AvailPhys)
	m := map[string]any{"total": total, "available": available, "used": total - available, "free": available,
		"buffers": int64(0), "cached": int64(0), "dirty": int64(0), "swapTotal": int64(0), "swapUsed": int64(0),
		"commitUsed": int64(perf.CommitTotal) * page, "commitLimit": int64(perf.CommitLimit) * page}
	if lists, ok := readMemoryLists(); ok {
		var standby int64
		for _, n := range lists.ByPriority {
			standby += int64(n)
		}
		m["cached"] = standby * page
		m["free"] = int64(lists.Free+lists.Zero) * page
		m["dirty"] = int64(lists.Modified) * page
	}
	return m
}

func readMemoryLists() (memoryListInformation, bool) {
	var lists memoryListInformation
	var size uint32
	err := windows.NtQuerySystemInformation(systemMemoryListInformation, unsafe.Pointer(&lists), uint32(unsafe.Sizeof(lists)), &size)
	return lists, err == nil
}

type localProcess struct {
	key      processKey
	ppid     uint32
	name     string
	cpu      float64
	private  int64
	critical bool
}

func (s *localSampler) readProcessList() []localProcess {
	if len(s.procBuf) == 0 {
		s.procBuf = make([]byte, 1<<20)
	}
	for {
		var size uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&s.procBuf[0]), uint32(len(s.procBuf)), &size)
		if err == windows.STATUS_INFO_LENGTH_MISMATCH && len(s.procBuf) < 64<<20 {
			s.procBuf = make([]byte, max(int(size), len(s.procBuf))*2)
			continue
		}
		if err != nil {
			return nil
		}
		break
	}
	var list []localProcess
	for offset := 0; offset < len(s.procBuf); {
		p := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&s.procBuf[offset]))
		if pid := uint32(p.UniqueProcessID); pid != 0 { // 0 is the idle process
			name := p.ImageName.String()
			if pid == 4 && name == "" {
				name = "System"
			}
			list = append(list, localProcess{key: processKey{pid, p.CreateTime}, ppid: uint32(p.InheritedFromUniqueProcessID),
				name: name, cpu: float64(p.UserTime + p.KernelTime), private: p.WorkingSetPrivateSize})
		}
		if p.NextEntryOffset == 0 {
			break
		}
		offset += int(p.NextEntryOffset)
	}
	return list
}

// appProcesses is this app and the WebView2 processes that draw its window.
func appProcesses(list []localProcess) map[uint32]bool {
	children := map[uint32][]uint32{}
	for _, p := range list {
		if p.ppid != p.key.pid && strings.EqualFold(p.name, "msedgewebview2.exe") {
			children[p.ppid] = append(children[p.ppid], p.key.pid)
		}
	}
	mine := map[uint32]bool{}
	queue := []uint32{uint32(os.Getpid())}
	for len(queue) > 0 && len(mine) < 512 {
		pid := queue[0]
		queue = queue[1:]
		if mine[pid] {
			continue
		}
		mine[pid] = true
		queue = append(queue, children[pid]...)
	}
	return mine
}

func (s *localSampler) owner(key processKey) processOwner {
	if o, ok := s.owners[key]; ok {
		return o
	}
	var o processOwner
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, key.pid); err == nil {
		var token windows.Token
		if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token) == nil {
			if u, err := token.GetTokenUser(); err == nil {
				o.user = s.accountName(u.User.Sid)
				o.own = u.User.Sid.String() == s.userSID
			}
			token.Close()
		}
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			o.path = windows.UTF16ToString(buf[:n])
		}
		windows.CloseHandle(h)
	}
	s.owners[key] = o
	return o
}

func (s *localSampler) processes(elapsed float64, cores int, totalMemory int64) ([]map[string]any, int) {
	list := s.readProcessList()
	mine := appProcesses(list)
	seen := make(map[processKey]bool, len(list))
	for i := range list {
		p := &list[i]
		seen[p.key] = true
		previous, ok := s.procTimes[p.key]
		s.procTimes[p.key] = int64(p.cpu)
		p.cpu = 0
		if ok && elapsed > 0 {
			p.cpu = max(0, 100*float64(s.procTimes[p.key]-previous)/(elapsed*1e7*float64(cores)))
		}
	}
	for key := range s.procTimes {
		if !seen[key] {
			delete(s.procTimes, key)
			delete(s.owners, key)
		}
	}
	chosen := map[uint32]localProcess{}
	byCPU := slices.Clone(list)
	slices.SortFunc(byCPU, func(a, b localProcess) int { return cmpFloat(b.cpu, a.cpu) })
	byMemory := slices.Clone(list)
	slices.SortFunc(byMemory, func(a, b localProcess) int { return cmpFloat(float64(b.private), float64(a.private)) })
	for _, p := range append(byCPU[:min(60, len(byCPU))], byMemory[:min(60, len(byMemory))]...) {
		chosen[p.key.pid] = p
	}
	rows := make([]map[string]any, 0, len(chosen))
	for _, p := range chosen {
		o := s.owner(p.key)
		lower := strings.ToLower(p.name)
		kernel := p.key.pid == 4 || (p.ppid == 4 && !strings.HasSuffix(lower, ".exe"))
		user := o.user
		if user == "" && kernel {
			user = "SYSTEM"
		}
		rows = append(rows, map[string]any{"pid": p.key.pid, "ppid": p.ppid, "start": p.key.created / 10000, "name": p.name,
			"user": user, "own": o.own, "cpu": round2(p.cpu), "rss": p.private, "mem": round2(percent(p.private, totalMemory)),
			"kernel": kernel, "protected": mine[p.key.pid] || windowsCriticalProcessNames[lower], "cmd": o.path})
	}
	return rows, len(list)
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *localSampler) readNetwork(elapsed float64) []map[string]any {
	size := uint32(32 << 10)
	var buf []byte
	for range 4 {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return []map[string]any{}
		}
		break
	}
	current := map[uint64][2]uint64{}
	interfaces := []map[string]any{}
	for a := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); a != nil; a = a.Next {
		if a.OperStatus != windows.IfOperStatusUp || a.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		row := windows.MibIfRow2{InterfaceLuid: a.Luid}
		if windows.GetIfEntry2Ex(windows.MibIfEntryNormal, &row) != nil {
			continue
		}
		current[a.Luid] = [2]uint64{row.InOctets, row.OutOctets}
		var rx, tx float64
		if old, ok := s.network[a.Luid]; ok && elapsed > 0 {
			if row.InOctets >= old[0] {
				rx = float64(row.InOctets-old[0]) / elapsed
			}
			if row.OutOctets >= old[1] {
				tx = float64(row.OutOctets-old[1]) / elapsed
			}
		}
		interfaces = append(interfaces, map[string]any{"name": windows.UTF16PtrToString(a.FriendlyName), "rx": rx, "tx": tx,
			"rxTotal": row.InOctets, "txTotal": row.OutOctets})
	}
	s.network = current
	return interfaces
}

// handle carries out one action from the page and returns its result message.
func (s *localSampler) handle(line []byte) map[string]any {
	var a MonitorAction
	if err := json.Unmarshal(line, &a); err != nil {
		return map[string]any{"type": "result", "ok": false, "message": "Invalid request."}
	}
	var message string
	var err error
	switch a.Type {
	case "kill":
		message, err = s.endProcess(a)
	case "dropcaches":
		message, err = s.purgeStandby()
	default:
		err = errors.New("Unknown request.")
	}
	if errors.Is(err, errLocalProcessExited) {
		message, err = err.Error(), nil
	}
	if err != nil {
		return map[string]any{"type": "result", "id": a.ID, "ok": false, "message": err.Error()}
	}
	return map[string]any{"type": "result", "id": a.ID, "ok": true, "message": message}
}

func (s *localSampler) endProcess(a MonitorAction) (string, error) {
	pid := uint32(a.PID)
	list := s.readProcessList()
	var target *localProcess
	for i := range list {
		if list[i].key.pid == pid {
			target = &list[i]
		}
	}
	if pid <= 4 || appProcesses(list)[pid] || target != nil && windowsCriticalProcessNames[strings.ToLower(target.name)] {
		return "", errors.New("This process is part of Windows or of this app and cannot be ended here.")
	}
	if target == nil || target.key.created/10000 != a.Start {
		return "", errLocalProcessExited
	}
	label := fmt.Sprintf("%s (PID %d)", target.name, pid)
	if a.Signal == "TERM" {
		windowsClosed, err := closeProcessWindows(pid)
		if err != nil {
			if !s.elevated {
				return "", fmt.Errorf("%s did not accept the request to close. Restart KamkmDirStat as administrator to close programs running as administrator.", label)
			}
			return "", fmt.Errorf("%s did not accept the request to close: %v", label, err)
		}
		if windowsClosed == 0 {
			return "", fmt.Errorf("%s has no open window to close. Use Force end to stop it.", label)
		}
		return "Asked " + label + " to close. It may ask to save work first.", nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", errLocalProcessExited
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) && !s.elevated {
			return "", fmt.Errorf("You can only end your own processes. Restart KamkmDirStat as administrator to end %s.", label)
		}
		return "", fmt.Errorf("Windows does not allow ending %s: %v", label, err)
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil || (int64(created.HighDateTime)<<32|int64(created.LowDateTime))/10000 != a.Start {
		return "", errLocalProcessExited
	}
	if err := windows.TerminateProcess(h, 1); err != nil {
		return "", fmt.Errorf("Windows does not allow ending %s: %v", label, err)
	}
	return "Force-ended " + label + ".", nil
}

var enumWindowsState struct {
	sync.Mutex
	pid     uint32
	windows []windows.HWND
}

var enumWindowsCallback = sync.OnceValue(func() uintptr {
	return windows.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		windows.GetWindowThreadProcessId(hwnd, &pid)
		if pid == enumWindowsState.pid && windows.IsWindowVisible(hwnd) {
			enumWindowsState.windows = append(enumWindowsState.windows, hwnd)
		}
		return 1
	})
})

// closeProcessWindows asks a program to exit by closing its windows, as Task Manager's End
// task does for applications.
func closeProcessWindows(pid uint32) (int, error) {
	enumWindowsState.Lock()
	defer enumWindowsState.Unlock()
	enumWindowsState.pid = pid
	enumWindowsState.windows = nil
	_ = windows.EnumWindows(enumWindowsCallback(), nil)
	closed := 0
	var lastErr error
	for _, hwnd := range enumWindowsState.windows {
		if r, _, err := procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0); r == 0 {
			lastErr = err
			continue
		}
		closed++
	}
	if closed == 0 && lastErr != nil {
		return 0, lastErr
	}
	return closed, nil
}

func (s *localSampler) purgeStandby() (string, error) {
	if !s.elevated {
		return "", errors.New("Freeing the cache needs administrator rights. Restart KamkmDirStat as administrator.")
	}
	before, _ := readMemoryLists()
	for _, command := range []uint32{memoryFlushModifiedList, memoryPurgeStandbyList} {
		c := command
		if err := windows.NtSetSystemInformation(systemMemoryListInformation, unsafe.Pointer(&c), uint32(unsafe.Sizeof(c))); err != nil {
			return "", fmt.Errorf("Windows did not clear the cache: %v", err)
		}
	}
	after, _ := readMemoryLists()
	var freed int64
	for i := range before.ByPriority {
		freed += int64(before.ByPriority[i]) - int64(after.ByPriority[i])
	}
	page := int64(os.Getpagesize())
	return fmt.Sprintf("Standby cache cleared. About %d MiB of cached memory was released.", max(0, freed*page)>>20), nil
}

// ---- GPUs ----

// gpuSampler lists adapters with DXGI and reads their use from the GPU performance
// counters, which cover every vendor. nvidia-smi adds temperature and power when present.
type gpuSampler struct {
	adapters []gpuAdapter
	mu       sync.Mutex
	counters gpuCounters
	pending  bool // the counters are still being set up
	closed   bool
	nvidia   map[string]nvidiaReading
	stop     chan struct{}
}

type gpuCounters struct{ query, engine, memory, shared uintptr }

type gpuAdapter struct {
	name, vendor, luid string
	dedicated, shared  int64
}

type nvidiaReading struct{ temp, power *float64 }

type dxgiAdapterDesc1 struct {
	Description                                                     [128]uint16
	VendorID, DeviceID, SubSysID, Revision                          uint32
	DedicatedVideoMemory, DedicatedSystemMemory, SharedSystemMemory uintptr
	LuidLow                                                         uint32
	LuidHigh                                                        int32
	Flags                                                           uint32
}

func newGPUSampler() *gpuSampler {
	g := &gpuSampler{adapters: listGPUAdapters(), nvidia: map[string]nvidiaReading{}, stop: make(chan struct{})}
	if len(g.adapters) > 0 && procPdhOpenQueryW.Find() == nil {
		g.pending = true
		go g.openCounters()
	}
	for _, a := range g.adapters {
		if a.vendor == "NVIDIA" {
			if smi, err := exec.LookPath("nvidia-smi"); err == nil {
				go g.watchNvidia(smi)
			}
			break
		}
	}
	return g
}

// openCounters sets up the GPU performance counters. The first time the app does this,
// Windows takes about a second to load them, so it runs beside the monitor instead of
// holding up its first sample; GPU use appears once the counters are ready.
func (g *gpuSampler) openCounters() {
	var c gpuCounters
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&c.query))); r == 0 {
		c.engine = pdhCounter(c.query, `\GPU Engine(*)\Utilization Percentage`)
		c.memory = pdhCounter(c.query, `\GPU Adapter Memory(*)\Dedicated Usage`)
		c.shared = pdhCounter(c.query, `\GPU Adapter Memory(*)\Shared Usage`)
		procPdhCollectQueryData.Call(c.query)
	} else {
		c.query = 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pending = false
	if g.closed {
		if c.query != 0 {
			procPdhCloseQuery.Call(c.query)
		}
		return
	}
	g.counters = c
}

func (g *gpuSampler) close() {
	close(g.stop)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	if g.counters.query != 0 {
		procPdhCloseQuery.Call(g.counters.query)
		g.counters = gpuCounters{}
	}
}

func pdhCounter(query uintptr, path string) uintptr {
	p, _ := windows.UTF16PtrFromString(path)
	var c uintptr
	if r, _, _ := procPdhAddEnglishCounterW.Call(query, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&c))); r != 0 {
		return 0
	}
	return c
}

// values returns each counter instance's current value.
func (g *gpuSampler) values(counter uintptr) map[string]float64 {
	const fmtDouble, fmtNoCap, moreData = 0x200, 0x8000, 0x800007D2
	type item struct {
		name   *uint16
		status uint32
		_      uint32
		value  float64
	}
	out := map[string]float64{}
	if counter == 0 {
		return out
	}
	for range 3 {
		var size, count uint32
		r, _, _ := procPdhGetFormattedArrayW.Call(counter, fmtDouble|fmtNoCap, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
		if r != moreData || size == 0 {
			return out
		}
		buf := make([]byte, size)
		r, _, _ = procPdhGetFormattedArrayW.Call(counter, fmtDouble|fmtNoCap, uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buf[0])))
		if r == moreData {
			continue
		}
		if r != 0 || count == 0 {
			return out
		}
		for _, it := range unsafe.Slice((*item)(unsafe.Pointer(&buf[0])), count) {
			if it.status <= 1 { // valid or new data
				out[strings.ToLower(windows.UTF16PtrToString(it.name))] += it.value
			}
		}
		runtime.KeepAlive(buf)
		return out
	}
	return out
}

func instanceField(instance, key, end string) string {
	i := strings.Index(instance, key)
	if i < 0 {
		return ""
	}
	rest := instance[i+len(key):]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

func (g *gpuSampler) sample() ([]map[string]any, string) {
	if len(g.adapters) == 0 {
		return []map[string]any{}, "No GPU found."
	}
	// Samples and close run on the same goroutine, so the counters stay open while in use.
	g.mu.Lock()
	c, pending, readings := g.counters, g.pending, g.nvidia
	g.mu.Unlock()
	use := map[string]float64{}
	dedicated, shared := map[string]float64{}, map[string]float64{}
	if c.query != 0 {
		procPdhCollectQueryData.Call(c.query)
		// Each engine's use is the sum over processes; an adapter shows its busiest engine.
		engines := map[string]float64{}
		for instance, v := range g.values(c.engine) {
			luid, engine := instanceField(instance, "luid_", "_phys"), instanceField(instance, "_eng_", "_")
			engines[luid+"/"+engine] += v
		}
		for key, v := range engines {
			luid := key[:strings.IndexByte(key, '/')]
			use[luid] = max(use[luid], min(100, v))
		}
		for instance, v := range g.values(c.memory) {
			dedicated[instanceField(instance, "luid_", "_phys")] += v
		}
		for instance, v := range g.values(c.shared) {
			shared[instanceField(instance, "luid_", "_phys")] += v
		}
	}
	gpus := []map[string]any{}
	for _, a := range g.adapters {
		gpu := map[string]any{"vendor": a.vendor, "name": a.name, "util": nil, "memUsed": nil, "memTotal": nil, "temp": nil, "power": nil}
		if c.query != 0 {
			gpu["util"] = round1(use[a.luid])
			if a.dedicated >= 256<<20 {
				gpu["memUsed"], gpu["memTotal"] = dedicated[a.luid], float64(a.dedicated)
			} else if a.shared > 0 {
				gpu["memUsed"], gpu["memTotal"] = shared[a.luid], float64(a.shared)
			}
		}
		if r, ok := readings[a.name]; ok {
			if r.temp != nil {
				gpu["temp"] = *r.temp
			}
			if r.power != nil {
				gpu["power"] = *r.power
			}
		}
		gpus = append(gpus, gpu)
	}
	if c.query == 0 && !pending {
		return gpus, "GPU use is unavailable because the Windows GPU performance counters could not be read."
	}
	return gpus, ""
}

func (g *gpuSampler) watchNvidia(smi string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, smi, "--query-gpu=name,temperature.gpu,power.draw", "--format=csv,noheader,nounits")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		out, err := cmd.Output()
		cancel()
		if err == nil {
			readings := map[string]nvidiaReading{}
			for _, line := range strings.Split(string(out), "\n") {
				parts := strings.Split(line, ",")
				if len(parts) < 3 {
					continue
				}
				number := func(s string) *float64 {
					v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
					if err != nil || v <= 0 {
						return nil
					}
					return &v
				}
				readings[strings.TrimSpace(parts[0])] = nvidiaReading{temp: number(parts[1]), power: number(parts[2])}
			}
			g.mu.Lock()
			g.nvidia = readings
			g.mu.Unlock()
		}
		select {
		case <-g.stop:
			return
		case <-ticker.C:
		}
	}
}

// listGPUAdapters enumerates hardware graphics adapters through DXGI.
func listGPUAdapters() []gpuAdapter {
	if procCreateDXGIFactory1.Find() != nil {
		return nil
	}
	iid := windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	var factory unsafe.Pointer
	if r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&factory))); r != 0 || factory == nil {
		return nil
	}
	vtable := func(object unsafe.Pointer) *[16]uintptr { return *(**[16]uintptr)(object) }
	defer syscall.SyscallN(vtable(factory)[2], uintptr(factory)) // Release
	var adapters []gpuAdapter
	for i := 0; i < 16; i++ {
		var adapter unsafe.Pointer
		if r, _, _ := syscall.SyscallN(vtable(factory)[12], uintptr(factory), uintptr(i), uintptr(unsafe.Pointer(&adapter))); r != 0 || adapter == nil {
			break // DXGI_ERROR_NOT_FOUND after the last adapter
		}
		var desc dxgiAdapterDesc1
		r, _, _ := syscall.SyscallN(vtable(adapter)[10], uintptr(adapter), uintptr(unsafe.Pointer(&desc))) // GetDesc1
		syscall.SyscallN(vtable(adapter)[2], uintptr(adapter))
		if r != 0 || desc.Flags&2 != 0 { // skip software adapters such as the Basic Render Driver
			continue
		}
		vendor := map[uint32]string{0x10de: "NVIDIA", 0x1002: "AMD", 0x1022: "AMD", 0x8086: "Intel", 0x5143: "Qualcomm"}[desc.VendorID]
		adapters = append(adapters, gpuAdapter{name: strings.TrimSpace(windows.UTF16ToString(desc.Description[:])), vendor: vendor,
			luid:      fmt.Sprintf("0x%08x_0x%08x", uint32(desc.LuidHigh), desc.LuidLow),
			dedicated: int64(desc.DedicatedVideoMemory), shared: int64(desc.SharedSystemMemory)})
	}
	return adapters
}

// launchElevated starts another copy of the app as administrator. It returns an error if
// the user declines the Windows prompt.
func launchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	if err := windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("Administrator access was not granted.")
		}
		return err
	}
	return nil
}
