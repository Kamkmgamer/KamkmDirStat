//go:build !windows && !cgo

package main

import (
	"fmt"
	"os"
)

// The Linux and macOS window needs cgo. Builds without it, such as test-only builds, still
// compile and explain how to build the app.
func main() {
	fmt.Fprintln(os.Stderr, "This copy of KamkmDirStat was built without cgo, so it cannot open a window. Build it with CGO_ENABLED=1; see README.md.")
	os.Exit(1)
}
