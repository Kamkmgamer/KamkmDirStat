package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestFingerprintAndHostKeyMismatch(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	var authCalls atomic.Int32
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) { authCalls.Add(1); return nil, nil }}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				server, _, _, e := ssh.NewServerConn(conn, cfg)
				if e == nil {
					server.Close()
				} else {
					conn.Close()
				}
			}()
		}
	}()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	c := Connection{Host: "127.0.0.1", Port: port, User: "tester", Password: "secret", Path: "/"}
	fingerprint, err := fingerprint(c)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("unexpected fingerprint: %s", fingerprint)
	}
	if authCalls.Load() != 0 {
		t.Fatal("authentication attempted during fingerprint discovery")
	}
	c.Fingerprint = "SHA256:wrong"
	_, err = scan(c)
	if err == nil || !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("expected host key mismatch, got %v", err)
	}
	if authCalls.Load() != 0 {
		t.Fatal("credentials sent to a server with a mismatched host key")
	}
	listener.Close()
	<-done
}
func TestEndpointRejectsInvalidHost(t *testing.T) {
	for _, host := range []string{"", "a/b", "host\nother"} {
		if _, err := endpoint(Connection{Host: host}); err == nil {
			t.Errorf("accepted %q", host)
		}
	}
}
