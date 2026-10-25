//go:build linux

package ui

import (
	"log"
	"time"
)

// The menu's window on Linux: an override-redirect GTK popup with an alpha
// channel, drawn with cairo. It takes the pointer and keyboard while open,
// like GTK's own menus, so a click anywhere else closes it.
type menuWin struct {
	win        uintptr // GtkWindow
	hand, std  uintptr // cursors
	handShown  bool
	grabbed    bool
	grabTries  int
	pressedOut bool // the button went down outside: its release does nothing
}

// Signal and timer numbers for the C callbacks.
const (
	evDraw = iota + 1
	evPress
	evRelease
	evMotion
	evLeave
	evScroll
	evKey
	evGrabBroken
	tmFade
	tmGrab
)

func (m *menu) toggle(items func() []Item) {
	if m.visible {
		m.hide()
		return
	}
	// The click on the icon that broke the grab has just closed it: that
	// click meant "close", not "open again".
	if time.Since(m.closedAt) < 300*time.Millisecond {
		return
	}
	if m.win == 0 {
		m.create()
	}
	m.items = items
	cx, cy := pointer()
	m.ar = monitorArea(cx, cy)
	m.scroll, m.hot, m.focus = 0, "", ""
	m.relayout()
	m.place(cx, cy)
	m.alpha = 0
	m.visible = true
	m.watchSections()
	m.render()
	gtkWidgetShow(m.win)
	m.grabTries = 0
	every(tmGrab, 30, m.grab)
	every(tmFade, 15, func() bool {
		if !m.visible {
			return false
		}
		m.alpha = min(255, m.alpha+64)
		gtkWidgetSetOpacity(m.win, float64(m.alpha)/255)
		return m.alpha < 255
	})
}

func (m *menu) create() {
	m.win = gtkWindowNew(1) // GTK_WINDOW_POPUP
	if v := gdkScreenGetRGBAVisual(gtkWidgetGetScreen(m.win)); v != 0 {
		gtkWidgetSetVisual(m.win, v) // see-through corners and a shadow
	}
	gtkWidgetSetPaintable(m.win, true)
	const (
		pointerMotion = 1 << 2
		buttonPress   = 1 << 8
		buttonRelease = 1 << 9
		keyPress      = 1 << 10
		leaveNotify   = 1 << 13
		scroll        = 1 << 21
		smoothScroll  = 1 << 23
	)
	gtkWidgetAddEvents(m.win, pointerMotion|buttonPress|buttonRelease|keyPress|leaveNotify|scroll|smoothScroll)
	m.hand = gdkCursorNewFromName(display, "pointer")
	m.std = gdkCursorNewFromName(display, "default")

	connect(m.win, "draw", evDraw, func(cr uintptr) bool {
		c := &canvas{cr: cr, w: m.w, h: m.h}
		c.clear()
		if m.visible {
			m.paint(c)
		}
		return true
	})
	connect(m.win, "button-press-event", evPress, func(ev uintptr) bool {
		x, y := coords(ev)
		m.pressedOut = !m.onCard(x, y)
		if m.pressedOut { // clicked elsewhere
			m.hide()
		}
		return true
	})
	connect(m.win, "button-release-event", evRelease, func(ev uintptr) bool {
		var b uint32
		gdkEventGetButton(ev, &b)
		if b == 1 && !m.pressedOut {
			if h := m.hitAt(coords(ev)); h != nil {
				m.activate(*h)
			}
		}
		return true
	})
	connect(m.win, "motion-notify-event", evMotion, func(ev uintptr) bool {
		m.hover(coords(ev))
		m.setCursor()
		return true
	})
	connect(m.win, "leave-notify-event", evLeave, func(uintptr) bool {
		if m.hot != "" {
			m.hot = ""
			m.render()
		}
		m.setCursor()
		return true
	})
	connect(m.win, "scroll-event", evScroll, func(ev uintptr) bool {
		var dir int32
		var dy float64
		gdkEventGetScrollDir(ev, &dir)
		switch dir {
		case 0: // GDK_SCROLL_UP
			dy = -1
		case 1: // GDK_SCROLL_DOWN
			dy = 1
		case 4: // GDK_SCROLL_SMOOTH
			var dx float64
			gdkEventGetScrollDeltas(ev, &dx, &dy)
		}
		m.scroll = max(0, min(m.scroll+float32(dy)*48*m.ar.scale, m.maxScroll()))
		m.hover(coords(ev))
		m.render()
		return true
	})
	connect(m.win, "key-press-event", evKey, func(ev uintptr) bool {
		var k uint32
		gdkEventGetKeyval(ev, &k)
		// The Windows virtual-key codes the menu understands.
		vk := map[uint32]uintptr{0xff1b: 0x1B, 0xff52: 0x26, 0xff54: 0x28, 0xff0d: 0x0D, 0xff8d: 0x0D, 0x20: 0x20}[k]
		if vk != 0 {
			m.key(vk)
		}
		return true
	})
	connect(m.win, "grab-broken-event", evGrabBroken, func(uintptr) bool {
		m.grabbed = false
		m.hide() // something else took over the screen (the overview, a lock)
		return false
	})
}

