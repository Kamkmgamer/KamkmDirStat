//go:build !windows

package driveindex

import (
	"context"
	"errors"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

var errDrivesWindowsOnly = errors.New("Searching every drive without a scan is available on Windows.")

func Search(context.Context, search.Query) (search.Result, error) {
	return search.Result{}, errDrivesWindowsOnly
}

func Current() Status { return Status{Drives: []DriveState{}} }

func Refresh(string) error { return errDrivesWindowsOnly }

func SetupService(bool) error { return errDrivesWindowsOnly }

func Stop() {}
