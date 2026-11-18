//go:build windows

package main

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"unsafe"

	webview "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// WebView2 does not learn by itself that its window is minimised, hidden in the notification
// area or covered by other windows, so the page would keep drawing every second for nobody.
// windowVisibility tells it: a minimised or hidden window hides the WebView, which also makes
// document.hidden true and lets the browser slow its timers, and a window that other windows
// cover completely tells the page through __setWindowCovered, so it stops redrawing until it can
// be seen again. The WebView itself stays shown while covered, so uncovering it never shows an
// empty window.

const (
	wmSize              = 0x0005
	wmShowWindow        = 0x0018
	wmTimer             = 0x0113
	sizeMinimized       = 1
	gwHwndPrev          = 3
	gaRoot              = 2
	gwlExStyle          = ^uintptr(19) // -20
	wsExTransparent     = 0x00000020
	wsExLayered         = 0x00080000
	lwaColorKey         = 0x1
	lwaAlpha            = 0x2
	dwmwaExtendedFrame  = 9
	dwmwaCloaked        = 14
	smXVirtualScreen    = 76
	smYVirtualScreen    = 77
	smCxVirtualScreen   = 78
	smCyVirtualScreen   = 79
	rgnDiff             = 4
	nullRegion          = 1
	winEventOutOfCtx    = 0x0000
	objidWindow         = 0
	childidSelf         = 0
	eventForeground     = 0x0003
	eventMoveSizeEnd    = 0x000B
	eventMinimizeStart  = 0x0016
	eventMinimizeEnd    = 0x0017
	eventObjectDestroy  = 0x8001
	eventObjectShow     = 0x8002
	eventObjectHide     = 0x8003
	eventObjectCloaked  = 0x8017
	eventObjectUncloak  = 0x8018
	coverCheckTimer     = 0x4b31
	coverPollTimer      = 0x4b32
	coverCheckDelayMs   = 60
	coverPollIntervalMs = 2000
	maxWindowsAbove     = 4096
)

var (
	gdi32                          = windows.NewLazySystemDLL("gdi32.dll")
	procGetWindow                  = user32.NewProc("GetWindow")
	procGetAncestor                = user32.NewProc("GetAncestor")
	procIsWindowVisible            = user32.NewProc("IsWindowVisible")
	procGetWindowLongPtrW          = user32.NewProc("GetWindowLongPtrW")
	procGetLayeredWindowAttributes = user32.NewProc("GetLayeredWindowAttributes")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	procSetTimer                   = user32.NewProc("SetTimer")
	procKillTimer                  = user32.NewProc("KillTimer")
	procSetWinEventHook            = user32.NewProc("SetWinEventHook")
	procUnhookWinEvent             = user32.NewProc("UnhookWinEvent")
	procCreateRectRgn              = gdi32.NewProc("CreateRectRgn")
	procCombineRgn                 = gdi32.NewProc("CombineRgn")
	procDeleteObject               = gdi32.NewProc("DeleteObject")
)

type windowVisibility struct {
	hwnd         uintptr
	previousProc uintptr
	w            webview.WebView
	controller   *edge.ICoreWebView2Controller
	shown        bool // what the WebView was last told
	covered      atomic.Bool
	hooks        []uintptr
}

// windowVisibilities is only used on the UI thread, where window messages and the
// out-of-context window events arrive.
var windowVisibilities = map[uintptr]*windowVisibility{}

var windowVisibilityCallback = windows.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	if v := windowVisibilities[hwnd]; v != nil {
		return v.handleMessage(uint32(msg), wparam, lparam)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
})

// Another window appearing, closing, moving or coming to the front may change what covers
// ours, so each such event asks for a check a moment later. Events arriving together make one
// check.
var winEventCallback = windows.NewCallback(func(hook, event, hwnd, idObject, idChild, thread, time uintptr) uintptr {
	if int32(idObject) != objidWindow || int32(idChild) != childidSelf {
		return 0
	}
	if (event == eventObjectShow || event == eventObjectHide) && hwnd != 0 {
		// Controls inside other windows show and hide all the time; only whole windows matter.
		if root, _, _ := procGetAncestor.Call(hwnd, gaRoot); root != hwnd {
			return 0
		}
	}
	for _, v := range windowVisibilities {
		v.scheduleCheck()
	}
	return 0
})

