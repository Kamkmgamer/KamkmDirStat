package main

import "runtime"

// Drive is a disk or volume offered for a local scan. Kind is fixed, removable, network,
// optical, ram or boot; only fixed drives are selected at first.
type Drive struct {
	Path       string `json:"path"`
	Label      string `json:"label"`
	FileSystem string `json:"fileSystem"`
	Kind       string `json:"kind"`
	Total      int64  `json:"total"`
	Free       int64  `json:"free"`
}

// localRootName is the top of the report when several drives or folders are scanned.
func localRootName() string {
	switch runtime.GOOS {
	case "windows":
		return "This PC"
	case "darwin":
		return "This Mac"
	}
	return "This computer"
}
