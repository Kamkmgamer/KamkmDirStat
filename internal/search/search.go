// Package search finds entries of a scan or a drive by name and type. Names are matched
// fuzzily, as fzf does: the letters of the query must appear in order, and matches that
// are consecutive or start words rank first. The app uses it for scans of this computer,
// and the native server scanner uses it for scans whose results stay on the server.
//
// An Index stores every entry in a few flat arrays, about 23 bytes plus the name each,
// instead of a tree of nodes, and is searched on every core.
package search

import (
	"bytes"
	"cmp"
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// Kind is a broad file type, decided by the extension.
type Kind uint8

const (
	Other Kind = iota
	Image
	Video
	Audio
	Document
	Archive
)

var kindNames = [...]string{"other", "images", "videos", "audio", "documents", "archives"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "other"
}

var kinds = map[string]Kind{}

func init() {
	// .ts is left out of videos: on most computers it is TypeScript.
	for k, list := range map[Kind]string{
		Image:    "jpg jpeg jpe jfif png gif bmp webp tif tiff heic heif avif svg ico psd jxl raw cr2 cr3 nef arw dng orf rw2 raf",
		Video:    "mp4 m4v mkv mov avi wmv flv webm mpg mpeg m2ts mts 3gp vob ogv divx",
		Audio:    "mp3 wav flac aac m4a ogg oga opus wma aif aiff alac mid midi ape",
		Document: "pdf doc docx docm odt rtf txt md xls xlsx xlsm ods csv ppt pptx odp epub mobi azw3 pages numbers key tex",
		Archive:  "zip rar 7z tar gz tgz bz2 xz zst lz4 lzma iso dmg cab",
	} {
		for _, e := range strings.Fields(list) {
			kinds[e] = k
		}
	}
}

// extBytes returns the extension of name without the dot, as the window's treemap colours
// it: a name that starts with a dot or whose extension is longer than 11 characters has none.
func extBytes(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || len(name)-i > 12 {
		return ""
	}
	return name[i+1:]
}

// Ext returns name's extension in lower case, without the dot.
func Ext(name string) string { return strings.ToLower(extBytes(name)) }

// KindOf returns the broad type of a file name.
func KindOf(name string) Kind {
	e := extBytes(name)
	if e == "" {
		return Other
	}
	if k, ok := kinds[e]; ok {
		return k
	}
	return kinds[strings.ToLower(e)]
}

// Flags in Index.info.
const (
	infoDir   = 1 << 0
	infoWide  = 1 << 1 // the name has bytes outside ASCII
	infoGone  = 1 << 2 // a removed entry or an unused slot
	kindShift = 4
)

// maxName is the longest name kept, in bytes. File systems allow at most 255 characters.
const maxName = 1<<16 - 1

// Index holds every entry of a scan. Folders are known by the scan's folder ids, and each
// entry records the id of the folder holding it.
//
// An index made with NewLive instead addresses entries by slot numbers its owner picks,
// such as the records of an NTFS master file table, and a folder's id is its slot. Its
// entries can be changed while it is searched.
type Index struct {
	mu     sync.RWMutex
	names  []byte
	start  []uint32 // entry i's name is names[start[i]:start[i]+nlen[i]]
	nlen   []uint16
	parent []int32 // folder id holding the entry, -1 for the root
	id     []int32 // folder id for folders, -1 for files; nil for a live index
	size   []int64
	info   []uint8
	folder []int32 // folder id -> entry, -1 if unknown; nil for a live index
	live   bool
	used   int // entries not removed, for a live index
	unused int // bytes of names no longer referred to

	typesMu    sync.Mutex
	types      []TypeStat
	typesStale bool // a live index recounts its types when they are next asked for
}

// TypeStat totals the files with one extension.
type TypeStat struct {
	Ext   string `json:"ext"`
	Kind  string `json:"kind"`
	Files int    `json:"files"`
	Size  int64  `json:"size"`
}

// Builder makes an Index. Add each entry once; the order does not matter.
type Builder struct {
	x     *Index
	types typeCounter
}

// NewBuilder starts an index. entries and nameBytes are hints for the totals.
func NewBuilder(entries, nameBytes int) *Builder {
	return &Builder{
		x: &Index{
			names:  make([]byte, 0, nameBytes),
			start:  make([]uint32, 0, entries),
			nlen:   make([]uint16, 0, entries),
			parent: make([]int32, 0, entries),
			id:     make([]int32, 0, entries),
			size:   make([]int64, 0, entries),
			info:   make([]uint8, 0, entries),
		},
		types: typeCounter{},
	}
}

func nameInfo(name string, dir bool) uint8 {
	var info uint8
	for i := 0; i < len(name); i++ {
		if name[i] >= utf8.RuneSelf {
			info |= infoWide
			break
		}
	}
	if dir {
		return info | infoDir
	}
	return info | uint8(KindOf(name))<<kindShift
}

// Add records one entry. parent is the id of the folder holding it, or -1 for the root. For
// a folder, id is its own folder id.
func (b *Builder) Add(name string, parent int, size int64, dir bool, id int) {
	x := b.x
	if len(name) > maxName || len(x.names)+len(name) > 1<<32-1 {
		return // over 4 GB of names; the rest cannot be searched
	}
	entry := int32(len(x.start))
	x.start = append(x.start, uint32(len(x.names)))
	x.nlen = append(x.nlen, uint16(len(name)))
	x.names = append(x.names, name...)
	x.parent = append(x.parent, int32(parent))
	x.size = append(x.size, size)
	x.info = append(x.info, nameInfo(name, dir))
	if dir {
		x.id = append(x.id, int32(id))
		for len(x.folder) <= id {
			x.folder = append(x.folder, -1)
		}
		x.folder[id] = entry
	} else {
		x.id = append(x.id, -1)
		b.types.add(name, size)
	}
}

// Finish returns the index. The builder must not be used afterwards.
func (b *Builder) Finish() *Index {
	x := b.x
	x.types = b.types.list()
	b.x, b.types = nil, nil
	return x
}

// typeCounter totals files by extension. Extensions differing only in case are one type.
type typeCounter map[string]*TypeStat

func (c typeCounter) add(name string, size int64) {
	e := extBytes(name)
	t := c[e]
	if t == nil {
		lower := strings.ToLower(e)
		if t = c[lower]; t == nil {
			t = &TypeStat{Ext: lower, Kind: KindOf(name).String()}
			c[lower] = t
		}
		c[e] = t
	}
	t.Files++
	t.Size += size
}

func (c typeCounter) list() []TypeStat {
	out := []TypeStat{}
	seen := map[*TypeStat]bool{}
	for _, t := range c {
		if !seen[t] {
			seen[t] = true
			out = append(out, *t)
		}
	}
	sortTypes(out)
	return out
}

func sortTypes(t []TypeStat) {
	slices.SortFunc(t, func(a, b TypeStat) int {
		if c := cmp.Compare(b.Size, a.Size); c != 0 {
			return c
		}
		return strings.Compare(a.Ext, b.Ext)
	})
}

// NewLive returns an empty index whose entries are addressed by slot numbers.
func NewLive() *Index { return &Index{live: true, types: []TypeStat{}} }

// Live reports whether the index was made with NewLive.
func (x *Index) Live() bool { return x.live }

// Len is the number of entries, or for a live index the number of slots in use.
func (x *Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	if x.live {
		return x.used
	}
	return len(x.start)
}

func (x *Index) name(i int) []byte {
	s := x.start[i]
	return x.names[s : s+uint32(x.nlen[i])]
}

// entryOf returns the entry of folder id, or -1.
func (x *Index) entryOf(id int) int {
	if id < 0 {
		return -1
	}
	if x.live {
		if id < len(x.info) && x.info[id]&(infoDir|infoGone) == infoDir {
			return id
		}
		return -1
	}
	if id < len(x.folder) {
		return int(x.folder[id])
	}
	return -1
}

// Trail returns the names of the folders from the root down to folder id.
func (x *Index) Trail(id int) []string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	names, _ := x.trail(id)
	return names
}

