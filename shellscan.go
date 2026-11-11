package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/ssh"
)

// Listing directories over SFTP costs several network round trips per folder, which takes
// hours for a whole disk on a distant server. Running find on the server lists everything
// in one pass and streams the result back compressed. Each record carries the entry's depth
// and name rather than its full path: find lists a folder before its contents, so the path
// can be rebuilt here, and the listing is far smaller to download.

const (
	shellHeaderTimeout = 20 * time.Second
	maxReportedErrors  = 200
	scanHeaderPrefix   = "SSE1 "
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// findScript is sent on stdin to "sh -s" so the user's login shell only has to parse
// "sh -s". The first output line is "SSE1 <format> [entries]", where entries is the file
// system's used inode count when the scan covers a whole file system, for a progress
// estimate. Records follow, NUL separated: "<type> <512-byte blocks> <depth> <name>", or
// "h <dev:inode> <blocks> <depth> <name>" for files with several hard links so they can
// be counted once.
func findScript(root string, oneFileSystem bool) string {
	xdev, estimate := "", ""
	if oneFileSystem {
		xdev = " -xdev"
		estimate = "if [ \"$(stat -c %m -- \"$p\" 2>/dev/null)\" = \"$p\" ]; then n=$(df -Pi -- \"$p\" 2>/dev/null | awk 'NR==2{print $3}'); fi\n"
	}
	return "LC_ALL=C; export LC_ALL\n" +
		"p=" + shellQuote(root) + "\n" +
		"if [ ! -d \"$p\" ]; then echo '" + scanHeaderPrefix + "notdir'; exit 0; fi\n" +
		"if ! find \"$p\" -maxdepth 0 -printf '' >/dev/null 2>&1; then echo '" + scanHeaderPrefix + "nofind'; exit 0; fi\n" +
		"n=\n" + estimate +
		"scan() { find -H \"$p\"" + xdev + " \\( -type f -links +1 -printf 'h %D:%i %b %d %f\\0' \\) -o -printf '%y %b %d %f\\0'; }\n" +
		"if command -v zstd >/dev/null 2>&1; then echo \"" + scanHeaderPrefix + "zstd $n\"; scan | zstd -9 -q -c\n" +
		"elif command -v gzip >/dev/null 2>&1; then echo \"" + scanHeaderPrefix + "gzip $n\"; scan | gzip -6\n" +
		"else echo \"" + scanHeaderPrefix + "plain $n\"; scan; fi\n"
}

type findRecord struct {
	kind   byte
	blocks int64
	depth  int
	inode  string
	name   string
}

func cutField(b []byte) ([]byte, []byte, bool) {
	i := bytes.IndexByte(b, ' ')
	if i <= 0 {
		return nil, nil, false
	}
	return b[:i], b[i+1:], true
}

func parseFindRecord(b []byte) (findRecord, bool) {
	if len(b) < 3 || b[1] != ' ' {
		return findRecord{}, false
	}
	r := findRecord{kind: b[0]}
	rest := b[2:]
	var field []byte
	var ok bool
	if r.kind == 'h' {
		if field, rest, ok = cutField(rest); !ok {
			return r, false
		}
		r.inode = string(field)
	}
	if field, rest, ok = cutField(rest); !ok {
		return r, false
	}
	blocks, err := strconv.ParseInt(string(field), 10, 64)
	if err != nil || blocks < 0 {
		return r, false
	}
	if field, rest, ok = cutField(rest); !ok {
		return r, false
	}
	depth, err := strconv.Atoi(string(field))
	if err != nil || depth < 0 || len(rest) == 0 {
		return r, false
	}
	r.blocks, r.depth, r.name = blocks, depth, string(rest)
	return r, true
}

type findBuilder struct {
	t        *Tree
	stack    []*Node  // open folders by depth
	rels     []string // their paths relative to the root
	dirs     map[string]*Node
	seen     map[string]struct{}
	progress ScanProgress
	lastDir  string
	lastName string
	bad      int
}

func newFindBuilder(t *Tree) *findBuilder {
	return &findBuilder{t: t, stack: []*Node{t.Root}, rels: []string{""}, dirs: map[string]*Node{"": t.Root}, seen: map[string]struct{}{}, progress: ScanProgress{Mode: "shell"}}
}

func splitRel(rel string) (string, string) {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i], rel[i+1:]
	}
	return "", rel
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// apply must be called with the tree locked. find lists a folder before its contents, so an
// entry at depth d belongs to the most recent folder at depth d-1.
func (b *findBuilder) apply(r findRecord) {
	b.progress.Entries++
	if r.depth == 0 {
		if r.kind == 'd' { // the scanned folder itself
			b.t.addOwnSize(b.t.Root, r.blocks*512)
			b.progress.Bytes += r.blocks * 512
		}
		return
	}
	if r.depth > len(b.stack) || strings.ContainsRune(r.name, '/') {
		b.bad++
		return
	}
	parent, parentRel := b.stack[r.depth-1], b.rels[r.depth-1]
	b.stack, b.rels = b.stack[:r.depth], b.rels[:r.depth]
	switch r.kind {
	case 'd':
		n := b.t.addChild(parent, r.name, true, 0)
		b.t.addOwnSize(n, r.blocks*512)
		rel := joinRel(parentRel, r.name)
		b.stack, b.rels = append(b.stack, n), append(b.rels, rel)
		b.dirs[rel] = n
		b.progress.Directories++
		b.progress.Bytes += r.blocks * 512
	case 'l':
		return // symlinks are not followed or counted
	default:
		size := r.blocks * 512
		if r.kind == 'h' {
			if _, dup := b.seen[r.inode]; dup {
				size = 0
			} else {
				b.seen[r.inode] = struct{}{}
			}
		}
		b.t.addChild(parent, r.name, false, size)
		b.progress.Files++
		b.progress.Bytes += size
	}
	b.lastDir, b.lastName = parentRel, r.name
}

