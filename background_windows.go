//go:build windows

package main

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmNull            = 0x0000
	wmLButtonUp       = 0x0202
	wmLButtonDblClk   = 0x0203
	wmRButtonUp       = 0x0205
	wmContextMenu     = 0x007b
	wmApp             = 0x8000
	trayMessage       = wmApp + 41
	trayIconID        = 1
	trayOpenCommand   = 1
	trayExitCommand   = 2
	gwlpWndProc       = ^uintptr(3) // -4
	swHide            = 0
	swShow            = 5
	swRestore         = 9
	imageIcon         = 1
	lrDefaultSize     = 0x0040
	lrShared          = 0x8000
	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nifMessage        = 0x0001
	nifIcon           = 0x0002
	nifTip            = 0x0004
	nifInfo           = 0x0010
	niifInfo          = 0x0001
	mfString          = 0
	tpmRightButton    = 0x0002
	tpmReturnCmd      = 0x0100
	hwndBroadcast     = 0xffff
	msgfltAllow       = 1
	trayTooltip       = "KamkmDirStat: monitoring and history"
	trayNoticeTitle   = "KamkmDirStat is still running"
	trayNoticeMessage = "Monitoring and history continue in the background. Use this icon to reopen the window or exit."

	// restartedArg tells a copy started by restartElevated to wait for this one to exit
	// instead of handing over to it.
	restartedArg   = "--restarted"
	appMutexName   = `Local\KamkmDirStat.App`
	showWindowName = "KamkmDirStat.ShowWindow"
)

var (
	procShellNotifyIconW         = shell32.NewProc("Shell_NotifyIconW")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procDefWindowProcW           = user32.NewProc("DefWindowProcW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetForegroundWnd         = user32.NewProc("SetForegroundWindow")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	procAppendMenuW              = user32.NewProc("AppendMenuW")
	procSetMenuDefault           = user32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	procDestroyMenu              = user32.NewProc("DestroyMenu")
	procRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	procChangeWindowMessageFilEx = user32.NewProc("ChangeWindowMessageFilterEx")
	procAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")
)

// Explorer sends taskbarCreatedMessage when it restarts, after which the icon must be added
// again. A second copy of the app sends showWindowMessage to bring this one back.
var (
	taskbarCreatedMessage = registerWindowMessage("TaskbarCreated")
	showWindowMessage     = registerWindowMessage(showWindowName)
)

func registerWindowMessage(name string) uint32 {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	r, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(r)
}

type point struct{ X, Y int32 }

// notifyIconData is NOTIFYICONDATAW as used by every supported version of Windows.
type notifyIconData struct {
	Size             uint32
	Window           uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             windows.GUID
	BalloonIcon      uintptr
}

// backgroundWindow turns the window's close button into hide, and gives the tray icon the
// only ordinary way to exit. Monitors and history belong to the process, so hiding the page
// leaves them running.
type backgroundWindow struct {
	hwnd         uintptr
	previousProc uintptr
	icon         uintptr
	iconAdded    bool
	notified     bool
	exiting      bool
}

// backgroundWindows is only used on the UI thread, where window messages arrive.
var backgroundWindows = map[uintptr]*backgroundWindow{}

var backgroundWindowCallback = windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	if b := backgroundWindows[hwnd]; b != nil {
		return b.handleMessage(uint32(msg), wparam, lparam)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
})

// keepWindowInBackground installs the close-button handler and the notification-area icon.
// It must be called on the webview's UI thread, and closed there when the app exits.
func keepWindowInBackground(hwnd uintptr) (*backgroundWindow, error) {
	b := &backgroundWindow{hwnd: hwnd}
	backgroundWindows[hwnd] = b
	previous, _, callErr := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, backgroundWindowCallback)
	if previous == 0 {
		delete(backgroundWindows, hwnd)
		return nil, fmt.Errorf("could not handle the window close button: %w", callErr)
	}
	b.previousProc = previous
	// A copy started without administrator rights may still ask an elevated copy to show.
	for _, msg := range []uint32{showWindowMessage, taskbarCreatedMessage} {
		if msg != 0 {
			procChangeWindowMessageFilEx.Call(hwnd, uintptr(msg), msgfltAllow, 0)
		}
	}
	if err := b.addIcon(); err != nil {
		_ = b.close()
		return nil, err
	}
	return b, nil
}

func (b *backgroundWindow) handleMessage(msg uint32, wparam, lparam uintptr) uintptr {
	switch {
	case msg == wmClose && !b.exiting:
		b.hide()
		return 0
	case msg == trayMessage:
		switch lparam {
		case wmLButtonUp, wmLButtonDblClk:
			b.show()
		case wmRButtonUp, wmContextMenu:
			b.showMenu()
		}
		return 0
	case msg != 0 && msg == showWindowMessage:
		b.show()
		return 0
	case msg != 0 && msg == taskbarCreatedMessage:
		b.iconAdded = false
		_ = b.addIcon()
	}
	r, _, _ := procCallWindowProcW.Call(b.previousProc, b.hwnd, uintptr(msg), wparam, lparam)
	return r
}

