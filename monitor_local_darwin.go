//go:build darwin && cgo

package main

/*
#include <mach/mach.h>
#include <mach/mach_host.h>
#include <mach/mach_time.h>
#include <mach/processor_info.h>
#include <mach/vm_page_size.h>
#include <libproc.h>
#include <net/if.h>
#include <net/route.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/sysctl.h>

static mach_port_t kam_host_port = MACH_PORT_NULL;
static mach_port_t kam_host(void) {
	if (kam_host_port == MACH_PORT_NULL) kam_host_port = mach_host_self();
	return kam_host_port;
}

static double kam_timebase(void) {
	mach_timebase_info_data_t t;
	if (mach_timebase_info(&t) != KERN_SUCCESS || t.denom == 0) return 1;
	return (double)t.numer / (double)t.denom;
}

// kam_cpu fills ticks with user, system, idle and nice ticks for each processor and returns
// the number of processors, or -1.
static int kam_cpu(unsigned long long *ticks, int max) {
	natural_t count = 0;
	processor_info_array_t info;
	mach_msg_type_number_t n = 0;
	if (host_processor_info(kam_host(), PROCESSOR_CPU_LOAD_INFO, &count, &info, &n) != KERN_SUCCESS) return -1;
	int cpus = (int)count < max ? (int)count : max;
	for (int i = 0; i < cpus; i++) {
		ticks[i * 4 + 0] = (unsigned)info[i * CPU_STATE_MAX + CPU_STATE_USER];
		ticks[i * 4 + 1] = (unsigned)info[i * CPU_STATE_MAX + CPU_STATE_SYSTEM];
		ticks[i * 4 + 2] = (unsigned)info[i * CPU_STATE_MAX + CPU_STATE_IDLE];
		ticks[i * 4 + 3] = (unsigned)info[i * CPU_STATE_MAX + CPU_STATE_NICE];
	}
	vm_deallocate(mach_task_self(), (vm_address_t)info, n * sizeof(integer_t));
	return cpus;
}

typedef struct {
	int ok;
	unsigned long long page, free, active, inactive, wired, speculative, compressor, purgeable, external, internal;
	unsigned long long swap_total, swap_used;
} kam_vm;

static kam_vm kam_memory(void) {
	kam_vm out;
	memset(&out, 0, sizeof(out));
	vm_statistics64_data_t s;
	mach_msg_type_number_t n = HOST_VM_INFO64_COUNT;
	if (host_statistics64(kam_host(), HOST_VM_INFO64, (host_info64_t)&s, &n) == KERN_SUCCESS) {
		out.ok = 1;
		out.page = vm_kernel_page_size;
		out.free = s.free_count;
		out.active = s.active_count;
		out.inactive = s.inactive_count;
		out.wired = s.wire_count;
		out.speculative = s.speculative_count;
		out.compressor = s.compressor_page_count;
		out.purgeable = s.purgeable_count;
		out.external = s.external_page_count;
		out.internal = s.internal_page_count;
	}
	struct xsw_usage swap;
	size_t size = sizeof(swap);
	if (sysctlbyname("vm.swapusage", &swap, &size, NULL, 0) == 0) {
		out.swap_total = swap.xsu_total;
		out.swap_used = swap.xsu_used;
	}
	return out;
}

typedef struct {
	int ok;
	int ppid;
	unsigned uid;
	char name[2 * MAXCOMLEN + 1];
	unsigned long long start_ms;
} kam_bsd;

static kam_bsd kam_bsdinfo(int pid) {
	kam_bsd out;
	memset(&out, 0, sizeof(out));
	struct proc_bsdinfo bi;
	if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &bi, sizeof(bi)) == (int)sizeof(bi)) {
		out.ok = 1;
		out.ppid = (int)bi.pbi_ppid;
		out.uid = bi.pbi_uid;
		strlcpy(out.name, bi.pbi_name[0] ? bi.pbi_name : bi.pbi_comm, sizeof(out.name));
		out.start_ms = (unsigned long long)bi.pbi_start_tvsec * 1000 + bi.pbi_start_tvusec / 1000;
	}
	return out;
}

typedef struct {
	int ok;
	unsigned long long cpu_ns, footprint;
} kam_usage;

// Other users' processes need root; for them ok is 0.
static kam_usage kam_rusage(int pid, double timebase) {
	kam_usage out;
	memset(&out, 0, sizeof(out));
	struct rusage_info_v2 ri;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&ri) == 0) {
		out.ok = 1;
		out.cpu_ns = (unsigned long long)((double)(ri.ri_user_time + ri.ri_system_time) * timebase);
		out.footprint = ri.ri_phys_footprint;
	}
	return out;
}

typedef struct {
	char name[IFNAMSIZ];
	unsigned long long rx, tx;
} kam_if;

// kam_interfaces reads 64-bit traffic counters for interfaces that are up.
static int kam_interfaces(kam_if *out, int max) {
	int mib[6] = {CTL_NET, PF_ROUTE, 0, 0, NET_RT_IFLIST2, 0};
	size_t len = 0;
	if (sysctl(mib, 6, NULL, &len, NULL, 0) < 0 || len == 0) return -1;
	char *buf = malloc(len);
	if (buf == NULL) return -1;
	if (sysctl(mib, 6, buf, &len, NULL, 0) < 0) {
		free(buf);
		return -1;
	}
	int n = 0;
	for (char *p = buf; p + sizeof(struct if_msghdr) <= buf + len && n < max;) {
		struct if_msghdr *ifm = (struct if_msghdr *)p;
		if (ifm->ifm_msglen == 0) break;
		if (ifm->ifm_type == RTM_IFINFO2) {
			struct if_msghdr2 *m = (struct if_msghdr2 *)p;
			if ((m->ifm_flags & IFF_UP) && !(m->ifm_flags & IFF_LOOPBACK) && if_indextoname(m->ifm_index, out[n].name) != NULL) {
				out[n].rx = m->ifm_data.ifi_ibytes;
				out[n].tx = m->ifm_data.ifi_obytes;
				n++;
			}
		}
		p += ifm->ifm_msglen;
	}
	free(buf);
	return n;
}
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The Mac monitor reads the same figures Activity Monitor shows. Administrators are asked
// for their password to end other users' processes or free the cache with purge.

func runLocalMonitor(ctx context.Context, writes <-chan []byte, update func(func(*MonitorStatus)) bool) error {
	s := newMacSampler()
	defer s.gpu.stop()
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
	s.sample()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			data, err := json.Marshal(s.sample())
			if err != nil {
				return err
			}
			if !update(func(st *MonitorStatus) { st.Sample = data; st.Seq++ }) {
				return context.Canceled
			}
		}
	}
}

type macKey struct {
	pid   int
	start uint64
}

type macSampler struct {
	privilege string
	me        uint32
	timebase  float64
	last      time.Time
	cores     [][4]uint64
	cpuTimes  map[macKey]uint64
	users     map[uint32]string
	network   map[string][2]uint64
	gpu       *macGPU
	mu        sync.Mutex // guards users for handle, which runs beside sample
}

func newMacSampler() *macSampler {
	return &macSampler{privilege: macPrivilege(), me: uint32(os.Geteuid()), timebase: float64(C.kam_timebase()),
		cpuTimes: map[macKey]uint64{}, users: map[uint32]string{}, network: map[string][2]uint64{}, gpu: newMacGPU()}
}

// macPrivilege mirrors remote_monitor.py: sudo is only probed for administrators.
func macPrivilege() string {
	if os.Geteuid() == 0 {
		return "root"
	}
	u, err := user.Current()
	if err != nil {
		return "none"
	}
	ids, _ := u.GroupIds()
	admin := false
	for _, id := range ids {
		if g, err := user.LookupGroupId(id); err == nil && g.Name == "admin" {
			admin = true
		}
	}
	if !admin {
		return "none"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "sudo", "-n", "true").Run() == nil {
		return "sudo"
	}
	return "sudo-password"
}

func (s *macSampler) userName(uid uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name, ok := s.users[uid]; ok {
		return name
	}
	name := strconv.Itoa(int(uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	s.users[uid] = name
	return name
}

func (s *macSampler) hello() map[string]any {
	host, _ := os.Hostname()
	version, _ := unix.Sysctl("kern.osproductversion")
	model, _ := unix.Sysctl("machdep.cpu.brand_string")
	return map[string]any{"type": "hello", "platform": "darwin", "privilege": s.privilege, "user": s.userName(s.me),
		"host": host, "system": strings.TrimSpace("macOS " + version), "machine": runtime.GOARCH, "cpuModel": strings.TrimSpace(model)}
}

func (s *macSampler) sample() map[string]any {
	now := time.Now()
	elapsed := now.Sub(s.last).Seconds()
	first := s.last.IsZero()
	s.last = now

	ticks := make([]C.ulonglong, 4*1024)
	n := int(C.kam_cpu(&ticks[0], 1024))
	cores := make([][4]uint64, max(0, n))
	coreUse := make([]float64, len(cores))
	var dUser, dSystem, dIdle, dTotal uint64
	for i := range cores {
		for j := range 4 {
			cores[i][j] = uint64(ticks[i*4+j])
		}
		if first || i >= len(s.cores) {
			continue
		}
		p := s.cores[i]
		user, system, idle, nice := cores[i][0]-p[0], cores[i][1]-p[1], cores[i][2]-p[2], cores[i][3]-p[3]
		total := user + system + idle + nice
		dUser, dSystem, dIdle, dTotal = dUser+user+nice, dSystem+system, dIdle+idle, dTotal+total
		coreUse[i] = round1(100 - macPercent(idle, total))
	}
	s.cores = cores
	cpu := map[string]any{"total": round1(100 - macPercent(dIdle, dTotal)), "user": round1(macPercent(dUser, dTotal)),
		"system": round1(macPercent(dSystem, dTotal)), "cores": coreUse}
	if dTotal == 0 {
		cpu["total"] = 0.0
	}

	memory := macMemory()
	processes, count := s.processes(elapsed, max(1, len(cores)), memory["total"].(int64))
	var load []float64
	var avg [3]C.double
	if C.getloadavg(&avg[0], 3) == 3 {
		load = []float64{float64(avg[0]), float64(avg[1]), float64(avg[2])}
	}
	var uptime float64
	if boot, err := unix.SysctlTimeval("kern.boottime"); err == nil {
		uptime = now.Sub(time.Unix(boot.Sec, int64(boot.Usec)*1000)).Seconds()
	}
	gpus, note := s.gpu.latest()
	return map[string]any{"type": "sample", "time": float64(now.UnixNano()) / 1e9, "uptime": uptime, "load": load,
		"cpu": cpu, "memory": memory, "gpus": gpus, "gpuNote": note, "network": s.interfaces(elapsed),
		"processes": processes, "processCount": count}
}

func macPercent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return min(100, 100*float64(part)/float64(whole))
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// macMemory splits memory as Activity Monitor does: used is app memory plus wired and
// compressed memory; cached is file-backed and purgeable memory.
func macMemory() map[string]any {
	v := C.kam_memory()
	total, _ := unix.SysctlUint64("hw.memsize")
	page := uint64(v.page)
	used := (uint64(v.internal) - min(uint64(v.purgeable), uint64(v.internal)) + uint64(v.wired) + uint64(v.compressor)) * page
	cached := (uint64(v.external) + uint64(v.purgeable)) * page
	used = min(used, total)
	free := (uint64(v.free) + uint64(v.speculative)) * page
	return map[string]any{"total": int64(total), "used": int64(used), "available": int64(total - used),
		"free": int64(min(free, total-used)), "cached": int64(min(cached, total-used)), "buffers": int64(0), "dirty": int64(0),
		"wired": int64(uint64(v.wired) * page), "compressed": int64(uint64(v.compressor) * page),
		"swapTotal": int64(v.swap_total), "swapUsed": int64(v.swap_used)}
}

type macProcess struct {
	key   macKey
	ppid  int
	uid   uint32
	name  string
	cpu   float64
	mem   int64
	known bool // CPU and memory could be read
}

func listMacPids() []int {
	buf := make([]C.int, 16384)
	n := int(C.proc_listallpids(unsafe.Pointer(&buf[0]), C.int(len(buf)*int(unsafe.Sizeof(buf[0])))))
	pids := make([]int, 0, max(0, n))
	for _, p := range buf[:max(0, min(n, len(buf)))] {
		pids = append(pids, int(p))
	}
	return pids
}

func macBSD(pid int) (C.kam_bsd, bool) {
	b := C.kam_bsdinfo(C.int(pid))
	return b, b.ok != 0
}

func (s *macSampler) processes(elapsed float64, cores int, totalMemory int64) ([]map[string]any, int) {
	var list []macProcess
	seen := map[macKey]bool{}
	for _, pid := range listMacPids() {
		b, ok := macBSD(pid)
		if !ok {
			continue
		}
		p := macProcess{key: macKey{pid, uint64(b.start_ms)}, ppid: int(b.ppid), uid: uint32(b.uid), name: C.GoString(&b.name[0])}
		if pid == 0 {
			p.name = "kernel_task"
		}
		if u := C.kam_rusage(C.int(pid), C.double(s.timebase)); u.ok != 0 {
			p.known, p.mem = true, int64(u.footprint)
			cpuNow := uint64(u.cpu_ns)
			if previous, ok := s.cpuTimes[p.key]; ok && elapsed > 0 && cpuNow >= previous {
				p.cpu = 100 * float64(cpuNow-previous) / (elapsed * 1e9 * float64(cores))
			}
			s.cpuTimes[p.key] = cpuNow
		}
		seen[p.key] = true
		list = append(list, p)
	}
	for key := range s.cpuTimes {
		if !seen[key] {
			delete(s.cpuTimes, key)
		}
	}
	byCPU := slices.Clone(list)
	slices.SortFunc(byCPU, func(a, b macProcess) int { return cmpFloat64(b.cpu, a.cpu) })
	byMemory := slices.Clone(list)
	slices.SortFunc(byMemory, func(a, b macProcess) int { return cmpFloat64(float64(b.mem), float64(a.mem)) })
	chosen := map[int]macProcess{}
	for _, p := range append(byCPU[:min(60, len(byCPU))], byMemory[:min(60, len(byMemory))]...) {
		chosen[p.key.pid] = p
	}
	self := os.Getpid()
	rows := make([]map[string]any, 0, len(chosen))
	for _, p := range chosen {
		path := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
		n := C.proc_pidpath(C.int(p.key.pid), unsafe.Pointer(&path[0]), C.uint32_t(len(path)))
		cmd := ""
		if n > 0 {
			cmd = string(path[:n])
		}
		rows = append(rows, map[string]any{"pid": p.key.pid, "ppid": p.ppid, "start": p.key.start, "name": p.name,
			"user": s.userName(p.uid), "own": p.uid == s.me, "cpu": round2mac(p.cpu), "rss": p.mem,
			"mem": round2mac(100 * float64(p.mem) / float64(max(1, totalMemory))), "kernel": p.key.pid == 0,
			"protected": p.key.pid <= 1 || p.key.pid == self, "cmd": cmd})
	}
	return rows, len(list)
}

func round2mac(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func cmpFloat64(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (s *macSampler) interfaces(elapsed float64) []map[string]any {
	buf := make([]C.kam_if, 256)
	n := int(C.kam_interfaces(&buf[0], C.int(len(buf))))
	current := map[string][2]uint64{}
	out := []map[string]any{}
	for _, it := range buf[:max(0, n)] {
		name := C.GoString(&it.name[0])
		rx, tx := uint64(it.rx), uint64(it.tx)
		if rx+tx == 0 {
			continue // virtual interfaces that have never carried traffic
		}
		current[name] = [2]uint64{rx, tx}
		var rxRate, txRate float64
		if old, ok := s.network[name]; ok && elapsed > 0 {
			if rx >= old[0] {
				rxRate = float64(rx-old[0]) / elapsed
			}
			if tx >= old[1] {
				txRate = float64(tx-old[1]) / elapsed
			}
		}
		out = append(out, map[string]any{"name": name, "rx": rxRate, "tx": txRate, "rxTotal": rx, "txTotal": tx})
	}
	s.network = current
	return out
}

func (s *macSampler) handle(line []byte) map[string]any {
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
		message, err = s.purge(a.Password)
	default:
		err = errors.New("Unknown request.")
	}
	if err != nil {
		return map[string]any{"type": "result", "id": a.ID, "ok": false, "message": err.Error()}
	}
	return map[string]any{"type": "result", "id": a.ID, "ok": true, "message": message}
}

func (s *macSampler) endProcess(a MonitorAction) (string, error) {
	sig := map[string]syscall.Signal{"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL}[a.Signal]
	if sig == 0 {
		return "", errors.New("Invalid request to end a process.")
	}
	if a.PID <= 1 || a.PID == os.Getpid() {
		return "", errors.New("This process is part of macOS or of this app and cannot be ended here.")
	}
	b, ok := macBSD(a.PID)
	if !ok || int64(b.start_ms) != a.Start {
		return "The process had already exited.", nil
	}
	label := fmt.Sprintf("%s (PID %d)", C.GoString(&b.name[0]), a.PID)
	verb := map[syscall.Signal]string{syscall.SIGTERM: "Asked " + label + " to end.", syscall.SIGKILL: "Force-ended " + label + "."}[sig]
	err := syscall.Kill(a.PID, sig)
	if err == nil {
		return verb, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return "The process had already exited.", nil
	}
	if !errors.Is(err, syscall.EPERM) || s.privilege == "none" {
		return "", fmt.Errorf("You can only end your own processes. Use an administrator account to end %s.", label)
	}
	if err := s.sudo([]string{"/bin/kill", "-s", a.Signal, strconv.Itoa(a.PID)}, a.Password); err != nil {
		return "", err
	}
	return verb, nil
}

func (s *macSampler) purge(password string) (string, error) {
	if s.privilege == "none" {
		return "", errors.New("Freeing the cache needs an administrator account.")
	}
	before := macMemory()["cached"].(int64)
	if err := s.sudo([]string{"/usr/sbin/purge"}, password); err != nil {
		return "", err
	}
	freed := max(0, before-macMemory()["cached"].(int64))
	return fmt.Sprintf("Cache freed. About %d MiB of cached memory was released.", freed>>20), nil
}

// sudo runs a command as root. With a password it is passed to sudo on standard input.
func (s *macSampler) sudo(command []string, password string) error {
	var argv []string
	switch s.privilege {
	case "root":
		argv = command
	case "sudo":
		argv = append([]string{"sudo", "-n", "--"}, command...)
	case "sudo-password":
		argv = append([]string{"sudo", "-S", "-p", "", "--"}, command...)
	default:
		return errors.New("This account is not an administrator.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if s.privilege == "sudo-password" {
		cmd.Stdin = strings.NewReader(password + "\n")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		message := strings.TrimSpace(lines[len(lines)-1])
		if s.privilege != "root" && (message == "" || strings.HasPrefix(message, "sudo:")) {
			return errors.New("sudo refused the command. Check the password and that this account may use sudo.")
		}
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}

// ---- GPUs ----

// macGPU reads GPU use from the IOAccelerator statistics that ioreg lists, every two
// seconds in the background because ioreg takes a moment to run.
type macGPU struct {
	mu    sync.Mutex
	gpus  []map[string]any
	note  string
	done  chan struct{}
	model string
}

func newMacGPU() *macGPU {
	model, _ := unix.Sysctl("machdep.cpu.brand_string")
	g := &macGPU{done: make(chan struct{}), note: "Reading GPU statistics…", gpus: []map[string]any{}, model: strings.TrimSpace(model)}
	go g.watch()
	return g
}

func (g *macGPU) stop() { close(g.done) }

func (g *macGPU) latest() ([]map[string]any, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gpus, g.note
}

func (g *macGPU) watch() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		gpus, note := g.read()
		g.mu.Lock()
		g.gpus, g.note = gpus, note
		g.mu.Unlock()
		select {
		case <-g.done:
			return
		case <-ticker.C:
		}
	}
}

func (g *macGPU) read() ([]map[string]any, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-r", "-d", "1", "-w", "0", "-c", "IOAccelerator").Output()
	if err != nil {
		return []map[string]any{}, "GPU statistics are unavailable: ioreg could not be run."
	}
	return parseIoregGPUs(string(out), g.model)
}

// macProcessStart is a process's start time in milliseconds, as samples report it.
func macProcessStart(pid int) (int64, bool) {
	b, ok := macBSD(pid)
	return int64(b.start_ms), ok
}
