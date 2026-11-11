package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// OpenSSH itself applies Host, Match and Include rules. Do not use a shell.
func resolveSSH(c Connection) (Connection, error) { return resolveSSHWithConfig(c, "") }
func resolveSSHWithConfig(c Connection, configFile string) (Connection, error) {
	host := strings.TrimSpace(c.Host)
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "/\\\x00\r\n\t ") {
		return c, errors.New("Enter a valid SSH host or config alias.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-G"}
	if configFile != "" {
		args = append(args, "-F", configFile)
	}
	if c.User != "" {
		args = append(args, "-l", c.User)
	}
	if c.Port != "" {
		args = append(args, "-p", c.Port)
	}
	args = append(args, "--", host)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	configureSSHCommand(cmd)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return c, fmt.Errorf("could not read SSH configuration: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return c, fmt.Errorf("could not read SSH configuration using ssh -G: %w", err)
	}
	return applySSHConfig(c, string(out))
}
func applySSHConfig(c Connection, text string) (Connection, error) {
	values := map[string][]string{}
	s := bufio.NewScanner(strings.NewReader(text))
	for s.Scan() {
		line := s.Text()
		i := strings.IndexByte(line, ' ')
		if i > 0 {
			key := strings.ToLower(line[:i])
			values[key] = append(values[key], strings.TrimSpace(line[i+1:]))
		}
	}
	if err := s.Err(); err != nil {
		return c, err
	}
	first := func(key string) string {
		if len(values[key]) > 0 {
			return values[key][0]
		}
		return ""
	}
	for _, key := range []string{"proxyjump", "proxycommand"} {
		if v := first(key); v != "" && !strings.EqualFold(v, "none") {
			return c, fmt.Errorf("SSH config uses %s, which this version does not support.", key)
		}
	}
	if h := first("hostname"); h != "" {
		c.Host = h
	}
	if c.User == "" {
		c.User = first("user")
	}
	if c.Port == "" {
		c.Port = first("port")
	}
	if _, err := endpoint(c); err != nil {
		return c, err
	}
	if c.User == "" {
		return c, errors.New("The SSH config did not specify a username.")
	}
	c.IdentityFiles = nil
	for _, p := range values["identityfile"] {
		if !strings.EqualFold(p, "none") {
			c.IdentityFiles = append(c.IdentityFiles, expandIdentity(p, c))
		}
	}
	c.IdentitiesOnly = strings.EqualFold(first("identitiesonly"), "yes")
	c.AgentPath = first("identityagent")
	if c.AgentPath != "" && c.AgentPath != "SSH_AUTH_SOCK" && !strings.EqualFold(c.AgentPath, "none") {
		if strings.HasPrefix(c.AgentPath, "$") {
			name := strings.TrimPrefix(c.AgentPath, "$")
			value, ok := os.LookupEnv(name)
			if !ok || value == "" {
				return c, fmt.Errorf("SSH config IdentityAgent references unset environment variable %s", name)
			}
			c.AgentPath = value
		} else {
			c.AgentPath = expandIdentity(c.AgentPath, c)
		}
	}
	return c, nil
}
func expandIdentity(p string, c Connection) string {
	home, _ := os.UserHomeDir()
	local := ""
	if u, err := user.Current(); err == nil {
		local = u.Username
	}
	if p == "~" {
		p = home
	} else if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(home, p[2:])
	}
	return strings.NewReplacer("%%", "\x00", "%d", home, "%h", c.Host, "%r", c.User, "%p", c.Port, "%u", local, "\x00", "%").Replace(p)
}
