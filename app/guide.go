package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"movienight/assets"
)

// WriteGuide saves the "How it works" guide (with the icon inlined) into dir and
// returns its path, so it can be opened in the default browser.
func WriteGuide(dir string) (string, error) {
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString(assets.Icon128)
	html := strings.Replace(string(assets.Guide), "{{ICON}}", icon, 1)
	path := filepath.Join(dir, "movie-night-guide.html")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(html), 0o644)
}
