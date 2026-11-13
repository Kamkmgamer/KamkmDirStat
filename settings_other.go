//go:build !windows

package main

import (
	"bufio"
	"os"
	"path/filepath"
)

// platformDownloadsDir reads the Downloads folder from the XDG user directories on Linux.
// It returns "" when they do not name one, and on macOS, where it is always ~/Downloads.
func platformDownloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	f, err := os.Open(filepath.Join(config, "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()
	return xdgDownloadDir(bufio.NewScanner(f), home)
}
