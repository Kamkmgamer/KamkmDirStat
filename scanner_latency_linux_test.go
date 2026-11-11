//go:build linux

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureList []os.FileInfo

func (l fixtureList) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(dst, l[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

type slowFiles struct {
	delay      time.Duration
	mappedRoot string
}

func (s slowFiles) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	p := r.Filepath
	if s.mappedRoot != "" {
		p = filepath.Join(s.mappedRoot, strings.TrimPrefix(p, "/"))
	}
	if r.Method == "List" {
		time.Sleep(s.delay)
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, err
		}
		var list fixtureList
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			list = append(list, info)
		}
		return list, nil
	}
	var info os.FileInfo
	var err error
	if r.Method == "Lstat" {
		info, err = os.Lstat(p)
	} else {
		info, err = os.Stat(p)
	}
	if err != nil {
		return nil, err
	}
	return fixtureList{info}, nil
}
func latencyFixture(t *testing.T, count int, delay time.Duration, virtual bool, maxChannels ...int) (Connection, string) {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < count; i++ {
		p := filepath.Join(root, fmt.Sprintf("d%02d", i))
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "file"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			raw, e := listener.Accept()
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
				var active atomic.Int32
				limit := int32(0)
				if len(maxChannels) > 0 {
					limit = int32(maxChannels[0])
				}
				for ch := range chans {
					if limit > 0 && active.Add(1) > limit {
						active.Add(-1)
						ch.Reject(ssh.Prohibited, "channel limit")
						continue
					}
					channel, requests, e := ch.Accept()
					if e != nil {
						if limit > 0 {
							active.Add(-1)
						}
						continue
					}
					go func() {
						defer channel.Close()
						if limit > 0 {
							defer active.Add(-1)
						}
						for req := range requests {
							if req.Type == "subsystem" {
								req.Reply(true, nil)
								mapped := ""
								if virtual {
									mapped = root
								}
								server := sftp.NewRequestServer(channel, sftp.Handlers{FileList: slowFiles{delay: delay, mappedRoot: mapped}})
								_ = server.Serve()
								return
							}
							req.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	c := Connection{Host: "127.0.0.1", Port: strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), User: "test", Password: "test", Path: root, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	if virtual {
		c.Path = "/"
	}
	return c, root
}
func TestScanHandlesLatencyAcrossDirectories(t *testing.T) {
	c, _ := latencyFixture(t, 24, 50*time.Millisecond, false)
	started := time.Now()
	result, err := scan(c)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 24 || result.Root.Size != 24 {
		t.Fatalf("incorrect scan: %+v", result)
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("scan took %s for 24 small directories with 50ms latency; expected under 900ms", elapsed)
	}
}

func TestCancelSlowScan(t *testing.T) {
	c, _ := latencyFixture(t, 24, 250*time.Millisecond, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress := make(chan ScanProgress, 1)
	done := make(chan error, 1)
	go func() {
		_, err := scanWithContext(ctx, c, func(p ScanProgress) {
			select {
			case progress <- p:
			default:
			}
		})
		done <- err
	}()
	select {
	case p := <-progress:
		if p.Directories < 1 {
			t.Fatal("missing directory progress")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scan did not report progress")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancelled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not stop after cancellation")
	}
}
func TestSkipVirtualDirectoriesAtRoot(t *testing.T) {
	c, root := latencyFixture(t, 1, 0, true)
	for _, name := range []string{"proc", "sys", "dev"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c.SkipVirtual = true
	r, err := scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 1 || len(r.Skipped) != 3 {
		t.Fatalf("virtual paths not skipped: %+v", r)
	}
	c.SkipVirtual = false
	r, err = scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 4 || len(r.Skipped) != 0 {
		t.Fatalf("explicit inclusion failed: %+v", r)
	}
}

func TestScanWorksWithOneSFTPChannel(t *testing.T) {
	c, _ := latencyFixture(t, 5, 0, false, 1)
	r, err := scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 5 || r.Root.Size != 5 {
		t.Fatalf("single-channel fallback lost data: %+v", r)
	}
}

func TestCancellationOnLastDirectory(t *testing.T) {
	c, _ := latencyFixture(t, 1, 0, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := scanWithContext(ctx, c, func(p ScanProgress) {
		if p.Files == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation even when the last directory finishes, got %v", err)
	}
}