// nearest must be called with the tree locked.
func (b *findBuilder) nearest(rel string) *Node {
	for {
		if n, ok := b.dirs[rel]; ok {
			return n
		}
		if rel == "" {
			return b.t.Root
		}
		rel, _ = splitRel(rel)
	}
}

func joinRemote(root, rel string) string {
	switch {
	case rel == "":
		return root
	case root == "/":
		return "/" + rel
	default:
		return root + "/" + rel
	}
}

func relativeTo(root, p string) (string, bool) {
	if p == root {
		return "", true
	}
	prefix := root + "/"
	if root == "/" {
		prefix = "/"
	}
	if strings.HasPrefix(p, prefix) {
		return p[len(prefix):], true
	}
	return "", false
}

// consumeFindStream adds records to the tree in batches. It never holds the tree lock while
// waiting for the network, so the window can show partial results during the scan.
func consumeFindStream(ctx context.Context, src io.Reader, b *findBuilder, report func(ScanProgress)) error {
	r := bufio.NewReaderSize(src, 256<<10)
	batch := make([]findRecord, 0, 4096)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		b.t.mu.Lock()
		for _, rec := range batch {
			b.apply(rec)
		}
		b.t.mu.Unlock()
		batch = batch[:0]
		if report != nil {
			p := b.progress
			p.CurrentPath = joinRemote(b.t.RootPath, joinRel(b.lastDir, b.lastName))
			report(p)
		}
	}
	for {
		line, err := r.ReadSlice(0)
		if errors.Is(err, bufio.ErrBufferFull) {
			buf := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = r.ReadSlice(0)
				buf = append(buf, line...)
			}
			line = buf
		}
		if err == nil {
			if rec, ok := parseFindRecord(line[:len(line)-1]); ok {
				batch = append(batch, rec)
			} else {
				b.bad++
			}
		}
		if len(batch) == cap(batch) || r.Buffered() == 0 || err != nil {
			flush()
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// findErrorPath extracts the path from "find: '/some/path': Permission denied".
func findErrorPath(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "find: '")
	if !ok {
		return "", false
	}
	i := strings.LastIndex(rest, "': ")
	if i < 0 {
		return "", false
	}
	return rest[:i], true
}

