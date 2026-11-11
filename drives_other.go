//go:build !windows && !linux && !darwin

package main

func listDrives() []Drive { return []Drive{{Path: "/", Label: "System", Kind: "fixed"}} }
