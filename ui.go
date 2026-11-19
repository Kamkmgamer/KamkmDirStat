package main

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

// The interface is kept in the ui folder as one HTML file, its style sheet and its scripts,
// and shown as a single page: a webview given HTML directly has no address to load other
// files from. A line of ui/index.html holding only {{name}} stands for the file of that
// name. The scripts are put into one script element in the order listed, so they share
// their top-level names as if they were one file.

//go:embed ui
var uiFiles embed.FS

var page = mustBuildPage()

var includeLine = regexp.MustCompile(`(?m)^\{\{([\w.]+)\}\}\n`)

func mustBuildPage() string {
	files, err := fs.Sub(uiFiles, "ui")
	if err == nil {
		var p string
		if p, err = buildPage(files); err == nil {
			return p
		}
	}
	panic(err)
}

// buildPage puts the files index.html names into it.
func buildPage(files fs.FS) (string, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return "", err
	}
	var failed error
	page := includeLine.ReplaceAllStringFunc(string(index), func(line string) string {
		name := includeLine.FindStringSubmatch(line)[1]
		data, err := fs.ReadFile(files, name)
		if err != nil {
			failed = fmt.Errorf("ui/index.html includes %s: %w", name, err)
			return line
		}
		if s := string(data); strings.HasSuffix(s, "\n") {
			return s
		}
		return string(data) + "\n"
	})
	return page, failed
}
