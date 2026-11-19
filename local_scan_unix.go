//go:build !windows

package main

import (
	"path/filepath"

	"github.com/Kamkmgamer/KamkmDirStat/internal/fslist"
	"golang.org/x/sys/unix"
)

// volumeUsedBytes is the used space when path is the top of a mounted file system, and 0
// otherwise.
func volumeUsedBytes(path string) int64 {
	if path != "/" && fslist.DeviceOf(path) == fslist.DeviceOf(filepath.Dir(path)) {
		return 0
	}
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0
	}
	return int64(st.Blocks-st.Bfree) * int64(st.Bsize)
}
