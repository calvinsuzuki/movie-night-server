package app

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"movienight/assets"
	"movienight/stream"
)

const (
	TailscaleMissing   = "missing"   // not installed
	TailscaleSignedOut = "signedout" // installed but no tailnet address
	TailscaleReady     = "ready"
)

// TailscaleState reports whether Tailscale is installed and connected.
func TailscaleState() string {
	if stream.TailscaleIP() != nil {
		return TailscaleReady
	}
	if _, err := exec.LookPath("tailscale"); err == nil {
		return TailscaleSignedOut
	}
	if runtime.GOOS == "windows" {
		for _, dir := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if _, err := os.Stat(filepath.Join(dir, "Tailscale", "tailscale.exe")); dir != "" && err == nil {
				return TailscaleSignedOut
			}
		}
	}
	return TailscaleMissing
}

func tailscaleStatus(state string) string {
	switch state {
	case TailscaleMissing:
		return "Tailscale isn't installed — see the Tailscale guide"
	case TailscaleSignedOut:
		return "Sign in to Tailscale to start sharing"
	}
	return ""
}

// WriteGuide saves the Tailscale guide (with the icon inlined) into dir and
// returns its path, so it can be opened in the default browser.
func WriteGuide(dir string) (string, error) {
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString(assets.Icon128)
	html := strings.Replace(string(assets.Guide), "{{ICON}}", icon, 1)
	path := filepath.Join(dir, "tailscale-guide.html")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(html), 0o644)
}
