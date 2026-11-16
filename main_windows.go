//go:build windows

package main

import (
	"log"
	"os"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The window icon is resource 1 in the .syso files go-winres makes from winres/winres.json.
const iconResource = 1

func main() {
	if handleServiceCommand(os.Args[1:]) {
		return
	}
	release, ok := claimSingleInstance(len(os.Args) > 1 && os.Args[1] == restartedArg)
	if !ok {
		return
	}
	defer release()
	settings := loadSettings()
	look, hasLook := savedWindowLook(settings)
	stopColouring := func() {}
	if hasLook {
		setWebViewBackground(look)
		stopColouring = colourNewWindow(look)
	}
	width, height := initialWindowSize()
	w := webview.NewWithOptions(webview.WebViewOptions{
		WindowOptions: webview.WindowOptions{
			Title:  windowTitle,
			Width:  uint(width),
			Height: uint(height),
			IconId: iconResource,
			Center: true,
		},
	})
	stopColouring()
	if w == nil {
		log.Fatal("Microsoft Edge WebView2 Runtime is required")
	}
	defer w.Destroy()
	// Programs started from the app, such as ssh, do not need the setting.
	os.Unsetenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR")

	hwnd := uintptr(w.Window())
	centerWindow(hwnd)
	dpi := windowDPI(hwnd)
	minWidth, minHeight := outerSize(scaled(760, dpi), scaled(520, dpi))
	w.SetSize(minWidth, minHeight, webview.HintMin)

	// The page's own bar replaces the native caption. It must be installed before background
	// mode, whose handler then runs first and is removed first.
	var frame *customFrame
	if f, err := useCustomFrame(hwnd); err != nil {
		log.Printf("Using the native title bar: %v", err)
	} else {
		frame = f
		defer frame.close()
	}

	// Closing the window hides it so monitoring and history keep running; the tray icon's
	// Exit ends the app. Without the icon, closing the window exits as before.
	if background, err := keepWindowInBackground(hwnd); err != nil {
		log.Printf("Background mode is unavailable: %v", err)
	} else {
		defer background.close()
	}

	extra := map[string]interface{}{
		"restartElevated": func() error {
			if err := launchElevated(); err != nil {
				return err
			}
			w.Dispatch(w.Terminate)
			return nil
		},
		// isElevated tells the page whether the app already has administrator rights, which
		// let scans read NTFS drives' file tables directly.
		"isElevated": func() bool { return windows.GetCurrentProcessToken().IsElevated() },
		"setWindowLook": func(dark bool, caption, text string) {
			w.Dispatch(func() {
				applyWindowLook(hwnd, windowLook{Dark: dark, Caption: caption, Text: text})
			})
		},
	}
	// Only a window without its native caption offers the page's window buttons.
	if frame != nil {
		extra["windowControl"] = frame.control
	}
	err := runWindow(w, settings, extra)
	if err != nil {
		log.Fatal(err)
	}
}
