package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useSettingsDir(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), "KamkmDirStat")
	old := settingsDir
	settingsDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { settingsDir = old })
	return dir
}

func TestSettingsSaveAndLoad(t *testing.T) {
	dir := useSettingsDir(t)
	if s := loadSettings(); s != nil {
		t.Fatalf("settings before any were saved: %s", s)
	}
	want := `{"theme":"ocean","mode":"dark","window":{"dark":true,"background":"#0f1012"}}`
	if err := saveSettings(want); err != nil {
		t.Fatal(err)
	}
	if got := string(loadSettings()); got != want {
		t.Fatalf("loaded %s, want %s", got, want)
	}
	if err := saveSettings(`{"theme":"mono"}`); err != nil {
		t.Fatal(err)
	}
	if got := string(loadSettings()); got != `{"theme":"mono"}` {
		t.Fatalf("after a second save, loaded %s", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatalf("the settings folder holds %v, want only settings.json", entries)
	}
}

func TestSettingsRejectsWhatIsNotAnObject(t *testing.T) {
	useSettingsDir(t)
	for _, s := range []string{"", "null", "[]", `"text"`, "{", `{"a":1} trailing`} {
		if err := saveSettings(s); err == nil {
			t.Errorf("saveSettings(%q) succeeded", s)
		}
	}
	big := `{"a":"` + strings.Repeat("x", settingsLimit) + `"}`
	if err := saveSettings(big); err == nil {
		t.Error("saveSettings accepted settings over the size limit")
	}
	if loadSettings() != nil {
		t.Error("a rejected save left settings behind")
	}
}

func TestLoadSettingsIgnoresDamagedFile(t *testing.T) {
	dir := useSettingsDir(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"theme":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := loadSettings(); s != nil {
		t.Fatalf("loaded damaged settings: %s", s)
	}
}

func TestPageWithSettings(t *testing.T) {
	page := "<script id=\"boot\">" + settingsPlaceholder + "</script><p>window.KAMKM_SETTINGS=null;</p>"
	if got := pageWithSettings(page, nil); got != page {
		t.Fatalf("without settings the page changed: %s", got)
	}
	got := pageWithSettings(page, json.RawMessage("{\n \"name\": \"</script><script>alert(1)</script>\" }"))
	if strings.Contains(got, "</script><script>") {
		t.Fatalf("the settings can end the script element: %s", got)
	}
	if !strings.HasPrefix(got, `<script id="boot">window.KAMKM_SETTINGS={"name":"`) || !strings.Contains(got, `u003c/script`) {
		t.Fatalf("settings not placed in the boot script: %s", got)
	}
	if !strings.HasSuffix(got, "<p>window.KAMKM_SETTINGS=null;</p>") {
		t.Fatalf("more than the first placeholder was replaced: %s", got)
	}
	if got := pageWithSettings(page, json.RawMessage(`[1,2]`)); got != page {
		t.Fatalf("settings that are not an object were used: %s", got)
	}
}

func TestPageHasSettingsPlaceholder(t *testing.T) {
	if !strings.Contains(page, `<script id="boot">`+settingsPlaceholder+`</script>`) {
		t.Fatal("index.html no longer has the boot script the settings go into")
	}
}

func TestSavedWindowLook(t *testing.T) {
	look, ok := savedWindowLook(json.RawMessage(`{"window":{"dark":true,"background":"#0f1012","caption":"#16171a","text":"#ececee"}}`))
	if !ok || !look.Dark || look.Background != "#0f1012" || look.Caption != "#16171a" || look.Text != "#ececee" {
		t.Fatalf("got %+v, %v", look, ok)
	}
	for _, s := range []string{``, `{}`, `{"window":null}`, `{"window":3}`} {
		if _, ok := savedWindowLook(json.RawMessage(s)); ok {
			t.Errorf("found a window look in %q", s)
		}
	}
}

func TestParseHexColor(t *testing.T) {
	if r, g, b, ok := parseHexColor("#0F10a2"); !ok || r != 0x0f || g != 0x10 || b != 0xa2 {
		t.Fatalf("got %d %d %d %v", r, g, b, ok)
	}
	for _, s := range []string{"", "#fff", "0f1012", "#0f101", "#0f10123", "#0g1012", "#+f1012", "rgb(1,2,3)"} {
		if _, _, _, ok := parseHexColor(s); ok {
			t.Errorf("parseHexColor(%q) succeeded", s)
		}
	}
}

func TestThemeFileName(t *testing.T) {
	for id, want := range map[string]string{
		"ocean":            "ocean-theme",
		"My Theme!":        "mytheme-theme",
		"../../etc/passwd": "etcpasswd-theme",
		"--":               "theme-theme",
		"":                 "theme-theme",
		"custom-1a2b":      "custom-1a2b-theme",
	} {
		if got := themeFileName(id); got != want {
			t.Errorf("themeFileName(%q) = %q, want %q", id, got, want)
		}
	}
	if got := themeFileName(strings.Repeat("a", 200)); len(got) > 60 {
		t.Errorf("long ids make long names: %q", got)
	}
}

func TestExportThemeDoesNotReplaceFiles(t *testing.T) {
	downloads := t.TempDir()
	old := themeExportDir
	themeExportDir = func() string { return downloads }
	t.Cleanup(func() { themeExportDir = old })
	content := `{"version":1,"id":"ocean","name":"Ocean","appearance":"dark","colors":{}}`
	first, err := exportTheme("ocean", content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := exportTheme("ocean", content)
	if err != nil {
		t.Fatal(err)
	}
	if first != filepath.Join(downloads, "ocean-theme.json") || second != filepath.Join(downloads, "ocean-theme (2).json") {
		t.Fatalf("saved to %s and %s", first, second)
	}
	if b, err := os.ReadFile(second); err != nil || string(b) != content {
		t.Fatalf("read back %q, %v", b, err)
	}
	if _, err := exportTheme("x", "not json"); err == nil {
		t.Fatal("exported something that is not a theme file")
	}
}

func TestXDGDownloadDir(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Téléchargements"), 0o700); err != nil {
		t.Fatal(err)
	}
	read := func(s string) string { return xdgDownloadDir(bufio.NewScanner(strings.NewReader(s)), home) }
	if got := read("# comment\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\nXDG_DOWNLOAD_DIR=\"$HOME/Téléchargements\"\n"); got != filepath.Join(home, "Téléchargements") {
		t.Fatalf("got %q", got)
	}
	for _, s := range []string{"", `XDG_DOWNLOAD_DIR="$HOME/"`, `XDG_DOWNLOAD_DIR="$HOME/missing"`, `XDG_DOWNLOAD_DIR="relative"`} {
		if got := read(s); got != "" {
			t.Errorf("read(%q) = %q", s, got)
		}
	}
}
