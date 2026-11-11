package main

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// listDrives returns the Mac's data volume and other mounted disks. The read-only system
// volume at / is left out: user files live on the data volume.
func listDrives() []Drive {
	drives := []Drive{}
	n, err := unix.Getfsstat(nil, unix.MNT_NOWAIT)
	if err != nil || n <= 0 {
		return drives
	}
	list := make([]unix.Statfs_t, n)
	if n, err = unix.Getfsstat(list, unix.MNT_NOWAIT); err != nil {
		return drives
	}
	for _, st := range list[:n] {
		mount := unix.ByteSliceToString(st.Mntonname[:])
		fsType := unix.ByteSliceToString(st.Fstypename[:])
		kind := "fixed"
		switch fsType {
		case "apfs", "hfs", "exfat", "msdos", "ntfs":
			if strings.HasPrefix(mount, "/Volumes/") {
				kind = "removable"
			}
		case "smbfs", "afpfs", "nfs", "webdav":
			kind = "network"
		default:
			continue
		}
		label := filepath.Base(mount)
		switch {
		case mount == "/System/Volumes/Data":
			label = "Macintosh HD"
		case mount == "/" || strings.HasPrefix(mount, "/System/Volumes/") || strings.HasPrefix(mount, "/Library/Developer/"):
			continue // system, recovery, VM and simulator volumes
		}
		if st.Flags&unix.MNT_DONTBROWSE != 0 && mount != "/System/Volumes/Data" {
			continue
		}
		drives = append(drives, Drive{Path: mount, Label: label, FileSystem: fsType, Kind: kind,
			Total: int64(st.Blocks) * int64(st.Bsize), Free: int64(st.Bfree) * int64(st.Bsize)})
	}
	return drives
}