// grab takes the pointer and keyboard, retrying while GNOME Shell still
// holds them from the click that opened the menu.
func (m *menu) grab() bool {
	if !m.visible || m.grabbed {
		return false
	}
	const all = 15 // GDK_SEAT_CAPABILITY_ALL
	if gdkSeatGrab(seat, gtkWidgetGetWindow(m.win), all, true, 0, 0, 0, 0) == 0 {
		m.grabbed = true
		return false
	}
	if m.grabTries++; m.grabTries == 30 {
		log.Print("ui: can't take the mouse and keyboard for the menu; Esc and clicking elsewhere won't close it")
		return false
	}
	return true
}

func (m *menu) hide() {
	if !m.visible {
		return
	}
	m.visible = false
	m.closedAt = time.Now()
	m.watchSections()
	if m.grabbed {
		gdkSeatUngrab(seat)
		m.grabbed = false
	}
	gtkWidgetHide(m.win)
}

// place hangs the menu from the top bar, under the pointer. With the top bar
// hidden and a dock at the bottom, it grows up from the pointer instead.
func (m *menu) place(cx, cy int32) {
	s, wk := m.ar.scale, m.ar.work
	margin, gap := int32(mMargin*s), int32(8*s)
	m.x = max(wk.Left+gap-margin, min(cx-m.w/2, wk.Right-gap+margin-m.w))
	m.fromTop, m.anchorY = true, wk.Top+gap-margin
	if cy > (wk.Top+wk.Bottom)/2 {
		m.fromTop, m.anchorY = false, min(cy, wk.Bottom-gap+margin)
	}
	m.y = m.anchored(m.h)
}

// render moves and sizes the window, and redraws it soon.
func (m *menu) render() {
	if !m.visible {
		return
	}
	gtkWindowMove(m.win, m.x, m.y)
	gtkWidgetSetSize(m.win, m.w, m.h)
	gtkWindowResize(m.win, m.w, m.h)
	gtkWidgetSetOpacity(m.win, float64(m.alpha)/255)
	gtkWidgetQueueDraw(m.win)
}

// resize is the window's next size (render applies it).
func (m *menu) resize(w, h int32) bool {
	m.w, m.h = w, h
	return true
}

// onCard reports whether x, y (window coordinates) is on the card rather
// than its shadow or outside the window.
func (m *menu) onCard(x, y float32) bool {
	off := mMargin * m.ar.scale
	return x >= off && y >= off && x < float32(m.w)-off && y < float32(m.h)-off
}

func (m *menu) setCursor() {
	h := m.hitByID(m.hot)
	hand := h != nil && !h.off && !h.inert
	if hand != m.handShown {
		m.handShown = hand
		cur := m.std
		if hand {
			cur = m.hand
		}
		gdkWindowSetCursor(gtkWidgetGetWindow(m.win), cur)
	}
}

func coords(ev uintptr) (float32, float32) {
	var x, y float64
	gdkEventGetCoords(ev, &x, &y)
	return float32(x), float32(y)
}
