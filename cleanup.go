package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// Clean up lists space in this computer's scan that programs download or build again, such
// as dependency folders, build output, package and browser caches, downloaded models, agent
// worktrees without changes, Docker's build cache and the Recycle Bin, and clears what the
// user picks. Nothing is removed until the user confirms, and only inside the scanned
// folders. What is removed drops out of the scan's views as well.

// cleanPathLimit is how many paths of one item the page receives.
const cleanPathLimit = 200

type CleanPath struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// CleanItem is one line of the clean-up list. Its targets are cleared together.
type CleanItem struct {
	ID     string      `json:"id"`
	Group  string      `json:"group"`
	Name   string      `json:"name"`
	Detail string      `json:"detail"`
	Note   string      `json:"note,omitempty"` // what to know before clearing it
	Kept   string      `json:"kept,omitempty"` // what was found but left out, and why
	Size   int64       `json:"size"`
	Count  int         `json:"count"`
	Paths  []CleanPath `json:"paths,omitempty"`
	More   int         `json:"more,omitempty"`
	Freed  int64       `json:"freed,omitempty"`
	Failed []string    `json:"failed,omitempty"`
}

type CleanStatus struct {
	Seq        int         `json:"seq"`
	Finding    bool        `json:"finding"`
	Cleaning   bool        `json:"cleaning"`
	Cancelling bool        `json:"cancelling,omitempty"`
	Found      bool        `json:"found,omitempty"`
	Error      string      `json:"error,omitempty"`
	Items      []CleanItem `json:"items"`
	// HomeIn tells whether the user's folder is part of the scan; without it, caches were
	// not looked for.
	HomeIn bool `json:"homeIn,omitempty"`
	// While clearing.
	Total   int64  `json:"total,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Current string `json:"current,omitempty"`
	Freed   int64  `json:"freed,omitempty"`
	// Cleared is set once a clean-up finishes.
	Cleared bool `json:"cleared,omitempty"`
}

type cleanSession struct {
	sync.Mutex
	status  CleanStatus
	entries []*cleanEntry
	tree    *Tree
	cancel  context.CancelFunc
}

var cleanState cleanSession

// finishedLocalTree is this computer's scan, once it is complete.
func finishedLocalTree() (*Tree, error) {
	state := scanFor(true)
	state.Lock()
	tree, running := state.tree, state.status.Running
	state.Unlock()
	if tree == nil {
		return nil, errors.New("Scan this computer first.")
	}
	if running {
		return nil, errors.New("Wait for the scan to finish.")
	}
	return tree, nil
}

// startCleanupFind lists what the finished scan of this computer holds that can be cleared.
func startCleanupFind() error {
	tree, err := finishedLocalTree()
	if err != nil {
		return err
	}
	tree.mu.RLock()
	plan := tree.clutter
	tree.mu.RUnlock()
	if plan == nil {
		return errors.New("This scan did not finish, so it cannot be cleaned up. Scan again.")
	}
	s := &cleanState
	s.Lock()
	if s.status.Finding || s.status.Cleaning {
		s.Unlock()
		return errors.New("Clean up is already busy.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.tree = tree
	s.entries = nil
	s.status = CleanStatus{Seq: s.status.Seq + 1, Finding: true, Items: []CleanItem{}, HomeIn: plan.homeIn}
	s.Unlock()
	go func() {
		defer cancel()
		var entries []*cleanEntry
		for _, e := range plan.entries {
			entries = append(entries, tree.stillThere(e.clone()))
		}
		entries = append(entries, worktreeEntries(ctx, plan.worktrees)...)
		entries = append(entries, cleanupDocker(ctx)...)
		if e := cleanupBin(); e != nil {
			entries = append(entries, e)
		}
		entries = slices.DeleteFunc(entries, func(e *cleanEntry) bool {
			if e == nil || len(e.targets) == 0 {
				return true
			}
			finishEntry(e)
			return e.item.Size <= 0
		})
		slices.SortStableFunc(entries, func(a, b *cleanEntry) int {
			if c := cmp.Compare(slices.Index(groupOrder, a.item.Group), slices.Index(groupOrder, b.item.Group)); c != 0 {
				return c
			}
			return cmp.Compare(b.item.Size, a.item.Size)
		})
		s.Lock()
		defer s.Unlock()
		if s.tree != tree {
			return
		}
		s.status.Finding, s.status.Cancelling = false, false
		if ctx.Err() != nil {
			s.status.Error = "Stopped."
			return
		}
		s.entries = entries
		s.status.Found = true
		s.status.Items = s.itemsLocked()
	}()
	return nil
}

// stillThere leaves out targets that were deleted from the scan since it finished.
func (t *Tree) stillThere(e *cleanEntry) *cleanEntry {
	e.targets = slices.DeleteFunc(e.targets, func(c cleanTarget) bool { return c.dir >= 0 && t.isForgotten(c.dir, c.name) })
	return e
}

// listedPaths is the largest targets' paths, for the page to show.
func listedPaths(targets []cleanTarget) ([]CleanPath, int) {
	ts := slices.Clone(targets)
	slices.SortStableFunc(ts, func(a, b cleanTarget) int { return cmp.Compare(b.size, a.size) })
	var paths []CleanPath
	for i, t := range ts {
		if i >= cleanPathLimit {
			return paths, len(ts) - cleanPathLimit
		}
		paths = append(paths, CleanPath{Path: t.path, Size: t.size})
	}
	return paths, 0
}

func (s *cleanSession) itemsLocked() []CleanItem {
	items := make([]CleanItem, 0, len(s.entries))
	for _, e := range s.entries {
		items = append(items, e.item)
	}
	return items
}

func cleanupStatus() CleanStatus {
	s := &cleanState
	s.Lock()
	defer s.Unlock()
	st := s.status
	st.Items = slices.Clone(st.Items)
	return st
}

func cancelCleanup() {
	s := &cleanState
	s.Lock()
	defer s.Unlock()
	if s.cancel != nil && (s.status.Finding || s.status.Cleaning) {
		s.status.Cancelling = true
		s.cancel()
	}
}

// revealCleanupPath shows one of the listed paths in the file manager.
func revealCleanupPath(id string, index int) error {
	s := &cleanState
	s.Lock()
	var p string
	for _, e := range s.entries {
		if e.item.ID == id && index >= 0 && index < len(e.item.Paths) {
			p = e.item.Paths[index].Path
		}
	}
	s.Unlock()
	if p == "" || !filepath.IsAbs(p) {
		return errors.New("That item is no longer listed.")
	}
	if _, err := os.Lstat(p); err != nil {
		return fmt.Errorf("%s is no longer there.", p)
	}
	return launch(p, true)
}

// startCleanup clears the chosen items one target at a time.
func startCleanup(ids []string) error {
	current, err := finishedLocalTree()
	if err != nil {
		return err
	}
	s := &cleanState
	s.Lock()
	if s.status.Finding || s.status.Cleaning {
		s.Unlock()
		return errors.New("Clean up is already busy.")
	}
	tree := s.tree
	if tree != current {
		s.Unlock()
		return errors.New("The scan changed since Clean up looked at it. Choose Clean up again.")
	}
	var work []*cleanEntry
	var total int64
	for _, e := range s.entries {
		if slices.Contains(ids, e.item.ID) && len(e.targets) > 0 {
			work = append(work, e)
			total += e.item.Size
		}
	}
	if len(work) == 0 {
		s.Unlock()
		return errors.New("Choose something to clear first.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	roots := tree.scanRoots()
	s.status.Cleaning, s.status.Cleared, s.status.Error = true, false, ""
	s.status.Total, s.status.Done, s.status.Freed, s.status.Current = total, 0, 0, ""
	for _, e := range work {
		e.item.Freed, e.item.Failed = 0, nil
	}
	s.status.Items = s.itemsLocked()
	s.Unlock()

	go func() {
		defer cancel()
		for _, e := range work {
			var kept []cleanTarget
			for _, t := range e.targets {
				if ctx.Err() != nil {
					kept = append(kept, t)
					continue
				}
				s.Lock()
				s.status.Current = t.path
				s.Unlock()
				freed, err := clearTarget(ctx, roots, t)
				tree.forgetCleared(t, freed, err == nil)
				s.Lock()
				e.item.Freed += freed
				s.status.Freed += freed
				s.status.Done += t.size
				if err != nil {
					if len(e.item.Failed) < 20 {
						e.item.Failed = append(e.item.Failed, err.Error())
					}
					t.size = max(0, t.size-freed)
					kept = append(kept, t)
				}
				s.Unlock()
			}
			s.Lock()
			e.targets = kept
			finishEntry(e)
			s.status.Items = s.itemsLocked()
			s.Unlock()
		}
		s.Lock()
		defer s.Unlock()
		s.status.Cleaning, s.status.Cancelling, s.status.Current = false, false, ""
		s.status.Cleared = true
		if ctx.Err() != nil {
			s.status.Error = "Stopped. What was cleared before stopping is gone."
		}
		// Items with nothing left drop out of the list.
		s.entries = slices.DeleteFunc(s.entries, func(e *cleanEntry) bool { return len(e.targets) == 0 && len(e.item.Failed) == 0 })
		s.status.Items = s.itemsLocked()
	}()
	return nil
}

// forgetCleared takes what was removed out of the scan's views.
func (t *Tree) forgetCleared(c cleanTarget, freed int64, whole bool) {
	switch {
	case c.dir < 0 || freed <= 0:
	case c.kind == removeContents:
		files := 0
		if whole {
			files = c.files
		}
		t.forgetContents(c.id, c.path, freed, files)
	case whole:
		t.forget(c.dir, c.name, c.path, c.size, c.files)
	}
}

// --- Clearing ---

// clearTarget removes one target and returns the bytes it freed.
func clearTarget(ctx context.Context, roots []string, t cleanTarget) (int64, error) {
	switch t.kind {
	case runDocker:
		cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, t.program, t.args...)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return 0, fmt.Errorf("%s: %s", t.path, firstLine(string(out), err))
		}
		return t.size, nil
	case emptyBin:
		if !filepath.IsAbs(t.path) {
			if err := emptyRecycleBin(); err != nil {
				return 0, fmt.Errorf("Recycle Bin: %w", err)
			}
			return t.size, nil
		}
	}
	if err := checkRemovable(roots, t.path); err != nil {
		return 0, err
	}
	switch t.kind {
	case removeWorktree:
		if why := worktreeBusy(ctx, t.path); why != "" {
			return 0, fmt.Errorf("%s was left alone: it is now %s.", t.path, why)
		}
		gitDir, err := worktreeGitDir(t.path)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", t.path, err)
		}
		// Run git from the main repository, not from inside the folder it removes.
		common := filepath.Dir(filepath.Dir(gitDir))
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "git", "--git-dir", common, "worktree", "remove", t.path)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return sizeGone(t), fmt.Errorf("%s: %s", t.path, firstLine(string(out), err))
		}
		return t.size, nil
	case runCommand:
		// The tool's own command knows its cache best; whatever it leaves is removed after.
		if p, err := exec.LookPath(t.program); err == nil {
			cmd := exec.CommandContext(ctx, p, t.args...)
			hideWindow(cmd)
			cmd.Run()
		}
		if _, err := os.Lstat(t.path); err != nil {
			return t.size, nil
		}
		return removeTree(ctx, t)
	case removeAVD:
		freed, err := removeTree(ctx, t)
		if err == nil {
			os.Remove(strings.TrimSuffix(t.path, filepath.Ext(t.path)) + ".ini")
		}
		return freed, err
	case removeContents, emptyBin:
		entries, err := os.ReadDir(t.path)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", t.path, err)
		}
		inUse := 0
		for _, e := range entries {
			if ctx.Err() != nil {
				break
			}
			if removeAll(filepath.Join(t.path, e.Name())) != nil {
				inUse++
			}
		}
		t.kind = removeContents
		freed := sizeGone(t)
		if ctx.Err() != nil {
			return freed, ctx.Err()
		}
		if inUse > 0 {
			return freed, fmt.Errorf("%s: %s could not be removed, most likely because programs have them open.", t.path, countText(inUse, "item"))
		}
		return freed, nil
	default:
		return removeTree(ctx, t)
	}
}

func removeTree(ctx context.Context, t cleanTarget) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err := removeAll(t.path); err != nil {
		return sizeGone(t), fmt.Errorf("%s: %s", t.path, describeRemoveError(err))
	}
	return t.size, nil
}

// removeAll removes p and everything below it without following links. Folders that are
// read-only, as in Go's module cache, are made writable first.
func removeAll(p string) error {
	err := os.RemoveAll(p)
	if err == nil || runtime.GOOS == "windows" {
		return err
	}
	filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(path, 0o700)
		}
		return nil
	})
	return os.RemoveAll(p)
}

func describeRemoveError(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("%s could not be removed (%v). A program may have it open.", pe.Path, pe.Err)
	}
	return err.Error()
}

// sizeGone works out how much of a target was freed from what is left of it.
func sizeGone(t cleanTarget) int64 {
	if _, err := os.Lstat(t.path); err != nil {
		return t.size
	}
	var left int64
	filepath.WalkDir(t.path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				left += info.Size()
			}
		}
		return nil
	})
	return max(0, t.size-left)
}

func firstLine(out string, err error) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}

// checkRemovable refuses anything outside the scanned folders, the scanned folders and
// drives themselves, and anything reached through a link, whose target is not ours.
func checkRemovable(roots []string, p string) error {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%q is not a full path.", p)
	}
	if isDriveRoot(p) {
		return fmt.Errorf("%s is a whole drive and is never removed.", p)
	}
	var root string
	for _, r := range roots {
		if samePath(r, p) {
			return fmt.Errorf("%s is a scanned folder and is never removed.", p)
		}
		if isInside(p, r) {
			root = r
		}
	}
	if root == "" {
		return fmt.Errorf("%s is outside the scanned folders and was left alone.", p)
	}
	// No folder on the way down may be a link: it could lead anywhere.
	rel, _ := filepath.Rel(root, filepath.Dir(p))
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "." || seg == "" {
			continue
		}
		if seg == ".." {
			return fmt.Errorf("%q is not a plain path.", p)
		}
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("%s is no longer there.", p)
		}
		if !info.IsDir() || isLink(cur, info) {
			return fmt.Errorf("%s is reached through a link and was left alone.", p)
		}
	}
	if _, err := os.Lstat(p); err != nil {
		return fmt.Errorf("%s is no longer there.", p)
	}
	return nil
}
