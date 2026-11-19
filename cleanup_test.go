package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

func writeSized(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cleanupWorld makes a folder that stands in for a drive, with the user's folder inside it,
// and keeps this machine's Docker, Recycle Bin and git out of the results.
func cleanupWorld(t *testing.T) (root, home string) {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	root = t.TempDir()
	home = filepath.Join(root, "Users", "me")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	oldHome, oldDocker, oldBin, oldBusy, oldTop, oldTrash := cleanupHome, cleanupDocker, cleanupBin, worktreeBusy, systemTop, trash
	cleanupHome = func() (string, error) { return home, nil }
	cleanupDocker = func(context.Context) []*cleanEntry { return nil }
	cleanupBin = func() *cleanEntry { return nil }
	systemTop = func(p string) bool { return samePath(p, root) }
	t.Cleanup(func() {
		cleanupHome, cleanupDocker, cleanupBin, worktreeBusy, systemTop, trash = oldHome, oldDocker, oldBin, oldBusy, oldTop, oldTrash
		closeResults()
		cleanState = cleanSession{}
	})
	return root, home
}

// scanLocal scans folders as the Storage view does and waits until the results have moved
// to the cache file.
func scanLocal(t *testing.T, paths ...string) *Tree {
	t.Helper()
	if err := startScan(Connection{Local: true, Paths: paths}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := scanStatusFor("local")
		if !st.Running && st.OnDisk {
			tree, _ := localMachine.scanTree()
			return tree
		}
		if !st.Running && st.Error != "" {
			t.Fatal(st.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan did not finish")
	return nil
}

func waitCleanup(t *testing.T) CleanStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := cleanupStatus()
		if !st.Finding && !st.Cleaning {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clean up did not finish")
	return CleanStatus{}
}

func findCleanup(t *testing.T) CleanStatus {
	t.Helper()
	if err := startCleanupFind(); err != nil {
		t.Fatal(err)
	}
	st := waitCleanup(t)
	if st.Error != "" {
		t.Fatal(st.Error)
	}
	return st
}

func itemNamed(st CleanStatus, name string) *CleanItem {
	for i := range st.Items {
		if st.Items[i].Name == name {
			return &st.Items[i]
		}
	}
	return nil
}

func itemPaths(it *CleanItem) []string {
	var out []string
	for _, p := range it.Paths {
		out = append(out, p.Path)
	}
	slices.Sort(out)
	return out
}

// folderID finds a folder of a finished scan by its path below the scan root.
func folderID(t *testing.T, tree *Tree, names ...string) int {
	t.Helper()
	id := 0
	for _, name := range names {
		v, err := tree.view(id)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(v.Entries, func(e DirEntry) bool { return e.Name == name && e.Directory })
		if i < 0 {
			t.Fatalf("%s not found in %v", name, v.Entries)
		}
		id = v.Entries[i].ID
	}
	return id
}

func entryNames(t *testing.T, tree *Tree, id int) []string {
	t.Helper()
	v, err := tree.view(id)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range v.Entries {
		names = append(names, e.Name)
	}
	slices.Sort(names)
	return names
}

func TestCleanupFindsClutterInTheScan(t *testing.T) {
	root, home := cleanupWorld(t)
	h := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	r := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }

	// JavaScript projects: only the outer node_modules is listed, in the user's folder and
	// in projects elsewhere on the drive.
	writeSized(t, h("code", "web", "package.json"), 10)
	writeSized(t, h("code", "web", "node_modules", "react", "index.js"), 1000)
	writeSized(t, h("code", "web", "node_modules", "react", "node_modules", "x", "y.js"), 500)
	writeSized(t, r("projects", "api", "node_modules", "express", "index.js"), 2000)
	// Programs' and other people's node_modules are left alone.
	writeSized(t, r(systemSkips[runtime.GOOS][0], "Editor", "resources", "node_modules", "a.js"), 3000)
	writeSized(t, r("Users", "other", "code", "node_modules", "a.js"), 3000)
	writeSized(t, h("AppData", "Local", "Programs", "Editor", "node_modules", "a.js"), 3000)
	writeSized(t, h(".vscode", "extensions", "ext", "node_modules", "a.js"), 3000)
	// Rust: target counts only beside Cargo.toml.
	writeSized(t, h("code", "rs", "Cargo.toml"), 10)
	writeSized(t, h("code", "rs", "target", "debug", "app"), 4000)
	writeSized(t, h("code", "notrust", "target", "keep.txt"), 4000)
	// Python: a .venv needs pyvenv.cfg.
	writeSized(t, h("code", "py", ".venv", "pyvenv.cfg"), 10)
	writeSized(t, h("code", "py", ".venv", "lib", "x.py"), 600)
	writeSized(t, h("code", "py2", "venv", "data.csv"), 600)
	// Caches at fixed places.
	writeSized(t, h("AppData", "Local", "npm-cache", "_cacache", "a"), 700)
	writeSized(t, h(".npm", "_cacache", "b"), 300)
	writeSized(t, h("AppData", "Local", "Google", "Chrome", "User Data", "Default", "Cache", "f"), 900)
	writeSized(t, h("AppData", "Local", "Google", "Chrome", "User Data", "Profile 1", "Cache", "f"), 100)
	writeSized(t, h("AppData", "Local", "Google", "Chrome", "User Data", "Default", "Bookmarks"), 50)
	// Installers: only old ones.
	writeSized(t, h("Downloads", "old-setup.exe"), 1200)
	writeSized(t, h("Downloads", "new-setup.msi"), 1200)
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(h("Downloads", "old-setup.exe"), old, old); err != nil {
		t.Fatal(err)
	}
	// Agent worktrees: one in use, one free, one whose repository forgot it.
	for _, name := range []string{"busy", "free", "orphan"} {
		writeSized(t, h(".t3", "worktrees", "app", name, ".git"), 20)
		writeSized(t, h(".t3", "worktrees", "app", name, "node_modules", "dep.js"), 1500)
	}
	writeSized(t, r("projects", "repo", ".claude", "worktrees", "feat", ".git"), 20)
	writeSized(t, r("projects", "repo", ".claude", "worktrees", "feat", "main.go"), 400)
	worktreeBusy = func(_ context.Context, dir string) string {
		switch filepath.Base(dir) {
		case "busy":
			return "with changes"
		case "orphan":
			return worktreeOrphaned
		}
		return ""
	}

	scanLocal(t, root)
	st := findCleanup(t)
	if !st.HomeIn {
		t.Fatal("the user's folder is in the scan")
	}
	nm := itemNamed(st, "node_modules folders")
	want := []string{r("projects", "api", "node_modules"), h("code", "web", "node_modules")}
	slices.Sort(want)
	if nm == nil || !slices.Equal(itemPaths(nm), want) || nm.Size != 3500 {
		t.Fatalf("node_modules = %+v, want %v", nm, want)
	}
	if it := itemNamed(st, "Rust build output"); it == nil || !slices.Equal(itemPaths(it), []string{h("code", "rs", "target")}) {
		t.Fatalf("Rust = %+v", it)
	}
	if it := itemNamed(st, "Python virtual environments"); it == nil || !slices.Equal(itemPaths(it), []string{h("code", "py", ".venv")}) {
		t.Fatalf("Python = %+v", it)
	}
	if it := itemNamed(st, "npm cache"); it == nil || it.Size != 1000 || it.Count != 2 {
		t.Fatalf("npm = %+v", it)
	}
	if it := itemNamed(st, "Chrome cache"); it == nil || it.Size != 1000 || it.Count != 2 {
		t.Fatalf("Chrome = %+v", it)
	}
	if it := itemNamed(st, "Old installers"); it == nil || !slices.Equal(itemPaths(it), []string{h("Downloads", "old-setup.exe")}) {
		t.Fatalf("installers = %+v", it)
	}
	t3 := itemNamed(st, "T3 Code worktrees")
	if t3 == nil || !slices.Equal(itemPaths(t3), []string{h(".t3", "worktrees", "app", "free")}) || !strings.Contains(t3.Kept, "1 with changes") {
		t.Fatalf("T3 worktrees = %+v", t3)
	}
	if it := itemNamed(st, "Orphaned worktrees"); it == nil || it.Note == "" || !slices.Equal(itemPaths(it), []string{h(".t3", "worktrees", "app", "orphan")}) {
		t.Fatalf("orphans = %+v", it)
	}
	if it := itemNamed(st, "Claude Code worktrees"); it == nil || it.Count != 1 {
		t.Fatalf("Claude Code worktrees = %+v", it)
	}
	// Nothing is listed twice.
	seen := map[string]string{}
	for _, it := range st.Items {
		for _, p := range it.Paths {
			for q, other := range seen {
				if p.Path == q || isInside(p.Path, q) || isInside(q, p.Path) {
					t.Fatalf("%s (%s) overlaps %s (%s)", p.Path, it.Name, q, other)
				}
			}
			seen[p.Path] = it.Name
		}
	}
}

func TestCleanupClearsAndTheScanForgetsIt(t *testing.T) {
	root, home := cleanupWorld(t)
	h := func(parts ...string) string { return filepath.Join(append([]string{home}, parts...)...) }
	writeSized(t, h("code", "a", "node_modules", "zzqqdep.js"), 1000)
	writeSized(t, h("code", "a", "index.js"), 10)
	writeSized(t, h(".gradle", "caches", "c"), 800)
	writeSized(t, h("AppData", "Local", "Temp", "t1.tmp"), 100)
	writeSized(t, h("AppData", "Local", "Temp", "sub", "t2.tmp"), 100)

	tree := scanLocal(t, root)
	codeA := folderID(t, tree, "Users", "me", "code", "a")
	before, _ := tree.view(0)
	st := findCleanup(t)
	var ids []string
	for _, name := range []string{"node_modules folders", "Temporary files"} {
		it := itemNamed(st, name)
		if it == nil {
			t.Fatalf("no %s in %+v", name, st.Items)
		}
		ids = append(ids, it.ID)
	}
	if err := startCleanup(ids); err != nil {
		t.Fatal(err)
	}
	st = waitCleanup(t)
	if st.Error != "" || !st.Cleared || st.Freed != 1200 {
		t.Fatalf("after clearing: %+v", st)
	}
	for _, gone := range []string{h("code", "a", "node_modules"), h("AppData", "Local", "Temp", "t1.tmp"), h("AppData", "Local", "Temp", "sub")} {
		if _, err := os.Lstat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	for _, kept := range []string{h("code", "a", "index.js"), h(".gradle", "caches", "c"), h("AppData", "Local", "Temp")} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was removed", kept)
		}
	}
	if itemNamed(st, "node_modules folders") != nil || itemNamed(st, "Gradle caches") == nil {
		t.Fatalf("items after clearing: %+v", st.Items)
	}
	// The scan's views no longer show what was cleared, and the folders above shrank.
	if got := entryNames(t, tree, codeA); !slices.Equal(got, []string{"index.js"}) {
		t.Fatalf("code/a now lists %v", got)
	}
	if got := entryNames(t, tree, folderID(t, tree, "Users", "me", "AppData", "Local", "Temp")); len(got) != 0 {
		t.Fatalf("Temp now lists %v", got)
	}
	after, _ := tree.view(0)
	if before.Size-after.Size != 1200 || before.Files-after.Files != 3 {
		t.Fatalf("root went from %d bytes, %d files to %d, %d", before.Size, before.Files, after.Size, after.Files)
	}
	res, err := tree.search(context.Background(), search.Query{Text: "zzqqdep"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("search still finds %+v", res.Hits)
	}
	// Looking again does not offer what is gone.
	st = findCleanup(t)
	if itemNamed(st, "node_modules folders") != nil {
		t.Fatalf("cleared items are offered again: %+v", st.Items)
	}
}

func TestDeleteFromTheScan(t *testing.T) {
	root, home := cleanupWorld(t)
	writeSized(t, filepath.Join(home, "big.iso"), 5000)
	writeSized(t, filepath.Join(home, "keep.txt"), 7)
	writeSized(t, filepath.Join(home, "old", "a.bin"), 300)
	tree := scanLocal(t, root)
	me := folderID(t, tree, "Users", "me")
	var trashed []string
	trash = func(p string) error { trashed = append(trashed, p); return os.RemoveAll(p) }

	waitDelete := func(seq int) DeleteStatus {
		t.Helper()
		for range 3000 {
			if s := deleteStatus(); s.Seq == seq && !s.Running {
				return s
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("delete did not finish")
		return DeleteStatus{}
	}
	seq, err := startDelete(DeleteRequest{Dir: me, Name: "big.iso"})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitDelete(seq); s.Error != "" || s.Freed != 5000 {
		t.Fatalf("to the Recycle Bin: %+v", s)
	}
	if !slices.Equal(trashed, []string{filepath.Join(home, "big.iso")}) {
		t.Fatalf("trashed %v", trashed)
	}
	seq, err = startDelete(DeleteRequest{Dir: me, Name: "old", Permanent: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitDelete(seq); s.Error != "" || s.Freed != 300 {
		t.Fatalf("permanent: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(home, "old")); err == nil {
		t.Fatal("old is still there")
	}
	if got := entryNames(t, tree, me); !slices.Equal(got, []string{"keep.txt"}) {
		t.Fatalf("the user's folder now lists %v", got)
	}
	// The scanned folder itself, and requests for a server, are refused.
	if _, err := startDelete(DeleteRequest{Dir: 0, Name: ""}); err == nil {
		t.Error("deleting the scanned folder was allowed")
	}
	if _, err := startDelete(DeleteRequest{Target: "remote", Dir: me, Name: "keep.txt"}); err == nil {
		t.Error("deleting on a server was allowed")
	}
}

func TestCheckRemovableStaysInsideScannedFolders(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeSized(t, filepath.Join(root, "p", "node_modules", "x"), 1)
	writeSized(t, filepath.Join(other, "precious", "x"), 1)
	roots := []string{root}
	if err := checkRemovable(roots, filepath.Join(root, "p", "node_modules")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{root, other, filepath.Join(other, "precious"), "relative", filepath.VolumeName(root) + string(filepath.Separator), filepath.Join(root, "p", "..", "..", filepath.Base(other))} {
		if err := checkRemovable(roots, p); err == nil {
			t.Errorf("%s was allowed", p)
		}
	}
	// A folder reached through a link is refused, whatever it points at.
	link := filepath.Join(root, "link")
	if err := makeDirLink(filepath.Join(other, "precious"), link); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
	if err := checkRemovable(roots, filepath.Join(link, "x")); err == nil {
		t.Error("a path through a link was allowed")
	}
}

// makeDirLink makes a junction on Windows, which needs no privilege, and a symlink elsewhere.
func makeDirLink(target, link string) error {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
	}
	return os.Symlink(target, link)
}

func TestCleanupRemovesFolderLinksNotTheirTargets(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	writeSized(t, filepath.Join(other, "keep.txt"), 5)
	nm := filepath.Join(root, "p", "node_modules")
	writeSized(t, filepath.Join(nm, "a.js"), 5)
	if err := makeDirLink(other, filepath.Join(nm, "linked")); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
	if _, err := clearTarget(context.Background(), []string{root}, cleanTarget{kind: removePath, path: nm, size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "keep.txt")); err != nil {
		t.Fatal("the link's target lost its files")
	}
}

func TestWorktreeRemovedWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	writeSized(t, filepath.Join(repo, "a.txt"), 3)
	git(root, "init", "-q", repo)
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "first")
	wt := filepath.Join(root, ".t3", "worktrees", "repo", "wt")
	git(repo, "worktree", "add", "-q", "-b", "feature", wt)

	// A worktree just made counts as recently used.
	if why := worktreeBusy(context.Background(), wt); why != "used recently" {
		t.Fatalf("fresh worktree: %q", why)
	}
	old := worktreeBusy
	defer func() { worktreeBusy = old }()
	worktreeBusy = func(context.Context, string) string { return "" }
	if _, err := clearTarget(context.Background(), []string{root}, cleanTarget{kind: removeWorktree, path: wt, size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("worktree folder is still there")
	}
	// The branch stays.
	git(repo, "rev-parse", "--verify", "feature")
}

func TestOrphanedWorktree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, "gone", ".git", "worktrees", "x")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := worktreeBusy(context.Background(), dir); why != "used recently" {
		t.Fatalf("fresh orphan: %q", why)
	}
	old := time.Now().Add(-10 * 24 * time.Hour)
	for _, p := range []string{filepath.Join(dir, ".git"), dir} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if why := worktreeBusy(context.Background(), dir); why != worktreeOrphaned {
		t.Fatalf("old orphan: %q", why)
	}
}

func TestDockerBytes(t *testing.T) {
	for in, want := range map[string]int64{"1.25GB": 1_250_000_000, "512.3MB (40%)": 512_300_000, "0B": 0, "10kB": 10_000, "2TB": 2e12} {
		if got, ok := dockerBytes(in); !ok || got != want {
			t.Errorf("dockerBytes(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	if _, ok := dockerBytes("n/a"); ok {
		t.Error("n/a was read as a size")
	}
}

func TestOpenWithAction(t *testing.T) {
	root, home := cleanupWorld(t)
	writeSized(t, filepath.Join(home, "notes.txt"), 3)
	tree := scanLocal(t, root)
	me := folderID(t, tree, "Users", "me")
	var opened []string
	old := launchWith
	launchWith = func(p string) error { opened = append(opened, p); return nil }
	defer func() { launchWith = old }()
	s := runOpen(context.Background(), tree, OpenRequest{Dir: me, Name: "notes.txt", Action: "openwith"})
	if s.Error != "" || !slices.Equal(opened, []string{filepath.Join(home, "notes.txt")}) {
		t.Fatalf("open with: %+v, opened %v", s, opened)
	}
}
