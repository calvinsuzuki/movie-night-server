//go:build windows

package ui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestPreview draws the menu and the overlay into PNGs, for looking at
// changes without sharing anything:
//
//	MOVIENIGHT_PREVIEW=some/dir go test ./ui -run Preview
func TestPreview(t *testing.T) {
	dir := os.Getenv("MOVIENIGHT_PREVIEW")
	if dir == "" {
		t.Skip("set MOVIENIGHT_PREVIEW to a folder to write previews")
	}
	runtime.LockOSThread()
	g, err := newGfx()
	if err != nil {
		t.Fatal(err)
	}
	gfxDev = g
	nop := func() {}
	items := []Item{
		{Kind: Header, ID: "status", Label: "Movie night", Detail: "Sharing · tender-otter-movie-night.trycloudflare.com/s/k3Jx9", Tone: ToneLive, OnClick: nop},
		{Kind: Primary, ID: "share", Label: "Stop sharing", Tone: ToneBad, OnClick: nop, Children: []Item{
			{ID: "pause", Icon: IconPause, Label: "Resume picture", Checked: true, Tone: ToneWarn, OnClick: nop},
			{ID: "copy", Icon: IconLink, Label: "Copied!", Checked: true, OnClick: nop},
			{ID: "open", Icon: IconEye, Label: "Open viewer page", OnClick: nop},
		}},
		{Kind: Label, ID: "waiting", Label: "Waiting to join"},
		{Kind: Request, ID: "req1", Label: "Ana", Detail: "wants to watch", OnClick: nop, OnAlt: nop},
		{Kind: Label, ID: "watching", Label: "Watching (2)"},
		{Kind: Viewer, ID: "v1", Label: "vanass", Detail: "12 min", OnClick: nop},
		{Kind: Viewer, ID: "v2", Label: "maskes 🐸", Detail: "just joined", OnClick: nop},
		{Kind: Separator, ID: "sep1"},
		{Kind: Section, ID: "screen", Label: "Screen", Detail: "Monitor 1 · 2560×1440"},
		{Kind: Section, ID: "quality", Label: "Quality", Detail: "1080p · 5 Mbps", Children: []Item{
			{Kind: Radio, ID: "q480", Label: "480p · 24 fps", OnClick: nop},
			{Kind: Radio, ID: "q720", Label: "720p · 24 fps", OnClick: nop},
			{Kind: Radio, ID: "q1080", Label: "1080p · 24 fps", Checked: true, OnClick: nop},
			{Kind: Section, ID: "bitrate", Label: "Bitrate (advanced)", Detail: "Automatic"},
		}},
		{Kind: Section, ID: "sound", Label: "Sound", Detail: "All apps"},
		{Kind: Section, ID: "advanced", Label: "Advanced", Children: []Item{
			{Kind: Section, ID: "network", Label: "Network", Detail: "Internet link"},
			{Kind: Section, ID: "overlay", Label: "Screen overlays", Children: []Item{
				{Kind: Toggle, ID: "show", Label: "Watchers and emoji bubbles", Checked: true, OnClick: nop},
				{Kind: Toggle, ID: "hideip", Label: "Hide my IP (relay only)", Disabled: true, OnClick: nop},
				{Kind: Check, ID: "chk", Label: "vlc.exe", Checked: true, OnClick: nop},
				{Kind: Note, ID: "note", Label: "Friends never see these: they're left out of what you share."},
			}},
			{Kind: Toggle, ID: "autostart", Label: "Start with Windows", Checked: true, OnClick: nop},
			{Kind: Action, ID: "guide", Label: "How it works", OnClick: nop},
			{Kind: Action, ID: "log", Label: "Open log", OnClick: nop},
		}},
		{Kind: Separator, ID: "sep2"},
		{Kind: Action, ID: "quit", Label: "Quit Movie night", OnClick: nop},
	}
	for _, scale := range []float32{1, 1.5} {
		m := &menu{open: map[string]bool{"advanced": true, "overlay": true}, visible: true, alpha: 255,
			hot: "status", focus: "", items: func() []Item { return items }}
		m.ar = area{scale: scale, work: winRect{0, 0, 3000, 3000}}
		m.relayout()
		m.render()
		save(t, m.cv, filepath.Join(dir, "menu-"+name(scale)+".png"))

		o := &overlay{scale: scale, pillA: 1, status: Status{Visible: true, Text: "2 watching", Tone: ToneLive}}
		now := time.Now()
		o.pops = []*pop{
			{emoji: "🔗", text: "Invite link copied · paste it to your friends", born: now.Add(-time.Second), life: 4 * time.Second, note: true},
			{emoji: "🍿", text: "vanass", born: now.Add(-time.Second), life: BubbleTime},
			{emoji: "💩", text: "maskes", born: now.Add(-100 * time.Millisecond), life: BubbleTime},
		}
		cv, err := g.newCanvas(int32(ovW*scale), int32(ovH*scale))
		if err != nil {
			t.Fatal(err)
		}
		if err := o.draw(cv, now); err != nil {
			t.Fatal(err)
		}
		save(t, cv, filepath.Join(dir, "overlay-"+name(scale)+".png"))
	}
}

func name(scale float32) string {
	if scale == 1 {
		return "100"
	}
	return "150"
}

// save writes the canvas over a mid-grey "movie", as it would look on screen.
func save(t *testing.T, c *canvas, path string) {
	t.Helper()
	if c == nil {
		t.Fatal("no canvas")
	}
	img := image.NewRGBA(image.Rect(0, 0, int(c.w), int(c.h)))
	px := c.pixels()
	for i := 0; i < len(px); i += 4 {
		b, g, r, a := px[i], px[i+1], px[i+2], px[i+3]
		bg := uint8(70 * int(255-a) / 255) // premultiplied over grey
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r+bg, g+bg, b+bg, 255
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
