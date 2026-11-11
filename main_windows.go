//go:build windows

package main

import (
	"log"

	webview "github.com/jchv/go-webview2"
)

func main() {
	w := webview.New(false)
	if w == nil {
		log.Fatal("Microsoft Edge WebView2 Runtime is required")
	}
	defer w.Destroy()
	w.SetSize(windowWidth, windowHeight, webview.HintNone)
	err := runWindow(w, map[string]interface{}{
		"restartElevated": func() error {
			if err := launchElevated(); err != nil {
				return err
			}
			w.Dispatch(w.Terminate)
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
