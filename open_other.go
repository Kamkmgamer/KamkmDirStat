//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
)

// launchPath opens p with its default program, or with reveal shows it in the file manager.
func launchPath(p string, reveal bool) error {
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin" && reveal:
		cmd = exec.Command("open", "-R", p)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", p)
	case reveal:
		// File managers differ in how they select an item, so open its folder.
		cmd = exec.Command("xdg-open", filepath.Dir(p))
	default:
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
