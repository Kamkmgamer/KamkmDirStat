package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What Clean up looks for, in a finished scan of this computer. The scan's tree is matched
// against these rules while it is still in memory, before it moves to the cache file, so
// pressing Clean up needs no second scan. Only the checks that ask other programs (git,
// Docker, the Recycle Bin) wait until then.

// Groups, in the order the page lists them.
const (
	groupProjects  = "projects"  // dependency folders and build output inside projects
	groupWorktrees = "worktrees" // coding agents' git worktrees
	groupPackages  = "packages"  // package manager caches
	groupBuild     = "build"     // compiler and editor caches
	groupDevices   = "devices"   // Xcode data and Android emulators
	groupBrowsers  = "browsers"  // browser caches and browsers installed for tests
	groupModels    = "models"    // downloaded AI models
	groupSystem    = "system"    // temporary files, old installers, Docker, the Recycle Bin
)

var groupOrder = []string{groupProjects, groupWorktrees, groupPackages, groupBuild, groupDevices, groupBrowsers, groupModels, groupSystem}

// Worktrees changed or used more recently than this may still be in use by an agent.
const worktreeQuiet = 72 * time.Hour

// Installers in Downloads older than this are offered.
const installerAge = 30 * 24 * time.Hour

type targetKind int

const (
	removePath     targetKind = iota // the folder or file itself
	removeContents                   // everything inside the folder
	removeWorktree                   // git worktree remove, which refuses one with changes
	removeAVD                        // an Android emulator's folder and its .ini file
	runDocker                        // a docker prune command
	emptyBin                         // the Recycle Bin or Trash
	runCommand                       // a tool's own clean command, then whatever is left
)

// cleanTarget is one thing to remove. dir and name place it in the scan (dir is -1 for
// targets outside it, such as Docker), so the views can leave it out once it is gone.
type cleanTarget struct {
	kind    targetKind
	path    string
	size    int64
	files   int
	dir     int    // folder id of its parent in the scan
	name    string // its name in that folder
	id      int    // its own folder id, for removeContents
	args    []string
	program string
}

type cleanEntry struct {
	item    CleanItem
	targets []cleanTarget
}

func (e *cleanEntry) clone() *cleanEntry {
	c := *e
	c.targets = slices.Clone(e.targets)
	c.item.Paths = slices.Clone(e.item.Paths)
	return &c
}

// clutterPlan is what a finished scan holds that can be cleared.
type clutterPlan struct {
	entries   []*cleanEntry
	worktrees []worktreeCandidate
	homeIn    bool // the user's folder is part of the scan
}

type worktreeCandidate struct {
	tool   string
	target cleanTarget
}

// cleanupHome is the user's folder. Tests replace it.
var cleanupHome = os.UserHomeDir

// planClutter matches a finished local scan against the rules. It must be called while the
// tree is still in memory.
func (t *Tree) planClutter() {
	home, err := cleanupHome()
	if err != nil {
		home = ""
	}
	t.mu.RLock()
	if t.Root == nil || t.store != nil {
		t.mu.RUnlock()
		return
	}
	f := newFinder(t, filepath.Clean(home))
	f.fixedRules()
	f.projects()
	f.installers()
	f.androidEmulators()
	plan := &clutterPlan{entries: f.entries, worktrees: f.worktreeCandidates(), homeIn: f.home != nil}
	if b := f.trashTarget(); b != nil {
		plan.entries = append(plan.entries, b)
	}
	t.mu.RUnlock()
	t.mu.Lock()
	t.clutter = plan
	t.mu.Unlock()
}

type finder struct {
	t       *Tree
	homeDir string
	home    *Node
	roots   map[*Node]string // scanned folder node to its path
	claimed map[*Node]bool   // targets found so far
	holders map[*Node]bool   // folders that contain a target
	skip    map[*Node]bool   // folders the project search leaves alone
	bases   []*Node          // where Claude Code keeps worktrees in projects
	entries []*cleanEntry
	paths   map[*Node]string
}

