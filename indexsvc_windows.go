package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The background indexer is this program running as a Windows service under the system
// account, which may read drives directly. It keeps live indexes of every drive and answers
// searches over a named pipe that only the user who set it up and administrators may open.
// Setting it up copies the program to Program Files, where other users cannot replace it.

const (
	indexServiceName = "KamkmDirStatIndex"
	indexPipeName    = `\\.\pipe\KamkmDirStatIndex`
	indexServiceDir  = "KamkmDirStat Index" // in Program Files
	indexServiceExe  = "KamkmDirStatIndex.exe"
)

// Exit codes of the elevated helper that sets the service up.
var setupFailures = map[uint32]string{
	2: "The background indexer could not be copied to Program Files.",
	3: "The background indexer could not be registered with Windows.",
	4: "The background indexer was set up but did not start. Check that no security program blocks it.",
	5: "The background indexer could not be removed.",
	6: "The account to give access to is not valid.",
}

var buildID = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
})

// queryIndexService returns the service's state and process id, without needing
// administrator rights. The state is "" when it is not set up.
func queryIndexService() (string, uint32) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", 0
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(indexServiceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "", 0
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)), uint32(unsafe.Sizeof(st)), &need); err != nil {
		return "stopped", 0
	}
	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "running", st.ProcessId
	case windows.SERVICE_STOPPED:
		return "stopped", 0
	}
	return "starting", st.ProcessId
}

// callService sends one request to the background indexer. It checks that the pipe belongs
// to the service, so another program cannot pose as it.
func callService(ctx context.Context, req indexServiceRequest) (indexServiceReply, error) {
	state, pid := queryIndexService()
	if state != "running" {
		return indexServiceReply{}, errors.New("The background indexer is not running.")
	}
	conn, err := winio.DialPipeContext(ctx, indexPipeName)
	if err != nil {
		return indexServiceReply{}, fmt.Errorf("The background indexer could not be reached: %w", err)
	}
	defer conn.Close()
	if f, ok := conn.(interface{ Fd() uintptr }); ok {
		var server uint32
		if err := windows.GetNamedPipeServerProcessId(windows.Handle(f.Fd()), &server); err != nil || server != pid {
			return indexServiceReply{}, errors.New("Another program answered in place of the background indexer.")
		}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Minute)
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return indexServiceReply{}, err
	}
	var reply indexServiceReply
	if err := json.NewDecoder(bufio.NewReaderSize(conn, 1<<16)).Decode(&reply); err != nil {
		if ctx.Err() != nil {
			return indexServiceReply{}, ctx.Err()
		}
		return indexServiceReply{}, fmt.Errorf("The background indexer did not answer: %w", err)
	}
	return reply, nil
}

// searchDrives searches every drive, with the background indexer when it runs and with
// this window's own indexes otherwise.
func searchDrives(ctx context.Context, q search.Query) (search.Result, error) {
	if state, _ := queryIndexService(); state == "running" {
		reply, err := callService(ctx, indexServiceRequest{Op: "search", Query: &q})
		if err == nil {
			if reply.Error != "" {
				return search.Result{}, errors.New(reply.Error)
			}
			if reply.Result != nil {
				go dropAppIndexes()
				return *reply.Result, nil
			}
		}
		if ctx.Err() != nil {
			return search.Result{}, ctx.Err()
		}
	}
	return driveIndex.search(ctx, q)
}

// dropAppIndexes frees this window's own indexes once the service answers instead.
func dropAppIndexes() {
	if driveIndex.loaded() {
		driveIndex.stop(time.Minute)
	}
}

var setupState struct {
	sync.Mutex
	running string
	err     string
}

func driveIndexStatus() DriveIndexStatus {
	setupState.Lock()
	setup, setupErr := setupState.running, setupState.err
	setupState.Unlock()
	state, _ := queryIndexService()
	var st DriveIndexStatus
	if state == "running" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		reply, err := callService(ctx, indexServiceRequest{Op: "status"})
		cancel()
		if err == nil && reply.Status != nil {
			st = *reply.Status
			st.Source, st.Outdated = "service", reply.Build != buildID()
			go dropAppIndexes()
		} else {
			st = appIndexStatus()
			state = "unreachable"
			if err != nil {
				st.Note = err.Error()
			}
		}
	} else {
		st = appIndexStatus()
	}
	st.Supported, st.Admin, st.Service = true, isElevated(), state
	st.Setup, st.SetupErr = setup, setupErr
	return st
}

func refreshDriveIndex(root string) error {
	if state, _ := queryIndexService(); state == "running" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		reply, err := callService(ctx, indexServiceRequest{Op: "refresh", Root: root})
		if err != nil {
			return err
		}
		if reply.Error != "" {
			return errors.New(reply.Error)
		}
		return nil
	}
	return driveIndex.refreshDrive(root)
}

