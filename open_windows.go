//go:build windows

package main

import (
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	procILCreateFromPathW        = shell32.NewProc("ILCreateFromPathW")
	procILFree                   = shell32.NewProc("ILFree")
	procSHOpenFolderAndSelectIts = shell32.NewProc("SHOpenFolderAndSelectItems")
)

// launchPath opens p as Explorer does on a double-click, or with reveal shows it selected in
// an Explorer window.
func launchPath(p string, reveal bool) error {
	// The shell needs COM on the calling thread, and some handlers need a single-threaded
	// apartment.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil {
		defer windows.CoUninitialize()
	}
	file, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return err
	}
	if reveal {
		if procSHOpenFolderAndSelectIts.Find() == nil {
			pidl, _, _ := procILCreateFromPathW.Call(uintptr(unsafe.Pointer(file)))
			if pidl != 0 {
				hr, _, _ := procSHOpenFolderAndSelectIts.Call(pidl, 0, 0, 0)
				procILFree.Call(pidl)
				if hr == 0 {
					return nil
				}
			}
		}
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + p + `"`}
		return cmd.Start()
	}
	err = windows.ShellExecute(0, nil, file, nil, nil, windows.SW_SHOWNORMAL)
	if errors.Is(err, windows.ERROR_NO_ASSOCIATION) {
		// No program is set for this type: let the user choose one, as Explorer does.
		verb, _ := windows.UTF16PtrFromString("openas")
		err = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	}
	return err
}