// trail also reports whether the folders reach up to the root; in a live index a folder
// may have been removed while entries inside it are still to be.
func (x *Index) trail(id int) ([]string, bool) {
	var names []string
	limit := len(x.info)
	for {
		if id == -1 {
			slices.Reverse(names)
			return names, true
		}
		e := x.entryOf(id)
		if e < 0 || len(names) > limit {
			slices.Reverse(names)
			return names, false
		}
		names = append(names, string(x.name(e)))
		id = int(x.parent[e])
	}
}

// Types returns the extensions of the indexed files, largest total first.
func (x *Index) Types(limit int) []TypeStat {
	x.typesMu.Lock()
	defer x.typesMu.Unlock()
	if x.typesStale {
		c := typeCounter{}
		x.mu.RLock()
		for i, info := range x.info {
			if info&(infoDir|infoGone) == 0 {
				c.add(string(x.name(i)), x.size[i])
			}
		}
		x.mu.RUnlock()
		x.types, x.typesStale = c.list(), false
	}
	if limit <= 0 || limit > len(x.types) {
		limit = len(x.types)
	}
	return slices.Clone(x.types[:limit])
}

// Changes are made to a live index through an Updater, which holds the index's lock.
type Updater struct{ x *Index }

// Update runs f with the index locked against searches. x must be a live index.
func (x *Index) Update(f func(u Updater)) {
	if !x.live {
		panic("search: Update on an index that is not live")
	}
	x.mu.Lock()
	f(Updater{x})
	x.compact()
	x.mu.Unlock()
	x.typesMu.Lock()
	x.typesStale = true
	x.typesMu.Unlock()
}

