package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// A server's host key is checked against the keys the user chose to always trust, which the
// app keeps in known_hosts form beside its settings, and against the user's own
// ~/.ssh/known_hosts, so servers already reached with ssh connect without asking. Only the
// app's file is ever written, and its host names are hashed as ssh-keygen -H does.

const (
	hostKnown   = "known"   // a trusted key for this server matches
	hostNew     = "new"     // no key is trusted for this server yet
	hostChanged = "changed" // a different key is trusted for this server, or this one is revoked
)

const trustedHostsName = "known_hosts"

var trustedHostsMu sync.Mutex

func trustedHostsPath() (string, error) {
	dir, err := settingsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, trustedHostsName), nil
}

// hostKeyFiles lists the known_hosts files to check, app's first.
func hostKeyFiles() []string {
	var files []string
	if p, err := trustedHostsPath(); err == nil {
		files = append(files, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".ssh", "known_hosts"))
	}
	return files
}

// checkHostKey tells whether key is trusted for hostname ("host:port") in any of files.
// Files that are missing or cannot be read are skipped, so the user is asked instead.
func checkHostKey(hostname string, remote net.Addr, key ssh.PublicKey, files []string) string {
	trustedHostsMu.Lock()
	defer trustedHostsMu.Unlock()
	result := hostNew
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			continue
		}
		callback, err := knownhosts.New(file)
		if err != nil {
			continue
		}
		err = callback(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		var revoked *knownhosts.RevokedError
		switch {
		case err == nil:
			return hostKnown
		case errors.As(err, &revoked):
			return hostChanged
		case errors.As(err, &keyErr) && len(keyErr.Want) > 0:
			result = hostChanged
		}
	}
	return result
}

// rememberHostKey makes key the only key the app trusts for hostname.
func rememberHostKey(hostname string, key ssh.PublicKey) error {
	path, err := trustedHostsPath()
	if err != nil {
		return err
	}
	trustedHostsMu.Lock()
	defer trustedHostsMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	host := knownhosts.Normalize(hostname)
	var out bytes.Buffer
	lines := bufio.NewScanner(bytes.NewReader(old))
	lines.Buffer(make([]byte, 64*1024), 1024*1024)
	for lines.Scan() {
		if line := lines.Text(); !lineNamesHost(line, host) {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	out.WriteString(knownhosts.Line([]string{knownhosts.HashHostname(host)}, key))
	out.WriteByte('\n')
	temp := path + ".tmp"
	if err := os.WriteFile(temp, out.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// lineNamesHost tells whether a known_hosts line is for host, given in normalized form. It
// reads the hashed names the app writes and plain comma-separated names.
func lineNamesHost(line, host string) bool {
	fields := strings.Fields(line)
	if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "@") {
		return false
	}
	names := fields[0]
	if strings.HasPrefix(names, "|1|") {
		parts := strings.Split(names[3:], "|")
		if len(parts) != 2 {
			return false
		}
		salt, err1 := base64.StdEncoding.DecodeString(parts[0])
		want, err2 := base64.StdEncoding.DecodeString(parts[1])
		if err1 != nil || err2 != nil {
			return false
		}
		mac := hmac.New(sha1.New, salt)
		mac.Write([]byte(host))
		return hmac.Equal(mac.Sum(nil), want)
	}
	for _, name := range strings.Split(names, ",") {
		if name == host {
			return true
		}
	}
	return false
}
