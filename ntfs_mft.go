package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"unicode/utf16"
)

// A direct NTFS scan reads the volume's master file table (MFT) in large sequential reads
// instead of opening every folder. Every file and folder has a record there with its
// names, parent folder and size, so one pass over the table gives the whole tree. It works
// for any io.ReaderAt holding an NTFS volume, which lets the tests use image files.

const (
	ntfsRootRecord = 5 // the root folder's record number

	ntfsAttrStandardInfo = 0x10
	ntfsAttrFileName     = 0x30
	ntfsAttrData         = 0x80
	ntfsAttrReparse      = 0xC0
	ntfsAttrEnd          = 0xFFFFFFFF

	ntfsRecordInUse     = 0x0001
	ntfsRecordDirectory = 0x0002

	ntfsAttributeReparsePoint = 0x400
	ntfsNamespaceDOS          = 2

	ntfsTagMountPoint = 0xA0000003 // junctions and volume mount points
	ntfsTagSymlink    = 0xA000000C

	ntfsChunkSize = 4 << 20
)

var errNotNTFS = errors.New("not an NTFS volume")

type ntfsRun struct {
	lcn      int64 // -1 for a sparse run
	clusters int64
}

type ntfsVolume struct {
	r           io.ReaderAt
	clusterSize int64
	recordSize  int
	mftRuns     []ntfsRun
	mftSize     int64
}

// mftEntry is what the scan keeps per record. Records are numbered by their position in
// the table.
type mftEntry struct {
	name   string
	size   int64
	parent uint32
	pseq   uint16 // the parent's expected sequence number
	seq    uint16
	flags  uint8
	links  uint8 // further names (hard links) not counted again, up to 255
}

const (
	mftUsed uint8 = 1 << iota
	mftDir
	mftLink // junction, mount point or symbolic link: not listed, as in the other scans
	mftNamed
	mftShortName // the name so far is only the 8.3 short name
)

// mftExtra is information found in an extension record, which belongs to its base record.
type mftExtra struct {
	base   uint32
	size   int64
	hasDat bool
	name   string
	short  bool // name is only an 8.3 short name
	parent uint32
	pseq   uint16
	link   bool
}

type mftIndex struct {
	entries []mftEntry
	damaged int
}

// openNTFS reads the boot sector and the MFT's own record to find where the table lies.
func openNTFS(r io.ReaderAt) (*ntfsVolume, error) {
	boot := make([]byte, 4096)
	if _, err := r.ReadAt(boot, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if string(boot[3:11]) != "NTFS    " {
		return nil, errNotNTFS
	}
	sector := int64(binary.LittleEndian.Uint16(boot[0x0B:]))
	perCluster := int64(boot[0x0D])
	if perCluster > 0x80 { // stored as a negative power of two on large-cluster volumes
		perCluster = 1 << (256 - perCluster)
	}
	v := &ntfsVolume{r: r, clusterSize: sector * perCluster}
	if sector < 256 || sector > 4096 || v.clusterSize < 512 || v.clusterSize > 2<<20 {
		return nil, fmt.Errorf("unsupported NTFS geometry: %d-byte sectors, %d-byte clusters", sector, v.clusterSize)
	}
	if n := int8(boot[0x40]); n < 0 {
		v.recordSize = 1 << -n
	} else {
		v.recordSize = int(n) * int(v.clusterSize)
	}
	if v.recordSize < 512 || v.recordSize > 64<<10 || v.recordSize%512 != 0 {
		return nil, fmt.Errorf("unsupported MFT record size %d", v.recordSize)
	}
	mftStart := int64(binary.LittleEndian.Uint64(boot[0x30:])) * v.clusterSize
	first := make([]byte, max(int64(v.recordSize), v.clusterSize, 4096))
	if _, err := r.ReadAt(first, mftStart); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read the MFT: %w", err)
	}
	record := first[:v.recordSize]
	if !applyFixups(record) {
		return nil, errors.New("the MFT's first record is damaged")
	}
	found := false
	eachAttribute(record, func(kind uint32, a []byte) bool {
		if kind != ntfsAttrData || a[8] == 0 || a[9] != 0 || binary.LittleEndian.Uint64(a[0x10:]) != 0 {
			return true
		}
		runs, err := decodeRuns(a[binary.LittleEndian.Uint16(a[0x20:]):])
		if err != nil {
			return true
		}
		var clusters int64
		for _, run := range runs {
			clusters += run.clusters
		}
		// A very fragmented MFT continues its run list in other records. That is rare and
		// not supported here; the caller falls back to listing folders.
		if lastVCN := int64(binary.LittleEndian.Uint64(a[0x18:])); lastVCN+1 != clusters {
			return true
		}
		v.mftRuns, v.mftSize, found = runs, int64(binary.LittleEndian.Uint64(a[0x30:])), true
		return false
	})
	if !found || v.mftSize < int64(v.recordSize)*16 {
		return nil, errors.New("could not find the complete MFT")
	}
	return v, nil
}

