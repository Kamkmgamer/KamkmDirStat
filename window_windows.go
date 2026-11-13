//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// platformDownloadsDir is the Downloads folder, which the user may have moved.
func platformDownloadsDir() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0)
	if err != nil || !isDir(dir) {
		return ""
	}
	return dir
}

var (
	procGetDpiForSystem      = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow      = user32.NewProc("GetDpiForWindow")
	procAdjustWindowRectEx   = user32.NewProc("AdjustWindowRectEx")
	procMonitorFromWindow    = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	procSetWindowPos         = user32.NewProc("SetWindowPos")
	procGetWindowRect        = user32.NewProc("GetWindowRect")
	procSystemParametersInfo = user32.NewProc("SystemParametersInfoW")
	procSetWindowsHookExW    = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx  = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx       = user32.NewProc("CallNextHookEx")
	procSetClassLongPtrW     = user32.NewProc("SetClassLongPtrW")
	procGetClassNameW        = user32.NewProc("GetClassNameW")
	procCreateSolidBrush     = windows.NewLazySystemDLL("gdi32.dll").NewProc("CreateSolidBrush")
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfo struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
}

const (
	wsOverlappedWindow = 0x00CF0000
	baseDPI            = 96
)

// systemDPI is the scale the window opens at; 96 means 100%.
func systemDPI() int {
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			return int(dpi)
		}
	}
	return baseDPI
}

func windowDPI(hwnd uintptr) int {
	if procGetDpiForWindow.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(hwnd); dpi > 0 {
			return int(dpi)
		}
	}
	return systemDPI()
}

func scaled(v, dpi int) int { return v * dpi / baseDPI }

// workArea is the part of the primary screen outside the taskbar.
func workArea() rect {
	var r rect
	const spiGetWorkArea = 0x0030
	if ok, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok == 0 || r.Right <= r.Left {
		return rect{0, 0, 1280, 720}
	}
	return r
}

// outerSize turns a page size in pixels into the size of the window around it.
func outerSize(width, height int) (int, int) {
	r := rect{0, 0, int32(width), int32(height)}
	procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&r)), wsOverlappedWindow, 0, 0)
	return int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// initialWindowSize is the window size for the screen it opens on: the usual size scaled for
// the display, made smaller when the screen has less room.
func initialWindowSize() (int, int) {
	dpi := systemDPI()
	width, height := outerSize(scaled(windowWidth, dpi), scaled(windowHeight, dpi))
	area := workArea()
	return fitWindow(width, height, int(area.Right-area.Left), int(area.Bottom-area.Top))
}

// fitWindow keeps a window inside an area, leaving a margin around it when it has to shrink.
func fitWindow(width, height, areaWidth, areaHeight int) (int, int) {
	if width > areaWidth {
		width = areaWidth * 94 / 100
	}
	if height > areaHeight {
		height = areaHeight * 94 / 100
	}
	return width, height
}

// centerWindow moves the window to the middle of the work area of its screen.
func centerWindow(hwnd uintptr) {
	var win rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&win))); ok == 0 {
		return
	}
	area := workArea()
	const monitorDefaultToNearest = 2
	if monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest); monitor != 0 {
		info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok != 0 {
			area = info.Work
		}
	}
	w, h := win.Right-win.Left, win.Bottom-win.Top
	x := area.Left + max(0, (area.Right-area.Left-w)/2)
	y := area.Top + max(0, (area.Bottom-area.Top-h)/2)
	const swpNoSize, swpNoZOrder, swpNoActivate = 0x1, 0x4, 0x10
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

// colorRef turns #rrggbb into the 0x00bbggrr form Windows uses.
func colorRef(s string) (uint32, bool) {
	r, g, b, ok := parseHexColor(s)
	if !ok {
		return 0, false
	}
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16, true
}

// applyWindowLook colours the title bar like the page's title bar. Windows 10 only switches
// between its light and dark title bar; Windows 11 also takes the exact colours.
func applyWindowLook(hwnd uintptr, look windowLook) {
	h := windows.HWND(hwnd)
	var dark uint32
	if look.Dark {
		dark = 1
	}
	const (
		useImmersiveDarkMode    = 20
		useImmersiveDarkModeOld = 19
		captionColor            = 35
		textColor               = 36
	)
	if windows.DwmSetWindowAttribute(h, useImmersiveDarkMode, unsafe.Pointer(&dark), 4) != nil {
		windows.DwmSetWindowAttribute(h, useImmersiveDarkModeOld, unsafe.Pointer(&dark), 4)
	}
	if c, ok := colorRef(look.Caption); ok {
		windows.DwmSetWindowAttribute(h, captionColor, unsafe.Pointer(&c), 4)
	}
	if c, ok := colorRef(look.Text); ok {
		windows.DwmSetWindowAttribute(h, textColor, unsafe.Pointer(&c), 4)
	}
}

// setWebViewBackground sets the colour WebView2 shows before the page draws, so the window
// does not flash white on a dark theme. It must run before the webview starts.
func setWebViewBackground(look windowLook) {
	const name = "WEBVIEW2_DEFAULT_BACKGROUND_COLOR"
	if os.Getenv(name) != "" {
		return
	}
	if r, g, b, ok := parseHexColor(look.Background); ok {
		os.Setenv(name, fmt.Sprintf("FF%02X%02X%02X", r, g, b))
	}
}

// colourNewWindow gives the app window the theme's colours from the moment it is created, so
// it does not show a white window while WebView2 starts. The webview library creates and
// shows the window itself, so a hook on this thread catches it as it is made. Call the
// returned function once the window exists.
func colourNewWindow(look windowLook) (done func()) {
	background, ok := colorRef(look.Background)
	if !ok {
		return func() {}
	}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(background))
	const (
		whCBT             = 5
		hcbtCreateWnd     = 3
		hcbtActivate      = 5
		gclpHbrBackground = ^uintptr(9) // -10
	)
	var hook uintptr
	callback := windows.NewCallback(func(code int32, wparam, lparam uintptr) uintptr {
		if (code == hcbtCreateWnd || code == hcbtActivate) && isWebviewWindow(wparam) {
			if code == hcbtCreateWnd && brush != 0 {
				procSetClassLongPtrW.Call(wparam, gclpHbrBackground, brush)
			}
			applyWindowLook(wparam, look)
		}
		r, _, _ := procCallNextHookEx.Call(hook, uintptr(code), wparam, lparam)
		return r
	})
	hook, _, _ = procSetWindowsHookExW.Call(whCBT, callback, 0, uintptr(windows.GetCurrentThreadId()))
	return func() {
		if hook != 0 {
			procUnhookWindowsHookEx.Call(hook)
			hook = 0
		}
	}
}

// isWebviewWindow tells whether a window is the webview library's top-level window.
func isWebviewWindow(hwnd uintptr) bool {
	var name [16]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	return windows.UTF16ToString(name[:n]) == "webview"
}
