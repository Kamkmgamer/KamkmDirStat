//go:build !windows

package main

import (
	"errors"
	"io"
)

var errNeedsAdmin = errors.New("reading an NTFS drive directly needs administrator rights")

// Direct NTFS reading is only used on Windows; other systems list folders.
func openNTFSVolume(string) (io.ReaderAt, func(), error) { return nil, nil, errNotNTFS }
