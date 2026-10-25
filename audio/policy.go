// Package audio decides which applications' sound is shared and captures it.
//
// Windows: one per-process loopback capture per allowed app, mixed in-process.
// Linux:   one PulseAudio stream monitor (parec) per allowed app's stream,
// mixed in-process.
package audio

import (
	"path/filepath"
	"sort"
	"strings"
)

const (
	ModeAll       = "all"       // every app except blocked ones
	ModeWhitelist = "whitelist" // only whitelisted apps (still never blocked ones)
)

// DefaultBlocklist keeps voice chat out of the stream so friends don't hear
// themselves echoed back. Entries match any app whose name contains them.
var DefaultBlocklist = []string{"discord", "teamspeak", "ts3client"}

type Policy struct {
	Mode      string
	Whitelist []string
	Blocklist []string
}

// Normalize turns "C:\...\Discord.exe" or "Discord" into "discord".
func Normalize(name string) string {
	name = strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	return strings.TrimSuffix(name, ".exe")
}

func (p Policy) Blocked(name string) bool {
	n := Normalize(name)
	for _, b := range p.Blocklist {
		if b = Normalize(b); b != "" && strings.Contains(n, b) {
			return true
		}
	}
	return false
}

func (p Policy) Whitelisted(name string) bool {
	n := Normalize(name)
	for _, w := range p.Whitelist {
		if Normalize(w) == n {
			return true
		}
	}
	return false
}

func (p Policy) Allows(name string) bool {
	if p.Blocked(name) {
		return false
	}
	return p.Mode != ModeWhitelist || p.Whitelisted(name)
}

// AppStatus describes one application for the tray menu.
type AppStatus struct {
	Name    string // normalized key, e.g. "chrome"
	Display string // human name, e.g. "Google Chrome"
	Running bool   // currently has an audio stream
	Sharing bool   // its sound goes to viewers
	Blocked bool   // voice chat: never shared
}

// buildStatus merges running apps with whitelisted-but-not-running ones.
func buildStatus(p Policy, running map[string]string) []AppStatus {
	seen := map[string]bool{}
	var out []AppStatus
	for name, display := range running {
		seen[name] = true
		out = append(out, AppStatus{Name: name, Display: display, Running: true,
			Sharing: p.Allows(name), Blocked: p.Blocked(name)})
	}
	for _, w := range p.Whitelist {
		if n := Normalize(w); !seen[n] {
			seen[n] = true
			out = append(out, AppStatus{Name: n, Display: n, Blocked: p.Blocked(n)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display)
	})
	return out
}
