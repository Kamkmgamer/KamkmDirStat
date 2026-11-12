//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// execServer is an SSH server that runs exec requests with the local sh, with PATH set to
// path, and also serves SFTP.
func execServer(t *testing.T, path string, env ...string) Connection {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			raw, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer raw.Close()
				conn, chans, reqs, e := ssh.NewServerConn(raw, cfg)
				if e != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					channel, requests, e := ch.Accept()
					if e != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							switch {
							case req.Type == "subsystem" && string(req.Payload[4:]) == "sftp":
								req.Reply(true, nil)
								if server, e := sftp.NewServer(channel); e == nil {
									_ = server.Serve()
								}
								return
							case req.Type == "exec":
								command := string(req.Payload[4:])
								req.Reply(true, nil)
								ctx, cancel := context.WithCancel(context.Background())
								go func() {
									for range requests {
									}
									cancel() // client closed the channel
								}()
								cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
								cmd.Env = append([]string{"PATH=" + path}, env...)
								cmd.Stdin, cmd.Stdout, cmd.Stderr = channel, channel, channel.Stderr()
								cmd.WaitDelay = time.Second
								status := uint32(0)
								if e := cmd.Run(); e != nil {
									status = 1
									// Report the real exit code, as sshd does.
									var exit *exec.ExitError
									if errors.As(e, &exit) && exit.ExitCode() > 0 {
										status = uint32(exit.ExitCode())
									}
								}
								code := make([]byte, 4)
								binary.BigEndian.PutUint32(code, status)
								_, _ = channel.SendRequest("exit-status", false, code)
								return
							default:
								req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return Connection{Host: "127.0.0.1", Port: strconv.Itoa(l.Addr().(*net.TCPAddr).Port), User: "test", Password: "test", Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
}

// binDir holds symlinks to the named system tools so a test can hide find or gzip.
func binDir(t *testing.T, tools ...string) string {
	dir := t.TempDir()
	for _, tool := range tools {
		target, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s not installed: %v", tool, err)
		}
		if err := os.Symlink(target, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeFile(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func allocated(t *testing.T, p string) int64 {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Blocks * 512
}

func shellFixture(t *testing.T) (string, int64, int) {
	root := filepath.Join(t.TempDir(), "it's a \"tree\"")
	writeFile(t, filepath.Join(root, "big", "data.bin"), 300000)
	writeFile(t, filepath.Join(root, "big", "large.bin"), 500000) // keeps big largest whichever hard link find meets first
	writeFile(t, filepath.Join(root, "big", "nested", "more.bin"), 70000)
	writeFile(t, filepath.Join(root, "small.txt"), 10)
	writeFile(t, filepath.Join(root, "line\nbreak"), 5000)
	if err := os.Link(filepath.Join(root, "big", "data.bin"), filepath.Join(root, "hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "big"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	want := int64(0)
	for _, p := range []string{"", "big", "big/data.bin", "big/large.bin", "big/nested", "big/nested/more.bin", "small.txt", "line\nbreak"} {
		want += allocated(t, filepath.Join(root, p))
	}
	return root, want, 6 // the second hard link is counted as a file of size zero
}

func TestShellScan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []string
		mode  string
	}{
		{"zstd", []string{"sh", "find", "zstd"}, "shell"},
		{"gzip", []string{"sh", "find", "gzip"}, "shell"},
		{"plain", []string{"sh", "find"}, "shell"},
		{"no find falls back to SFTP", []string{"sh"}, "sftp"},
		{"no shell falls back to SFTP", nil, "sftp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, want, files := shellFixture(t)
			c := execServer(t, binDir(t, tc.tools...))
			c.Path = root
			r, err := scan(c)
			if err != nil {
				t.Fatal(err)
			}
			if r.Mode != tc.mode {
				t.Fatalf("mode = %s, want %s", r.Mode, tc.mode)
			}
			if r.Files != files || r.Root.Files != files || r.ErrorCount != 0 {
				t.Fatalf("files=%d root=%d errors=%v", r.Files, r.Root.Files, r.Errors)
			}
			if tc.mode == "shell" && r.Root.Size != want {
				t.Fatalf("size = %d, want %d (du semantics)", r.Root.Size, want)
			}
			if r.Root.Children[0].Name != "big" {
				t.Fatalf("largest entry should be first: %s", r.Root.Children[0].Name)
			}
			found := false
			for _, c := range r.Root.Children {
				found = found || c.Name == "line\nbreak"
				if c.Name == "link" {
					t.Fatal("symlink was counted")
				}
			}
			if !found {
				t.Fatal("file name with a newline was lost")
			}
		})
	}
}

func TestShellScanEstimatesWholeFileSystem(t *testing.T) {
	bin := binDir(t, "sh", "find", "zstd", "awk")
	// Pretend the scanned folder is a mount point with 42 inodes in use.
	fakes := map[string]string{
		"stat": "#!/bin/sh\nfor a; do last=$a; done\necho \"$last\"\n",
		"df":   "#!/bin/sh\nprintf 'Filesystem Inodes IUsed IFree IUse%% Mounted\\n/dev/x 100 42 58 42%% /\\n'\n",
	}
	for name, script := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	root, _, _ := shellFixture(t)
	c := execServer(t, bin)
	c.Path, c.SkipVirtual = root, true
	var last ScanProgress
	if _, err := scanInto(context.Background(), c, newTree(root), func(p ScanProgress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Expected != 42 || last.Entries != 10 {
		t.Fatalf("progress = %+v", last)
	}
	c.SkipVirtual = false // crossing file systems: the inode count no longer applies
	if _, err := scanInto(context.Background(), c, newTree(root), func(p ScanProgress) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Expected != 0 {
		t.Fatalf("estimate used when crossing file systems: %+v", last)
	}
}

func TestShellScanPermissionErrorsMarkIncomplete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every directory")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "locked", "secret"), 10)
	writeFile(t, filepath.Join(root, "open"), 10)
	if err := os.Chmod(filepath.Join(root, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "locked"), 0o700) })
	c := execServer(t, binDir(t, "sh", "find", "gzip"))
	c.Path = root
	r, err := scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.ErrorCount != 1 || !r.Root.Incomplete {
		t.Fatalf("errors=%v incomplete=%v", r.Errors, r.Root.Incomplete)
	}
	for _, n := range r.Root.Children {
		if n.Incomplete != (n.Name == "locked") {
			t.Fatalf("%s incomplete=%v", n.Name, n.Incomplete)
		}
	}
}

func TestShellScanShowsProgressAndCancels(t *testing.T) {
	bin := binDir(t, "sh", "sleep")
	// A find that answers the capability probe, prints two entries and then stalls, like a
	// slow disk. Without gzip in PATH the entries arrive immediately.
	fake := "#!/bin/sh\ncase \"$*\" in *-maxdepth*) exit 0;; esac\nprintf 'd 0 0 x\\000f 8 1 first\\000'\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "find"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	c := execServer(t, bin)
	c.Path = t.TempDir()
	tree := newTree(c.Path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := make(chan ScanProgress, 10)
	done := make(chan error, 1)
	go func() {
		_, err := scanInto(ctx, c, tree, func(p ScanProgress) { progress <- p })
		done <- err
	}()
	select {
	case p := <-progress:
		if p.Files != 1 || p.Mode != "shell" {
			t.Fatalf("progress = %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no progress while the server was still listing")
	}
	if v, err := tree.view(0); err != nil || len(v.Entries) != 1 || v.Entries[0].Size != 4096 {
		t.Fatalf("partial view = %+v, %v", v, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not stop after cancellation")
	}
}