// View runs f with the index locked against changes, to read entries with Entry and Path.
// f must not change the index.
func (x *Index) View(f func(u Updater)) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	f(Updater{x})
}

// Grow makes room for slots below n.
func (u Updater) Grow(n int) {
	x := u.x
	if n <= len(x.info) {
		return
	}
	more := n - len(x.info)
	x.start = slices.Grow(x.start, more)[:n]
	x.nlen = slices.Grow(x.nlen, more)[:n]
	x.parent = slices.Grow(x.parent, more)[:n]
	x.size = slices.Grow(x.size, more)[:n]
	old := len(x.info)
	x.info = slices.Grow(x.info, more)[:n]
	for i := old; i < n; i++ {
		x.start[i], x.nlen[i], x.parent[i], x.size[i], x.info[i] = 0, 0, -1, 0, infoGone
	}
}

// Set puts an entry in slot, replacing what was there. parent is the slot of the folder
// holding it, or -1 for the root.
func (u Updater) Set(slot int, name string, parent int, size int64, dir bool) {
	x := u.x
	if slot < 0 || slot > 1<<31-2 || len(name) > maxName {
		return
	}
	if slot >= len(x.info) {
		u.Grow(max(slot+1, len(x.info)+len(x.info)/4))
	}
	if x.info[slot]&infoGone != 0 {
		x.used++
	} else {
		if string(x.name(slot)) == name {
			x.parent[slot], x.size[slot], x.info[slot] = int32(parent), size, nameInfo(name, dir)
			return
		}
		x.unused += int(x.nlen[slot])
	}
	if len(x.names)+len(name) > 1<<32-1 {
		x.info[slot] = infoGone
		x.used--
		return
	}
	x.start[slot], x.nlen[slot] = uint32(len(x.names)), uint16(len(name))
	x.names = append(x.names, name...)
	x.parent[slot], x.size[slot], x.info[slot] = int32(parent), size, nameInfo(name, dir)
}

// SetSize changes the size of the entry in slot.
func (u Updater) SetSize(slot int, size int64) {
	if slot >= 0 && slot < len(u.x.info) && u.x.info[slot]&infoGone == 0 {
		u.x.size[slot] = size
	}
}

// Remove empties slot.
func (u Updater) Remove(slot int) {
	x := u.x
	if slot < 0 || slot >= len(x.info) || x.info[slot]&infoGone != 0 {
		return
	}
	x.unused += int(x.nlen[slot])
	x.start[slot], x.nlen[slot], x.parent[slot], x.size[slot], x.info[slot] = 0, 0, -1, 0, infoGone
	x.used--
}

// Entry returns what slot holds.
func (u Updater) Entry(slot int) (name string, parent int, size int64, dir, ok bool) {
	x := u.x
	if slot < 0 || slot >= len(x.info) || x.info[slot]&infoGone != 0 {
		return "", -1, 0, false, false
	}
	return string(x.name(slot)), int(x.parent[slot]), x.size[slot], x.info[slot]&infoDir != 0, true
}

