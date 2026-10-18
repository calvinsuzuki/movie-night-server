// Package platform wraps the OS integration bits: hidden child processes,
// opening URLs, clipboard, autostart, single-instance lock and alerts.
package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

const AppName = "Movie night"

// ConfigDir is where config.json and the log live.
func ConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	name := "MovieNight"
	if runtime.GOOS == "linux" {
		name = "movienight"
	}
	return filepath.Join(base, name)
}

// AutostartLabel is the wording the OS users expect.
func AutostartLabel() string {
	if runtime.GOOS == "windows" {
		return "Start with Windows"
	}
	return "Start at login"
}