func newFinder(t *Tree, home string) *finder {
	f := &finder{t: t, homeDir: home, roots: map[*Node]string{}, claimed: map[*Node]bool{}, holders: map[*Node]bool{}, skip: map[*Node]bool{}, paths: map[*Node]string{}}
	if len(t.roots) <= 1 {
		f.roots[t.Root] = t.RootPath
	} else {
		for i, r := range t.roots {
			if i < len(t.Root.Children) {
				f.roots[t.Root.Children[i]] = r
			}
		}
	}
	for n, p := range f.roots {
		f.paths[n] = p
		if home == "" {
			continue
		}
		if samePath(p, home) {
			f.home = n
		} else if isInside(home, p) {
			rel, _ := filepath.Rel(p, home)
			cur := n
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if cur = child(cur, seg); cur == nil {
					break
				}
			}
			if cur != nil && cur.Directory {
				f.home = cur
			}
		}
	}
	return f
}

func samePath(a, b string) bool {
	if runtime.GOOS == "linux" {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func sameName(a, b string) bool {
	if runtime.GOOS == "linux" {
		return a == b
	}
	return strings.EqualFold(a, b)
}

func child(n *Node, name string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if sameName(c.Name, name) {
			return c
		}
	}
	return nil
}

func (f *finder) path(n *Node) string {
	if p, ok := f.paths[n]; ok {
		return p
	}
	if n.Parent == nil {
		return n.Name
	}
	p := filepath.Join(f.path(n.Parent), n.Name)
	f.paths[n] = p
	return p
}

// resolve finds the folders matching a slash-separated pattern under the user's folder. A
// segment of * matches any folder.
func (f *finder) resolve(pattern string) []*Node {
	if f.home == nil {
		return nil
	}
	nodes := []*Node{f.home}
	for _, seg := range strings.Split(pattern, "/") {
		var next []*Node
		for _, n := range nodes {
			if seg == "*" {
				for _, c := range n.Children {
					if c.Directory {
						next = append(next, c)
					}
				}
			} else if c := child(n, seg); c != nil {
				next = append(next, c)
			}
		}
		nodes = next
	}
	return nodes
}

// overlaps reports whether n is, contains or is inside a target found earlier.
func (f *finder) overlaps(n *Node) bool {
	for p := n; p != nil; p = p.Parent {
		if f.claimed[p] {
			return true
		}
	}
	return f.holders[n]
}

func (f *finder) mark(n *Node) {
	f.claimed[n] = true
	for p := n.Parent; p != nil && !f.holders[p]; p = p.Parent {
		f.holders[p] = true
	}
}

func (f *finder) target(n *Node, kind targetKind) cleanTarget {
	t := cleanTarget{kind: kind, path: f.path(n), size: n.Size, files: n.Files, dir: -1, name: n.Name, id: -1}
	if n.Parent != nil {
		t.dir = n.Parent.id
	}
	if n.Directory {
		t.id = n.id
	} else {
		t.files = 1
	}
	return t
}

// claim records the folder n as a target, unless it overlaps one found earlier or is a
// scanned folder itself.
func (f *finder) claim(n *Node, kind targetKind) (cleanTarget, bool) {
	if n == nil || !n.Directory || n.Size <= 0 || n.Parent == nil || f.roots[n] != "" || f.overlaps(n) {
		return cleanTarget{}, false
	}
	f.mark(n)
	return f.target(n, kind), true
}

func (f *finder) add(e *cleanEntry) {
	if e == nil || len(e.targets) == 0 {
		return
	}
	finishEntry(e)
	if e.item.Size > 0 {
		f.entries = append(f.entries, e)
	}
}

// finishEntry fills in an item's totals and its list of paths from its targets.
func finishEntry(e *cleanEntry) {
	e.item.Size, e.item.Count = 0, len(e.targets)
	for _, t := range e.targets {
		e.item.Size += t.size
	}
	e.item.Paths, e.item.More = listedPaths(e.targets)
	if e.item.ID == "" {
		e.item.ID = e.item.Group + ":" + e.item.Name
	}
}

// cleanRule is a cache at a fixed place in the user's folder. The paths cover every
// platform; only those that exist are offered.
type cleanRule struct {
	group, name, detail, note string
	paths                     []string
	kind                      targetKind
	program                   string // a tool whose own command clears it
	args                      []string
}

var cleanRules = []cleanRule{
	// Package managers.
	{group: groupPackages, name: "npm cache", detail: "Downloaded packages, fetched again when needed", paths: []string{"AppData/Local/npm-cache", ".npm/_cacache"}},
	{group: groupPackages, name: "pnpm store", detail: "Packages shared by pnpm projects; projects relink them on the next install", paths: []string{"AppData/Local/pnpm/store", "AppData/Local/pnpm-cache", ".local/share/pnpm/store", "Library/pnpm/store", ".cache/pnpm", "Library/Caches/pnpm"}},
	{group: groupPackages, name: "Yarn cache", detail: "Downloaded packages", paths: []string{"AppData/Local/Yarn/Cache", ".cache/yarn", "Library/Caches/Yarn", ".yarn/berry/cache"}},
	{group: groupPackages, name: "bun cache", detail: "Downloaded packages", paths: []string{".bun/install/cache"}},
	{group: groupPackages, name: "Go module cache", detail: "Downloaded modules, fetched again on the next build", paths: []string{"go/pkg/mod"}, kind: runCommand, program: "go", args: []string{"clean", "-modcache"}},
	{group: groupPackages, name: "Cargo registry", detail: "Downloaded crates and their unpacked sources", paths: []string{".cargo/registry/cache", ".cargo/registry/src", ".cargo/git/checkouts"}},
	{group: groupPackages, name: "uv cache", detail: "Python packages and builds", paths: []string{"AppData/Local/uv/cache", ".cache/uv", "Library/Caches/uv"}},
	{group: groupPackages, name: "pip cache", detail: "Python packages", paths: []string{"AppData/Local/pip/Cache", ".cache/pip", "Library/Caches/pip"}},
	{group: groupPackages, name: "Gradle caches", detail: "Dependencies and Gradle versions", paths: []string{".gradle/caches", ".gradle/wrapper/dists"}},
	{group: groupPackages, name: "Maven repository", detail: "Java dependencies", paths: []string{".m2/repository"}},
	{group: groupPackages, name: "NuGet packages", detail: ".NET dependencies", paths: []string{".nuget/packages", "AppData/Local/NuGet/v3-cache", "AppData/Local/NuGet/Cache"}},
	{group: groupPackages, name: "Deno cache", detail: "Downloaded modules", paths: []string{"AppData/Local/deno", ".cache/deno", "Library/Caches/deno"}},
	{group: groupPackages, name: "Composer cache", detail: "PHP packages", paths: []string{"AppData/Local/Composer", ".cache/composer", ".composer/cache", "Library/Caches/composer"}},
	{group: groupPackages, name: "Dart and Flutter packages", detail: "pub packages", paths: []string{"AppData/Local/Pub/Cache", ".pub-cache"}},
	{group: groupPackages, name: "Homebrew cache", detail: "Downloaded bottles and sources", paths: []string{"Library/Caches/Homebrew", ".cache/Homebrew"}},
	{group: groupPackages, name: "CocoaPods cache", detail: "Downloaded pods", paths: []string{"Library/Caches/CocoaPods"}},
	{group: groupPackages, name: "Electron downloads", detail: "Electron releases used by builds", paths: []string{"AppData/Local/electron/Cache", ".cache/electron", "Library/Caches/electron"}},
	// Build caches.
	{group: groupBuild, name: "Go build cache", detail: "Compiled packages, made again on the next build", paths: []string{"AppData/Local/go-build", ".cache/go-build", "Library/Caches/go-build"}},
	{group: groupBuild, name: "Bazel cache", detail: "Build outputs and downloads", paths: []string{".cache/bazel", "_bazel_cache"}},
	{group: groupBuild, name: "ccache and sccache", detail: "Compiler caches", paths: []string{"AppData/Local/ccache", ".cache/ccache", ".ccache", "Library/Caches/ccache", "AppData/Local/Mozilla/sccache", ".cache/sccache", "Library/Caches/Mozilla.sccache"}},
	{group: groupBuild, name: "JetBrains caches", detail: "IDE indexes and caches, rebuilt when a project opens", note: "Close JetBrains IDEs first.", paths: []string{"AppData/Local/JetBrains/*/caches", "AppData/Local/JetBrains/*/index", ".cache/JetBrains", "Library/Caches/JetBrains"}},
	{group: groupBuild, name: "VS Code caches", detail: "Editor caches and downloaded extension packages", note: "Close VS Code first; files it has open are skipped.", paths: []string{
		"AppData/Roaming/Code/Cache", "AppData/Roaming/Code/CachedData", "AppData/Roaming/Code/CachedExtensionVSIXs", "AppData/Roaming/Code/Code Cache", "AppData/Roaming/Code/GPUCache",
		".config/Code/Cache", ".config/Code/CachedData", ".config/Code/CachedExtensionVSIXs", ".config/Code/Code Cache",
		"Library/Application Support/Code/Cache", "Library/Application Support/Code/CachedData", "Library/Application Support/Code/CachedExtensionVSIXs", "Library/Application Support/Code/Code Cache"}},
	{group: groupBuild, name: "TypeScript cache", detail: "Type definitions downloaded by editors", paths: []string{"AppData/Local/Microsoft/TypeScript", ".cache/typescript", "Library/Caches/typescript"}},
	// Xcode and Android.
	{group: groupDevices, name: "Xcode derived data", detail: "Build products and indexes, made again on the next build", paths: []string{"Library/Developer/Xcode/DerivedData"}},
	{group: groupDevices, name: "Xcode archives", detail: "Archived app builds", note: "Archives hold the symbols needed to read crash reports from released builds.", paths: []string{"Library/Developer/Xcode/Archives"}},
	{group: groupDevices, name: "iOS device support", detail: "Symbols copied from connected devices, copied again when a device connects", paths: []string{"Library/Developer/Xcode/iOS DeviceSupport", "Library/Developer/Xcode/watchOS DeviceSupport"}},
	{group: groupDevices, name: "Simulator caches", detail: "Caches of the iOS simulators", paths: []string{"Library/Developer/CoreSimulator/Caches"}},
	// Browsers.
	{group: groupBrowsers, name: "Test browsers", detail: "Browsers downloaded by Playwright, Cypress and Puppeteer, downloaded again by the next test run", paths: []string{"AppData/Local/ms-playwright", ".cache/ms-playwright", "Library/Caches/ms-playwright", "AppData/Local/Cypress/Cache", ".cache/Cypress", "Library/Caches/Cypress", ".cache/puppeteer"}},
	{group: groupBrowsers, name: "Chrome cache", detail: "Web pages and images kept by Chrome", note: "Close Chrome first; files it has open are skipped.", paths: []string{"AppData/Local/Google/Chrome/User Data/*/Cache", "AppData/Local/Google/Chrome/User Data/*/Code Cache", ".cache/google-chrome", "Library/Caches/Google/Chrome"}},
	{group: groupBrowsers, name: "Edge cache", detail: "Web pages and images kept by Edge", note: "Close Edge first; files it has open are skipped.", paths: []string{"AppData/Local/Microsoft/Edge/User Data/*/Cache", "AppData/Local/Microsoft/Edge/User Data/*/Code Cache", ".cache/microsoft-edge", "Library/Caches/Microsoft Edge"}},
	{group: groupBrowsers, name: "Brave cache", detail: "Web pages and images kept by Brave", note: "Close Brave first; files it has open are skipped.", paths: []string{"AppData/Local/BraveSoftware/Brave-Browser/User Data/*/Cache", "AppData/Local/BraveSoftware/Brave-Browser/User Data/*/Code Cache", ".cache/BraveSoftware", "Library/Caches/BraveSoftware"}},
	{group: groupBrowsers, name: "Chromium cache", detail: "Web pages and images kept by Chromium", paths: []string{".cache/chromium", "Library/Caches/Chromium"}},
	{group: groupBrowsers, name: "Firefox cache", detail: "Web pages and images kept by Firefox", note: "Close Firefox first; files it has open are skipped.", paths: []string{"AppData/Local/Mozilla/Firefox/Profiles/*/cache2", ".cache/mozilla/firefox", "Library/Caches/Firefox"}},
	// Models.
	{group: groupModels, name: "Hugging Face models", detail: "Models and datasets", note: "Models are downloaded again the next time something uses them, which can take a long time.", paths: []string{".cache/huggingface"}},
	{group: groupModels, name: "Ollama models", detail: "Models pulled with Ollama", note: "Removed models have to be pulled again before Ollama can run them.", paths: []string{".ollama/models"}},
	{group: groupModels, name: "LM Studio models", detail: "Models downloaded in LM Studio", note: "Removed models have to be downloaded again in LM Studio.", paths: []string{".lmstudio/models", ".cache/lm-studio/models"}},
	{group: groupModels, name: "PyTorch models", detail: "Model weights downloaded by torch.hub", paths: []string{".cache/torch"}},
	// System.
	{group: groupSystem, name: "Temporary files", detail: "Files programs left in the temporary folder", note: "Files that are in use are skipped.", paths: []string{"AppData/Local/Temp"}, kind: removeContents},
	{group: groupSystem, name: "Crash dumps", detail: "Memory dumps written when programs crashed", paths: []string{"AppData/Local/CrashDumps"}, kind: removeContents},
}

func (f *finder) fixedRules() {
	for _, r := range cleanRules {
		e := &cleanEntry{item: CleanItem{Group: r.group, Name: r.name, Detail: r.detail, Note: r.note}}
		for _, pattern := range r.paths {
			for _, n := range f.resolve(pattern) {
				t, ok := f.claim(n, r.kind)
				if !ok {
					continue
				}
				t.program, t.args = r.program, r.args
				e.targets = append(e.targets, t)
			}
		}
		f.add(e)
	}
}

// Project folders found anywhere in the scan, outside programs' own folders.
var projectRules = []struct {
	name, detail string
	match        func(n *Node) bool
}{
	{"node_modules folders", "JavaScript dependencies, installed again by npm, pnpm, yarn or bun install", func(n *Node) bool { return sameName(n.Name, "node_modules") }},
	{"Rust build output", "target folders of Cargo projects, made again by cargo build", func(n *Node) bool {
		return sameName(n.Name, "target") && n.Parent != nil && child(n.Parent, "Cargo.toml") != nil
	}},
	{"Web build caches", ".next, .nuxt, .svelte-kit, .turbo, .parcel-cache and .angular folders", func(n *Node) bool {
		for _, s := range []string{".next", ".nuxt", ".svelte-kit", ".turbo", ".parcel-cache", ".angular"} {
			if sameName(n.Name, s) {
				return true
			}
		}
		return false
	}},
	{"Python virtual environments", ".venv and venv folders, made again from the project's requirements", func(n *Node) bool {
		if !sameName(n.Name, ".venv") && !sameName(n.Name, "venv") {
			return false
		}
		c := child(n, "pyvenv.cfg")
		return c != nil && !c.Directory
	}},
}

// Folders in the user's folder that belong to programs, where the project search does not
// look.
var homeSkips = []string{"AppData", "Library", "Application Data", "go", "snap"}

// Folders at the top of a drive or of / that belong to the system or to programs.
var systemSkips = map[string][]string{
	"windows": {"Windows", "Program Files", "Program Files (x86)", "ProgramData", "$Recycle.Bin", "System Volume Information", "Recovery", "$WinREAgent", "PerfLogs"},
	"darwin":  {"System", "Library", "Applications", "private", "usr", "bin", "sbin", "opt", "cores", "Volumes"},
	"linux":   {"bin", "boot", "dev", "etc", "lib", "lib32", "lib64", "libx32", "opt", "proc", "run", "sbin", "snap", "srv", "sys", "usr", "var", "nix"},
}

func (f *finder) projects() {
	has := func(list []string, name string) bool {
		return slices.ContainsFunc(list, func(s string) bool { return sameName(s, name) })
	}
	if f.home != nil {
		for _, c := range f.home.Children {
			if c.Directory && (strings.HasPrefix(c.Name, ".") || has(homeSkips, c.Name)) {
				f.skip[c] = true
			}
		}
		// Other people's folders beside the user's own are theirs.
		if p := f.home.Parent; p != nil {
			for _, c := range p.Children {
				if c != f.home && c.Directory {
					f.skip[c] = true
				}
			}
		}
	}
	found := make([][]cleanTarget, len(projectRules))
	var walk func(n *Node, top bool)
	walk = func(n *Node, top bool) {
		for _, c := range n.Children {
			if !c.Directory || f.skip[c] || f.claimed[c] || sameName(c.Name, ".git") {
				continue
			}
			if top && has(systemSkips[runtime.GOOS], c.Name) {
				continue
			}
			// Claude Code keeps worktrees in .claude/worktrees of each project.
			if sameName(c.Name, "worktrees") && sameName(n.Name, ".claude") {
				f.bases = append(f.bases, c)
				continue
			}
			matched := false
			for i, r := range projectRules {
				if r.match(c) {
					if t, ok := f.claim(c, removePath); ok {
						found[i] = append(found[i], t)
					}
					matched = true
					break
				}
			}
			if !matched {
				walk(c, false)
			}
		}
	}
	// Agents' worktrees are offered whole, so the search leaves them out.
	for _, b := range agentWorktreeBases {
		for _, n := range f.resolve(b.dir) {
			f.skip[n] = true
		}
	}
	for n, p := range f.roots {
		walk(n, systemTop(p))
	}
	for i, r := range projectRules {
		f.add(&cleanEntry{item: CleanItem{Group: groupProjects, Name: r.name, Detail: fmt.Sprintf("%s. %s", countText(len(found[i]), "folder"), r.detail)}, targets: found[i]})
	}
}

func isDriveRoot(p string) bool { return filepath.Dir(p) == p }

// systemTop tells whether a scanned folder holds the system's own folders, such as Windows
// and Program Files, which the project search leaves out. Tests replace it.
var systemTop = isDriveRoot

func countText(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// --- Agent worktrees ---

var agentWorktreeBases = []struct{ tool, dir string }{
	{"T3 Code", ".t3/worktrees"},
	{"Codex", ".codex/worktrees"},
	{"Cursor", ".cursor/worktrees"},
	{"Conductor", "conductor/workspaces"},
	{"Claude Code", ".claude/worktrees"},
	{"Capy", ".capy/worktrees"},
	{"Verdent", ".verdent/worktrees"},
}

// worktreeCandidates lists git worktrees up to three levels below the agents' folders.
func (f *finder) worktreeCandidates() []worktreeCandidate {
	var out []worktreeCandidate
	look := func(tool string, base *Node) {
		var walk func(n *Node, depth int)
		walk = func(n *Node, depth int) {
			for _, c := range n.Children {
				if !c.Directory || f.overlaps(c) {
					continue
				}
				if g := child(c, ".git"); g != nil && !g.Directory {
					f.mark(c)
					out = append(out, worktreeCandidate{tool: tool, target: f.target(c, removeWorktree)})
				} else if depth < 3 {
					walk(c, depth+1)
				}
			}
		}
		walk(base, 1)
	}
	for _, b := range agentWorktreeBases {
		for _, n := range f.resolve(b.dir) {
			look(b.tool, n)
		}
	}
	for _, n := range f.bases {
		look("Claude Code", n)
	}
	return out
}

const worktreeOrphaned = "orphaned"

// worktreeBusy reports why a worktree should be kept, or "" when it can go. Tests replace it.
var worktreeBusy = func(ctx context.Context, dir string) string {
	gitDir, err := worktreeGitDir(dir)
	if err != nil {
		return "not a linked worktree"
	}
	if _, err := os.Stat(gitDir); errors.Is(err, fs.ErrNotExist) {
		// The repository no longer knows this worktree: it was pruned, or the repository
		// moved or was deleted.
		if touchedWithin(dir, worktreeQuiet) {
			return "used recently"
		}
		return worktreeOrphaned
	}
	for _, name := range []string{"index", "HEAD", "logs/HEAD"} {
		if info, err := os.Stat(filepath.Join(gitDir, filepath.FromSlash(name))); err == nil && time.Since(info.ModTime()) < worktreeQuiet {
			return "used recently"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := gitOutput(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "unreadable by git"
	}
	if strings.TrimSpace(out) != "" {
		return "with changes"
	}
	// A detached HEAD whose commit is on no branch would be lost.
	if _, err := gitOutput(ctx, dir, "symbolic-ref", "-q", "HEAD"); err != nil {
		refs, err := gitOutput(ctx, dir, "for-each-ref", "--count=1", "--contains", "HEAD", "refs/heads", "refs/remotes", "refs/tags")
		if err != nil || strings.TrimSpace(refs) == "" {
			return "with commits on no branch"
		}
	}
	return ""
}

// touchedWithin reports whether the folder or anything at its top changed within d.
func touchedWithin(dir string, d time.Duration) bool {
	if info, err := os.Stat(dir); err == nil && time.Since(info.ModTime()) < d {
		return true
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) < d {
			return true
		}
	}
	return false
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	hideWindow(cmd)
	out, err := cmd.Output()
	return string(out), err
}

// worktreeGitDir reads the .git file of a linked worktree.
func worktreeGitDir(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	p, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", errors.New("no gitdir")
	}
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p), nil
}

// worktreeEntries asks git about each worktree and groups the ones that can go by tool.
func worktreeEntries(ctx context.Context, candidates []worktreeCandidate) []*cleanEntry {
	busy := make([]string, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				busy[i] = "not checked"
				return
			}
			busy[i] = worktreeBusy(ctx, c.target.path)
		}()
	}
	wg.Wait()
	type group struct {
		targets []cleanTarget
		kept    map[string]int
	}
	byTool := map[string]*group{}
	var tools []string
	orphans := &cleanEntry{item: CleanItem{Group: groupWorktrees, Name: "Orphaned worktrees",
		Detail: "Worktree folders whose repository no longer lists them, unchanged for 3 days",
		Note:   "Git can no longer tell whether these folders hold uncommitted work. Look through them first if you are not sure."}}
	for i, c := range candidates {
		if busy[i] == worktreeOrphaned {
			t := c.target
			t.kind = removePath
			orphans.targets = append(orphans.targets, t)
			continue
		}
		g := byTool[c.tool]
		if g == nil {
			g = &group{kept: map[string]int{}}
			byTool[c.tool] = g
			tools = append(tools, c.tool)
		}
		if busy[i] != "" {
			g.kept[busy[i]]++
			continue
		}
		g.targets = append(g.targets, c.target)
	}
	var out []*cleanEntry
	for _, tool := range tools {
		g := byTool[tool]
		var kept []string
		total := 0
		for why, n := range g.kept {
			kept = append(kept, fmt.Sprintf("%d %s", n, why))
			total += n
		}
		slices.Sort(kept)
		e := &cleanEntry{item: CleanItem{Group: groupWorktrees, Name: tool + " worktrees",
			Detail: countText(len(g.targets), "worktree") + " without changes and unused for 3 days. Their branches stay in the repository."}, targets: g.targets}
		if total > 0 {
			e.item.Kept = fmt.Sprintf("%s kept: %s.", countText(total, "worktree"), strings.Join(kept, ", "))
		}
		out = append(out, e)
	}
	return append(out, orphans)
}

