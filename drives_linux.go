package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var linuxDiskFileSystems = map[string]string{
	"ext2": "fixed", "ext3": "fixed", "ext4": "fixed", "xfs": "fixed", "btrfs": "fixed", "f2fs": "fixed", "zfs": "fixed",
	"jfs": "fixed", "reiserfs": "fixed", "bcachefs": "fixed", "vfat": "fixed", "exfat": "fixed", "ntfs": "fixed", "ntfs3": "fixed",
	"fuseblk": "fixed", "hfsplus": "fixed", "nfs": "network", "nfs4": "network", "cifs": "network", "smb3": "network",
	"fuse.sshfs": "network", "9p": "network",
}

// listDrives returns mounted disk file systems from /proc/self/mountinfo. Pseudo file
// systems, snap packages and repeated mounts of the same device are left out.
func listDrives() []Drive {
	drives := []Drive{}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return drives
	}
	defer f.Close()
	seen := map[string]int{} // device -> index in drives
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		// id parent major:minor root mountpoint options [optional...] - fstype source superoptions
		left, right, ok := strings.Cut(scanner.Text(), " - ")
		if !ok {
			continue
		}
		fields, tail := strings.Fields(left), strings.Fields(right)
		if len(fields) < 5 || len(tail) < 2 {
			continue
		}
		device, root, mount, fsType := fields[2], unescapeMount(fields[3]), unescapeMount(fields[4]), tail[0]
		kind, ok := linuxDiskFileSystems[fsType]
		if !ok || root != "/" || strings.HasPrefix(mount, "/snap/") || strings.HasPrefix(mount, "/var/lib/") ||
			strings.HasPrefix(mount, "/proc") || strings.HasPrefix(mount, "/sys") || strings.HasPrefix(mount, "/run/user/") {
			continue
		}
		switch {
		case kind == "fixed" && (strings.HasPrefix(mount, "/media/") || strings.HasPrefix(mount, "/run/media/")):
			kind = "removable"
		case mount == "/boot" || mount == "/boot/efi" || mount == "/efi":
			kind = "boot" // listed, but not selected at first
		}
		var st unix.Statfs_t
		if unix.Statfs(mount, &st) != nil || st.Blocks == 0 {
			continue
		}
		d := Drive{Path: mount, Label: driveLabel(mount), FileSystem: fsType, Kind: kind,
			Total: int64(st.Blocks) * int64(st.Bsize), Free: int64(st.Bfree) * int64(st.Bsize)}
		if i, dup := seen[device]; dup {
			if len(mount) < len(drives[i].Path) {
				drives[i] = d
			}
			continue
		}
		seen[device] = len(drives)
		drives = append(drives, d)
	}
	return drives
}

func driveLabel(mount string) string {
	switch mount {
	case "/":
		return "System"
	case "/home":
		return "Home"
	case "/boot":
		return "Boot"
	case "/boot/efi", "/efi":
		return "EFI"
	}
	return filepath.Base(mount)
}

// unescapeMount decodes the octal escapes mountinfo uses for spaces and other characters.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '3' {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
