package app

import (
	"encoding/json"
	"os"
	"path/filepath"

	"movienight/audio"
)

// Preset is a resolution choice; its bitrate is used unless the sharer picks
// one under "Bitrate (advanced)".
type Preset struct {
	ID     string
	Label  string
	Height int
	Kbps   int
}

const FPS = 24

var Presets = []Preset{
	{"480p", "480p · 24 fps", 480, 1500},
	{"720p", "720p · 24 fps", 720, 3000},
	{"1080p", "1080p · 24 fps", 1080, 5000},
}

// Bitrates offered in the advanced menu, in kbps.
var Bitrates = []int{500, 1000, 1500, 2000, 2500, 3000, 4000, 5000, 6000, 7000, 8000}

func presetByID(id string) Preset {
	for _, p := range Presets {
		if p.ID == id {
			return p
		}
	}
	return Presets[len(Presets)-1]
}

type Config struct {
	Monitor     int      `json:"monitor"`
	Quality     string   `json:"quality"`
	BitrateKbps int      `json:"bitrateKbps"` // 0 = preset default
	ShareAudio  bool     `json:"shareAudio"`
	AudioMode   string   `json:"audioMode"` // audio.ModeAll | audio.ModeWhitelist
	Whitelist   []string `json:"whitelist"`
	Blocklist   []string `json:"blocklist"`
	Port        int      `json:"port"`
	ICEPort     int      `json:"icePort"`
	Encoder     string   `json:"encoder"`  // auto | nvenc | qsv | amf | x264 ...
	AllowLAN    bool     `json:"allowLan"` // accept viewers outside Tailscale
}

func defaultConfig() Config {
	return Config{
		Quality: "1080p", ShareAudio: true, AudioMode: audio.ModeAll,
		Blocklist: append([]string(nil), audio.DefaultBlocklist...),
		Port:      8080, ICEPort: 8443, Encoder: "auto",
	}
}

// EffectiveKbps is the bitrate actually used.
func (c Config) EffectiveKbps() int {
	if c.BitrateKbps > 0 {
		return c.BitrateKbps
	}
	return presetByID(c.Quality).Kbps
}

func (c Config) policy() audio.Policy {
	return audio.Policy{Mode: c.AudioMode, Whitelist: c.Whitelist, Blocklist: c.Blocklist}
}

func loadConfig(path string) Config {
	cfg := defaultConfig()
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &cfg)
	}
	if cfg.Blocklist == nil {
		cfg.Blocklist = append([]string(nil), audio.DefaultBlocklist...)
	}
	if cfg.AudioMode != audio.ModeWhitelist {
		cfg.AudioMode = audio.ModeAll
	}
	return cfg
}

func saveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
