package assets

import (
	"encoding/json"
	"testing"
)

// sounds.json is edited by hand: catch broken JSON and names that don't
// match a sound file.
func TestSoundOdds(t *testing.T) {
	var odds struct{ Throw, Hit map[string]float64 }
	if err := json.Unmarshal(soundOdds, &odds); err != nil {
		t.Fatalf("sounds.json: %v", err)
	}
	throwFiles, hitFiles := Sounds()
	for kind, c := range map[string]struct {
		weights map[string]float64
		files   []string
	}{"throw": {odds.Throw, throwFiles}, "hit": {odds.Hit, hitFiles}} {
		known := map[string]bool{}
		for _, f := range c.files {
			known[f] = true
		}
		any := false
		for name, w := range c.weights {
			if !known[name] {
				t.Errorf("sounds.json %q lists %s, but there's no such file in assets/", kind, name)
			}
			if w < 0 {
				t.Errorf("sounds.json %q: %s has a negative weight", kind, name)
			}
			any = any || w > 0
		}
		if !any && len(c.weights) == len(c.files) {
			t.Errorf("sounds.json %q: every sound is at 0, so throws would be silent", kind)
		}
	}
}
