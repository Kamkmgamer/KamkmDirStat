package driveindex

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/Kamkmgamer/KamkmDirStat/internal/drives"
	"github.com/Kamkmgamer/KamkmDirStat/internal/fslist"
	"github.com/Kamkmgamer/KamkmDirStat/internal/ntfs"
	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
	"golang.org/x/sys/windows"
)

// The change journal is a log NTFS keeps of every file created, renamed, changed or
// deleted on a drive. An index read from the master file table is kept current by reading
// the journal from the point the table was read at.
const (
	fsctlQueryUsnJournal  = 0x000900f4
	fsctlReadUsnJournal   = 0x000900bb
	fsctlCreateUsnJournal = 0x000900e7

	usnDataOverwrite  = 0x00000001
	usnDataExtend     = 0x00000002
	usnDataTruncation = 0x00000004
	usnFileCreate     = 0x00000100
	usnFileDelete     = 0x00000200
	usnRenameOldName  = 0x00001000
	usnRenameNewName  = 0x00002000
	usnHardLinkChange = 0x00010000
	usnReparseChange  = 0x00100000
)

type usnJournalData struct {
	ID              uint64
	FirstUsn        int64
	NextUsn         int64
	LowestValidUsn  int64
	MaxUsn          int64
	MaximumSize     uint64
	AllocationDelta uint64
}

type readUsnJournalData struct {
	StartUsn          int64
	ReasonMask        uint32
	ReturnOnlyOnClose uint32
	Timeout           uint64
	BytesToWaitFor    uint64
	UsnJournalID      uint64
}

type usnRecord struct {
	ref, parent   uint32 // MFT record numbers
	reason, attrs uint32
	name          string
}

var (
	// errUseSnapshot means the drive cannot be read directly here, so its folders are read.
	errUseSnapshot = errors.New("folders are read instead")
	errJournalGone = errors.New("the change journal no longer holds every change since the index was made")
)

const (
	driveIdle        = 5 * time.Minute  // the app drops indexes from memory this long after the last search
	driveListEvery   = 30 * time.Second // how often drives that come and go are looked for
	driveSaveEvery   = 10 * time.Minute // how often a changing live index is saved
	journalPollEvery = time.Second
)

// snapshotStale is how old a snapshot gets before its folders are read again. The service
// reads them more often, because it does so in the background with nobody waiting.
func snapshotStale(service bool) time.Duration {
	if service {
		return 6 * time.Hour
	}
	return 24 * time.Hour
}

var driveIndex = &driveIndexer{}

type driveIndexer struct {
	service bool
	dir     string // where indexes are saved; "" for the default

	mu       sync.Mutex
	stores   map[string]*driveStore // by volume
	listed   time.Time
	used     time.Time
	watching bool
}

type driveStore struct {
	ix      *driveIndexer
	root    string // C:\
	label   string
	key     string
	cancel  context.CancelFunc
	done    chan struct{} // closed when the store has stopped and saved
	loaded  chan struct{} // closed once a saved index has been read, or there was none
	refresh chan struct{}
	read    atomic.Int64 // entries read so far while building

	mu       sync.Mutex
	idx      *search.Index
	rootSlot int
	mode     string
	state    string
	err      string
	built    time.Time // when the folders were last read completely
	synced   time.Time // when the change journal was last read to the end
	journal  uint64
	next     int64
	dirty    bool // changed since it was saved
	partial  bool // still being built for the first time
}

type driveFileHeader struct {
	Root     string `json:"root"`
	Mode     string `json:"mode"`
	RootSlot int    `json:"rootSlot"`
	Built    int64  `json:"built"`
	Synced   int64  `json:"synced,omitempty"`
	Journal  uint64 `json:"journal,omitempty"`
	Next     int64  `json:"next,omitempty"`
}

const driveFileMagic = "KDSDRIVE1\n"

func (ix *driveIndexer) folder() (string, error) {
	dir := ix.dir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "KamkmDirStat", "drives")
	}
	return dir, os.MkdirAll(dir, 0o700)
}

