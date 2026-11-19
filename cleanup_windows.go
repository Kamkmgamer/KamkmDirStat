package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/Kamkmgamer/KamkmDirStat/internal/fslist"
	"golang.org/x/sys/windows"
)

var (
	procSHQueryRecycleBinW = shell32.NewProc("SHQueryRecycleBinW")
	procSHEmptyRecycleBinW = shell32.NewProc("SHEmptyRecycleBinW")
	procSHFileOperationW   = shell32.NewProc("SHFileOperationW")
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// isLink reports junctions and symbolic links, which the scanner does not follow either.
// Other reparse points, such as OneDrive's cloud files, are ordinary folders.
func isLink(p string, info fs.FileInfo) bool {
	if info.Mode()&fs.ModeSymlink != 0 {
		return true
	}
	name, err := windows.UTF16PtrFromString(fslist.LongPath(p))
	if err != nil {
		return true
	}
	var d windows.Win32finddata
	h, err := windows.FindFirstFile(name, &d)
	if err != nil {
		return false
	}
	windows.FindClose(h)
	return d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 &&
		(d.Reserved0 == windows.IO_REPARSE_TAG_MOUNT_POINT || d.Reserved0 == windows.IO_REPARSE_TAG_SYMLINK)
}

type shQueryRBInfo struct {
	size     uint32
	bytes    int64
	numItems int64
}

func recycleBinSize() (int64, int64, error) {
	if err := procSHQueryRecycleBinW.Find(); err != nil {
		return 0, 0, err
	}
	info := shQueryRBInfo{size: uint32(unsafe.Sizeof(shQueryRBInfo{}))}
	hr, _, _ := procSHQueryRecycleBinW.Call(0, uintptr(unsafe.Pointer(&info)))
	if hr != 0 {
		return 0, 0, fmt.Errorf("SHQueryRecycleBin failed: 0x%08x", uint32(hr))
	}
	return info.bytes, info.numItems, nil
}

// recycleBinItem offers to empty the Recycle Bin of every drive.
func recycleBinItem() *cleanEntry {
	size, items, err := recycleBinSize()
	if err != nil || size <= 0 {
		return nil
	}
	return &cleanEntry{item: CleanItem{Group: groupSystem, Name: "Recycle Bin", Detail: fmt.Sprintf("%s on all drives", countText(int(items), "deleted item"))},
		targets: []cleanTarget{{kind: emptyBin, path: "Recycle Bin", size: size, dir: -1, id: -1}}}
}

// comThread runs f on a locked thread with COM set up, as the shell needs.
func comThread(f func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
		defer windows.CoUninitialize()
	}
	return f()
}

func emptyRecycleBin() error {
	if err := procSHEmptyRecycleBinW.Find(); err != nil {
		return err
	}
	return comThread(func() error {
		const flags = 0x1 | 0x2 | 0x4 // no confirmation, no progress window, no sound
		hr, _, _ := procSHEmptyRecycleBinW.Call(0, 0, flags)
		// S_OK, or E_UNEXPECTED when the bin was already empty.
		if hr != 0 && uint32(hr) != 0x8000FFFF {
			return errors.New(windows.Errno(hr & 0xFFFF).Error())
		}
		return nil
	})
}

type shFileOpStruct struct {
	hwnd          uintptr
	wFunc         uint32
	pFrom         *uint16
	pTo           *uint16
	fFlags        uint16
	anyAborted    int32
	nameMappings  uintptr
	progressTitle *uint16
}

// moveToTrash moves p to the Recycle Bin. Windows asks first when p is too large for the
// bin, rather than deleting it for good.
func moveToTrash(p string) error {
	if err := procSHFileOperationW.Find(); err != nil {
		return err
	}
	if len(p) >= windows.MAX_PATH {
		return errors.New("The path is too long for the Recycle Bin. Use Delete permanently instead.")
	}
	from, err := windows.UTF16FromString(p)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list of paths ends with two zeros
	const (
		foDelete          = 3
		fofSilent         = 0x4
		fofNoConfirmation = 0x10
		fofAllowUndo      = 0x40
		fofNoErrorUI      = 0x400
		fofWantNukeWarn   = 0x4000
	)
	op := shFileOpStruct{wFunc: foDelete, pFrom: &from[0], fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofWantNukeWarn}
	return comThread(func() error {
		r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
		if op.anyAborted != 0 {
			return errors.New("Cancelled.")
		}
		if r != 0 {
			return fmt.Errorf("Windows could not move it to the Recycle Bin (error 0x%x). A program may have it open.", r)
		}
		return nil
	})
}

// canOpenWith tells the page whether Open with is offered.
const canOpenWith = true

// openWith shows the Windows "Open with" dialog for p.
func openWith(p string) error {
	return comThread(func() error {
		file, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return err
		}
		verb, _ := windows.UTF16PtrFromString("openas")
		return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
	})
}
