package search

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

const indexMagic = "KDSIDX2\n"

var errDamaged = errors.New("The search index file is damaged.")

// Save writes the index so it can be dropped from memory and read back with ReadIndex. A
// live index may be changed while it is saved; the file holds it as it was at one moment.
func (x *Index) Save(dst io.Writer) error {
	x.mu.RLock()
	defer x.mu.RUnlock()
	w := bufio.NewWriterSize(dst, 1<<20)
	nameBytes := len(x.names) - x.unused
	var types []TypeStat
	if !x.live {
		types = x.types
	}
	live := 0
	if x.live {
		live = 1
	}
	var head []byte
	head = append(head, indexMagic...)
	for _, v := range []int{len(x.info), nameBytes, len(x.folder), len(types), live} {
		head = binary.AppendUvarint(head, uint64(v))
	}
	w.Write(head)
	// Names are written in entry order without the gaps left by changes.
	written := 0
	for i, info := range x.info {
		if info&infoGone == 0 {
			n, _ := w.Write(x.name(i))
			written += n
		}
	}
	if written != nameBytes {
		return errors.New("search index names do not add up")
	}
	buf := make([]byte, 0, 64<<10)
	flush := func() {
		w.Write(buf)
		buf = buf[:0]
	}
	for i, v := range x.nlen {
		if x.info[i]&infoGone != 0 {
			v = 0
		}
		buf = binary.LittleEndian.AppendUint16(buf, v)
		if len(buf) >= 60<<10 {
			flush()
		}
	}
	for _, list := range [][]int32{x.parent, x.id, x.folder} {
		for _, v := range list {
			buf = binary.LittleEndian.AppendUint32(buf, uint32(v))
			if len(buf) >= 60<<10 {
				flush()
			}
		}
	}
	for _, v := range x.size {
		buf = binary.LittleEndian.AppendUint64(buf, uint64(v))
		if len(buf) >= 60<<10 {
			flush()
		}
	}
	flush()
	w.Write(x.info)
	for _, t := range types {
		buf = binary.AppendUvarint(buf, uint64(len(t.Ext)))
		buf = append(buf, t.Ext...)
		buf = binary.AppendUvarint(buf, uint64(len(t.Kind)))
		buf = append(buf, t.Kind...)
		buf = binary.AppendUvarint(buf, uint64(t.Files))
		buf = binary.AppendVarint(buf, t.Size)
	}
	flush()
	return w.Flush()
}

// ReadIndex reads an index saved by Save.
func ReadIndex(src io.Reader) (*Index, error) {
	r := bufio.NewReaderSize(src, 1<<20)
	magic := make([]byte, len(indexMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != indexMagic {
		return nil, errDamaged
	}
	var counts [5]int
	for i := range counts {
		v, err := binary.ReadUvarint(r)
		if err != nil || v > 1<<32 {
			return nil, errDamaged
		}
		counts[i] = int(v)
	}
	n, nameBytes, folders, types, live := counts[0], counts[1], counts[2], counts[3], counts[4] == 1
	if n > 1<<31-1 || live && folders != 0 {
		return nil, errDamaged
	}
	x := &Index{live: live, names: make([]byte, nameBytes), info: make([]uint8, n)}
	if _, err := io.ReadFull(r, x.names); err != nil {
		return nil, errDamaged
	}
	buf := make([]byte, 64<<10)
	read := func(count, width int, put func(b []byte)) error {
		for count > 0 {
			k := min(count, len(buf)/width)
			b := buf[:k*width]
			if _, err := io.ReadFull(r, b); err != nil {
				return errDamaged
			}
			put(b)
			count -= k
		}
		return nil
	}
	x.nlen = make([]uint16, 0, n)
	x.start = make([]uint32, 0, n)
	offset := 0
	if err := read(n, 2, func(b []byte) {
		for i := 0; i < len(b); i += 2 {
			l := binary.LittleEndian.Uint16(b[i:])
			x.nlen = append(x.nlen, l)
			x.start = append(x.start, uint32(min(offset, 1<<32-1)))
			offset += int(l)
		}
	}); err != nil {
		return nil, err
	}
	if offset != nameBytes {
		return nil, errDamaged
	}
	readInt32 := func(count int) ([]int32, error) {
		out := make([]int32, 0, count)
		err := read(count, 4, func(b []byte) {
			for i := 0; i < len(b); i += 4 {
				out = append(out, int32(binary.LittleEndian.Uint32(b[i:])))
			}
		})
		return out, err
	}
	var err error
	if x.parent, err = readInt32(n); err != nil {
		return nil, err
	}
	if !live {
		if x.id, err = readInt32(n); err != nil {
			return nil, err
		}
	}
	if x.folder, err = readInt32(folders); err != nil {
		return nil, err
	}
	if live {
		x.folder = nil
	}
	x.size = make([]int64, 0, n)
	if err := read(n, 8, func(b []byte) {
		for i := 0; i < len(b); i += 8 {
			x.size = append(x.size, int64(binary.LittleEndian.Uint64(b[i:])))
		}
	}); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, x.info); err != nil {
		return nil, errDamaged
	}
	str := func() (string, error) {
		l, err := binary.ReadUvarint(r)
		if err != nil || l > 1<<16 {
			return "", errDamaged
		}
		b := make([]byte, l)
		if _, err := io.ReadFull(r, b); err != nil {
			return "", errDamaged
		}
		return string(b), nil
	}
	x.types = []TypeStat{}
	for range types {
		var t TypeStat
		if t.Ext, err = str(); err != nil {
			return nil, err
		}
		if t.Kind, err = str(); err != nil {
			return nil, err
		}
		files, err1 := binary.ReadUvarint(r)
		size, err2 := binary.ReadVarint(r)
		if err1 != nil || err2 != nil {
			return nil, errDamaged
		}
		t.Files, t.Size = int(files), size
		x.types = append(x.types, t)
	}
	// Ids must stay in range, so a damaged file cannot make a search panic.
	for _, e := range x.folder {
		if e < -1 || int(e) >= n {
			return nil, errDamaged
		}
	}
	ids := len(x.folder)
	if live {
		ids = n
		x.typesStale = true
	}
	for i := range n {
		if x.parent[i] < -1 || int(x.parent[i]) >= ids {
			return nil, errDamaged
		}
		if x.info[i]&infoGone == 0 {
			if live {
				x.used++
			}
		} else if x.nlen[i] != 0 {
			return nil, errDamaged
		}
	}
	return x, nil
}
