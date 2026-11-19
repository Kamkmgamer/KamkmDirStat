package main

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildPage(t *testing.T) {
	files := fstest.MapFS{
		"index.html": {Data: []byte("<style>\n{{a.css}}\n</style>\n<script>\n{{one.js}}\n{{two.js}}\n</script>\n<p>{{inline}}</p>\n")},
		"a.css":      {Data: []byte("p{color:red}\n")},
		"one.js":     {Data: []byte("let a=1;\n// {{two.js}}\n")},
		"two.js":     {Data: []byte("let b=2;")}, // no final newline
	}
	got, err := buildPage(files)
	if err != nil {
		t.Fatal(err)
	}
	want := "<style>\np{color:red}\n</style>\n<script>\nlet a=1;\n// {{two.js}}\nlet b=2;\n</script>\n<p>{{inline}}</p>\n"
	if got != want {
		t.Fatalf("page:\n%s\nwant:\n%s", got, want)
	}
	delete(files, "two.js")
	if _, err := buildPage(files); err == nil || !strings.Contains(err.Error(), "two.js") {
		t.Fatalf("a missing file was not reported: %v", err)
	}
}

// A file left out of index.html would silently drop part of the interface.
func TestPageIncludesEveryFile(t *testing.T) {
	index, err := fs.ReadFile(uiFiles, "ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(uiFiles, "ui")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "index.html" && strings.Count(string(index), "\n{{"+name+"}}\n") != 1 {
			t.Errorf("ui/index.html does not include %s exactly once", name)
		}
	}
	if includeLine.MatchString(page) {
		t.Error("the page still holds an include line")
	}
}