// applyFixups restores the last two bytes of each 512-byte block, which NTFS replaces with
// a check value so torn writes can be detected. It reports whether the record is intact.
func applyFixups(record []byte) bool {
	if len(record) < 48 || string(record[0:4]) != "FILE" {
		return false
	}
	offset, count := int(binary.LittleEndian.Uint16(record[4:])), int(binary.LittleEndian.Uint16(record[6:]))
	if count != len(record)/512+1 || offset+2*count > len(record) {
		return false
	}
	check := record[offset : offset+2]
	for i := 1; i < count; i++ {
		end := i*512 - 2
		if record[end] != check[0] || record[end+1] != check[1] {
			return false
		}
		copy(record[end:end+2], record[offset+2*i:offset+2*i+2])
	}
	return true
}

// eachAttribute calls f with each attribute's type and bytes until f returns false.
func eachAttribute(record []byte, f func(kind uint32, attribute []byte) bool) {
	offset := int(binary.LittleEndian.Uint16(record[0x14:]))
	used := min(int(binary.LittleEndian.Uint32(record[0x18:])), len(record))
	for offset+16 <= used {
		kind := binary.LittleEndian.Uint32(record[offset:])
		if kind == ntfsAttrEnd {
			return
		}
		length := int(binary.LittleEndian.Uint32(record[offset+4:]))
		if length < 16 || offset+length > used {
			return
		}
		a := record[offset : offset+length]
		if a[8] == 0 && length < 24 || a[8] != 0 && length < 0x40 {
			return
		}
		if !f(kind, a) {
			return
		}
		offset += length
	}
}

// residentValue returns a resident attribute's value, or nil if it does not fit.
func residentValue(a []byte) []byte {
	if a[8] != 0 {
		return nil
	}
	size, offset := int(binary.LittleEndian.Uint32(a[0x10:])), int(binary.LittleEndian.Uint16(a[0x14:]))
	if offset+size > len(a) {
		return nil
	}
	return a[offset : offset+size]
}

// decodeRuns decodes a non-resident attribute's run list: pairs of length and cluster
// offset, each offset relative to the previous run.
func decodeRuns(b []byte) ([]ntfsRun, error) {
	var runs []ntfsRun
	var lcn int64
	for i := 0; i < len(b) && b[i] != 0; {
		lengthSize, offsetSize := int(b[i]&0x0F), int(b[i]>>4)
		i++
		if lengthSize == 0 || lengthSize > 8 || offsetSize > 8 || i+lengthSize+offsetSize > len(b) {
			return nil, errors.New("damaged run list")
		}
		var length int64
		for j := lengthSize - 1; j >= 0; j-- {
			length = length<<8 | int64(b[i+j])
		}
		i += lengthSize
		if offsetSize == 0 {
			runs = append(runs, ntfsRun{lcn: -1, clusters: length})
			continue
		}
		delta := int64(int8(b[i+offsetSize-1])) // sign-extend the top byte
		for j := offsetSize - 2; j >= 0; j-- {
			delta = delta<<8 | int64(b[i+j])
		}
		i += offsetSize
		lcn += delta
		if length <= 0 || lcn < 0 {
			return nil, errors.New("damaged run list")
		}
		runs = append(runs, ntfsRun{lcn: lcn, clusters: length})
	}
	return runs, nil
}

type mftChunk struct {
	first int // record number of the first record
	data  []byte
}

