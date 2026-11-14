//go:build !windows

package main

import (
	"context"
	"errors"

	"github.com/Kamkmgamer/KamkmDirStat/internal/search"
)

var errDrivesWindowsOnly = errors.New("Searching every drive without a scan is available on Windows.")

func searchDrives(context.Context, search.Query) (search.Result, error) {
	return search.Result{}, errDrivesWindowsOnly
}

func driveIndexStatus() DriveIndexStatus { return DriveIndexStatus{Drives: []DriveIndexState{}} }

func refreshDriveIndex(string) error { return errDrivesWindowsOnly }

func startIndexServiceSetup(bool) error { return errDrivesWindowsOnly }

func stopDriveIndex() {}
