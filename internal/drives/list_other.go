//go:build !windows && !linux && !darwin

package drives

func List() []Drive { return []Drive{{Path: "/", Label: "System", Kind: "fixed"}} }
