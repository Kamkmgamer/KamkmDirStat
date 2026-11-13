package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The page keeps its preferences (theme, light or dark mode, custom themes, the last kind of
// connection, panel sizes) as one JSON object. The app stores it in the user's configuration
// folder and puts it into the page before the page loads, so the theme applies before anything
// is drawn. The page decides what the object holds.

const (
	settingsLimit       = 512 << 10
	settingsPlaceholder = "window.KAMKM_SETTINGS=null;"
)

var settingsMu sync.Mutex

// settingsDir is the folder for the settings file. Tests replace it.
var settingsDir = func() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "KamkmDirStat"), nil
}

func isJSONObject(b []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(b, &v) == nil && v != nil
}

// loadSettings returns the saved settings, or nil when there are none or they cannot be used.
func loadSettings() json.RawMessage {
	dir, err := settingsDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || len(b) > settingsLimit || !isJSONObject(b) {
		return nil
	}
	return b
}

// saveSettings replaces the saved settings. The new file is written beside the old one and
// renamed over it, so an interrupted save leaves the previous settings intact.
func saveSettings(s string) error {
	b := []byte(s)
	if len(b) > settingsLimit {
		return errors.New("the settings are too large to save")
	}
	if !isJSONObject(b) {
		return errors.New("the settings must be a JSON object")
	}
	dir, err := settingsDir()
	if err != nil {
		return err
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "settings-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, "settings.json"))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// pageWithSettings puts the settings into the page. They are escaped for HTML, so nothing in
// them can end the script element they are placed in.
func pageWithSettings(page string, settings json.RawMessage) string {
	value := "null"
	var compact bytes.Buffer
	if settings != nil && json.Compact(&compact, settings) == nil && isJSONObject(compact.Bytes()) {
		var escaped bytes.Buffer
		json.HTMLEscape(&escaped, compact.Bytes())
		value = escaped.String()
	}
	return strings.Replace(page, settingsPlaceholder, "window.KAMKM_SETTINGS="+value+";", 1)
}

// windowLook is how the page last coloured the window, so the window frame and the area behind
// the page can match the theme before the page has loaded.
type windowLook struct {
	Dark       bool   `json:"dark"`
	Background string `json:"background"`
	Caption    string `json:"caption"`
	Text       string `json:"text"`
}

func savedWindowLook(settings json.RawMessage) (windowLook, bool) {
	var s struct {
		Window *windowLook `json:"window"`
	}
	if settings == nil || json.Unmarshal(settings, &s) != nil || s.Window == nil {
		return windowLook{}, false
	}
	return *s.Window, true
}

// parseHexColor reads a colour written as #rrggbb.
func parseHexColor(s string) (r, g, b uint8, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

// exportTheme saves a theme file in the Downloads folder without replacing an existing file,
// and returns where it went.
func exportTheme(id, content string) (string, error) {
	if len(content) > settingsLimit || !isJSONObject([]byte(content)) {
		return "", errors.New("this is not a theme file")
	}
	dir := themeExportDir()
	base := themeFileName(id)
	for i := 1; i <= 1000; i++ {
		name := base + ".json"
		if i > 1 {
			name = fmt.Sprintf("%s (%d).json", base, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.WriteString(content)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path)
			return "", err
		}
		return path, nil
	}
	return "", errors.New("there are too many theme files with this name in " + dir)
}

// themeExportDir is where exported themes go. Tests replace it.
var themeExportDir = downloadsDir

// themeFileName keeps letters, digits and hyphens from a theme id.
func themeFileName(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 48 {
			break
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "theme"
	}
	return name + "-theme"
}

// downloadsDir is the user's Downloads folder, or the home folder when there is none.
func downloadsDir() string {
	if dir := platformDownloadsDir(); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	if dir := filepath.Join(home, "Downloads"); isDir(dir) {
		return dir
	}
	return home
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// xdgDownloadDir finds XDG_DOWNLOAD_DIR="$HOME/Downloads" in a user-dirs.dirs file.
func xdgDownloadDir(lines *bufio.Scanner, home string) string {
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		value, ok := strings.CutPrefix(line, "XDG_DOWNLOAD_DIR=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		if rest, ok := strings.CutPrefix(value, "$HOME"); ok {
			value = home + rest
		}
		value = filepath.Clean(filepath.FromSlash(value))
		// A value equal to the home folder means the folder is turned off.
		if !filepath.IsAbs(value) || value == filepath.Clean(home) || !isDir(value) {
			return ""
		}
		return value
	}
	return ""
}
