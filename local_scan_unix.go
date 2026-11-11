//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// listLocalDir skips symbolic links and other file systems mounted inside the scan.
func listLocalDir(dir string, device uint64) ([]localEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(-1)
	entries := make([]localEntry, 0, len(list))
	for _, e := range list {
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue // removed since the folder was listed
		}
		if info.IsDir() {
			if st, ok := info.Sys().(*syscall.Stat_t); ok && uint64(st.Dev) != device {
				continue
			}
			entries = append(entries, localEntry{name: e.Name(), kind: localDir})
		} else if info.Mode().IsRegular() {
			entries = append(entries, localEntry{name: e.Name(), kind: localFile, size: info.Size()})
		}
	}
	return entries, err
}

func deviceOf(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

// volumeUsedBytes is the used space when path is the top of a mounted file system, and 0
// otherwise.
func volumeUsedBytes(path string) int64 {
	if path != "/" && deviceOf(path) == deviceOf(filepath.Dir(path)) {
		return 0
	}
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0
	}
	return int64(st.Blocks-st.Bfree) * int64(st.Bsize)
}
