//go:build !windows

package main

import "os/exec"

func configureSSHCommand(*exec.Cmd) {}
