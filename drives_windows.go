package main

import "golang.org/x/sys/windows"

// listDrives returns the drives that are ready to scan. Empty card readers and optical
// drives without a disc are left out.
func listDrives() []Drive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	drives := []Drive{}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		path := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(path)
		var kind string
		switch windows.GetDriveType(p) {
		case windows.DRIVE_FIXED:
			kind = "fixed"
		case windows.DRIVE_REMOVABLE:
			kind = "removable"
		case windows.DRIVE_REMOTE:
			kind = "network"
		case windows.DRIVE_CDROM:
			kind = "optical"
		case windows.DRIVE_RAMDISK:
			kind = "ram"
		default:
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil {
			continue
		}
		label := make([]uint16, windows.MAX_PATH+1)
		fsName := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(p, &label[0], uint32(len(label)), nil, nil, nil, &fsName[0], uint32(len(fsName)))
		drives = append(drives, Drive{Path: path, Label: windows.UTF16ToString(label), FileSystem: windows.UTF16ToString(fsName),
			Kind: kind, Total: int64(total), Free: int64(totalFree)})
	}
	return drives
}