// --- Installers and emulators ---

var installerExts = []string{".exe", ".msi", ".msix", ".dmg", ".pkg", ".deb", ".rpm", ".appimage"}

func (f *finder) installers() {
	downloads := child(f.home, "Downloads")
	if downloads == nil {
		return
	}
	e := &cleanEntry{item: CleanItem{Group: groupSystem, Name: "Old installers", Detail: "Installers in Downloads not changed for 30 days"}}
	for _, c := range downloads.Children {
		if c.Directory || !slices.Contains(installerExts, strings.ToLower(filepath.Ext(c.Name))) {
			continue
		}
		t := f.target(c, removePath)
		info, err := os.Lstat(t.path)
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < installerAge {
			continue
		}
		e.targets = append(e.targets, t)
	}
	f.add(e)
}

func (f *finder) androidEmulators() {
	avd := child(child(f.home, ".android"), "avd")
	if avd == nil {
		return
	}
	e := &cleanEntry{item: CleanItem{Group: groupDevices, Name: "Android emulators", Detail: "Emulator images that are not running", Note: "The emulators are deleted with their data; Android Studio can make new ones."}}
	running := 0
	for _, c := range avd.Children {
		if !c.Directory || !strings.HasSuffix(strings.ToLower(c.Name), ".avd") {
			continue
		}
		if slices.ContainsFunc(c.Children, func(k *Node) bool { return strings.HasSuffix(strings.ToLower(k.Name), ".lock") }) {
			running++
			continue
		}
		if t, ok := f.claim(c, removeAVD); ok {
			e.targets = append(e.targets, t)
		}
	}
	if running > 0 {
		e.item.Kept = countText(running, "running emulator") + " kept."
	}
	f.add(e)
}

