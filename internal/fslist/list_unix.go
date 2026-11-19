//go:build !windows

package fslist

import (
	"io/fs"
	"os"
	"syscall"
)

// List lists a folder, skipping symbolic links and other file systems mounted inside the
// one device is on.
func List(dir string, device uint64) ([]Entry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	list, err := f.ReadDir(-1)
	entries := make([]Entry, 0, len(list))
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
			entries = append(entries, Entry{Name: e.Name(), Kind: Dir})
		} else if info.Mode().IsRegular() {
			entries = append(entries, Entry{Name: e.Name(), Kind: File, Size: info.Size()})
		}
	}
	return entries, err
}

// DeviceOf is the file system path is on, for List.
func DeviceOf(path string) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}