// trackWindowVisibility must be called on the webview's UI thread after the window's other
// handlers are installed, and closed there before them.
func trackWindowVisibility(w webview.WebView) (*windowVisibility, error) {
	hwnd := uintptr(w.Window())
	v := &windowVisibility{hwnd: hwnd, w: w, shown: true}
	windowVisibilities[hwnd] = v
	previous, _, callErr := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, windowVisibilityCallback)
	if previous == 0 {
		delete(windowVisibilities, hwnd)
		return nil, fmt.Errorf("could not follow the window's visibility: %w", callErr)
	}
	v.previousProc = previous
	for _, r := range [][2]uintptr{
		{eventForeground, eventForeground},
		{eventMoveSizeEnd, eventMoveSizeEnd},
		{eventMinimizeStart, eventMinimizeEnd},
		{eventObjectDestroy, eventObjectHide},
		{eventObjectCloaked, eventObjectUncloak},
	} {
		if hook, _, _ := procSetWinEventHook.Call(r[0], r[1], 0, winEventCallback, 0, 0, winEventOutOfCtx); hook != 0 {
			v.hooks = append(v.hooks, hook)
		}
	}
	// The events above miss some changes, such as another window being maximised, so the
	// window is also checked every few seconds while it is shown.
	procSetTimer.Call(hwnd, coverPollTimer, coverPollIntervalMs, 0)
	v.scheduleCheck()
	return v, nil
}

// isCovered tells the page, when it starts, whether its window is covered.
func (v *windowVisibility) isCovered() bool { return v.covered.Load() }

func (v *windowVisibility) handleMessage(msg uint32, wparam, lparam uintptr) uintptr {
	if msg == wmTimer && (wparam == coverCheckTimer || wparam == coverPollTimer) {
		if wparam == coverCheckTimer {
			procKillTimer.Call(v.hwnd, coverCheckTimer)
		}
		v.check()
		return 0
	}
	r, _, _ := procCallWindowProcW.Call(v.previousProc, v.hwnd, uintptr(msg), wparam, lparam)
	switch msg {
	case wmSize:
		visible, _, _ := procIsWindowVisible.Call(v.hwnd)
		v.setShown(wparam != sizeMinimized && visible != 0)
	case wmShowWindow:
		minimized, _, _ := procIsIconic.Call(v.hwnd)
		v.setShown(wparam != 0 && minimized == 0)
	}
	return r
}

func (v *windowVisibility) scheduleCheck() {
	procSetTimer.Call(v.hwnd, coverCheckTimer, coverCheckDelayMs, 0)
}

func (v *windowVisibility) setShown(shown bool) {
	if shown == v.shown {
		return
	}
	v.shown = shown
	if v.controller == nil {
		v.controller = webviewController(v.w)
	}
	if v.controller != nil {
		_ = v.controller.PutIsVisible(shown)
	}
	if shown {
		procSetTimer.Call(v.hwnd, coverPollTimer, coverPollIntervalMs, 0)
		v.scheduleCheck()
	} else {
		procKillTimer.Call(v.hwnd, coverPollTimer)
		procKillTimer.Call(v.hwnd, coverCheckTimer)
		// A hidden page is already hidden; it should not stay covered once it comes back.
		v.setCovered(false)
	}
}

func (v *windowVisibility) check() {
	if v.shown {
		v.setCovered(windowCovered(v.hwnd))
	}
}

func (v *windowVisibility) setCovered(covered bool) {
	if v.covered.Swap(covered) == covered {
		return
	}
	v.w.Eval(fmt.Sprintf("window.__setWindowCovered&&window.__setWindowCovered(%t)", covered))
}

// close stops following the window. The window may already be destroyed.
func (v *windowVisibility) close() error {
	for _, hook := range v.hooks {
		procUnhookWinEvent.Call(hook)
	}
	v.hooks = nil
	procKillTimer.Call(v.hwnd, coverPollTimer)
	procKillTimer.Call(v.hwnd, coverCheckTimer)
	delete(windowVisibilities, v.hwnd)
	if v.previousProc == 0 {
		return nil
	}
	result, _, err := procSetWindowLongPtrW.Call(v.hwnd, gwlpWndProc, v.previousProc)
	v.previousProc = 0
	if result == 0 {
		return err
	}
	return nil
}

