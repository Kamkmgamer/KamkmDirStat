//go:build windows

package main

import (
	"log"
	"os"

	webview "github.com/jchv/go-webview2"
)

// The window icon is resource 1 in the .syso files go-winres makes from winres/winres.json.
const iconResource = 1

func main() {
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

	err := runWindow(w, settings, map[string]interface{}{
		"restartElevated": func() error {
			if err := launchElevated(); err != nil {
				return err
			}
			w.Dispatch(w.Terminate)
			return nil
		},
		"setWindowLook": func(dark bool, caption, text string) {
			w.Dispatch(func() {
				applyWindowLook(hwnd, windowLook{Dark: dark, Caption: caption, Text: text})
			})
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
