package fslist

import (
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procFindFirstFileExW = windows.NewLazySystemDLL("kernel32.dll").NewProc("FindFirstFileExW")

const (
	findExInfoBasic       = 1
	findExSearchNameMatch = 0
	findFirstExLargeFetch = 2
)

// List lists a folder with FindFirstFileEx, which returns sizes, attributes and
// reparse tags in large batches. It is several times faster than opening each folder as a
// file. Junctions, mounted volumes and symbolic links are skipped. Other reparse points,
// such as OneDrive placeholders and deduplicated files, count as ordinary files and folders.
func List(dir string, _ uint64) ([]Entry, error) {
	pattern, err := windows.UTF16PtrFromString(LongPath(filepath.Join(dir, "*")))
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
	var entries []Entry
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

func classifyFind(name string, d *windows.Win32finddata) (Entry, bool) {
	if d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		switch d.Reserved0 {
		case windows.IO_REPARSE_TAG_MOUNT_POINT, windows.IO_REPARSE_TAG_SYMLINK:
			return Entry{}, false
		}
	}
	if d.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		return Entry{Name: name, Kind: Dir}, true
	}
	return Entry{Name: name, Kind: File, Size: int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow)}, true
}

type pathError struct {
	path string
	err  error
}

func (e *pathError) Error() string { return e.err.Error() }
func (e *pathError) Unwrap() error { return e.err }

// LongPath adds the \\?\ prefix so paths longer than 260 characters work.
func LongPath(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\`):
		return p
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

// DeviceOf is not needed on Windows, where List recognises mounted volumes itself.
func DeviceOf(string) uint64 { return 0 }

// Opening each folder by its full path makes Windows look up every folder along the way.
// A walk that opens each folder relative to its parent's open handle instead, and reads its
// entries in large batches, takes roughly half the time. A folder is opened only when a
// worker takes it, so few handles are open at once: each parent stays open until all of its
// subfolders have been opened.

const bufferSize = 64 << 10

// NewBuffer makes a buffer for Read. Windows wants it aligned to 8 bytes, which a plain
// byte slice is not guaranteed to be.
func NewBuffer() []byte {
	words := make([]uint64, bufferSize/8)
	return unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), bufferSize)
}

// Parent is an open folder whose subfolders still have to be opened.
type Parent struct {
	H       windows.Handle
	pending atomic.Int32
}

// Hold keeps h open until Release has been called once for each of its subfolders.
func Hold(h windows.Handle, subfolders int) *Parent {
	p := &Parent{H: h}
	p.pending.Store(int32(subfolders))
	return p
}

// Release notes that one subfolder has been opened, or will not be. It does nothing for
// the nil parent of a walk's root.
func (p *Parent) Release() {
	if p != nil && p.pending.Add(-1) == 0 {
		windows.CloseHandle(p.H)
	}
}

// NTRoot is the name to give OpenRelative, with no parent, for the folder at path.
func NTRoot(path string) string { return `\??\` + strings.TrimPrefix(LongPath(path), `\\?\`) }

// OpenRelative opens the folder called name inside the open folder root, or the folder
// NTRoot names when root is 0.
func OpenRelative(root windows.Handle, name string) (windows.Handle, error) {
	us, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: root,
		ObjectName: us, Attributes: windows.OBJ_CASE_INSENSITIVE}
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.FILE_LIST_DIRECTORY|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	if st, ok := err.(windows.NTStatus); ok {
		err = st.Errno() // "Access is denied." rather than an NTSTATUS code
	}
	return h, err
}

// Read reads an open folder's entries in batches, using buf, which must come from
// NewBuffer. Junctions, mounted volumes and symbolic links are left out, as in List.
func Read(h windows.Handle, buf []byte) ([]Entry, error) {
	if len(buf) < bufferSize || uintptr(unsafe.Pointer(&buf[0]))%8 != 0 {
		return nil, errors.New("fslist.Read needs a buffer from NewBuffer")
	}
	var entries []Entry
	for {
		if err := windows.GetFileInformationByHandleEx(h, windows.FileFullDirectoryInfo, &buf[0], uint32(len(buf))); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return entries, nil
			}
			return entries, err
		}
		// FILE_FULL_DIR_INFO: next offset, then size at 40, attributes at 56, name length at
		// 60, the reparse tag (in the EA size field) at 64 and the name at 68.
		for offset := 0; offset+68 <= len(buf); {
			e := buf[offset:]
			next := binary.LittleEndian.Uint32(e)
			attrs := binary.LittleEndian.Uint32(e[56:])
			nameLen := int(binary.LittleEndian.Uint32(e[60:]) / 2)
			if 68+2*nameLen > len(e) {
				break
			}
			units := unsafe.Slice((*uint16)(unsafe.Pointer(&e[68])), nameLen)
			dot := nameLen == 1 && units[0] == '.' || nameLen == 2 && units[0] == '.' && units[1] == '.'
			tag := binary.LittleEndian.Uint32(e[64:])
			link := attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 && (tag == windows.IO_REPARSE_TAG_MOUNT_POINT || tag == windows.IO_REPARSE_TAG_SYMLINK)
			if !dot && !link {
				name := utf16Name(units)
				if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
					entries = append(entries, Entry{Name: name, Kind: Dir})
				} else {
					entries = append(entries, Entry{Name: name, Kind: File, Size: int64(binary.LittleEndian.Uint64(e[40:]))})
				}
			}
			if next == 0 {
				break
			}
			offset += int(next)
		}
	}
}

// utf16Name converts a file name, with a single allocation for the common all-ASCII case.
func utf16Name(units []uint16) string {
	for _, u := range units {
		if u >= 0x80 {
			return string(utf16.Decode(units))
		}
	}
	b := make([]byte, len(units))
	for i, u := range units {
		b[i] = byte(u)
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}