type findErrors struct {
	done  chan struct{}
	lines []string
	paths []string
	count int
}

func collectFindErrors(r io.Reader) *findErrors {
	e := &findErrors{done: make(chan struct{})}
	go func() {
		defer close(e.done)
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for s.Scan() {
			line := strings.TrimSpace(s.Text())
			if !strings.HasPrefix(line, "find:") {
				continue // login scripts sometimes print to stderr; that is not a scan problem
			}
			e.count++
			if len(e.lines) < maxReportedErrors {
				e.lines = append(e.lines, line)
			}
			if p, ok := findErrorPath(line); ok && len(e.paths) < 100000 {
				e.paths = append(e.paths, p)
			}
		}
		_, _ = io.Copy(io.Discard, r)
	}()
	return e
}

// readScanHeader skips anything a login script printed before the header line.
func readScanHeader(r *bufio.Reader) string {
	read := 0
	for read < 64<<10 {
		line, err := r.ReadString('\n')
		read += len(line)
		if rest, ok := strings.CutPrefix(line, scanHeaderPrefix); ok {
			return strings.TrimSpace(rest)
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// shellScan returns handled=false, with nothing added to the tree, when the server cannot
// run the scan command (SFTP-only accounts, restricted shells, find without -printf). The
// caller then falls back to SFTP.
func shellScan(ctx context.Context, client *ssh.Client, c Connection, t *Tree, result *ScanResult, report func(ScanProgress)) (bool, error) {
	session, err := client.NewSession()
	if err != nil {
		return false, nil
	}
	defer session.Close()
	session.Stdin = strings.NewReader(findScript(t.RootPath, c.SkipVirtual))
	stdout, err := session.StdoutPipe()
	if err != nil {
		return false, nil
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return false, nil
	}
	if err := session.Start("sh -s"); err != nil {
		return false, nil
	}
	problems := collectFindErrors(stderr)
	out := bufio.NewReaderSize(stdout, 256<<10)
	header := make(chan string, 1)
	go func() { header <- readScanHeader(out) }()
	timer := time.NewTimer(shellHeaderTimeout)
	defer timer.Stop()
	var line string
	select {
	case line = <-header:
	case <-ctx.Done():
		return true, ctx.Err()
	case <-timer.C:
		return false, nil
	}
	mode, estimate, _ := strings.Cut(line, " ")
	var body io.Reader
	switch mode {
	case "zstd":
		z, err := zstd.NewReader(out, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return true, fmt.Errorf("reading scan results: %w", err)
		}
		defer z.Close()
		body = z
	case "gzip":
		z, err := gzip.NewReader(out)
		if err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			return true, fmt.Errorf("reading scan results: %w", err)
		}
		defer z.Close()
		body = z
	case "plain":
		body = out
	case "notdir":
		return true, errors.New("The remote path must be a directory.")
	default:
		return false, nil
	}
	result.Mode = "shell"
	b := newFindBuilder(t)
	if n, err := strconv.Atoi(strings.TrimSpace(estimate)); err == nil && n > 0 {
		b.progress.Expected = n
	}
	err = consumeFindStream(ctx, body, b, report)
	result.Files = b.progress.Files
	if err != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, fmt.Errorf("reading scan results: %w", err)
	}
	// A partial listing still exits non-zero; its problems are reported on stderr instead.
	exited := make(chan struct{})
	go func() { _ = session.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		session.Close()
	}
	select {
	case <-problems.done:
	case <-time.After(5 * time.Second):
		session.Close()
		<-problems.done
	}
	result.Errors = problems.lines
	result.ErrorCount = problems.count
	if b.bad > 0 {
		result.ErrorCount++
		result.Errors = append(result.Errors, fmt.Sprintf("%d unreadable entries in the server's listing", b.bad))
	}
	t.mu.Lock()
	for _, p := range problems.paths {
		if rel, ok := relativeTo(t.RootPath, p); ok {
			markIncomplete(b.nearest(rel))
		}
	}
	t.mu.Unlock()
	return true, nil
}