func (b *backgroundWindow) hide() {
	procShowWindow.Call(b.hwnd, swHide)
	if b.notified || !b.iconAdded {
		return
	}
	data := b.iconData()
	data.Flags = nifInfo
	data.InfoFlags = niifInfo
	copyUTF16(data.InfoTitle[:], trayNoticeTitle)
	copyUTF16(data.Info[:], trayNoticeMessage)
	if ok, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&data))); ok != 0 {
		b.notified = true
	}
}

func (b *backgroundWindow) show() {
	if minimized, _, _ := procIsIconic.Call(b.hwnd); minimized != 0 {
		procShowWindow.Call(b.hwnd, swRestore)
	} else {
		procShowWindow.Call(b.hwnd, swShow)
	}
	procSetForegroundWnd.Call(b.hwnd)
}

// exit closes the window for real, which ends the message loop and runs the app's cleanup.
func (b *backgroundWindow) exit() {
	if b.exiting {
		return
	}
	b.exiting = true
	b.removeIcon()
	procPostMessageW.Call(b.hwnd, wmClose, 0, 0)
}

func (b *backgroundWindow) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	open, _ := windows.UTF16PtrFromString("Open KamkmDirStat")
	exit, _ := windows.UTF16PtrFromString("Exit")
	procAppendMenuW.Call(menu, mfString, trayOpenCommand, uintptr(unsafe.Pointer(open)))
	procAppendMenuW.Call(menu, mfString, trayExitCommand, uintptr(unsafe.Pointer(exit)))
	procSetMenuDefault.Call(menu, trayOpenCommand, 0)

	// Windows only dismisses a notification-area menu on an outside click when the window is
	// in the foreground, and the empty message afterwards is part of the same workaround.
	procSetForegroundWnd.Call(b.hwnd)
	var at point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&at)))
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd, uintptr(at.X), uintptr(at.Y), 0, b.hwnd, 0)
	procPostMessageW.Call(b.hwnd, wmNull, 0, 0)
	switch command {
	case trayOpenCommand:
		b.show()
	case trayExitCommand:
		b.exit()
	}
}

func (b *backgroundWindow) iconData() notifyIconData {
	return notifyIconData{
		Size:            uint32(unsafe.Sizeof(notifyIconData{})),
		Window:          b.hwnd,
		ID:              trayIconID,
		CallbackMessage: trayMessage,
	}
}

func (b *backgroundWindow) addIcon() error {
	if b.icon == 0 {
		var instance windows.Handle
		if err := windows.GetModuleHandleEx(0, nil, &instance); err != nil {
			return fmt.Errorf("could not load the notification-area icon: %w", err)
		}
		icon, _, callErr := procLoadImageW.Call(uintptr(instance), iconResource, imageIcon, 0, 0, lrDefaultSize|lrShared)
		if icon == 0 {
			return fmt.Errorf("could not load the notification-area icon: %w", callErr)
		}
		b.icon = icon
	}
	data := b.iconData()
	data.Flags = nifMessage | nifIcon | nifTip
	data.Icon = b.icon
	copyUTF16(data.Tip[:], trayTooltip)
	if ok, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&data))); ok == 0 {
		return fmt.Errorf("could not add the notification-area icon: %w", callErr)
	}
	b.iconAdded = true
	return nil
}

func (b *backgroundWindow) removeIcon() {
	if !b.iconAdded {
		return
	}
	data := b.iconData()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	b.iconAdded = false
}

// close removes the icon and the close-button handler. The window may already be destroyed.
func (b *backgroundWindow) close() error {
	b.removeIcon()
	delete(backgroundWindows, b.hwnd)
	if b.previousProc == 0 {
		return nil
	}
	result, _, err := procSetWindowLongPtrW.Call(b.hwnd, gwlpWndProc, b.previousProc)
	b.previousProc = 0
	if result == 0 {
		return err
	}
	return nil
}

func copyUTF16(dst []uint16, value string) {
	encoded, _ := windows.UTF16FromString(value)
	copy(dst[:len(dst)-1], encoded)
}

// claimSingleInstance makes sure only one copy of the app runs, since two would write the
// same history files. When another copy is running it is asked to show its window and
// claimSingleInstance returns false. A copy started by restartElevated waits instead, because
// the copy that started it is about to exit.
func claimSingleInstance(restarted bool) (release func(), ok bool) {
	name, _ := windows.UTF16PtrFromString(appMutexName)
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			// The running copy has administrator rights and this one does not.
			showRunningInstance()
			return nil, false
		}
		return func() {}, true // without the mutex, starting is better than refusing to
	}
	timeout := uint32(0)
	if restarted {
		timeout = uint32((15 * time.Second).Milliseconds())
	}
	switch event, _ := windows.WaitForSingleObject(mutex, timeout); event {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
		return func() {
			_ = windows.ReleaseMutex(mutex)
			_ = windows.CloseHandle(mutex)
		}, true
	}
	_ = windows.CloseHandle(mutex)
	showRunningInstance()
	return nil, false
}

func showRunningInstance() {
	if showWindowMessage == 0 {
		return
	}
	// Let the running copy take the foreground, which Windows otherwise refuses it.
	procAllowSetForegroundWindow.Call(^uintptr(0)) // ASFW_ANY
	procPostMessageW.Call(hwndBroadcast, uintptr(showWindowMessage), 0, 0)
}