// trashNode is the Trash in the user's folder on macOS and Linux. The Recycle Bin of
// Windows is outside it and is asked for when Clean up starts.
func (f *finder) trashNode() *Node {
	switch runtime.GOOS {
	case "darwin":
		return child(f.home, ".Trash")
	case "linux":
		return child(child(child(f.home, ".local"), "share"), "Trash")
	}
	return nil
}

func (f *finder) trashTarget() *cleanEntry {
	n := f.trashNode()
	if n == nil || n.Size <= 0 || f.overlaps(n) {
		return nil
	}
	f.mark(n)
	t := f.target(n, emptyBin)
	e := &cleanEntry{item: CleanItem{Group: groupSystem, Name: "Trash", Detail: "Files you moved to the Trash"}, targets: []cleanTarget{t}}
	finishEntry(e)
	return e
}

// --- Docker ---

// Tests replace these to keep the machine's Docker and Recycle Bin out of their results.
var (
	cleanupDocker = dockerItems
	cleanupBin    = recycleBinItem
)

// dockerItems asks Docker how much its build cache and unused images take.
func dockerItems(ctx context.Context) []*cleanEntry {
	docker, err := exec.LookPath("docker")
	if err != nil {
		for _, p := range []string{"/usr/local/bin/docker", "/opt/homebrew/bin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker"} {
			if _, err := os.Stat(p); err == nil {
				docker = p
				break
			}
		}
	}
	if docker == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, docker, "system", "df", "--format", "{{.Type}}\t{{.Reclaimable}}")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var entries []*cleanEntry
	for _, line := range strings.Split(string(out), "\n") {
		kind, value, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		size, ok := dockerBytes(value)
		if !ok || size <= 0 {
			continue
		}
		switch kind {
		case "Build Cache":
			entries = append(entries, &cleanEntry{item: CleanItem{Group: groupSystem, Name: "Docker build cache", Detail: "Layers kept to speed up docker build"},
				targets: []cleanTarget{{kind: runDocker, path: "docker builder prune -af", size: size, dir: -1, id: -1, program: docker, args: []string{"builder", "prune", "-af"}}}})
		case "Images":
			entries = append(entries, &cleanEntry{item: CleanItem{Group: groupSystem, Name: "Unused Docker images", Detail: "Images no container uses", Note: "Volumes and containers are not touched."},
				targets: []cleanTarget{{kind: runDocker, path: "docker image prune -af", size: size, dir: -1, id: -1, program: docker, args: []string{"image", "prune", "-af"}}}})
		}
	}
	return entries
}

// dockerBytes reads a size such as "1.25GB" or "512.3MB (40%)". Docker uses decimal units.
func dockerBytes(s string) (int64, bool) {
	s, _, _ = strings.Cut(strings.TrimSpace(s), " ")
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"B": 1, "KB": 1e3, "kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[s[i:]]
	if mult == 0 {
		return 0, false
	}
	return int64(math.Round(v * mult)), true
}