// active starts indexing every drive that is not indexed yet and returns them all.
func (ix *driveIndexer) active() []*driveStore {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.used = time.Now()
	if !ix.watching {
		ix.watching = true
		go ix.watch()
	}
	if ix.stores == nil || time.Since(ix.listed) >= driveListEvery {
		ix.syncDrives()
	}
	stores := make([]*driveStore, 0, len(ix.stores))
	for _, s := range ix.stores {
		stores = append(stores, s)
	}
	slices.SortFunc(stores, func(a, b *driveStore) int { return cmp.Compare(a.root, b.root) })
	return stores
}

// indexable reports whether a drive gets an index: drives in this computer and ones
// plugged into it, but not network or optical drives.
func indexable(d drives.Drive) bool { return d.Kind == "fixed" || d.Kind == "removable" }

// volumeKey names a drive's saved index. It stays the same when the drive letter changes.
func volumeKey(root string) string {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	var serial uint32
	fs := make([]uint16, 64)
	if windows.GetVolumeInformation(p, nil, 0, &serial, nil, nil, &fs[0], uint32(len(fs))) != nil || serial == 0 {
		return ""
	}
	return fmt.Sprintf("%08X-%s", serial, windows.UTF16ToString(fs))
}

// syncDrives starts stores for new drives and stops those of drives that went away. ix.mu
// is held.
func (ix *driveIndexer) syncDrives() {
	ix.listed = time.Now()
	if ix.stores == nil {
		ix.stores = map[string]*driveStore{}
	}
	seen := map[string]bool{}
	for _, d := range drives.List() {
		if !indexable(d) {
			continue
		}
		key := volumeKey(d.Path)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		if s := ix.stores[key]; s != nil {
			if s.root == d.Path {
				continue
			}
			s.cancel() // the drive letter changed
		}
		ctx, cancel := context.WithCancel(context.Background())
		s := &driveStore{ix: ix, root: d.Path, label: d.Label, key: key, cancel: cancel, done: make(chan struct{}),
			loaded: make(chan struct{}), refresh: make(chan struct{}, 1), state: "loading"}
		ix.stores[key] = s
		go s.run(ctx)
	}
	for key, s := range ix.stores {
		if !seen[key] {
			s.cancel()
			delete(ix.stores, key)
		}
	}
}

// watch looks for drives coming and going and, in the app, drops the indexes from memory
// once nobody has searched for a while.
func (ix *driveIndexer) watch() {
	for {
		time.Sleep(driveListEvery)
		ix.mu.Lock()
		if !ix.service && time.Since(ix.used) > driveIdle {
			ix.watching = false
			stores := ix.stores
			ix.stores = nil
			ix.mu.Unlock()
			for _, s := range stores {
				s.cancel()
			}
			return
		}
		ix.syncDrives()
		ix.mu.Unlock()
	}
}

// stop saves and drops every index, waiting up to timeout for them to be saved.
func (ix *driveIndexer) stop(timeout time.Duration) {
	ix.mu.Lock()
	stores := ix.stores
	ix.stores = nil
	ix.mu.Unlock()
	deadline := time.After(timeout)
	for _, s := range stores {
		s.cancel()
	}
	for _, s := range stores {
		select {
		case <-s.done:
		case <-deadline:
			return
		}
	}
}

// loaded reports whether any index is in memory.
func (ix *driveIndexer) loaded() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.stores) > 0
}

func (ix *driveIndexer) states() []DriveState {
	out := []DriveState{}
	for _, s := range ix.active() {
		out = append(out, s.status())
	}
	return out
}

func (ix *driveIndexer) refreshDrive(root string) error {
	for _, s := range ix.active() {
		if root == "" || strings.EqualFold(s.root, root) {
			select {
			case s.refresh <- struct{}{}:
			default:
			}
			if root != "" {
				return nil
			}
		}
	}
	if root != "" {
		return fmt.Errorf("%s is not indexed.", root)
	}
	return nil
}

// search searches every drive whose index is ready or being built. It waits briefly for
// saved indexes to be read back into memory.
func (ix *driveIndexer) search(ctx context.Context, q search.Query) (search.Result, error) {
	stores := ix.active()
	wait := time.After(15 * time.Second)
	for _, s := range stores {
		select {
		case <-s.loaded:
		case <-ctx.Done():
			return search.Result{}, ctx.Err()
		case <-wait:
		}
	}
	var sources []search.Source
	for _, s := range stores {
		s.mu.Lock()
		if s.idx != nil {
			sources = append(sources, search.Source{Index: s.idx, Join: joinDrivePath})
		}
		s.mu.Unlock()
	}
	return search.SearchAll(ctx, q, sources)
}

