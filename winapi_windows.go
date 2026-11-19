package main

import "golang.org/x/sys/windows"

// System libraries and calls that several of the app's Windows files share.
var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	user32           = windows.NewLazySystemDLL("user32.dll")
	procPostMessageW = user32.NewProc("PostMessageW")
)

const wmClose = 0x0010
