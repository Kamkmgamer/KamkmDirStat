//go:build windows

package main

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The page draws the title bar itself: its tabs, the connection and the window buttons sit
// where Windows would put its caption. customFrame removes only the top of the native frame,
// so the left, right and bottom borders, the shadow, Snap and the taskbar behave as usual.
// The page asks for moving, the top-edge resize, the window buttons and the system menu
// through windowControl.

const (
	wmNCCalcSize    = 0x0083
	wmNCLButtonDown = 0x00a1
	wmSysCommand    = 0x0112
	htCaption       = 2
	htTop           = 12
	htTopLeft       = 13
	htTopRight      = 14
	swMaximize      = 3
	swMinimize      = 6
	smCyFrame       = 33
	smCxPaddedBordr = 92
	scSize          = 0xf000
	scMove          = 0xf010
	scMinimize      = 0xf020
	scMaximize      = 0xf030
	scClose         = 0xf060
	scRestore       = 0xf120
	mfEnabled       = 0x0000
	mfGrayed        = 0x0001
	tpmLeftAlign    = 0x0000
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
)

var (
	procReleaseCapture         = user32.NewProc("ReleaseCapture")
	procIsZoomed               = user32.NewProc("IsZoomed")
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")
	procGetSystemMenu          = user32.NewProc("GetSystemMenu")
	procEnableMenuItem         = user32.NewProc("EnableMenuItem")
)

type customFrame struct {
	hwnd         uintptr
	previousProc uintptr
}

// customFrames is only used on the UI thread, where window messages arrive.
var customFrames = map[uintptr]*customFrame{}

var customFrameCallback = windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	if f := customFrames[hwnd]; f != nil {
		return f.handleMessage(uint32(msg), wparam, lparam)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
})

// useCustomFrame hides the native caption. It must be called on the webview's UI thread,
// before keepWindowInBackground, and closed there after it.
func useCustomFrame(hwnd uintptr) (*customFrame, error) {
	f := &customFrame{hwnd: hwnd}
	customFrames[hwnd] = f
	previous, _, callErr := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, customFrameCallback)
	if previous == 0 {
		delete(customFrames, hwnd)
		return nil, fmt.Errorf("could not replace the title bar: %w", callErr)
	}
	f.previousProc = previous
	f.frameChanged()
	return f, nil
}

// frameChanged makes Windows measure the frame again.
func (f *customFrame) frameChanged() {
	procSetWindowPos.Call(f.hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

func (f *customFrame) handleMessage(msg uint32, wparam, lparam uintptr) uintptr {
	if msg == wmNCCalcSize && wparam != 0 {
		// The first rectangle is the window; Windows shrinks it to the client area. Keeping the
		// top where it was gives the caption's room to the page.
		client := *(**rect)(unsafe.Pointer(&lparam))
		top := client.Top
		r, _, _ := procCallWindowProcW.Call(f.previousProc, f.hwnd, uintptr(msg), wparam, lparam)
		client.Top = top
		// A maximised window reaches past the screen by its frame on every side, so the top
		// needs the same inset as the others or the page's bar would be cut off.
		if f.maximized() {
			client.Top += int32(f.frameThickness())
		}
		return r
	}
	r, _, _ := procCallWindowProcW.Call(f.previousProc, f.hwnd, uintptr(msg), wparam, lparam)
	return r
}

func (f *customFrame) maximized() bool {
	zoomed, _, _ := procIsZoomed.Call(f.hwnd)
	return zoomed != 0
}

func (f *customFrame) frameThickness() int {
	dpi := windowDPI(f.hwnd)
	if procGetSystemMetricsForDpi.Find() != nil {
		return scaled(8, dpi)
	}
	frame, _, _ := procGetSystemMetricsForDpi.Call(smCyFrame, uintptr(dpi))
	padding, _, _ := procGetSystemMetricsForDpi.Call(smCxPaddedBordr, uintptr(dpi))
	return int(frame + padding)
}

// control carries out a request from the page's title bar and tells whether the window is
// now maximised, so the page can show the right button.
func (f *customFrame) control(action string) bool {
	switch action {
	case "drag":
		f.startNative(htCaption)
	case "resize-top":
		f.startNative(htTop)
	case "resize-topleft":
		f.startNative(htTopLeft)
	case "resize-topright":
		f.startNative(htTopRight)
	case "minimize":
		procShowWindow.Call(f.hwnd, swMinimize)
	case "maximize":
		if f.maximized() {
			procShowWindow.Call(f.hwnd, swRestore)
		} else {
			procShowWindow.Call(f.hwnd, swMaximize)
		}
	case "close":
		procPostMessageW.Call(f.hwnd, wmClose, 0, 0)
	case "menu":
		f.showSystemMenu()
	}
	return f.maximized()
}

// startNative hands a press in the page's title bar to Windows, which then moves or resizes
// the window as if the press had been on its own frame, with Snap included.
func (f *customFrame) startNative(hit uintptr) {
	procReleaseCapture.Call()
	procPostMessageW.Call(f.hwnd, wmNCLButtonDown, hit, 0)
}

// showSystemMenu opens the window menu Windows shows on a right-click of its title bar.
func (f *customFrame) showSystemMenu() {
	menu, _, _ := procGetSystemMenu.Call(f.hwnd, 0)
	if menu == 0 {
		return
	}
	zoomed := f.maximized()
	state := func(on bool) uintptr {
		if on {
			return mfEnabled
		}
		return mfGrayed
	}
	procEnableMenuItem.Call(menu, scRestore, state(zoomed))
	procEnableMenuItem.Call(menu, scMove, state(!zoomed))
	procEnableMenuItem.Call(menu, scSize, state(!zoomed))
	procEnableMenuItem.Call(menu, scMinimize, mfEnabled)
	procEnableMenuItem.Call(menu, scMaximize, state(!zoomed))
	procEnableMenuItem.Call(menu, scClose, mfEnabled)
	var at point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&at)))
	command, _, _ := procTrackPopupMenu.Call(menu, tpmLeftAlign|tpmReturnCmd, uintptr(at.X), uintptr(at.Y), 0, f.hwnd, 0)
	if command != 0 {
		procPostMessageW.Call(f.hwnd, wmSysCommand, command, 0)
	}
}

// close gives the window its native caption back. The window may already be destroyed.
func (f *customFrame) close() error {
	delete(customFrames, f.hwnd)
	if f.previousProc == 0 {
		return nil
	}
	result, _, err := procSetWindowLongPtrW.Call(f.hwnd, gwlpWndProc, f.previousProc)
	f.previousProc = 0
	if result == 0 {
		return err
	}
	return nil
}
