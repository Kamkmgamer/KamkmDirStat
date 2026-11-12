package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// The native helper (cmd/kamkm-helper) scans Linux servers several times faster than find
// and Python. "go run ./tools/buildhelper" puts compressed builds in helperbin; a build
// without them uses the Python report. The helper is kept in ~/.cache/kamkmdirstat on the
// server, named by its checksum, so it is uploaded once per version.

//go:embed helperbin
var helperFiles embed.FS

// helperArchitectures are the server architectures with a helper build, by uname -m.
// helperUploads counts helpers stored on servers, for tests.
var helperUploads atomic.Int64

// noExecServers lists servers, by host-key fingerprint, where programs in the cache folder
// may not run (a noexec mount). They use the Python report for the rest of the session.
var noExecServers sync.Map

var errHelperNoExec = errors.New("this server does not allow running programs from ~/.cache")

var helperArchitectures = map[string][]string{"amd64": {"x86_64", "amd64"}, "arm64": {"aarch64", "arm64"}}

type helperBuild struct {
	compressed []byte
	sum        string // SHA-256 of the uncompressed program
}

// loadHelper returns the embedded helper for an architecture. Tests replace it.
var loadHelper = func(arch string) (helperBuild, bool) {
	return embeddedHelpers()[arch], len(embeddedHelpers()[arch].compressed) > 0
}

var embeddedHelpers = sync.OnceValue(func() map[string]helperBuild {
	builds := map[string]helperBuild{}
	for arch := range helperArchitectures {
		data, err := fs.ReadFile(helperFiles, "helperbin/kamkm-helper-linux-"+arch+".gz")
		if err != nil {
			continue
		}
		if build, err := newHelperBuild(data); err == nil {
			builds[arch] = build
		}
	}
	return builds
})

func newHelperBuild(compressed []byte) (helperBuild, error) {
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return helperBuild{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, z); err != nil {
		return helperBuild{}, err
	}
	return helperBuild{compressed: compressed, sum: hex.EncodeToString(h.Sum(nil))}, nil
}

const helperDir = `"${XDG_CACHE_HOME:-$HOME/.cache}/kamkmdirstat"`

// helperCommand runs the cached helper for the server's architecture, or reports
// "SSE2 needhelper ARCH" when it is missing and "SSE2 unavailable" when there is none.
func helperCommand(root string, oneFS bool) string {
	one := "0"
	if oneFS {
		one = "1"
	}
	var cases strings.Builder
	for arch, names := range helperArchitectures {
		build, ok := loadHelper(arch)
		if !ok {
			continue
		}
		for _, name := range names {
			fmt.Fprintf(&cases, `"Linux %s") a=%s h=helper-%s-%s ;; `, name, arch, arch, build.sum[:16])
		}
	}
	if cases.Len() == 0 {
		return "printf 'SSE2 unavailable\\n'"
	}
	return `[ -n "$HOME" ] || { printf 'SSE2 unavailable\n'; exit 0; }; ` +
		`case "$(uname -s) $(uname -m)" in ` + cases.String() + `*) printf 'SSE2 unavailable\n'; exit 0 ;; esac; ` +
		`p=` + helperDir + `/$h; if [ -x "$p" ]; then exec "$p" report ` + shellQuote(root) + ` ` + one + `; fi; ` +
		`printf 'SSE2 needhelper %s\n' "$a"`
}

// uploadHelper stores the helper on the server, checks it, and removes older versions.
func uploadHelper(ctx context.Context, client *ssh.Client, arch string) error {
	build, ok := loadHelper(arch)
	if !ok {
		return errors.New("no helper for " + arch)
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	name := "helper-" + arch + "-" + build.sum[:16]
	session.Stdin = bytes.NewReader(build.compressed)
	var stderr bytes.Buffer
	session.Stderr = &stderr
	command := `set -e; umask 077; d=` + helperDir + `; mkdir -p "$d"; t="$d/.upload-$$"; trap 'rm -f "$t"' EXIT; ` +
		`gzip -dc > "$t"; ` +
		`if command -v sha256sum >/dev/null 2>&1; then [ "$(sha256sum < "$t" | cut -c1-64)" = ` + build.sum + ` ]; fi; ` +
		`chmod 700 "$t"; mv -f "$t" "$d/` + name + `"; ` +
		`for f in "$d"/helper-*; do [ "$f" = "$d/` + name + `" ] || rm -f "$f"; done; ` +
		// On a noexec mount the helper cannot run; remove it rather than leave it unused.
		`if ! [ -x "$d/` + name + `" ]; then rm -f "$d/` + name + `"; exit 3; fi`
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case err := <-done:
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 3 {
			return errHelperNoExec
		}
		if err != nil {
			return fmt.Errorf("store the scan helper: %v %s", err, strings.TrimSpace(stderr.String()))
		}
		helperUploads.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("storing the scan helper took too long")
	}
}

// helperReportScan scans with the native helper, storing it on the server first if needed.
// It reports handled=false when the helper cannot be used, so the Python report runs.
func helperReportScan(ctx context.Context, client *ssh.Client, c Connection, t *Tree, result *ScanResult, report func(ScanProgress)) (bool, error) {
	if _, ok := noExecServers.Load(c.Fingerprint); ok {
		return false, nil
	}
	command := helperCommand(t.RootPath, c.SkipVirtual)
	for attempt := 0; attempt < 2; attempt++ {
		header, handled, err := runRemoteReport(ctx, client, command, t, result, report)
		if handled || ctx.Err() != nil {
			if handled && err == nil {
				result.Mode = "remote-native"
			}
			return handled, err
		}
		arch, ok := strings.CutPrefix(header, "needhelper ")
		if !ok {
			return false, nil // not a supported server, or the helper failed to start
		}
		if attempt > 0 {
			noExecServers.Store(c.Fingerprint, true) // stored, yet still reported missing
			return false, nil
		}
		if err := uploadHelper(ctx, client, arch); err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if errors.Is(err, errHelperNoExec) {
				noExecServers.Store(c.Fingerprint, true)
			}
			result.Notes = append(result.Notes, "The fast server scanner could not be used, so the standard scan ran: "+err.Error()+".")
			return false, nil
		}
	}
	return false, nil
}