// read parses the whole MFT. progress is called with the number of records parsed and the
// running totals of files and bytes.
func (v *ntfsVolume) read(ctx context.Context, progress func(records, files, bytes int64)) (*mftIndex, error) {
	total := int(v.mftSize / int64(v.recordSize))
	idx := &mftIndex{entries: make([]mftEntry, total)}
	chunkRecords := ntfsChunkSize / v.recordSize
	chunks := make(chan mftChunk, 4)
	// Raw volume reads must cover whole sectors, so each read is rounded up to a cluster.
	buffers := sync.Pool{New: func() any { return make([]byte, int64(chunkRecords*v.recordSize)+v.clusterSize) }}

	var readErr error
	go func() {
		defer close(chunks)
		record := 0
		for _, run := range v.mftRuns {
			runBytes := run.clusters * v.clusterSize
			for done := int64(0); done < runBytes && record < total; {
				if ctx.Err() != nil {
					readErr = ctx.Err()
					return
				}
				n := min(runBytes-done, int64(chunkRecords*v.recordSize), int64(total-record)*int64(v.recordSize))
				n -= n % int64(v.recordSize)
				if n <= 0 {
					break
				}
				readLen := min((n+v.clusterSize-1)/v.clusterSize*v.clusterSize, runBytes-done)
				buf := buffers.Get().([]byte)[:max(readLen, n)]
				if run.lcn < 0 {
					clear(buf) // sparse: no records here
				} else if _, err := v.r.ReadAt(buf[:readLen], run.lcn*v.clusterSize+done); err != nil && !errors.Is(err, io.EOF) {
					readErr = fmt.Errorf("read the MFT: %w", err)
					return
				}
				chunks <- mftChunk{first: record, data: buf[:n]}
				record += int(n) / v.recordSize
				done += n
			}
		}
	}()

	var parsed, files, bytes, damaged atomic.Int64
	var extraMu sync.Mutex
	var extras []mftExtra
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []mftExtra
			for c := range chunks {
				var f, b, d int64
				for i := 0; i*v.recordSize < len(c.data); i++ {
					number := c.first + i
					extra, kind := parseRecord(c.data[i*v.recordSize:(i+1)*v.recordSize], &idx.entries[number])
					switch kind {
					case recordDamaged:
						d++
					case recordExtension:
						local = append(local, extra)
					case recordFile:
						f++
						b += idx.entries[number].size
					}
				}
				buffers.Put(c.data[:cap(c.data)])
				files.Add(f)
				bytes.Add(b)
				damaged.Add(d)
				if p := parsed.Add(int64(len(c.data) / v.recordSize)); progress != nil {
					progress(p, files.Load(), bytes.Load())
				}
			}
			extraMu.Lock()
			extras = append(extras, local...)
			extraMu.Unlock()
		}()
	}
	wg.Wait()
	if readErr != nil {
		return nil, readErr
	}
	idx.applyExtras(extras)
	idx.damaged = int(damaged.Load())
	return idx, nil
}

// applyExtras merges what extension records hold into their base records. Large or heavily
// fragmented files and big folders keep some attributes in extension records.
func (idx *mftIndex) applyExtras(extras []mftExtra) {
	for _, x := range extras {
		if int(x.base) >= len(idx.entries) {
			continue
		}
		e := &idx.entries[x.base]
		if x.hasDat && e.flags&mftDir == 0 {
			e.size = x.size
		}
		// A long name in an extension record replaces a short name from the base record.
		if x.name != "" && (e.flags&mftNamed == 0 || e.flags&mftShortName != 0 && !x.short) {
			e.name, e.parent, e.pseq = x.name, x.parent, x.pseq
			e.flags = e.flags&^mftShortName | mftNamed
			if x.short {
				e.flags |= mftShortName
			}
		}
		if x.link {
			e.flags |= mftLink
		}
	}
}

type recordKind int

const (
	recordUnused recordKind = iota
	recordDamaged
	recordExtension
	recordFile
	recordDirectory
)

