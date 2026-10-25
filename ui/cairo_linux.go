//go:build linux

package ui

import "math"

// canvas draws with cairo into the menu window, during its "draw" signal.
type canvas struct {
	cr   uintptr
	w, h int32
}

// gfx measures text; it belongs to the UI thread.
type gfx struct {
	scratch uintptr // a cairo context to lay text out with, off screen
	fonts   map[font]uintptr
}

var gfxDev = &gfx{fonts: map[font]uintptr{}}

const pangoScale = 1024

func (g *gfx) desc(f font) uintptr {
	if d, ok := g.fonts[f]; ok {
		return d
	}
	d := pangoFontDescFromString("Ubuntu, Cantarell, sans-serif")
	pangoFontDescSetAbsSize(d, float64(f.Size*pangoScale))
	pangoFontDescSetWeight(d, int32(f.Weight))
	g.fonts[f] = d
	return d
}

// layout sets s out in font f, at most width wide.
func (g *gfx) layout(cr uintptr, s string, f font, width float32) uintptr {
	l := pangoCairoCreateLayout(cr)
	pangoLayoutSetFont(l, g.desc(f))
	pangoLayoutSetWidth(l, int32(width*pangoScale))
	if f.Wrap {
		pangoLayoutSetWrap(l, 2) // PANGO_WRAP_WORD_CHAR
	} else {
		pangoLayoutSetEllipsize(l, 3) // PANGO_ELLIPSIZE_END
		pangoLayoutSetSinglePar(l, true)
	}
	switch f.Align {
	case 1:
		pangoLayoutSetAlignment(l, 2) // PANGO_ALIGN_RIGHT
	case 2:
		pangoLayoutSetAlignment(l, 1) // PANGO_ALIGN_CENTER
	}
	pangoLayoutSetText(l, s, -1)
	return l
}

// measure returns the size s takes up in font f.
func (g *gfx) measure(s string, f font, maxWidth float32) (w, h float32) {
	if s == "" {
		return 0, f.Size * 1.33
	}
	if g.scratch == 0 {
		g.scratch = cairoCreate(cairoImageSurfaceCreate(0, 1, 1)) // CAIRO_FORMAT_ARGB32
	}
	l := g.layout(g.scratch, s, f, maxWidth)
	defer gObjectUnref(l)
	var tw, th int32
	pangoLayoutGetPixelSize(l, &tw, &th)
	return float32(tw), float32(th)
}

func (c *canvas) color(col color) {
	cairoSetSourceRGBA(c.cr, float64(col.R), float64(col.G), float64(col.B), float64(col.A))
}

// clear makes the whole window transparent.
func (c *canvas) clear() {
	cairoSetOperator(c.cr, 1) // CAIRO_OPERATOR_SOURCE
	cairoSetSourceRGBA(c.cr, 0, 0, 0, 0)
	cairoPaint(c.cr)
	cairoSetOperator(c.cr, 2) // CAIRO_OPERATOR_OVER
}

func (c *canvas) clip(r rectF) {
	cairoSave(c.cr)
	cairoNewPath(c.cr)
	cairoRectangle(c.cr, float64(r.L), float64(r.T), float64(r.w()), float64(r.h()))
	cairoClip(c.cr)
}

func (c *canvas) unclip() { cairoRestore(c.cr) }

func (c *canvas) fillRect(r rectF, col color) {
	cairoNewPath(c.cr)
	cairoRectangle(c.cr, float64(r.L), float64(r.T), float64(r.w()), float64(r.h()))
	c.color(col)
	cairoFill(c.cr)
}

func (c *canvas) round(r rectF, radius float32) {
	rad := float64(min(radius, r.w()/2, r.h()/2))
	l, t, rr, b := float64(r.L), float64(r.T), float64(r.R), float64(r.B)
	cairoNewPath(c.cr)
	cairoNewSubPath(c.cr)
	cairoArc(c.cr, rr-rad, t+rad, rad, -math.Pi/2, 0)
	cairoArc(c.cr, rr-rad, b-rad, rad, 0, math.Pi/2)
	cairoArc(c.cr, l+rad, b-rad, rad, math.Pi/2, math.Pi)
	cairoArc(c.cr, l+rad, t+rad, rad, math.Pi, 3*math.Pi/2)
	cairoClosePath(c.cr)
}

func (c *canvas) fillRound(r rectF, radius float32, col color) {
	c.round(r, radius)
	c.color(col)
	cairoFill(c.cr)
}

func (c *canvas) strokeRound(r rectF, radius, width float32, col color) {
	c.round(r, radius)
	c.color(col)
	cairoSetLineWidth(c.cr, float64(width))
	cairoStroke(c.cr)
}

func (c *canvas) circle(x, y, radius float32) {
	cairoNewPath(c.cr)
	cairoArc(c.cr, float64(x), float64(y), float64(radius), 0, 2*math.Pi)
}

func (c *canvas) fillCircle(x, y, radius float32, col color) {
	c.circle(x, y, radius)
	c.color(col)
	cairoFill(c.cr)
}

func (c *canvas) strokeCircle(x, y, radius, width float32, col color) {
	c.circle(x, y, radius)
	c.color(col)
	cairoSetLineWidth(c.cr, float64(width))
	cairoStroke(c.cr)
}

func (c *canvas) line(x0, y0, x1, y1, width float32, col color) {
	cairoNewPath(c.cr)
	cairoMoveTo(c.cr, float64(x0), float64(y0))
	cairoLineTo(c.cr, float64(x1), float64(y1))
	c.color(col)
	cairoSetLineWidth(c.cr, float64(width))
	cairoSetLineCap(c.cr, 1) // CAIRO_LINE_CAP_ROUND
	cairoStroke(c.cr)
}

// rotate turns what's drawn next by deg degrees around x, y, until unrotate.
func (c *canvas) rotate(deg, x, y float32) {
	cairoSave(c.cr)
	cairoTranslate(c.cr, float64(x), float64(y))
	cairoRotate(c.cr, float64(deg)*math.Pi/180)
	cairoTranslate(c.cr, -float64(x), -float64(y))
}

func (c *canvas) unrotate() { cairoRestore(c.cr) }

// text draws s in r, centered vertically.
func (c *canvas) text(s string, r rectF, f font, col color) {
	if s == "" {
		return
	}
	l := gfxDev.layout(c.cr, s, f, r.w())
	defer gObjectUnref(l)
	var tw, th int32
	pangoLayoutGetPixelSize(l, &tw, &th)
	c.color(col)
	cairoMoveTo(c.cr, float64(r.L), float64(r.T+(r.h()-float32(th))/2))
	pangoCairoShowLayout(c.cr, l)
}
