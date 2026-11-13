package main

import (
	_ "embed"
	"encoding/json"
	"runtime"
)

//go:embed index.html
var page string

// window is the part of a webview the app uses. WebView2 on Windows and the webview
// library on Linux and macOS both provide it.
type window interface {
	Bind(name string, f interface{}) error
	SetTitle(title string)
	SetHtml(html string)
	Run()
}

const (
	windowTitle  = "KamkmDirStat"
	windowWidth  = 1320
	windowHeight = 850
)

// appPlatform tells the page which operating system it runs on: windows, linux or darwin.
func appPlatform() string { return runtime.GOOS }

// bindAll exposes the app's functions to the page. extra holds platform-only functions.
func bindAll(w window, extra map[string]interface{}) error {
	functions := map[string]interface{}{
		"appPlatform":       appPlatform,
		"startResolve":      startResolve,
		"resolveStatus":     resolveStatus,
		"startFingerprint":  startFingerprint,
		"fingerprintStatus": fingerprintStatus,
		"startScan":         startScan,
		"scanStatus":        scanStatus,
		"cancelScan":        cancelScan,
		"startView":         startView,
		"viewStatus":        viewStatus,
		"startMonitor":      startMonitor,
		"stopMonitor":       stopMonitor,
		"monitorStatus":     monitorStatus,
		"monitorAction":     monitorAction,
		"listDrives":        listDrives,
		"saveSettings":      saveSettings,
		"exportTheme":       exportTheme,
	}
	for name, f := range extra {
		functions[name] = f
	}
	for name, f := range functions {
		if err := w.Bind(name, f); err != nil {
			return err
		}
	}
	return nil
}

// runWindow shows the page with the saved settings until the window closes, then stops any
// running work.
func runWindow(w window, settings json.RawMessage, extra map[string]interface{}) error {
	defer closeResults()
	defer stopMonitor()
	w.SetTitle(windowTitle)
	if err := bindAll(w, extra); err != nil {
		return err
	}
	w.SetHtml(pageWithSettings(page, settings))
	w.Run()
	return nil
}
