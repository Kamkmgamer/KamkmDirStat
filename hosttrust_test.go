package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestHostTrust(t *testing.T) {
	config := t.TempDir()
	t.Setenv("APPDATA", config)         // Windows
	t.Setenv("XDG_CONFIG_HOME", config) // Linux
	t.Setenv("HOME", config)            // macOS
	appFile, err := trustedHostsPath()
	if err != nil {
		t.Fatal(err)
	}
	files := []string{appFile}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 2222}
	host, other := "buildbox:2222", "other:22"
	first, second := testHostKey(t), testHostKey(t)

	if got := checkHostKey(host, remote, first, files); got != hostNew {
		t.Fatalf("an unknown server is %q, want %q", got, hostNew)
	}
	if err := rememberHostKey(host, first); err != nil {
		t.Fatal(err)
	}
	if err := rememberHostKey(other, second); err != nil {
		t.Fatal(err)
	}
	if got := checkHostKey(host, remote, first, files); got != hostKnown {
		t.Fatalf("a remembered key is %q, want %q", got, hostKnown)
	}
	if got := checkHostKey(host, remote, second, files); got != hostChanged {
		t.Fatalf("a different key is %q, want %q", got, hostChanged)
	}
	saved, _ := os.ReadFile(appFile)
	if strings.Contains(string(saved), "buildbox") {
		t.Fatal("host names must be saved hashed")
	}

	// Trusting the new key replaces the old one for that server only.
	if err := rememberHostKey(host, second); err != nil {
		t.Fatal(err)
	}
	if got := checkHostKey(host, remote, second, files); got != hostKnown {
		t.Fatalf("the new key is %q, want %q", got, hostKnown)
	}
	if got := checkHostKey(host, remote, first, files); got != hostChanged {
		t.Fatalf("the replaced key is %q, want %q", got, hostChanged)
	}
	if got := checkHostKey(other, remote, second, files); got != hostKnown {
		t.Fatalf("another server's key was lost: %q", got)
	}

	// A key trusted in the user's own known_hosts needs no confirmation either, and a
	// revoked key is never trusted.
	userFile := filepath.Join(t.TempDir(), "known_hosts")
	third := testHostKey(t)
	line := knownhosts.Line([]string{knownhosts.Normalize("fresh:22")}, third) + "\n@revoked * " + string(ssh.MarshalAuthorizedKey(first))
	if err := os.WriteFile(userFile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	both := []string{appFile, userFile, filepath.Join(t.TempDir(), "missing")}
	if got := checkHostKey("fresh:22", remote, third, both); got != hostKnown {
		t.Fatalf("a key from ~/.ssh/known_hosts is %q, want %q", got, hostKnown)
	}
	if got := checkHostKey("anything:22", remote, first, both); got != hostChanged {
		t.Fatalf("a revoked key is %q, want %q", got, hostChanged)
	}
}