func joinDrivePath(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return filepath.Join(names...)
}

func (s *driveStore) status() DriveState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := DriveState{Root: s.root, Label: s.label, Mode: s.mode, State: s.state, Error: s.err}
	if s.idx != nil {
		st.Entries = s.idx.Len()
	}
	if s.state == "building" {
		st.Read = s.read.Load()
	}
	if !s.built.IsZero() {
		st.Built = s.built.UnixMilli()
	}
	if s.mode == "live" && !s.synced.IsZero() {
		st.Built = s.synced.UnixMilli()
	}
	return st
}

func (s *driveStore) set(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
}

func (s *driveStore) run(ctx context.Context) {
	defer close(s.done)
	s.load()
	for ctx.Err() == nil {
		err := s.runLive(ctx)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, errUseSnapshot) {
			s.runSnapshot(ctx)
			break
		}
		if err != nil {
			s.set(func() { s.state, s.err = "error", err.Error() })
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			case <-s.refresh:
			}
		}
	}
	s.save()
}

func (s *driveStore) file() (string, error) {
	dir, err := s.ix.folder()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, s.key+".idx"), nil
}

// load reads the index saved by an earlier run, if there is one.
func (s *driveStore) load() {
	defer close(s.loaded)
	p, err := s.file()
	if err != nil {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	magic := make([]byte, len(driveFileMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != driveFileMagic {
		return
	}
	line, err := r.ReadSlice('\n')
	var h driveFileHeader
	if err != nil || json.Unmarshal(line, &h) != nil {
		return
	}
	idx, err := search.ReadIndex(r)
	if err != nil || !idx.Live() {
		return
	}
	ok := false
	idx.View(func(u search.Updater) {
		_, parent, _, dir, found := u.Entry(h.RootSlot)
		ok = found && dir && parent == -1
	})
	if !ok {
		return
	}
	if h.Root != s.root { // the drive letter changed
		idx.Update(func(u search.Updater) { u.Set(h.RootSlot, s.root, -1, 0, true) })
	}
	s.set(func() {
		s.idx, s.rootSlot, s.mode, s.state = idx, h.RootSlot, h.Mode, "ready"
		s.built, s.journal, s.next = time.UnixMilli(h.Built), h.Journal, h.Next
		if h.Synced != 0 {
			s.synced = time.UnixMilli(h.Synced)
		}
	})
}

// save writes the index so the next run starts from it.
func (s *driveStore) save() {
	s.mu.Lock()
	idx := s.idx
	if idx == nil || !s.dirty || s.partial {
		s.mu.Unlock()
		return
	}
	h := driveFileHeader{Root: s.root, Mode: s.mode, RootSlot: s.rootSlot, Built: s.built.UnixMilli(), Journal: s.journal, Next: s.next}
	if !s.synced.IsZero() {
		h.Synced = s.synced.UnixMilli()
	}
	s.dirty = false
	s.mu.Unlock()
	if err := s.writeFile(idx, h); err != nil {
		s.set(func() { s.dirty = true })
	}
}

func (s *driveStore) writeFile(idx *search.Index, h driveFileHeader) error {
	p, err := s.file()
	if err != nil {
		return err
	}
	head, _ := json.Marshal(h)
	tmp := p + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	w.WriteString(driveFileMagic)
	w.Write(head)
	w.WriteByte('\n')
	err = idx.Save(w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// runLive keeps a live index of an NTFS drive. It returns errUseSnapshot when the drive
// cannot be read directly, and nil when the index has to be made again. The drive is
// opened only while it is read, so a USB drive can still be safely removed.
func (s *driveStore) runLive(ctx context.Context) error {
	r, h, closeVolume, err := openJournal(s.root)
	if err != nil {
		return err
	}
	jd, err := queryJournal(h)
	if err != nil {
		closeVolume()
		return fmt.Errorf("%w: %v", errUseSnapshot, err)
	}
	s.mu.Lock()
	resume := s.idx != nil && s.mode == "live" && s.journal == jd.ID && s.next >= jd.LowestValidUsn && s.next <= jd.NextUsn
	s.mu.Unlock()
	if !resume {
		// Changes made while the table is read are in the journal from this point on, and
		// applying them again to entries read after they happened changes nothing.
		next := jd.NextUsn
		s.read.Store(0)
		s.set(func() { s.state, s.err = "building", "" })
		vol, err := ntfs.Open(r)
		if err != nil {
			closeVolume()
			return fmt.Errorf("%w: %v", errUseSnapshot, err)
		}
		m, err := vol.Read(ctx, func(records, files, bytes int64) { s.read.Store(records) })
		if err != nil {
			closeVolume()
			return err
		}
		idx := mftToIndex(m, s.root)
		s.set(func() {
			s.idx, s.rootSlot, s.mode, s.journal, s.next = idx, ntfs.RootRecord, "live", jd.ID, next
			s.built, s.dirty, s.partial = time.Now(), true, false
		})
	}
	buf := make([]byte, 1<<16)
	saved := time.Now()
	for {
		if h == 0 {
			if _, h, closeVolume, err = openJournal(s.root); err != nil {
				return err
			}
		}
		err := s.follow(ctx, h, buf)
		closeVolume()
		h = 0
		if errors.Is(err, errJournalGone) {
			s.set(func() { s.journal = 0 })
			return nil
		}
		if err != nil {
			return err
		}
		s.set(func() { s.state, s.err, s.synced = "ready", "", time.Now() })
		if time.Since(saved) > driveSaveEvery {
			s.save()
			saved = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(journalPollEvery):
		case <-s.refresh: // a live index is always current
		}
	}
}

// openJournal opens a drive for reading its file table and change journal, turning the
// journal on if it is off.
func openJournal(root string) (io.ReaderAt, windows.Handle, func(), error) {
	r, closeVolume, err := ntfs.OpenVolume(root)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("%w: %v", errUseSnapshot, err)
	}
	file, ok := r.(*os.File)
	if !ok {
		closeVolume()
		return nil, 0, nil, errUseSnapshot
	}
	h := windows.Handle(file.Fd())
	_, err = queryJournal(h)
	if errors.Is(err, windows.ERROR_JOURNAL_NOT_ACTIVE) || errors.Is(err, windows.ERROR_JOURNAL_DELETE_IN_PROGRESS) {
		if err = createJournal(root); err == nil {
			_, err = queryJournal(h)
		}
	}
	if err != nil {
		closeVolume()
		return nil, 0, nil, fmt.Errorf("%w: the change journal is not available: %v", errUseSnapshot, err)
	}
	return r, h, closeVolume, nil
}

func queryJournal(h windows.Handle) (usnJournalData, error) {
	var d usnJournalData
	var n uint32
	err := windows.DeviceIoControl(h, fsctlQueryUsnJournal, nil, 0, (*byte)(unsafe.Pointer(&d)), uint32(unsafe.Sizeof(d)), &n, nil)
	return d, err
}

// createJournal turns the change journal on, with the size Windows uses for system drives.
func createJournal(root string) error {
	p, err := windows.UTF16PtrFromString(`\\.\` + strings.TrimSuffix(root, `\`))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	in := struct{ MaximumSize, AllocationDelta uint64 }{32 << 20, 8 << 20}
	var n uint32
	return windows.DeviceIoControl(h, fsctlCreateUsnJournal, (*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)), nil, 0, &n, nil)
}

// follow applies the journal's changes until it reaches the end.
func (s *driveStore) follow(ctx context.Context, h windows.Handle, buf []byte) error {
	for ctx.Err() == nil {
		s.mu.Lock()
		in := readUsnJournalData{StartUsn: s.next, ReasonMask: 0xFFFFFFFF, UsnJournalID: s.journal}
		s.mu.Unlock()
		var n uint32
		err := windows.DeviceIoControl(h, fsctlReadUsnJournal, (*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)), &buf[0], uint32(len(buf)), &n, nil)
		if errors.Is(err, windows.ERROR_JOURNAL_ENTRY_DELETED) || errors.Is(err, windows.ERROR_JOURNAL_DELETE_IN_PROGRESS) ||
			errors.Is(err, windows.ERROR_JOURNAL_NOT_ACTIVE) || errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return errJournalGone
		}
		if err != nil {
			return err
		}
		if n < 8 {
			return nil
		}
		next := int64(binary.LittleEndian.Uint64(buf))
		if records := parseUsnRecords(buf[8:n]); len(records) > 0 {
			s.apply(records)
		}
		s.set(func() {
			if next != s.next {
				s.next, s.dirty = next, true
			}
		})
		if n == 8 || next == in.StartUsn {
			return nil
		}
	}
	return ctx.Err()
}

func parseUsnRecords(b []byte) []usnRecord {
	var out []usnRecord
	for off := 0; off+8 <= len(b); {
		length := int(binary.LittleEndian.Uint32(b[off:]))
		if length < 8 || off+length > len(b) {
			break
		}
		rec := b[off : off+length]
		off += length
		var r usnRecord
		var nameLen, nameOff int
		switch binary.LittleEndian.Uint16(rec[4:]) {
		case 2:
			if len(rec) < 60 {
				continue
			}
			r.ref = uint32(binary.LittleEndian.Uint64(rec[8:]) & 0xFFFFFFFFFFFF)
			r.parent = uint32(binary.LittleEndian.Uint64(rec[16:]) & 0xFFFFFFFFFFFF)
			r.reason = binary.LittleEndian.Uint32(rec[40:])
			r.attrs = binary.LittleEndian.Uint32(rec[52:])
			nameLen, nameOff = int(binary.LittleEndian.Uint16(rec[56:])), int(binary.LittleEndian.Uint16(rec[58:]))
		case 3:
			if len(rec) < 76 {
				continue
			}
			r.ref = uint32(binary.LittleEndian.Uint64(rec[8:]) & 0xFFFFFFFFFFFF)
			r.parent = uint32(binary.LittleEndian.Uint64(rec[24:]) & 0xFFFFFFFFFFFF)
			r.reason = binary.LittleEndian.Uint32(rec[56:])
			r.attrs = binary.LittleEndian.Uint32(rec[68:])
			nameLen, nameOff = int(binary.LittleEndian.Uint16(rec[72:])), int(binary.LittleEndian.Uint16(rec[74:]))
		default:
			continue
		}
		if nameOff+nameLen > len(rec) || nameLen%2 != 0 {
			continue
		}
		units := make([]uint16, nameLen/2)
		for i := range units {
			units[i] = binary.LittleEndian.Uint16(rec[nameOff+2*i:])
		}
		r.name = string(utf16.Decode(units))
		out = append(out, r)
	}
	return out
}

// apply changes the index as the journal records say, then reads the sizes of files that
// were created or written, which the journal does not record.
func (s *driveStore) apply(records []usnRecord) {
	s.mu.Lock()
	idx := s.idx
	s.mu.Unlock()
	need := map[int]bool{} // slot -> whether it may have become a link
	changed := applyUsnRecords(idx, records, need)
	if changed {
		s.set(func() { s.dirty = true })
	}
	if len(need) == 0 {
		return
	}
	type job struct {
		slot  int
		path  string
		check bool
	}
	var jobs []job
	idx.View(func(u search.Updater) {
		for slot, check := range need {
			if names, ok := u.Path(slot); ok {
				jobs = append(jobs, job{slot, joinDrivePath(names), check})
			}
		}
	})
	type found struct {
		size       int64
		link, gone bool
	}
	results := make([]found, len(jobs))
	for i, j := range jobs {
		var d windows.Win32finddata
		p, err := windows.UTF16PtrFromString(fslist.LongPath(j.path))
		if err != nil {
			results[i].gone = true
			continue
		}
		fh, err := windows.FindFirstFile(p, &d)
		if err != nil {
			results[i].gone = true
			continue
		}
		windows.FindClose(fh)
		results[i].size = int64(d.FileSizeHigh)<<32 | int64(d.FileSizeLow)
		results[i].link = d.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 &&
			(d.Reserved0 == windows.IO_REPARSE_TAG_MOUNT_POINT || d.Reserved0 == windows.IO_REPARSE_TAG_SYMLINK)
	}
	idx.Update(func(u search.Updater) {
		for i, j := range jobs {
			switch r := results[i]; {
			case r.link:
				u.Remove(j.slot) // links are not listed, as in scans
			case !r.gone:
				if _, _, _, dir, ok := u.Entry(j.slot); ok && !dir {
					u.SetSize(j.slot, r.size)
				}
			}
		}
	})
}

// applyUsnRecords makes the changes that journal records describe. need collects the slots
// whose size, or whether they are links, has to be read from the drive.
func applyUsnRecords(idx *search.Index, records []usnRecord, need map[int]bool) (changed bool) {
	idx.Update(func(u search.Updater) {
		for _, r := range records {
			slot := int(r.ref)
			if slot < 16 || slot > 1<<31-2 {
				continue // NTFS's own files
			}
			if r.reason&usnFileDelete != 0 {
				u.Remove(slot)
				delete(need, slot)
				changed = true
				continue
			}
			_, _, size, dir, ok := u.Entry(slot)
			named := r.reason&(usnFileCreate|usnRenameNewName) != 0
			if ok && !named {
				if _, seen := need[slot]; !seen && !dir && r.reason&(usnDataOverwrite|usnDataExtend|usnDataTruncation) != 0 {
					need[slot] = false
				}
				if r.reason&usnReparseChange != 0 {
					need[slot] = true
				}
				continue
			}
			if r.reason&usnRenameOldName != 0 && !named {
				continue // the record with the new name follows
			}
			if ok && r.reason&usnHardLinkChange != 0 && r.reason&usnRenameNewName == 0 {
				continue // another name for a file that is listed already
			}
			parent := int(r.parent)
			if _, _, _, pdir, pok := u.Entry(parent); !pok || !pdir {
				u.Remove(slot) // moved somewhere the index does not cover
				delete(need, slot)
				changed = true
				continue
			}
			dir = r.attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0
			if !ok || dir {
				size = 0
			}
			u.Set(slot, r.name, parent, size, dir)
			changed = true
			if reparse := r.attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0; !dir || reparse {
				need[slot] = need[slot] || reparse
			}
		}
	})
	return changed
}

// mftToIndex makes a live index of a drive from its master file table, with each entry in
// the slot of its record number.
func mftToIndex(m *ntfs.Index, root string) *search.Index {
	const (
		unknown uint8 = iota
		inside
		outside
	)
	entries := m.Entries
	state := make([]uint8, len(entries))
	if ntfs.RootRecord < len(state) {
		state[ntfs.RootRecord] = inside
	}
	var chain []uint32
	// within reports whether directory record r is in a folder that leads to the root.
	within := func(r uint32) bool {
		chain = chain[:0]
		for int(r) < len(state) && state[r] == unknown {
			e := &entries[r]
			if r < 16 || e.Flags&(ntfs.Used|ntfs.Dir|ntfs.Named) != ntfs.Used|ntfs.Dir|ntfs.Named || e.Flags&ntfs.Link != 0 || len(chain) > 4096 {
				break
			}
			chain = append(chain, r)
			p := e.Parent
			if int(p) >= len(entries) || entries[p].Seq != e.ParentSeq || p == r {
				r = uint32(len(state))
				break
			}
			r = p
		}
		result := int(r) < len(state) && state[r] == inside
		mark := outside
		if result {
			mark = inside
		}
		for _, c := range chain {
			state[c] = mark
		}
		return result
	}
	x := search.NewLive()
	x.Update(func(u search.Updater) {
		u.Grow(len(entries))
		u.Set(ntfs.RootRecord, root, -1, 0, true)
		for r := range entries {
			e := &entries[r]
			if r < 16 || e.Flags&(ntfs.Used|ntfs.Named) != ntfs.Used|ntfs.Named || e.Flags&ntfs.Link != 0 {
				continue
			}
			p := e.Parent
			if int(p) >= len(entries) || entries[p].Seq != e.ParentSeq {
				continue
			}
			dir := e.Flags&ntfs.Dir != 0
			if dir && !within(uint32(r)) || !dir && !within(p) {
				continue
			}
			u.Set(r, e.Name, int(p), e.Size, dir)
		}
	})
	return x
}

// runSnapshot keeps an index made by reading the drive's folders, reading them again when
// the index gets old or when asked to.
func (s *driveStore) runSnapshot(ctx context.Context) {
	s.mu.Lock()
	if s.idx != nil && s.mode == "live" && !s.synced.IsZero() {
		// Saved by a run that could follow the journal: it was current when last synced.
		s.built = s.synced
	}
	s.mode = "snapshot"
	s.mu.Unlock()
	stale := snapshotStale(s.ix.service)
	force := false
	for {
		s.mu.Lock()
		due := s.idx == nil || force || time.Since(s.built) >= stale
		wait := stale - time.Since(s.built)
		s.mu.Unlock()
		if due {
			wait = stale
			if err := s.walk(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				s.set(func() { s.state, s.err = "error", err.Error() })
				wait = 10 * time.Minute
			}
		}
		force = false
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, time.Minute)):
		case <-s.refresh:
			force = true
		}
	}
}

// walk reads every folder on the drive into a new index. A drive's first index can be
// searched while it is being made; a new one replaces the old when it is complete.
func (s *driveStore) walk(ctx context.Context) error {
	idx := search.NewLive()
	s.read.Store(0)
	s.set(func() {
		s.state, s.err = "building", ""
		if s.idx == nil {
			s.idx, s.rootSlot, s.partial = idx, 0, true
		}
	})
	if err := walkDrive(ctx, s.root, idx, &s.read); err != nil {
		return err
	}
	s.set(func() {
		s.idx, s.rootSlot, s.mode, s.state = idx, 0, "snapshot", "ready"
		s.built, s.dirty, s.partial, s.journal, s.next = time.Now(), true, false, 0, 0
	})
	s.save()
	return nil
}

// walkDrive lists every folder beneath root into idx, with the root in slot 0. Folders are
// opened relative to their parent, as in local scans.
func walkDrive(ctx context.Context, root string, idx *search.Index, read *atomic.Int64) error {
	idx.Update(func(u search.Updater) { u.Set(0, root, -1, 0, true) })
	type job struct {
		parent *fslist.Parent
		name   string
		slot   int
	}
	var next atomic.Int64
	next.Store(1)
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	stack := []job{{name: fslist.NTRoot(root)}}
	active := 0
	rootErr := error(nil)
	visit := func(j job, buf []byte) []job {
		var parent windows.Handle
		if j.parent != nil {
			parent = j.parent.H
		}
		if ctx.Err() != nil {
			j.parent.Release()
			return nil
		}
		h, err := fslist.OpenRelative(parent, j.name)
		j.parent.Release()
		if err != nil {
			if j.parent == nil {
				rootErr = err
			}
			return nil
		}
		entries, _ := fslist.Read(h, buf)
		if len(entries) == 0 {
			windows.CloseHandle(h)
			return nil
		}
		base := int(next.Add(int64(len(entries)))) - len(entries)
		dirs := 0
		idx.Update(func(u search.Updater) {
			for i, e := range entries {
				u.Set(base+i, e.Name, j.slot, e.Size, e.Kind == fslist.Dir)
				if e.Kind == fslist.Dir {
					dirs++
				}
			}
		})
		read.Add(int64(len(entries)))
		if dirs == 0 {
			windows.CloseHandle(h)
			return nil
		}
		ph := fslist.Hold(h, dirs)
		jobs := make([]job, 0, dirs)
		for i, e := range entries {
			if e.Kind == fslist.Dir {
				jobs = append(jobs, job{parent: ph, name: e.Name, slot: base + i})
			}
		}
		return jobs
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := fslist.NewBuffer()
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
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				active++
				mu.Unlock()
				jobs := visit(j, buf)
				mu.Lock()
				stack = append(stack, jobs...)
				active--
				mu.Unlock()
				cond.Broadcast()
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if rootErr != nil {
		return fmt.Errorf("%s could not be read: %w", root, rootErr)
	}
	return nil
}

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// appIndexStatus is the status of the indexes this window keeps itself.
func appIndexStatus() Status {
	return Status{Supported: true, Admin: isElevated(), Source: "app", Drives: driveIndex.states()}
}

// Stop saves this window's indexes when it closes.
func Stop() { driveIndex.stop(10 * time.Second) }
