package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// volumeUsedBytes is the used space when path is the root of a drive, and 0 otherwise.
func volumeUsedBytes(path string) int64 {
	if filepath.VolumeName(path)+`\` != path {
		return 0
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free, total, totalFree uint64
	if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil || total < totalFree {
		return 0
	}
	return int64(total - totalFree)
}
