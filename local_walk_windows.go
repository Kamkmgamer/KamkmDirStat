package main

import (
	"encoding/binary"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Opening each folder by its full path makes Windows look up every folder along the way.
// This walk opens each folder relative to its parent's open handle instead, and reads its
// entries in large batches, which roughly halves the time. A folder is opened only when a
// worker takes it, so few handles are open at once: each parent stays open until all of its
// subfolders have been opened.

const localWalkBuffer = 64 << 10

// parentHandle is an open folder whose subfolders still have to be opened.
type parentHandle struct {
	h       windows.Handle
	pending atomic.Int32
}

func (p *parentHandle) release() {
	if p != nil && p.pending.Add(-1) == 0 {
		windows.CloseHandle(p.h)
	}
}

type walkJob struct {
	parent *parentHandle // nil for the scan root
	name   string        // relative to parent, or the full NT path of the root
	path   string        // for messages
	node   *Node
}

// walk lists the folders beneath rootPath with a pool of workers that take the newest job
// first, which keeps the number of waiting folders small.
func (w *localWalk) walk(root *Node, rootPath string) {
	workers := min(16, max(8, runtime.NumCPU()))
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stack := []walkJob{{name: `\??\` + strings.TrimPrefix(longPath(rootPath), `\\?\`), path: rootPath, node: root}}
	active := 0
	push := func(jobs []walkJob) {
		mu.Lock()
		stack = append(stack, jobs...)
		mu.Unlock()
		cond.Broadcast()
	}
	buffers := sync.Pool{New: func() any { return make([]byte, localWalkBuffer) }}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				for len(stack) == 0 && active > 0 {
					cond.Wait()
				}
				if len(stack) == 0 {
					mu.Unlock()
					cond.Broadcast()
					return
				}
				job := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				active++
				mu.Unlock()
				w.visit(job, push, &buffers)
				mu.Lock()
				active--
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
}

func (w *localWalk) visit(job walkJob, push func([]walkJob), buffers *sync.Pool) {
	var root windows.Handle
	if job.parent != nil {
		root = job.parent.h
	}
	if w.ctx.Err() != nil {
		job.parent.release()
		return
	}
	h, err := openFolderRelative(root, job.name)
	job.parent.release()
	if err != nil {
		w.add(job.node, job.path, nil, err)
		return
	}
	w.current.Store(job.path)
	buf := buffers.Get().([]byte)
	entries, err := readFolder(h, buf)
	buffers.Put(buf)
	subs := w.add(job.node, job.path, entries, err)
	if len(subs) == 0 {
		windows.CloseHandle(h)
		return
	}
	parent := &parentHandle{h: h}
	parent.pending.Store(int32(len(subs)))
	jobs := make([]walkJob, 0, len(subs))
	i := 0
	for _, e := range entries {
		if e.kind == localDir {
			jobs = append(jobs, walkJob{parent: parent, name: e.name, path: job.path + `\` + e.name, node: subs[i]})
			i++
		}
	}
	if strings.HasSuffix(job.path, `\`) { // a drive root such as C:\
		for k := range jobs {
			jobs[k].path = job.path + jobs[k].name
		}
	}
	push(jobs)
}

func openFolderRelative(root windows.Handle, name string) (windows.Handle, error) {
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

// readFolder reads a folder's entries in batches. Junctions, mounted volumes and symbolic
// links are left out, as in listLocalDir.
func readFolder(h windows.Handle, buf []byte) ([]localEntry, error) {
	var entries []localEntry
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
					entries = append(entries, localEntry{name: name, kind: localDir})
				} else {
					entries = append(entries, localEntry{name: name, kind: localFile, size: int64(binary.LittleEndian.Uint64(e[40:]))})
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
