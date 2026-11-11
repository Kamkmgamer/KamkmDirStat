//go:build windows

package main

import (
	_ "embed"
	webview "github.com/jchv/go-webview2"
	"log"
)

//go:embed index.html
var page string

func main() {
	w := webview.New(false)
	if w == nil {
		log.Fatal("Microsoft Edge WebView2 Runtime is required")
	}
	defer w.Destroy()
	defer closeResults()
	w.SetTitle("KamkmDirStat")
	w.SetSize(1320, 850, webview.HintNone)
	if err := w.Bind("startResolve", startResolve); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("startFingerprint", startFingerprint); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("startScan", startScan); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("resolveStatus", resolveStatus); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("fingerprintStatus", fingerprintStatus); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("cancelScan", cancelScan); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("scanStatus", scanStatus); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("startView", startView); err != nil {
		log.Fatal(err)
	}
	if err := w.Bind("viewStatus", viewStatus); err != nil {
		log.Fatal(err)
	}
	w.SetHtml(page)
	w.Run()
}