// startIndexServiceSetup sets the background indexer up, or removes it, in the background.
// Windows asks for administrator approval unless this window already has it.
func startIndexServiceSetup(install bool) error {
	setupState.Lock()
	defer setupState.Unlock()
	if setupState.running != "" {
		return errors.New("The background indexer is still being set up.")
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid.String()
	setupState.running, setupState.err = map[bool]string{true: "install", false: "remove"}[install], ""
	go func() {
		var err error
		if isElevated() {
			code := uint32(0)
			if install {
				code = installIndexService(sid)
			} else {
				code = uninstallIndexService()
			}
			if code != 0 {
				err = errors.New(setupFailures[code])
			}
		} else {
			args := []string{"--uninstall-index-service"}
			if install {
				args = []string{"--install-index-service", sid}
			}
			err = runElevated(args)
		}
		if err == nil && install {
			// The app's own indexes are no longer needed.
			driveIndex.stop(time.Minute)
		}
		setupState.Lock()
		setupState.running = ""
		if err != nil {
			setupState.err = err.Error()
		}
		setupState.Unlock()
	}()
	return nil
}

var procShellExecuteExW = shell32.NewProc("ShellExecuteExW")

type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       uintptr
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    uintptr
	idList     uintptr
	class      *uint16
	keyClass   uintptr
	hotKey     uint32
	icon       uintptr
	process    windows.Handle
}

// runElevated runs this program with administrator rights and the given arguments, and
// waits for it to finish.
func runElevated(args []string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	const seeMaskNoCloseProcess, seeMaskNoAsync = 0x40, 0x100
	info := shellExecuteInfo{mask: seeMaskNoCloseProcess | seeMaskNoAsync, verb: verb, file: file, parameters: params, directory: dir, show: windows.SW_HIDE}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return errors.New("Administrator approval was not given.")
		}
		return callErr
	}
	if info.process == 0 {
		return errors.New("The setup did not start.")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return err
	}
	if code != 0 {
		if msg, ok := setupFailures[code]; ok {
			return errors.New(msg)
		}
		return fmt.Errorf("The setup failed (code %d).", code)
	}
	return nil
}

// serviceDataDir is where the service saves its indexes: the system account's own folder,
// which other users cannot open.
func serviceDataDir() string {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		sys = `C:\Windows\System32`
	}
	return filepath.Join(sys, "config", "systemprofile", "AppData", "Local", "KamkmDirStat", "drives")
}

func serviceExePath() (string, error) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pf, indexServiceDir, indexServiceExe), nil
}

// handleServiceCommand runs the service or its setup when the program was started for
// that, and reports whether it was.
func handleServiceCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "--index-service":
		user := ""
		if len(args) >= 3 && args[1] == "--user" {
			user = args[2]
		}
		if err := runIndexService(user); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "--install-index-service":
		if len(args) < 2 {
			os.Exit(6)
		}
		os.Exit(int(installIndexService(args[1])))
	case "--uninstall-index-service":
		os.Exit(int(uninstallIndexService()))
	}
	return false
}

func waitForState(s *mgr.Service, want svc.State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := s.Query(); err == nil && st.State == want {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// installIndexService copies this program to Program Files and runs it as the service. sid
// is the user who may search through it besides administrators. It must run elevated.
func installIndexService(sid string) uint32 {
	if s, err := windows.StringToSid(sid); err != nil || !s.IsValid() {
		return 6
	}
	dst, err := serviceExePath()
	if err != nil {
		return 2
	}
	m, err := mgr.Connect()
	if err != nil {
		return 3
	}
	defer m.Disconnect()
	existing, err := m.OpenService(indexServiceName)
	if err == nil {
		if st, err := existing.Query(); err == nil && st.State != svc.Stopped {
			existing.Control(svc.Stop)
			waitForState(existing, svc.Stopped, 30*time.Second)
		}
	} else {
		existing = nil
	}
	if err := copyServiceExe(dst); err != nil {
		if existing != nil {
			existing.Close()
		}
		return 2
	}
	args := []string{"--index-service", "--user", sid}
	var s *mgr.Service
	if existing != nil {
		s = existing
		c, err := s.Config()
		if err != nil {
			s.Close()
			return 3
		}
		quoted := []string{syscall.EscapeArg(dst)}
		for _, a := range args {
			quoted = append(quoted, syscall.EscapeArg(a))
		}
		c.BinaryPathName = strings.Join(quoted, " ")
		c.StartType, c.DelayedAutoStart, c.ServiceStartName = mgr.StartAutomatic, true, "LocalSystem"
		if err := s.UpdateConfig(c); err != nil {
			s.Close()
			return 3
		}
	} else {
		s, err = m.CreateService(indexServiceName, dst, mgr.Config{
			DisplayName:      "KamkmDirStat drive index",
			Description:      "Keeps an index of the names of every file on this computer's drives, so KamkmDirStat can search them instantly.",
			StartType:        mgr.StartAutomatic,
			DelayedAutoStart: true,
		}, args...)
		if err != nil {
			return 3
		}
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Minute},
	}, 24*60*60)
	if err := s.Start(); err != nil {
		return 4
	}
	if !waitForState(s, svc.Running, 30*time.Second) {
		return 4
	}
	return 0
}

