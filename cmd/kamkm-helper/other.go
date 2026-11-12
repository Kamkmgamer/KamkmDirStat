//go:build !linux

// kamkm-helper only runs on Linux servers; this keeps "go build ./..." working elsewhere.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "kamkm-helper runs on Linux servers only.")
	os.Exit(2)
}
