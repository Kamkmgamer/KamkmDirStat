//go:build !windows && cgo

package main

import (
	"log"

	webview "github.com/webview/webview_go"
)

// Linux uses GTK with WebKitGTK and macOS uses its built-in WebKit.
func main() {
	w := webview.New(false)
	if w == nil {
		log.Fatal("Could not open a window. On Linux, install WebKitGTK (libwebkit2gtk-4.1).")
	}
	defer w.Destroy()
	w.SetSize(windowWidth, windowHeight, webview.HintNone)
	if err := runWindow(w, loadSettings(), nil); err != nil {
		log.Fatal(err)
	}
}