// copyServiceExe puts a copy of this program at dst, unless it is that copy.
func copyServiceExe(dst string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(src), filepath.Clean(dst)) {
		return nil
	}
	dir := filepath.Dir(dst)
	if info, err := os.Lstat(dir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("the install folder is not a plain folder")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		// The old copy may take a moment to unlock after its service stops.
		for range 20 {
			if err = os.Rename(tmp, dst); err == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// uninstallIndexService stops and removes the service, its program and its indexes. It
// must run elevated.
func uninstallIndexService() uint32 {
	m, err := mgr.Connect()
	if err != nil {
		return 5
	}
	defer m.Disconnect()
	if s, err := m.OpenService(indexServiceName); err == nil {
		if st, err := s.Query(); err == nil && st.State != svc.Stopped {
			s.Control(svc.Stop)
			waitForState(s, svc.Stopped, 30*time.Second)
		}
		err := s.Delete()
		s.Close()
		if err != nil {
			return 5
		}
	}
	if dst, err := serviceExePath(); err == nil {
		for range 20 {
			if err := os.Remove(dst); err == nil || os.IsNotExist(err) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		os.Remove(filepath.Dir(dst))
	}
	os.RemoveAll(serviceDataDir())
	return 0
}

// pipeSecurity lets the system, administrators and the given user use the pipe, and no one
// over the network.
func pipeSecurity(user string) string {
	sd := "D:P(D;;GA;;;NU)(A;;GA;;;SY)(A;;GA;;;BA)"
	if s, err := windows.StringToSid(user); err == nil && s.IsValid() {
		sd += "(A;;GRGW;;;" + s.String() + ")"
	}
	return sd
}

type indexService struct{ user string }

func runIndexService(user string) error {
	if ok, err := svc.IsWindowsService(); err != nil || !ok {
		return errors.New("not started as a service")
	}
	return svc.Run(indexServiceName, &indexService{user: user})
}

func (s *indexService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	// Reading drives in the background should not slow down what the user is doing.
	windows.SetPriorityClass(windows.CurrentProcess(), windows.BELOW_NORMAL_PRIORITY_CLASS)
	ix := &driveIndexer{service: true, dir: serviceDataDir()}
	listener, err := winio.ListenPipe(indexPipeName, &winio.PipeConfig{SecurityDescriptor: pipeSecurity(s.user), InputBufferSize: 1 << 16, OutputBufferSize: 1 << 16})
	if err != nil {
		return true, 1
	}
	ix.active()
	go servePipe(listener, ix)
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for r := range requests {
		switch r.Cmd {
		case svc.Interrogate:
			changes <- r.CurrentStatus
		case svc.Stop, svc.Shutdown:
			changes <- svc.Status{State: svc.StopPending, WaitHint: 20000}
			listener.Close()
			ix.stop(15 * time.Second)
			return false, 0
		}
	}
	return false, 0
}

func servePipe(l net.Listener, ix *driveIndexer) {
	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, winio.ErrPipeListenerClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go serveConn(conn, ix)
	}
}

func serveConn(conn net.Conn, ix *driveIndexer) {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 1<<16)
	for {
		conn.SetDeadline(time.Now().Add(5 * time.Minute))
		line, err := r.ReadSlice('\n')
		if err != nil {
			return // closed, or a request over 64 KB
		}
		var req indexServiceRequest
		reply := indexServiceReply{Build: buildID()}
		if err := json.Unmarshal(line, &req); err != nil {
			reply.Error = "The request could not be read."
		} else {
			switch req.Op {
			case "status":
				st := DriveIndexStatus{Supported: true, Admin: true, Source: "service", Drives: ix.states()}
				reply.Status = &st
			case "search":
				if req.Query == nil {
					reply.Error = "No search was given."
					break
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				res, err := ix.search(ctx, *req.Query)
				cancel()
				if err != nil {
					reply.Error = err.Error()
				} else {
					reply.Result = &res
				}
			case "refresh":
				if err := ix.refreshDrive(req.Root); err != nil {
					reply.Error = err.Error()
				}
			default:
				reply.Error = "The background indexer does not understand the request."
			}
		}
		out, _ := json.Marshal(reply)
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}
