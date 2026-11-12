// buildhelper builds the native server scanner, cmd/kamkm-helper, for Linux x64 and ARM64
// and stores compressed copies in helperbin, where the app embeds them. Run it from the
// repository root before building the app: go run ./tools/buildhelper
package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	tmp, err := os.MkdirTemp("", "kamkm-helper")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(tmp)
	for _, arch := range []string{"amd64", "arm64"} {
		out := filepath.Join(tmp, arch)
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -buildid=", "-o", out, "./cmd/kamkm-helper")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fail(fmt.Errorf("build for %s: %w", arch, err))
		}
		program, err := os.ReadFile(out)
		if err != nil {
			fail(err)
		}
		var buf bytes.Buffer
		z, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		z.Write(program)
		z.Close()
		target := filepath.Join("helperbin", "kamkm-helper-linux-"+arch+".gz")
		if err := os.WriteFile(target, buf.Bytes(), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("%s: %d bytes, %d compressed\n", target, len(program), buf.Len())
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "buildhelper:", err)
	os.Exit(1)
}
