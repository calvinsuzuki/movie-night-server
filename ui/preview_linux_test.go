//go:build linux

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebitengine/purego"
)

// TestPreview draws the menu into a PNG, for looking at changes without
// opening it on screen:
//
//	MOVIENIGHT_PREVIEW=some/dir go test ./ui -run Preview
func TestPreview(t *testing.T) {
	dir := os.Getenv("MOVIENIGHT_PREVIEW")
	if dir == "" {
		t.Skip("set MOVIENIGHT_PREVIEW to a folder to write previews")
	}
	if !Supported() {
		t.Fatal("no GTK")
	}
	lib, err := purego.Dlopen("libcairo.so.2", purego.RTLD_NOW)
	if err != nil {
		t.Fatal(err)
	}
	var writePNG func(surface uintptr, path string) int32
	purego.RegisterLibFunc(&writePNG, lib, "cairo_surface_write_to_png")
	var cairoGetTarget func(cr uintptr) uintptr
	purego.RegisterLibFunc(&cairoGetTarget, lib, "cairo_get_target")

	nop := func() {}
	items := []Item{
		{Kind: Header, ID: "status", Label: "Movie night", Badge: "v0.4.0 nightly", Detail: "Sharing · tender-otter-movie-night.trycloudflare.com/s/k3Jx9", Tone: ToneLive, OnClick: nop},
		{Kind: Primary, ID: "share", Label: "Stop sharing", Tone: ToneBad, OnClick: nop, Children: []Item{
			{ID: "pause", Icon: IconPause, Label: "Resume picture", Checked: true, Tone: ToneWarn, OnClick: nop},
			{ID: "copy", Icon: IconLink, Label: "Copied!", Checked: true, OnClick: nop},
			{ID: "open", Icon: IconEye, Label: "Open viewer page", OnClick: nop},
		}},
		{Kind: Label, ID: "waiting", Label: "Waiting to join"},
		{Kind: Request, ID: "req1", Label: "Ana", Detail: "wants to watch", OnClick: nop, OnAlt: nop},
		{Kind: Label, ID: "watching", Label: "Watching (1)"},
		{Kind: Viewer, ID: "v1", Label: "vanass", Detail: "12 min", OnClick: nop},
		{Kind: Separator, ID: "sep1"},
		{Kind: Section, ID: "screen", Label: "Screen", Detail: "DP-2 · 1920×1080"},
		{Kind: Section, ID: "sound", Label: "Sound", Detail: "All apps", Children: []Item{
			{Kind: Toggle, ID: "share sound", Label: "Share sound", Checked: true, OnClick: nop},
			{Kind: Check, ID: "app", Label: "Google Chrome", Checked: true, OnClick: nop},
			{Kind: Note, ID: "note", Label: "Friends hear the apps checked here, never voice chat."},
		}},
		{Kind: Separator, ID: "sep2"},
		{Kind: Action, ID: "quit", Label: "Quit Movie night", OnClick: nop},
	}
	path := filepath.Join(dir, "menu.png")
	done := make(chan int32)
	run(func() {
		m := &menu{open: map[string]bool{"sound": true}, items: func() []Item { return items }}
		m.ar = area{work: winRect{0, 0, 1920, 1080}, scale: 1}
		m.relayout()
		cr := cairoCreate(cairoImageSurfaceCreate(0, m.w, m.h))
		c := &canvas{cr: cr, w: m.w, h: m.h}
		c.clear()
		m.paint(c)
		done <- writePNG(cairoGetTarget(cr), path)
	})
	if st := <-done; st != 0 {
		t.Fatalf("cairo status %d", st)
	}
	t.Log("wrote", path)
}
