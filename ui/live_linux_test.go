//go:build linux

package ui

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestLive opens the real menu on screen for a few seconds, saving a
// screenshot to MOVIENIGHT_SHOT if set: MOVIENIGHT_LIVE=1 go test ./ui -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("MOVIENIGHT_LIVE") == "" {
		t.Skip("set MOVIENIGHT_LIVE=1 to open the real menu")
	}
	if !Supported() {
		t.Fatal("not supported")
	}
	items := func() []Item {
		return []Item{
			{Kind: Header, ID: "status", Label: "Movie night", Detail: "Sharing · fancy-words.trycloudflare.com", Tone: ToneLive, OnClick: func() {}},
			{Kind: Primary, ID: "share", Label: "Stop sharing", Tone: ToneBad, OnClick: func() {}, Children: []Item{
				{Kind: Action, ID: "pause", Icon: IconPause, Label: "Pause picture", OnClick: func() {}},
				{Kind: Action, ID: "copy", Icon: IconLink, Label: "Copy invite link", OnClick: func() {}},
				{Kind: Action, ID: "open", Icon: IconEye, Label: "Open viewer page", OnClick: func() {}},
			}},
			{Kind: Label, ID: "waiting", Label: "Waiting to join"},
			{Kind: Request, ID: "req", Label: "Ana", Detail: "wants to watch", OnClick: func() {}, OnAlt: func() {}},
			{Kind: Label, ID: "watching", Label: "Watching (1)"},
			{Kind: Viewer, ID: "v", Label: "Bruno", Detail: "12 min", OnClick: func() {}},
			{Kind: Separator, ID: "sep"},
			{Kind: Section, ID: "screen", Label: "Screen", Detail: "DP-2 · 1920×1080", Children: []Item{
				{Kind: Radio, ID: "m1", Label: "DP-2 · 1920×1080", Checked: true, OnClick: func() {}},
				{Kind: Radio, ID: "m2", Label: "HDMI-0 · 1920×1080", OnClick: func() {}},
			}},
			{Kind: Section, ID: "sound", Label: "Sound", Detail: "All apps", Children: []Item{
				{Kind: Toggle, ID: "share sound", Label: "Share sound", Checked: true, OnClick: func() {}},
				{Kind: Check, ID: "app", Label: "Google Chrome", Checked: true, OnClick: func() {}},
				{Kind: Note, ID: "note", Label: "Friends hear the apps checked here, never voice chat."},
			}},
			{Kind: Separator, ID: "sep2"},
			{Kind: Action, ID: "quit", Label: "Quit Movie night", OnClick: func() {}},
		}
	}
	mn.open["sound"] = true
	ShowMenu(items)
	time.Sleep(1500 * time.Millisecond)
	if shot := os.Getenv("MOVIENIGHT_SHOT"); shot != "" {
		exec.Command("import", "-window", "root", "-crop", "1920x1080+0+0", shot).Run()
	}
	visible := make(chan bool)
	run(func() { visible <- mn.visible })
	if !<-visible {
		t.Fatal("the menu closed by itself")
	}
	time.Sleep(2 * time.Second)
}
