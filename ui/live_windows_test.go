//go:build windows

package ui

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

var (
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowDisplayAffinity = user32.NewProc("GetWindowDisplayAffinity")
	procGetWindowLongPtr         = user32.NewProc("GetWindowLongPtrW")
	procSendMessage              = user32.NewProc("SendMessageW")
)

// onUI runs fn on the UI thread and waits for it.
func onUI(fn func()) {
	done := make(chan struct{})
	run(func() { fn(); close(done) })
	<-done
}

// TestLive opens the real windows on screen and drives them with window
// messages: MOVIENIGHT_LIVE=1 go test ./ui -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("MOVIENIGHT_LIVE") == "" {
		t.Skip("set MOVIENIGHT_LIVE=1 to open real windows")
	}
	if !Supported() {
		t.Fatal("not supported")
	}
	var clicked, kept, alt, kicked atomic.Int32
	items := func() []Item {
		return []Item{
			{Kind: Header, ID: "status", Label: "Movie night", Detail: "Sharing · 1 watching", Tone: ToneLive},
			{Kind: Primary, ID: "share", Label: "Stop sharing", Tone: ToneBad, OnClick: func() { clicked.Add(1) }},
			{Kind: Toggle, ID: "toggle", Label: "A switch", OnClick: func() { kept.Add(1) }, KeepOpen: true},
			{Kind: Request, ID: "req", Label: "Ana", Detail: "wants to watch", OnClick: func() { kept.Add(1) }, OnAlt: func() { alt.Add(1) }},
			{Kind: Section, ID: "sec", Label: "Section", Children: []Item{{Kind: Radio, ID: "r1", Label: "One", OnClick: func() {}}}},
			{Kind: Viewer, ID: "v", Label: "Ana", OnClick: func() { kicked.Add(1) }, KeepOpen: true},
		}
	}

	SetStatus(Status{Visible: true, Text: "1 watching", Tone: ToneLive})
	Bubble("🍿", "vanass", BubbleTime)
	Toast("🔗", "Invite link copied", 3*time.Second)
	time.Sleep(500 * time.Millisecond)
	onUI(func() {
		if ov.hwnd == 0 {
			t.Fatal("no overlay window")
		}
		vis, _, _ := procIsWindowVisible.Call(ov.hwnd)
		var r winRect
		procGetWindowRect.Call(ov.hwnd, uintptr(unsafe.Pointer(&r)))
		var aff uint32
		procGetWindowDisplayAffinity.Call(ov.hwnd, uintptr(unsafe.Pointer(&aff)))
		ex, _, _ := procGetWindowLongPtr.Call(ov.hwnd, ^uintptr(19)) // GWL_EXSTYLE (-20)
		t.Logf("overlay visible=%d rect=%+v affinity=0x%x exstyle=0x%x ticking=%v pops=%d", vis, r, aff, ex, ov.ticking, len(ov.pops))
		if vis == 0 || aff != 0x11 || ex&exTransparent == 0 {
			t.Error("overlay should be visible, click-through and left out of capture")
		}
	})
	time.Sleep(4 * time.Second) // bubbles and the toast are gone by now
	onUI(func() {
		t.Logf("after 4 s: pops=%d ticking=%v", len(ov.pops), ov.ticking)
		if len(ov.pops) != 0 || ov.ticking {
			t.Error("bubbles should have expired and the frame timer stopped")
		}
	})

	ShowMenu(items)
	time.Sleep(300 * time.Millisecond)
	var hits []hit
	onUI(func() {
		vis, _, _ := procIsWindowVisible.Call(mn.hwnd)
		var r winRect
		procGetWindowRect.Call(mn.hwnd, uintptr(unsafe.Pointer(&r)))
		t.Logf("menu visible=%d rect=%+v alpha=%d work=%+v scale=%v", vis, r, mn.alpha, mn.ar.work, mn.ar.scale)
		if vis == 0 || r.Bottom > mn.ar.work.Bottom+int32(mMargin*mn.ar.scale) {
			t.Error("menu should be showing, above the taskbar")
		}
		hits = append(hits, mn.hits...)
	})
	click := func(id string) {
		for _, h := range hits {
			if h.id == id {
				off := mMargin * mn.ar.scale
				x, y := int32(h.r.L+off+4), int32(h.r.T+off-mn.scroll+4)
				procSendMessage.Call(mn.hwnd, wmLButtonUp, 0, uintptr(uint32(x)|uint32(y)<<16))
				return
			}
		}
		t.Fatalf("no hit %q", id)
	}
	click("toggle")
	click("req/no")
	click("v") // the name: nothing happens
	if kicked.Load() != 0 {
		t.Error("clicking a viewer's name kicked them")
	}
	click("v/kick")
	click("sec")
	time.Sleep(200 * time.Millisecond)
	onUI(func() {
		t.Logf("after clicks: open=%v visible=%v hits=%d", mn.open, mn.visible, len(mn.hits))
		if !mn.visible || !mn.open["sec"] || len(mn.hits) != len(hits)+1 {
			t.Error("switches and sections should keep the menu open; the section should show its child")
		}
	})
	procSendMessage.Call(mn.hwnd, wmKeyDown, 0x28, 0) // down: first usable row
	procSendMessage.Call(mn.hwnd, wmKeyDown, 0x0D, 0) // Enter: Stop sharing
	time.Sleep(200 * time.Millisecond)
	onUI(func() {
		t.Logf("after Enter: visible=%v", mn.visible)
		if mn.visible {
			t.Error("a plain action should close the menu")
		}
	})
	if clicked.Load() != 1 || kept.Load() != 1 || alt.Load() != 1 || kicked.Load() != 1 {
		t.Errorf("callbacks: primary %d, kept %d, alt %d, kick %d; want 1 each", clicked.Load(), kept.Load(), alt.Load(), kicked.Load())
	}
	time.Sleep(400 * time.Millisecond) // a click right after closing counts as "close", not "open again"
	ShowMenu(items)
	time.Sleep(300 * time.Millisecond)
	onUI(func() {
		if !mn.visible {
			t.Error("the menu should open again")
		}
	})
	procSendMessage.Call(mn.hwnd, wmKeyDown, 0x1B, 0) // Esc
	onUI(func() {
		if mn.visible {
			t.Error("Esc should close the menu")
		}
	})
	SetStatus(Status{})
	time.Sleep(600 * time.Millisecond)
	onUI(func() {
		vis, _, _ := procIsWindowVisible.Call(ov.hwnd)
		if vis != 0 {
			t.Error("overlay should hide when there's nothing to show")
		}
	})
}
