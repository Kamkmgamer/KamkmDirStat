package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Connection struct {
	Host           string   `json:"host"`
	Port           string   `json:"port"`
	User           string   `json:"user"`
	Password       string   `json:"password"`
	KeyFile        string   `json:"keyFile"`
	Passphrase     string   `json:"passphrase"`
	Path           string   `json:"path"`
	Fingerprint    string   `json:"fingerprint"`
	Method         string   `json:"method"`
	IdentityFiles  []string `json:"identityFiles,omitempty"`
	IdentitiesOnly bool     `json:"identitiesOnly,omitempty"`
	AgentPath      string   `json:"agentPath,omitempty"`
	// SkipVirtual keeps the scan on one file system, like du -x. Over SFTP, which cannot
	// see mount points, it skips /proc, /sys and /dev when scanning /.
	SkipVirtual  bool `json:"skipVirtual"`
	ServerReport bool `json:"serverReport"`
	// Local scans and monitors this PC instead of an SSH server. Paths lists the drives or
	// folders to scan.
	Local bool     `json:"local,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type ScanResult struct {
	Root       *Node
	Errors     []string
	ErrorCount int
	Files      int
	Skipped    []string
	Mode       string // "remote", "shell" (find on the server), "sftp", "local" or "local-ntfs"
	Notes      []string
}

type ScanProgress struct {
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
	Bytes       int64  `json:"bytes"`
	CurrentPath string `json:"currentPath"`
	Mode        string `json:"mode,omitempty"`
	// Entries counts every listed entry. Expected is the file system's used inode count when
	// a whole file system is scanned, and 0 when the total is unknown.
	Entries  int `json:"entries,omitempty"`
	Expected int `json:"expected,omitempty"`
	// ExpectedBytes is the used space on the scanned drives, for a progress estimate.
	ExpectedBytes int64 `json:"expectedBytes,omitempty"`
}

func endpoint(c Connection) (string, error) {
	host := strings.TrimSpace(c.Host)
	port := strings.TrimSpace(c.Port)
	if host == "" || strings.ContainsAny(host, "/\\\x00\r\n") {
		return "", errors.New("Enter a valid host name or IP address.")
	}
	if port == "" {
		port = "22"
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return "", errors.New("Enter a valid SSH port.")
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port), nil
}
func sshConnect(c Connection, callback ssh.HostKeyCallback, auth []ssh.AuthMethod) (*ssh.Client, error) {
	return sshConnectContext(context.Background(), c, callback, auth)
}
func sshConnectContext(ctx context.Context, c Connection, callback ssh.HostKeyCallback, auth []ssh.AuthMethod) (*ssh.Client, error) {
	address, err := endpoint(c)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.User) == "" {
		return nil, errors.New("Enter an SSH username.")
	}
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	config := &ssh.ClientConfig{User: c.User, Auth: auth, HostKeyCallback: callback, Timeout: 20 * time.Second}
	cc, chans, reqs, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(cc, chans, reqs), nil
}
func fingerprint(c Connection) (string, error) {
	var found string
	client, err := sshConnect(c, func(_ string, _ net.Addr, key ssh.PublicKey) error {
		found = ssh.FingerprintSHA256(key)
		return errors.New("host key received")
	}, nil)
	if client != nil {
		client.Close()
	}
	if found != "" {
		return found, nil
	}
	return "", err
}

func cleanRemotePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("Enter an absolute remote path beginning with /.")
	}
	if strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("The remote path contains unsupported characters.")
	}
	return path.Clean(p), nil
}

// connectVerified authenticates only to the server whose host key the user confirmed.
func connectVerified(ctx context.Context, c Connection) (*ssh.Client, error) {
	if c.Fingerprint == "" {
		return nil, errors.New("Verify the host key before connecting.")
	}
	auth, agentConn, err := authMethods(c)
	if err != nil {
		return nil, err
	}
	if agentConn != nil {
		defer agentConn.Close()
	}
	client, err := sshConnectContext(ctx, c, func(_ string, _ net.Addr, k ssh.PublicKey) error {
		if ssh.FingerprintSHA256(k) != c.Fingerprint {
			return errors.New("Server host key changed. Connect again and verify the new fingerprint.")
		}
		return nil
	}, auth)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SSH connection: %w", err)
	}
	return client, nil
}

func scan(c Connection) (ScanResult, error) { return scanWithContext(context.Background(), c, nil) }

func scanWithContext(ctx context.Context, c Connection, report func(ScanProgress)) (ScanResult, error) {
	root, err := cleanRemotePath(c.Path)
	if err != nil {
		return ScanResult{}, err
	}
	return scanInto(ctx, c, newTree(root), report)
}

// scanInto fills t, which the window may read while the scan runs. On cancellation or a
// lost connection, t keeps whatever was found.
func scanInto(ctx context.Context, c Connection, t *Tree, report func(ScanProgress)) (ScanResult, error) {
	result := ScanResult{Root: t.Root}
	client, err := connectVerified(ctx, c)
	if err != nil {
		return result, err
	}
	retained := false
	defer func() {
		if !retained {
			_ = client.Close()
		}
	}()
	stopClose := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopClose()
	defer t.finalize()

	// Open SFTP first: it gives clear errors for a bad path, and the channel is already held
	// if the server allows only one session and the shell scan is refused.
	files, sftpErr := sftp.NewClient(client)
	if sftpErr == nil {
		defer files.Close()
		info, err := files.Stat(t.RootPath)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, fmt.Errorf("open remote path: %w", err)
		}
		if !info.IsDir() {
			return result, errors.New("The remote path must be a directory.")
		}
	} else if ctx.Err() != nil {
		return result, ctx.Err()
	}

	if c.ServerReport {
		handled, err := remoteReportScan(ctx, client, c, t, &result, report)
		if handled {
			if err == nil {
				retained = true
			}
			if err != nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			return result, err
		}
	}
	handled, err := shellScan(ctx, client, c, t, &result, report)
	if !handled && err == nil {
		if sftpErr != nil {
			return result, fmt.Errorf("The server allows neither the find command nor SFTP: %w", sftpErr)
		}
		err = sftpScan(ctx, client, files, c, t, &result, report)
	}
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return result, err
}

// sftpScan is the fallback for servers that do not allow shell commands. It lists several
// folders at once over separate SFTP channels to hide some of the network latency.
func sftpScan(ctx context.Context, client *ssh.Client, first *sftp.Client, c Connection, t *Tree, result *ScanResult, report func(ScanProgress)) error {
	result.Mode = "sftp"
	const parallel = 4
	clients := []*sftp.Client{first}
	defer func() {
		for _, extra := range clients[1:] {
			_ = extra.Close()
		}
	}()
	type job struct {
		node *Node
		path string
	}
	type listing struct {
		job
		entries []os.FileInfo
		err     error
		channel int
	}
	results := make(chan listing, parallel)
	pending := []job{{t.Root, t.RootPath}}
	active := 0
	free := []int{0}
	canOpenMore := true
	progress := ScanProgress{Mode: "sftp"}
	defer func() { result.Files = progress.Files }()
	addError := func(message string) {
		result.ErrorCount++
		if len(result.Errors) < maxReportedErrors {
			result.Errors = append(result.Errors, message)
		}
	}
	for len(pending) > 0 || active > 0 {
		for len(pending) > 0 && active < parallel {
			if len(free) == 0 && canOpenMore && len(clients) < parallel {
				extra, err := sftp.NewClient(client)
				if err != nil {
					canOpenMore = false
				} else {
					clients = append(clients, extra)
					free = append(free, len(clients)-1)
				}
			}
			if len(free) == 0 {
				break
			}
			slot := free[len(free)-1]
			free = free[:len(free)-1]
			next := pending[0]
			pending[0] = job{}
			pending = pending[1:]
			active++
			go func(j job, sftpClient *sftp.Client, index int) {
				entries, err := sftpClient.ReadDirContext(ctx, j.path)
				results <- listing{job: j, entries: entries, err: err, channel: index}
			}(next, clients[slot], slot)
		}
		var item listing
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item = <-results:
		}
		active--
		free = append(free, item.channel)
		progress.Directories++
		progress.CurrentPath = item.path
		t.mu.Lock()
		if item.err != nil {
			addError(fmt.Sprintf("%s: %v", item.path, item.err))
			markIncomplete(item.node)
		}
		for _, info := range item.entries {
			name := info.Name()
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
				markIncomplete(item.node)
				addError("Invalid remote entry in " + item.path)
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			p := path.Join(item.path, name)
			if c.SkipVirtual && t.RootPath == "/" && item.node == t.Root && info.IsDir() && (name == "proc" || name == "sys" || name == "dev") {
				result.Skipped = append(result.Skipped, p)
				continue
			}
			if info.IsDir() {
				pending = append(pending, job{t.addChild(item.node, name, true, 0), p})
			} else {
				t.addChild(item.node, name, false, info.Size())
				progress.Files++
				progress.Bytes += info.Size()
			}
		}
		t.mu.Unlock()
		if report != nil {
			report(progress)
		}
	}
	return ctx.Err()
}
