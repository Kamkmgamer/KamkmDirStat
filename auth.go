package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func authMethods(c Connection) ([]ssh.AuthMethod, net.Conn, error) {
	if c.Method == "password" || c.Method == "" && c.Password != "" {
		if c.Password == "" {
			return nil, nil, errors.New("Enter an SSH password.")
		}
		return []ssh.AuthMethod{ssh.Password(c.Password)}, nil, nil
	}
	if c.Method == "key" || c.Method == "" && c.KeyFile != "" {
		if c.KeyFile == "" {
			return nil, nil, errors.New("Enter a private key path.")
		}
		b, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read private key: %w", err)
		}
		var key ssh.Signer
		if c.Passphrase != "" {
			key, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(c.Passphrase))
		} else {
			key, err = ssh.ParsePrivateKey(b)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(key)}, nil, nil
	}
	var signers []ssh.Signer
	var agentConn net.Conn
	var agentErr error
	if !strings.EqualFold(c.AgentPath, "none") {
		agentConn, agentErr = dialAgent(c.AgentPath)
		if agentErr == nil {
			signers, agentErr = agent.NewClient(agentConn).Signers()
			if agentErr != nil {
				agentConn.Close()
				agentConn = nil
			}
		}
	}
	if c.IdentitiesOnly {
		allowed := identityPublicKeys(c.IdentityFiles)
		var filtered []ssh.Signer
		for _, s := range signers {
			for _, k := range allowed {
				if bytes.Equal(k.Marshal(), s.PublicKey().Marshal()) {
					filtered = append(filtered, s)
					break
				}
			}
		}
		signers = filtered
	}
	var keyErr error
	for _, file := range c.IdentityFiles {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		key, err := ssh.ParsePrivateKey(b)
		if err != nil {
			keyErr = err
			continue
		}
		duplicate := false
		for _, s := range signers {
			if bytes.Equal(s.PublicKey().Marshal(), key.PublicKey().Marshal()) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			signers = append(signers, key)
		}
	}
	if len(signers) == 0 {
		if agentConn != nil {
			agentConn.Close()
		}
		if keyErr != nil {
			return nil, nil, fmt.Errorf("No usable SSH key found. Add your encrypted key to ssh-agent or select Private key and enter its passphrase: %w", keyErr)
		}
		if agentErr != nil {
			return nil, nil, fmt.Errorf("No usable SSH key found. Start ssh-agent and add a key, or select a private key: %w", agentErr)
		}
		return nil, nil, errors.New("No usable SSH key found in the agent or SSH config. Add a key or select Private key.")
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signers...)}, agentConn, nil
}
func identityPublicKeys(files []string) []ssh.PublicKey {
	var keys []ssh.PublicKey
	for _, file := range files {
		if b, err := os.ReadFile(file + ".pub"); err == nil {
			if k, _, _, _, err := ssh.ParseAuthorizedKey(b); err == nil {
				keys = append(keys, k)
				continue
			}
		}
		if b, err := os.ReadFile(file); err == nil {
			if s, err := ssh.ParsePrivateKey(b); err == nil {
				keys = append(keys, s.PublicKey())
			}
		}
	}
	return keys
}
