package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// launchElevated starts another copy of the app as administrator. It returns an error if
// the user declines the Windows prompt.
func launchElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	args, _ := windows.UTF16PtrFromString(restartedArg)
	if err := windows.ShellExecute(0, verb, file, args, dir, windows.SW_SHOWNORMAL); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("Administrator access was not granted.")
		}
		return err
	}
	return nil
}
