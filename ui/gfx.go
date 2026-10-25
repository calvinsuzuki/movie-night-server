//go:build windows || linux

package ui

// Drawing basics shared by the Windows (Direct2D) and Linux (cairo) canvases.

// Palette (the viewer page's style.css).
var (
	butter    = rgb(0xf8de8d)
	butterHot = rgb(0xffebb0)
	red       = rgb(0xab1313)
	redDeep   = rgb(0x901010)
	salt      = rgb(0xffffff)
	theater   = rgb(0x000000)
	live      = rgb(0x3ccf7a)
	panelBg   = rgb(0x0d0d10)
	muted     = salt.with(0.68)
)

func toneColor(t Tone) color {
	switch t {
	case ToneLive:
		return live
	case ToneWarn:
		return butter
	case ToneBad:
		return red
	}
	return muted
}

type color struct{ R, G, B, A float32 }

// rgb is a palette color; with sets its opacity.
func rgb(hex uint32) color {
	return color{float32(hex>>16&0xff) / 255, float32(hex>>8&0xff) / 255, float32(hex&0xff) / 255, 1}
}

func (c color) with(a float32) color { c.A = a; return c }

type rectF struct{ L, T, R, B float32 }

func (r rectF) w() float32                    { return r.R - r.L }
func (r rectF) h() float32                    { return r.B - r.T }
func (r rectF) inset(dx, dy float32) rectF    { return rectF{r.L + dx, r.T + dy, r.R - dx, r.B - dy} }
func (r rectF) has(x, y float32) bool         { return x >= r.L && x < r.R && y >= r.T && y < r.B }
func (r rectF) shift(dx, dy float32) rectF    { return rectF{r.L + dx, r.T + dy, r.R + dx, r.B + dy} }
func (r rectF) cut(l, t, rr, b float32) rectF { return rectF{r.L + l, r.T + t, r.R - rr, r.B - b} }

// font picks a text style. Text is centered vertically in its box; without
// Wrap it stays on one line and ends in "…" when it doesn't fit.
type font struct {
	Size   float32
	Weight int // 400 regular, 600 semibold, 700 bold
	Align  int // 0 left, 1 right, 2 center
	Wrap   bool
}
