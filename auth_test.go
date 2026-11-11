package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestAgentAuthentication(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	keys := agent.NewKeyring()
	if err = keys.Add(agent.AddedKey{PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	listener, sock := listenTestAgent(t)
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _ = agent.ServeAgent(keys, c) }()
		}
	}()
	serverConfig := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(signer.PublicKey().Marshal()) {
			return nil, ssh.ErrNoAuth
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sshListener.Close()
	go func() {
		for {
			conn, e := sshListener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				server, chans, reqs, e := ssh.NewServerConn(conn, serverConfig)
				if e != nil {
					return
				}
				defer server.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "unused")
				}
			}()
		}
	}()
	c := Connection{Host: "127.0.0.1", Port: strconv.Itoa(sshListener.Addr().(*net.TCPAddr).Port), User: "test", Method: "auto", AgentPath: sock}
	methods, agentConn, err := authMethods(c)
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.Close()
	client, err := sshConnect(c, func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if ssh.FingerprintSHA256(k) != ssh.FingerprintSHA256(signer.PublicKey()) {
			t.Fatal("unexpected host key")
		}
		return nil
	}, methods)
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	c.IdentitiesOnly = true
	if _, _, err = authMethods(c); err == nil || !strings.Contains(err.Error(), "No usable SSH key") {
		t.Fatalf("IdentitiesOnly must reject unrelated agent keys, got %v", err)
	}
}
func TestApplySSHConfig(t *testing.T) {
	c, err := applySSHConfig(Connection{Host: "alias"}, "hostname actual.example\nuser remote\nport 2205\nidentityfile ~/.ssh/id_ed25519\nidentitiesonly yes\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "actual.example" || c.User != "remote" || c.Port != "2205" || !c.IdentitiesOnly || len(c.IdentityFiles) != 1 {
		t.Fatalf("unexpected config: %+v", c)
	}
	for _, s := range []string{"proxyjump jump.example", "proxycommand ssh other"} {
		if _, err := applySSHConfig(Connection{Host: "alias"}, "hostname actual.example\nuser remote\nport 22\n"+s+"\n"); err == nil {
			t.Fatalf("accepted unsupported %s", s)
		}
	}
}

func TestOpenSSHConfigResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("temporary file ACLs are rejected by Windows OpenSSH; exercised on Linux")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH not installed")
	}
	dir := t.TempDir()
	included := filepath.Join(dir, "included")
	if err := os.WriteFile(included, []byte("Host buildbox\n  HostName build.example\n  User deploy\n  Port 2224\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config")
	contents := "Include " + filepath.ToSlash(included) + "\nHost *\n  IdentityFile ~/.ssh/example_key\n"
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := resolveSSHWithConfig(Connection{Host: "buildbox"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "build.example" || c.User != "deploy" || c.Port != "2224" || len(c.IdentityFiles) == 0 {
		t.Fatalf("unexpected resolved connection: %+v", c)
	}
	c, err = resolveSSHWithConfig(Connection{Host: "buildbox", User: "override", Port: "2022"}, config)
	if err != nil {
		t.Fatal(err)
	}
	if c.User != "override" || c.Port != "2022" {
		t.Fatalf("overrides ignored: %+v", c)
	}
}
