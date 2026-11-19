// Package drives lists the disks and volumes of this computer.
package drives

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
