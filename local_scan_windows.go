package main

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procFindFirstFileExW = kernel32.NewProc("FindFirstFileExW")

const (
	findExInfoBasic       = 1
	findExSearchNameMatch = 0
	findFirstExLargeFetch = 2
)

// listLocalDir lists a folder with FindFirstFileEx, which returns sizes, attributes and
// reparse tags in large batches. It is several times faster than opening each folder as a
// file. Junctions, mounted volumes and symbolic links are skipped. Other reparse points,
// such as OneDrive placeholders and deduplicated files, count as ordinary files and folders.
func listLocalDir(dir string, _ uint64) ([]localEntry, error) {
	pattern, err := windows.UTF16PtrFromString(longPath(filepath.Join(dir, "*")))
	if err != nil {
		return nil, err
	}
	var data windows.Win32finddata
	r, _, callErr := procFindFirstFileExW.Call(uintptr(unsafe.Pointer(pattern)), findExInfoBasic, uintptr(unsafe.Pointer(&data)),
		findExSearchNameMatch, 0, findFirstExLargeFetch)
	h := windows.Handle(r)
	if h == windows.InvalidHandle {
		if callErr == windows.ERROR_FILE_NOT_FOUND {
			return nil, nil
		}
		return nil, &pathError{dir, callErr}
	}
	defer windows.FindClose(h)
	var entries []localEntry
	for {
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." {
			if e, ok := classifyFind(name, &data); ok {
				entries = append(entries, e)
			}
		}
		if err := windows.FindNextFile(h, &data); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return entries, nil
			}
			return entries, &pathError{dir, err}
		}
	}
}

func classifyFind(name string, d *windows.Win32finddata) (localEntry, bool) {
	if d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		switch d.Reserved0 {
		case windows.IO_REPARSE_TAG_MOUNT_POINT, windows.IO_REPARSE_TAG_SYMLINK:
			return localEntry{}, false
		}
	}
	if d.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return localEntry{name: name, kind: localDir}, true
	}
	return localEntry{name: name, kind: localFile, size: int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow)}, true
}

type pathError struct {
	path string
	err  error
}

func (e *pathError) Error() string { return e.err.Error() }
func (e *pathError) Unwrap() error { return e.err }

// longPath adds the \\?\ prefix so paths longer than 260 characters work.
func longPath(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\`):
		return p
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

func deviceOf(string) uint64 { return 0 }

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