// Path returns the names from the root down to slot itself, and whether they reach the root.
func (u Updater) Path(slot int) ([]string, bool) {
	x := u.x
	if slot < 0 || slot >= len(x.info) || x.info[slot]&infoGone != 0 {
		return nil, false
	}
	names, ok := x.trail(int(x.parent[slot]))
	return append(names, string(x.name(slot))), ok
}

// Slots is the number of slots, used or not.
func (u Updater) Slots() int { return len(u.x.info) }

// compact drops names no longer referred to once they take more room than the rest.
func (x *Index) compact() {
	if x.unused < 1<<20 || x.unused < len(x.names)/2 {
		return
	}
	names := make([]byte, 0, len(x.names)-x.unused+len(x.names)/8)
	for i, info := range x.info {
		if info&infoGone == 0 {
			s := x.start[i]
			x.start[i] = uint32(len(names))
			names = append(names, x.names[s:s+uint32(x.nlen[i])]...)
		}
	}
	x.names, x.unused = names, 0
}

// Query is a search. Text is matched against names; Kind is "", "all", "files", "folders"
// or a Kind name such as "videos"; Exts limits files to those extensions. Results come best
// match first, or largest first when Sort is "size" or there is no text.
type Query struct {
	Text  string   `json:"text"`
	Kind  string   `json:"kind,omitempty"`
	Exts  []string `json:"exts,omitempty"`
	Sort  string   `json:"sort,omitempty"`
	Limit int      `json:"limit,omitempty"`
	Types bool     `json:"types,omitempty"` // also return the largest file types
	Scope string   `json:"scope,omitempty"` // for the app: "" for the scan, "drives" for every drive
}

// Hit is one result. Path is the folder holding it. Match lists the matched characters of
// Name, counted in Unicode code points.
type Hit struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Dir    bool   `json:"dir,omitempty"`
	ID     int    `json:"id,omitempty"`
	Parent int    `json:"parent"`
	Match  []int  `json:"match,omitempty"`
}

// Result is the best Limit hits, with the count and total size of every match.
type Result struct {
	Hits      []Hit      `json:"hits"`
	Total     int        `json:"total"`
	TotalSize int64      `json:"totalSize"`
	Sort      string     `json:"sort"`
	Files     bool       `json:"files,omitempty"` // an empty query lists only files
	Types     []TypeStat `json:"types,omitempty"`
}

const (
	DefaultLimit = 1000
	MaxLimit     = 5000
	// TypeLimit is how many extensions a query with Types returns.
	TypeLimit = 200
)

type filter struct {
	dirs, files bool
	kind        int // -1 for any
	exts        []string
}

func newFilter(q Query, hasText bool) filter {
	f := filter{dirs: true, files: true, kind: -1}
	switch q.Kind {
	case "", "all":
		if !hasText {
			f.dirs = false // with nothing typed, list the largest files
		}
	case "files":
		f.dirs = false
	case "folders":
		f.files = false
	default:
		f.dirs = false
		f.kind = -2 // an unknown kind matches nothing
		for k, n := range kindNames {
			if n == q.Kind && k != int(Other) {
				f.kind = k
			}
		}
	}
	for _, e := range q.Exts {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e != "" {
			f.exts = append(f.exts, e)
		}
	}
	if len(f.exts) > 0 {
		f.dirs = false
	}
	return f
}

func (f *filter) accept(info uint8, name []byte) bool {
	if info&infoDir != 0 {
		return f.dirs
	}
	if !f.files {
		return false
	}
	if f.kind != -1 && int(info>>kindShift) != f.kind {
		return false
	}
	if len(f.exts) == 0 {
		return true
	}
	i := bytes.LastIndexByte(name, '.')
	if i <= 0 || len(name)-i > 12 {
		return false
	}
	e := name[i+1:]
	for _, want := range f.exts {
		if len(want) == len(e) && equalFold(e, want) {
			return true
		}
	}
	return false
}

