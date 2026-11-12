package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// errNeedsAdmin means the volume is NTFS but Windows only lets administrators read it
// directly.
var errNeedsAdmin = errors.New("reading an NTFS drive directly needs administrator rights")

// openNTFSVolume opens the NTFS volume whose root folder is root, such as C:\, for direct
// reading. Folders inside a drive are listed normally instead: reading the whole table to
// scan one folder would be slower.
func openNTFSVolume(root string) (io.ReaderAt, func(), error) {
	root = filepath.Clean(root)
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, nil, err
	}
	buf := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return nil, nil, err
	}
	if !strings.EqualFold(windows.UTF16ToString(buf), root) {
		return nil, nil, errNotNTFS // a folder, not the top of a drive
	}
	fsName := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeInformation(p, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err != nil {
		return nil, nil, err
	}
	if windows.UTF16ToString(fsName) != "NTFS" {
		return nil, nil, errNotNTFS
	}
	// \\?\Volume{...}\ also works for drives mounted in folders; drop the final backslash
	// to open the volume rather than its root folder.
	device := `\\.\` + strings.TrimSuffix(root, `\`)
	if len(root) != 3 || root[1] != ':' {
		name := make([]uint16, windows.MAX_PATH+1)
		if err := windows.GetVolumeNameForVolumeMountPoint(p, &name[0], uint32(len(name))); err != nil {
			return nil, nil, err
		}
		device = strings.TrimSuffix(windows.UTF16ToString(name), `\`)
	}
	d, err := windows.UTF16PtrFromString(device)
	if err != nil {
		return nil, nil, err
	}
	h, err := windows.CreateFile(d, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, nil, errNeedsAdmin
		}
		return nil, nil, err
	}
	f := os.NewFile(uintptr(h), device)
	return f, func() { f.Close() }, nil
}