// webviewController finds the WebView2 controller, which the webview library keeps to
// itself. If a later version of the library stores it differently this returns nil, and the
// window simply goes on as before.
func webviewController(w webview.WebView) *edge.ICoreWebView2Controller {
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	field := v.Elem().FieldByName("browser")
	if !field.IsValid() || field.Kind() != reflect.Interface || !field.CanAddr() {
		return nil
	}
	field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	if field.IsNil() {
		return nil
	}
	c, ok := field.Interface().(interface {
		GetController() *edge.ICoreWebView2Controller
	})
	if !ok {
		return nil
	}
	return c.GetController()
}

// windowCovered tells whether other windows hide every part of hwnd that is on a screen.
// When unsure, such as with a see-through window on top, it says the window is not covered.
func windowCovered(hwnd uintptr) bool {
	if fg, _, _ := procGetForegroundWindow.Call(); fg == hwnd {
		return false
	}
	target, ok := windowBounds(hwnd)
	if !ok {
		return false
	}
	var above []rect
	h := hwnd
	for range maxWindowsAbove {
		h, _, _ = procGetWindow.Call(h, gwHwndPrev)
		if h == 0 {
			break
		}
		if !hidesWhatIsBelow(h) {
			continue
		}
		if r, ok := windowBounds(h); ok {
			above = append(above, r)
		}
	}
	return rectsCover(intersectRect(target, virtualScreen()), above)
}

// hidesWhatIsBelow tells whether a window is shown and opaque.
func hidesWhatIsBelow(hwnd uintptr) bool {
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return false
	}
	if minimized, _, _ := procIsIconic.Call(hwnd); minimized != 0 {
		return false
	}
	// Windows on other virtual desktops and suspended Store apps are cloaked: present but
	// not drawn.
	var cloaked uint32
	if windows.DwmGetWindowAttribute(windows.HWND(hwnd), dwmwaCloaked, unsafe.Pointer(&cloaked), 4) == nil && cloaked != 0 {
		return false
	}
	ex, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle)
	if ex&wsExTransparent != 0 {
		return false
	}
	if ex&wsExLayered != 0 {
		var key uint32
		var alpha byte
		var flags uint32
		ok, _, _ := procGetLayeredWindowAttributes.Call(hwnd, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&alpha)), uintptr(unsafe.Pointer(&flags)))
		// Without attributes the window draws with its own per-pixel transparency.
		if ok == 0 || flags&lwaColorKey != 0 || (flags&lwaAlpha != 0 && alpha < 255) {
			return false
		}
	}
	return true
}

// windowBounds is the visible frame of a window, without the invisible borders Windows 10 and
// 11 add for resizing, which would otherwise count as covering the windows beside it.
func windowBounds(hwnd uintptr) (rect, bool) {
	var r rect
	if windows.DwmGetWindowAttribute(windows.HWND(hwnd), dwmwaExtendedFrame, unsafe.Pointer(&r), uint32(unsafe.Sizeof(r))) != nil {
		if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
			return rect{}, false
		}
	}
	return r, r.Right > r.Left && r.Bottom > r.Top
}

// virtualScreen is the rectangle around all screens.
func virtualScreen() rect {
	metric := func(i uintptr) int32 { v, _, _ := procGetSystemMetrics.Call(i); return int32(v) }
	x, y := metric(smXVirtualScreen), metric(smYVirtualScreen)
	return rect{x, y, x + metric(smCxVirtualScreen), y + metric(smCyVirtualScreen)}
}

func intersectRect(a, b rect) rect {
	r := rect{max(a.Left, b.Left), max(a.Top, b.Top), min(a.Right, b.Right), min(a.Bottom, b.Bottom)}
	if r.Right <= r.Left || r.Bottom <= r.Top {
		return rect{}
	}
	return r
}

// rectsCover tells whether the rectangles in above together cover all of target. An empty
// target, such as a window entirely off the screens, counts as covered.
func rectsCover(target rect, above []rect) bool {
	if target.Right <= target.Left || target.Bottom <= target.Top {
		return true
	}
	region := func(r rect) uintptr {
		h, _, _ := procCreateRectRgn.Call(uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom))
		return h
	}
	rest := region(target)
	if rest == 0 {
		return false
	}
	defer procDeleteObject.Call(rest)
	for _, r := range above {
		if intersectRect(r, target) == (rect{}) {
			continue
		}
		o := region(r)
		if o == 0 {
			return false
		}
		result, _, _ := procCombineRgn.Call(rest, rest, o, rgnDiff)
		procDeleteObject.Call(o)
		if result == nullRegion {
			return true
		}
	}
	return false
}
