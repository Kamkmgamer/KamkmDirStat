// Package fslist lists the folders of this computer for scans and for the drive index.
// Sizes are file sizes, as in Explorer. Links, junctions and volumes mounted in a folder
// are left out, so nothing is reached twice through them.
package fslist

type Kind int

const (
	File Kind = iota
	Dir
)

// Entry is one item of a folder.
type Entry struct {
	Name string
	Kind Kind
	Size int64
}

// Workers is how many folders to list at once. More mostly contend inside the file system
// on Windows.
const Workers = 8