// parseRecord fills e from one MFT record, or returns what an extension record adds to
// its base record.
func parseRecord(record []byte, e *mftEntry) (mftExtra, recordKind) {
	if string(record[0:4]) != "FILE" {
		return mftExtra{}, recordUnused // never used, or cleared
	}
	if !applyFixups(record) {
		return mftExtra{}, recordDamaged
	}
	flags := binary.LittleEndian.Uint16(record[0x16:])
	if flags&ntfsRecordInUse == 0 {
		return mftExtra{}, recordUnused
	}
	base := binary.LittleEndian.Uint64(record[0x20:]) & 0xFFFFFFFFFFFF
	var x mftExtra
	var name string
	var parent uint64
	nameSpace := -1
	longNames := 0
	var size int64
	hasData := false
	// The reparse tag comes from the $REPARSE_POINT attribute when it is resident, which it
	// is for links; otherwise from the copy kept with the file name.
	var siReparse, rpFound bool
	var rpTag, fnTag uint32
	eachAttribute(record, func(kind uint32, a []byte) bool {
		switch kind {
		case ntfsAttrStandardInfo:
			if v := residentValue(a); len(v) >= 0x24 {
				siReparse = binary.LittleEndian.Uint32(v[0x20:])&ntfsAttributeReparsePoint != 0
			}
		case ntfsAttrFileName:
			v := residentValue(a)
			if len(v) < 0x42 {
				return true
			}
			length, ns := int(v[0x40]), int(v[0x41])
			if 0x42+2*length > len(v) {
				return true
			}
			if ns != ntfsNamespaceDOS {
				longNames++
			}
			// Skip short 8.3 names when a long name exists; keep the first long name, so a
			// file with several hard links is counted once.
			if ns == ntfsNamespaceDOS && nameSpace >= 0 || nameSpace >= 0 && nameSpace != ntfsNamespaceDOS {
				return true
			}
			units := make([]uint16, length)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(v[0x42+2*i:])
			}
			name, parent, nameSpace = string(utf16.Decode(units)), binary.LittleEndian.Uint64(v[0:]), ns
			if binary.LittleEndian.Uint32(v[0x38:])&ntfsAttributeReparsePoint != 0 {
				fnTag = binary.LittleEndian.Uint32(v[0x3C:])
			}
		case ntfsAttrData:
			if a[9] != 0 { // named streams are not counted, as in Explorer
				return true
			}
			if a[8] == 0 {
				size, hasData = int64(binary.LittleEndian.Uint32(a[0x10:])), true
			} else if binary.LittleEndian.Uint64(a[0x10:]) == 0 {
				size, hasData = int64(binary.LittleEndian.Uint64(a[0x30:])), true
			}
		case ntfsAttrReparse:
			if v := residentValue(a); len(v) >= 4 {
				rpTag, rpFound = binary.LittleEndian.Uint32(v), true
			}
		}
		return true
	})
	link := rpFound && isLinkTag(rpTag) || !rpFound && (siReparse || fnTag != 0) && isLinkTag(fnTag)
	if base != 0 {
		x = mftExtra{base: uint32(base), size: size, hasDat: hasData, link: link}
		if nameSpace >= 0 {
			x.name, x.parent, x.pseq, x.short = name, uint32(parent&0xFFFFFFFFFFFF), uint16(parent>>48), nameSpace == ntfsNamespaceDOS
		}
		return x, recordExtension
	}
	*e = mftEntry{size: size, seq: binary.LittleEndian.Uint16(record[0x10:]), flags: mftUsed, links: uint8(min(255, max(0, longNames-1)))}
	if nameSpace >= 0 {
		e.name, e.parent, e.pseq = name, uint32(parent&0xFFFFFFFFFFFF), uint16(parent>>48)
		e.flags |= mftNamed
		if nameSpace == ntfsNamespaceDOS {
			e.flags |= mftShortName
		}
	}
	if link {
		e.flags |= mftLink
	}
	if flags&ntfsRecordDirectory != 0 {
		e.flags |= mftDir
		e.size = 0
		return x, recordDirectory
	}
	return x, recordFile
}

func isLinkTag(tag uint32) bool { return tag == ntfsTagMountPoint || tag == ntfsTagSymlink }

// addToTree adds everything beneath the record root to the tree under top and returns how
// many files it added. The NTFS system files, records 0 to 15 other than the root folder,
// are left out because folder listings do not show them either.
func (idx *mftIndex) addToTree(t *Tree, top *Node, root uint32) (files int) {
	const (
		unknown uint8 = iota
		inside
		outside
	)
	state := make([]uint8, len(idx.entries))
	nodes := make(map[uint32]*Node)
	state[root], nodes[root] = inside, top
	var chain []uint32
	// resolve returns the tree node for directory record r, creating it and any missing
	// parents, or nil if r is not beneath root.
	resolve := func(r uint32) *Node {
		chain = chain[:0]
		for {
			if int(r) >= len(state) {
				break
			}
			if state[r] == inside {
				break
			}
			e := &idx.entries[r]
			if state[r] == outside || r < 16 || e.flags&(mftUsed|mftDir|mftNamed) != mftUsed|mftDir|mftNamed ||
				e.flags&mftLink != 0 || len(chain) > 4096 {
				break
			}
			chain = append(chain, r)
			parent := e.parent
			if int(parent) >= len(idx.entries) || idx.entries[parent].seq != e.pseq || parent == r {
				r = uint32(len(state)) // the parent was deleted and its record reused
				continue
			}
			r = parent
		}
		var node *Node
		if int(r) < len(state) && state[r] == inside {
			node = nodes[r]
		}
		for i := len(chain) - 1; i >= 0; i-- {
			c := chain[i]
			if node == nil {
				state[c] = outside
				continue
			}
			node = t.addChild(node, idx.entries[c].name, true, 0)
			state[c], nodes[c] = inside, node
		}
		return node
	}
	for r := range idx.entries {
		e := &idx.entries[r]
		if r < 16 || e.flags&(mftUsed|mftNamed) != mftUsed|mftNamed || e.flags&mftLink != 0 {
			continue
		}
		if e.flags&mftDir != 0 {
			resolve(uint32(r))
			continue
		}
		parent := e.parent
		if int(parent) >= len(idx.entries) || idx.entries[parent].seq != e.pseq {
			continue
		}
		if node := resolve(parent); node != nil {
			t.addChild(node, e.name, false, e.size)
			files++
		}
	}
	return files
}