// equalFold compares ASCII letters without case; want is lower case.
func equalFold(b []byte, want string) bool {
	for i := range b {
		c := b[i]
		if c-'A' < 26 {
			c += 'a' - 'A'
		}
		if c != want[i] {
			return false
		}
	}
	return true
}

type candidate struct {
	source  int32
	entry   int32
	score   int32
	nameLen int32
	size    int64
}

// before reports whether a ranks ahead of b.
func before(a, b candidate, bySize bool) bool {
	if !bySize {
		if a.score != b.score {
			return a.score > b.score
		}
		if a.nameLen != b.nameLen {
			return a.nameLen < b.nameLen
		}
	}
	if a.size != b.size {
		return a.size > b.size
	}
	if a.source != b.source {
		return a.source < b.source
	}
	return a.entry < b.entry
}

// top keeps the best candidates in a heap whose first element ranks last.
type top struct {
	items  []candidate
	limit  int
	bySize bool
}

func (t *top) worse(i, j int) bool { return before(t.items[j], t.items[i], t.bySize) }

func (t *top) push(c candidate) {
	if len(t.items) < t.limit {
		t.items = append(t.items, c)
		for i := len(t.items) - 1; i > 0; {
			p := (i - 1) / 2
			if !t.worse(i, p) {
				break
			}
			t.items[i], t.items[p] = t.items[p], t.items[i]
			i = p
		}
		return
	}
	if !before(c, t.items[0], t.bySize) {
		return
	}
	t.items[0] = c
	for i, n := 0, len(t.items); ; {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && t.worse(l, m) {
			m = l
		}
		if r < n && t.worse(r, m) {
			m = r
		}
		if m == i {
			break
		}
		t.items[i], t.items[m] = t.items[m], t.items[i]
		i = m
	}
}

// Source is one index of a search over several, such as one per drive. Join turns a
// folder's trail of names into the path shown for each hit.
type Source struct {
	Index *Index
	Join  func([]string) string
}

// Search runs q. join turns a folder's trail of names into the path shown for each hit.
func (x *Index) Search(ctx context.Context, q Query, join func([]string) string) (Result, error) {
	return SearchAll(ctx, q, []Source{{x, join}})
}

type plan struct {
	toks   []token
	f      filter
	limit  int
	bySize bool
	wide   bool
}

// SearchAll runs q over several indexes and merges the results.
func SearchAll(ctx context.Context, q Query, sources []Source) (Result, error) {
	p := plan{toks: parseTokens(q.Text)}
	p.limit = q.Limit
	if p.limit <= 0 {
		p.limit = DefaultLimit
	}
	p.limit = min(p.limit, MaxLimit)
	p.bySize = q.Sort == "size" || len(p.toks) == 0
	p.f = newFilter(q, len(p.toks) > 0)
	for _, t := range p.toks {
		p.wide = p.wide || t.wide
	}
	res := Result{Hits: []Hit{}, Sort: "match", Files: len(p.toks) == 0 && (q.Kind == "" || q.Kind == "all") && len(p.f.exts) == 0}
	if p.bySize {
		res.Sort = "size"
	}
	type ranked struct {
		c candidate
		h Hit
	}
	var all []ranked
	for s, src := range sources {
		cands, hits, total, size, err := src.Index.collect(ctx, &p, int32(s), src.Join)
		if err != nil {
			return Result{}, err
		}
		res.Total += total
		res.TotalSize += size
		for i := range cands {
			all = append(all, ranked{cands[i], hits[i]})
		}
	}
	slices.SortFunc(all, func(a, b ranked) int {
		if before(a.c, b.c, p.bySize) {
			return -1
		}
		return 1
	})
	if len(all) > p.limit {
		all = all[:p.limit]
	}
	for _, r := range all {
		res.Hits = append(res.Hits, r.h)
	}
	if q.Types {
		if len(sources) == 1 {
			res.Types = sources[0].Index.Types(TypeLimit)
		} else {
			res.Types = mergeTypes(sources)
		}
	}
	return res, nil
}

