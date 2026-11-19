//go:build !windows

package ntfs

import (
	"errors"
	"io"
)

var ErrNeedsAdmin = errors.New("reading an NTFS drive directly needs administrator rights")

// Direct NTFS reading is only used on Windows; other systems list folders.
func OpenVolume(string) (io.ReaderAt, func(), error) { return nil, nil, ErrNotNTFS }
