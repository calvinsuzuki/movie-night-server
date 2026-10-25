//go:build windows

package ui

// While the Screen section is open, each monitor shows its number in the
// middle, big, so you can tell which is which. The one being shared is
// butter with a tick, the others dark. Like the overlay, friends never see them.

const idSize = 200 // card size before scaling

type idWin struct {
	hwnd  uintptr
	cv    *canvas
	shown bool
}

var idWins []*idWin

// Identify shows marks on their monitors, replacing any shown before; nil
// hides them.
func Identify(marks []Mark) { run(func() { identify(marks) }) }

func identify(marks []Mark) {
	for len(idWins) < len(marks) {
		w := &idWin{}
		if w.hwnd = newLayered(exTransparent|exNoActivate, w.handle); w.hwnd == 0 {
			return
		}
		idWins = append(idWins, w)
	}
	for i, w := range idWins {
		if i >= len(marks) {
			if w.shown {
				procShowWindow.Call(w.hwnd, swHide)
				w.shown = false
			}
			continue
		}
		w.show(marks[i])
	}
}

func (w *idWin) handle(hwnd uintptr, message uint32, wparam, lparam uintptr) (uintptr, bool) {
	if message == wmMouseActivate {
		return 3, true // MA_NOACTIVATE
	}
	return 0, false
}

func (w *idWin) show(mk Mark) {
	ar := monitorArea(sharedMonitor(mk.Adapter, mk.Output))
	s := ar.scale
	size := int32((idSize + 24) * s) // room for the shadow
	if w.cv == nil || w.cv.w != size {
		if w.cv != nil {
			w.cv.close()
		}
		var err error
		if w.cv, err = gfxDev.newCanvas(size, size); err != nil {
			w.cv = nil
			return
		}
	}
	c := w.cv
	c.begin()
	card := rectF{12 * s, 8 * s, 12*s + idSize*s, 8*s + idSize*s}
	bg, fg, sub := panelBg.with(0.92), butter, muted
	if mk.Selected {
		bg, fg, sub = butter, theater, theater.with(0.65)
	}
	shadow(c, card, 28*s, s, 1)
	c.fillRound(card, 28*s, bg)
	c.strokeRound(card.inset(1.5*s, 1.5*s), 26.5*s, 3*s, butter.with(0.55))
	c.text(mk.Label, rectF{card.L, card.T, card.R, card.B - 34*s}, font{Size: 120 * s, Weight: 800, Align: 2}, fg)
	detail := mk.Detail
	if mk.Selected {
		detail = "✓ " + detail
	}
	c.text(detail, rectF{card.L + 10*s, card.B - 52*s, card.R - 10*s, card.B - 18*s}, font{Size: 14 * s, Weight: 600, Align: 2}, sub)
	if c.end() != nil {
		c.close()
		w.cv = nil
		return
	}
	wk := ar.work
	x := (wk.Left+wk.Right)/2 - size/2
	y := (wk.Top+wk.Bottom)/2 - size/2
	present(w.hwnd, c, x, y, 255)
	if !w.shown {
		procShowWindow.Call(w.hwnd, swShowNoActivate)
		w.shown = true
	}
	keepOnTop(w.hwnd)
}