func mergeTypes(sources []Source) []TypeStat {
	byExt := map[string]*TypeStat{}
	for _, s := range sources {
		for _, t := range s.Index.Types(0) {
			if m := byExt[t.Ext]; m != nil {
				m.Files += t.Files
				m.Size += t.Size
			} else {
				t := t
				byExt[t.Ext] = &t
			}
		}
	}
	out := []TypeStat{}
	for _, t := range byExt {
		out = append(out, *t)
	}
	sortTypes(out)
	if len(out) > TypeLimit {
		out = out[:TypeLimit]
	}
	return out
}

// collect finds the best matches in one index and makes their hits while the index is
// locked, so a live index cannot change between the two.
func (x *Index) collect(ctx context.Context, p *plan, source int32, join func([]string) string) ([]candidate, []Hit, int, int64, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	n := len(x.info)
	workers := runtime.GOMAXPROCS(0)
	if n < 1<<15 {
		workers = 1
	}
	type part struct {
		top   top
		total int
		size  int64
	}
	parts := make([]part, workers)
	chunk := (n + workers - 1) / workers
	toks, f := p.toks, &p.f
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pt := &parts[w]
			pt.top = top{limit: p.limit, bySize: p.bySize}
			var lowered []byte
			for i, end := w*chunk, min(n, (w+1)*chunk); i < end; i++ {
				if i&(1<<14-1) == 0 && ctx.Err() != nil {
					return
				}
				info := x.info[i]
				if x.parent[i] < 0 || info&infoGone != 0 {
					continue // the root itself, or an empty slot
				}
				name := x.name(i)
				if !f.accept(info, name) {
					continue
				}
				score := 0
				if len(toks) > 0 {
					s := name
					if p.wide && info&infoWide != 0 {
						lowered = appendLower(lowered[:0], name)
						s = lowered
					}
					var ok bool
					if score, ok = matchAll(name, s, toks, nil); !ok {
						continue
					}
				}
				pt.total++
				pt.size += x.size[i]
				pt.top.push(candidate{source: source, entry: int32(i), score: int32(score), nameLen: int32(len(name)), size: x.size[i]})
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, 0, err
	}
	var all []candidate
	total, size := 0, int64(0)
	for _, pt := range parts {
		total += pt.total
		size += pt.size
		all = append(all, pt.top.items...)
	}
	slices.SortFunc(all, func(a, b candidate) int {
		if before(a, b, p.bySize) {
			return -1
		}
		return 1
	})
	if len(all) > p.limit {
		all = all[:p.limit]
	}
	hits := make([]Hit, 0, len(all))
	paths := map[int32]string{}
	for _, c := range all {
		i := int(c.entry)
		name := x.name(i)
		h := Hit{Name: string(name), Size: x.size[i], Parent: int(x.parent[i])}
		if x.info[i]&infoDir != 0 {
			h.Dir, h.ID = true, i
			if !x.live {
				h.ID = int(x.id[i])
			}
		}
		pp, ok := paths[x.parent[i]]
		if !ok {
			trail, _ := x.trail(h.Parent)
			pp = join(trail)
			paths[x.parent[i]] = pp
		}
		h.Path = pp
		if len(toks) > 0 {
			h.Match = matchPositions(name, toks)
		}
		hits = append(hits, h)
	}
	return all, hits, total, size, nil
}

func appendLower(dst, name []byte) []byte {
	return append(dst, bytes.ToLower(name)...)
}

// matchPositions returns the matched characters of name for the hit list, in code points.
func matchPositions(name []byte, toks []token) []int {
	s := name
	for _, t := range toks {
		if t.wide {
			if lower := bytes.ToLower(name); len(lower) == len(name) {
				s = lower
			} else {
				return nil // lower case changed the length, so offsets would not line up
			}
			break
		}
	}
	var pos []int
	if _, ok := matchAll(name, s, toks, &pos); !ok || len(pos) == 0 {
		return nil
	}
	slices.Sort(pos)
	pos = slices.Compact(pos)
	out := make([]int, 0, len(pos))
	r, j := 0, 0
	for i := 0; i < len(name) && j < len(pos); {
		_, w := utf8.DecodeRune(name[i:])
		for j < len(pos) && pos[j] < i+w {
			if pos[j] >= i {
				if len(out) == 0 || out[len(out)-1] != r {
					out = append(out, r)
				}
			}
			j++
		}
		i += w
		r++
	}
	return out
}
