//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func hideWindow(*exec.Cmd) {}

func isLink(_ string, info fs.FileInfo) bool { return info.Mode()&fs.ModeSymlink != 0 }

// The Trash of macOS and Linux is a folder in the user's folder, found in the scan.
func recycleBinItem() *cleanEntry { return nil }

func emptyRecycleBin() error { return errors.New("There is no Recycle Bin here.") }

// moveToTrash moves p to the Trash: through Finder on macOS, so it can be put back, and with
// gio, which follows the freedesktop.org Trash specification, on Linux.
func moveToTrash(p string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p)
		cmd = exec.Command("osascript", "-e", `tell application "Finder" to delete (POSIX file "`+quoted+`" as alias)`)
	} else {
		gio, err := exec.LookPath("gio")
		if err != nil {
			return errors.New("Moving to the Trash needs gio, which is part of GLib. Use Delete permanently instead.")
		}
		cmd = exec.Command(gio, "trash", "--", p)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(firstLine(string(out), err))
	}
	if _, err := os.Lstat(p); err == nil {
		return errors.New("It is still there after moving it to the Trash.")
	}
	return nil
}

const canOpenWith = false

func openWith(string) error { return errors.New("Open with is not available here.") }
