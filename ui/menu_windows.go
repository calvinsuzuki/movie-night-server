//go:build windows

package ui

import (
	"time"
	"unsafe"
)

// The menu's window on Windows: a layered window drawn with Direct2D.
type menuWin struct {
	hwnd     uintptr
	cv       *canvas
	tracking bool
}

func (m *menu) toggle(items func() []Item) {
	if m.visible {
		m.hide()
		return
	}
	// The click on the icon that took focus away has just closed it: that
	// click meant "close", not "open again".
	if time.Since(m.closedAt) < 300*time.Millisecond {
		return
	}
	if m.hwnd == 0 {
		if m.hwnd = newLayered(0, m.handle); m.hwnd == 0 {
			return
		}
	}
	m.items = items
	cx, cy := cursorPos()
	m.ar = monitorArea(monitorAt(cx, cy))
	m.scroll, m.hot, m.focus = 0, "", ""
	m.relayout()
	if m.cv == nil {
		return
	}
	m.place(cx, cy)
	m.alpha = 0
	m.visible = true
	m.watchSections()
	m.render()
	procShowWindow.Call(m.hwnd, swShow)
	procSetForegroundWindow.Call(m.hwnd)
	procSetFocus.Call(m.hwnd)
	procSetTimer.Call(m.hwnd, 1, 15, 0)  // fade in
	procSetTimer.Call(m.hwnd, 2, 100, 0) // close on clicks elsewhere, even without focus
}

func (m *menu) hide() {
	if !m.visible {
		return
	}
	m.visible = false
	m.closedAt = time.Now()
	m.watchSections()
	procKillTimer.Call(m.hwnd, 1)
	procKillTimer.Call(m.hwnd, 2)
	procShowWindow.Call(m.hwnd, swHide)
}

// place puts the menu next to the taskbar, by the pointer.
func (m *menu) place(cx, cy int32) {
	s, wk := m.ar.scale, m.ar.work
	w, h := m.cv.w, m.cv.h
	margin, gap := int32(mMargin*s), int32(8*s)
	m.x = cx - w/2
	switch {
	case cx < wk.Left: // taskbar on the left
		m.x = wk.Left + gap - margin
	case cx >= wk.Right: // on the right
		m.x = wk.Right - gap + margin - w
	}
	m.x = max(wk.Left+gap-margin, min(m.x, wk.Right-gap+margin-w))
	switch {
	case cy < wk.Top: // taskbar on top: hang down from it
		m.fromTop, m.anchorY = true, wk.Top+gap-margin
	case cy >= wk.Bottom: // the usual: grow up from the taskbar
		m.fromTop, m.anchorY = false, wk.Bottom-gap+margin
	default:
		m.fromTop, m.anchorY = false, min(cy, wk.Bottom-gap+margin)
	}
	m.y = m.anchored(h)
}

func (m *menu) handle(hwnd uintptr, message uint32, wparam, lparam uintptr) (uintptr, bool) {
	switch message {
	case wmActivate:
		if wparam&0xFFFF == 0 { // WA_INACTIVE: clicked elsewhere
			m.hide()
		}
		return 0, true
	case wmTimer:
		switch wparam {
		case 1:
			if m.alpha = min(255, m.alpha+64); m.alpha == 255 {
				procKillTimer.Call(hwnd, 1)
			}
			m.render()
		case 2:
			m.closeIfClickedOutside()
		}
		return 0, true
	case wmMouseMove:
		if !m.tracking {
			tme := trackMouseEvent{Flags: 0x2, Track: hwnd} // TME_LEAVE
			tme.Size = uint32(unsafe.Sizeof(tme))
			procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			m.tracking = true
		}
		m.hover(lparamXY(lparam))
		return 0, true
	case wmMouseLeave:
		m.tracking = false
		if m.hot != "" {
			m.hot = ""
			m.render()
		}
		return 0, true
	case wmSetCursor:
		if h := m.hitByID(m.hot); h != nil && !h.off && !h.inert {
			handCursor()
			return 1, true
		}
	case wmLButtonUp:
		if h := m.hitAt(lparamXY(lparam)); h != nil {
			m.activate(*h)
		}
		return 0, true
	case wmMouseWheel:
		delta := float32(int16(wparam >> 16))
		m.scroll = max(0, min(m.scroll-delta/120*48*m.ar.scale, m.maxScroll()))
		cx, cy := cursorPos()
		m.hover(float32(cx-m.x), float32(cy-m.y))
		m.render()
		return 0, true
	case wmKeyDown:
		m.key(wparam)
		return 0, true
	}
	return 0, false
}

func (m *menu) closeIfClickedOutside() {
	if fg, _, _ := procGetForegroundWindow.Call(); fg == m.hwnd {
		return // focus closes it as usual
	}
	l, _, _ := procGetAsyncKeyState.Call(0x01)
	r, _, _ := procGetAsyncKeyState.Call(0x02)
	if l&0x8000 == 0 && r&0x8000 == 0 {
		return
	}
	cx, cy := cursorPos()
	if cx < m.x || cy < m.y || cx >= m.x+m.cv.w || cy >= m.y+m.cv.h {
		m.hide()
	}
}

func (m *menu) render() {
	if m.cv == nil || !m.visible {
		return
	}
	c := m.cv
	c.begin()
	m.paint(c)
	if c.end() != nil {
		c.close()
		m.cv = nil
		return
	}
	present(m.hwnd, c, m.x, m.y, byte(m.alpha))
}

// resize makes the canvas w×h.
func (m *menu) resize(w, h int32) bool {
	if m.cv == nil || m.cv.w != w || m.cv.h != h {
		if m.cv != nil {
			m.cv.close()
		}
		var err error
		if m.cv, err = gfxDev.newCanvas(w, h); err != nil {
			m.cv = nil
			return false
		}
	}
	m.w, m.h = w, h
	return true
}
