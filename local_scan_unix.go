//go:build !windows

package main

import (
	"io/fs"
	"os"
	"syscall"
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

func volumeUsedBytes(string) int64 { return 0 }
