//go:build linux

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestSFTPScan(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "data"), []byte("1234567"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other"), []byte("123"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "folder"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) != "secret" {
			return nil, os.ErrPermission
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc, chans, requests, e := ssh.NewServerConn(conn, cfg)
				if e != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(requests)
				for ch := range chans {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					c, reqs, e := ch.Accept()
					if e != nil {
						continue
					}
					go func() {
						defer c.Close()
						for req := range reqs {
							if req.Type == "subsystem" && string(req.Payload[4:]) == "sftp" {
								req.Reply(true, nil)
								server, e := sftp.NewServer(c)
								if e == nil {
									server.Serve()
									server.Close()
								}
								return
							}
							req.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	c := Connection{Host: "127.0.0.1", Port: strconv.Itoa(l.Addr().(*net.TCPAddr).Port), User: "user", Password: "secret", Path: root, Fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	r, err := scan(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Root.Size != 10 || r.Files != 2 || len(r.Root.Children) != 2 || len(r.Errors) != 0 {
		t.Fatalf("unexpected scan: %+v", r)
	}
	if r.Root.Children[0].Name != "folder" || r.Root.Children[0].Size != 7 {
		t.Fatalf("unexpected folder size: %+v", r.Root.Children[0])
	}
}
