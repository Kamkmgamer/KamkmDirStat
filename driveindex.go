package main

import "github.com/Kamkmgamer/KamkmDirStat/internal/search"

// Searching every drive does not need a scan. Each drive has an index of every file and
// folder name, made once and then kept up to date:
//
//   - On NTFS, when the app runs as administrator, the index is read straight from the
//     master file table in seconds and then follows the drive's change journal, so it is
//     always current, even across restarts.
//   - Otherwise the drive's folders are read like a scan, and the index is a snapshot that
//     is read again when it gets old or on request.
//   - A background service, set up once by an administrator, keeps live indexes of every
//     drive, so the app can search them all instantly without administrator rights.
//
// Indexes are saved between runs. In the app they leave memory a few minutes after the
// last search; the service keeps them.

// DriveIndexState is one drive's index.
type DriveIndexState struct {
	Root    string `json:"root"`
	Label   string `json:"label,omitempty"`
	Mode    string `json:"mode,omitempty"`  // "live" (follows the change journal) or "snapshot"
	State   string `json:"state"`           // "loading", "building", "ready" or "error"
	Entries int    `json:"entries"`         // files and folders in the index
	Read    int64  `json:"read,omitempty"`  // entries read so far while building
	Built   int64  `json:"built,omitempty"` // when the folders were last read completely, Unix ms
	Error   string `json:"error,omitempty"`
}

// DriveIndexStatus says how every-drive search works at the moment.
type DriveIndexStatus struct {
	Supported bool              `json:"supported"`
	Admin     bool              `json:"admin"`             // this window can read drives directly
	Source    string            `json:"source,omitempty"`  // "service" or "app": who answers searches
	Service   string            `json:"service,omitempty"` // "", "running", "stopped", "starting" or "unreachable"
	Outdated  bool              `json:"outdated,omitempty"`
	Note      string            `json:"note,omitempty"`
	Setup     string            `json:"setup,omitempty"` // "install" or "remove" while the background indexer is set up
	SetupErr  string            `json:"setupError,omitempty"`
	Drives    []DriveIndexState `json:"drives"`
}

// indexServiceRequest is a line sent to the background indexer.
type indexServiceRequest struct {
	Op    string        `json:"op"` // "status", "search" or "refresh"
	Query *search.Query `json:"query,omitempty"`
	Root  string        `json:"root,omitempty"`
}

// indexServiceReply is its answer.
type indexServiceReply struct {
	Status *DriveIndexStatus `json:"status,omitempty"`
	Result *search.Result    `json:"result,omitempty"`
	Build  string            `json:"build,omitempty"` // identifies the service's program file
	Error  string            `json:"error,omitempty"`
}
