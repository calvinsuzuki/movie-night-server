//go:build windows

package ui

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Direct2D and DirectWrite, called through their COM vtables: smooth shapes,
// per-pixel transparency and color emoji, none of which GDI can draw. A
// canvas paints into a 32-bit bitmap that a layered window then shows.

var (
	d2d1   = windows.NewLazySystemDLL("d2d1.dll")
	dwrite = windows.NewLazySystemDLL("dwrite.dll")
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procD2D1CreateFactory   = d2d1.NewProc("D2D1CreateFactory")
	procDWriteCreateFactory = dwrite.NewProc("DWriteCreateFactory")
	procCreateCompatibleDC  = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection    = gdi32.NewProc("CreateDIBSection")
	procSelectObject        = gdi32.NewProc("SelectObject")
	procDeleteObject        = gdi32.NewProc("DeleteObject")
	procDeleteDC            = gdi32.NewProc("DeleteDC")

	iidD2D1Factory   = windows.GUID{Data1: 0x06152247, Data2: 0x6f50, Data3: 0x465a, Data4: [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07}}
	iidDWriteFactory = windows.GUID{Data1: 0xb859ee5a, Data2: 0xd838, Data3: 0x4b5b, Data4: [8]byte{0xa2, 0xe8, 0x1a, 0xdc, 0x7d, 0x93, 0xdb, 0x48}}
)

// Vtable slots, counted from the Windows SDK headers (d2d1.h, dwrite.h).
const (
	slotRelease = 2

	slotCreateDCRenderTarget  = 16 // ID2D1Factory
	slotCreateSolidColorBrush = 8  // ID2D1RenderTarget
	slotDrawLine              = 15
	slotFillRectangle         = 17
	slotDrawRoundedRectangle  = 18
	slotFillRoundedRectangle  = 19
	slotDrawEllipse           = 20
	slotFillEllipse           = 21
	slotDrawText              = 27
	slotSetTransform          = 30
	slotSetTextAntialiasMode  = 34
	slotPushAxisAlignedClip   = 45
	slotPopAxisAlignedClip    = 46
	slotClear                 = 47
	slotBeginDraw             = 48
	slotEndDraw               = 49
	slotBindDC                = 57 // ID2D1DCRenderTarget
	slotSetColor              = 8  // ID2D1SolidColorBrush

	slotCreateTextFormat           = 15 // IDWriteFactory
	slotCreateTextLayout           = 18
	slotCreateEllipsisTrimmingSign = 20
	slotSetTextAlignment           = 3 // IDWriteTextFormat
	slotSetParagraphAlignment      = 4
	slotSetWordWrapping            = 5
	slotSetTrimming                = 9
	slotGetMetrics                 = 60 // IDWriteTextLayout
)

type com struct{ vtbl *[64]uintptr }

// call invokes a method by vtable slot; uintptrescapes keeps pointer
// arguments alive and in place for the duration of the call.
//
//go:uintptrescapes
func (o *com) call(slot int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

func (o *com) release() {
	if o != nil {
		o.call(slotRelease)
	}
}

func f32(v float32) uintptr { return uintptr(math.Float32bits(v)) }

// point packs a D2D1_POINT_2F, which is passed by value in one register.
func point(x, y float32) uintptr { return f32(x) | f32(y)<<32 }

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

type roundedRect struct {
	rectF
	RX, RY float32
}

type ellipse struct{ X, Y, RX, RY float32 }

type matrix struct{ M11, M12, M21, M22, DX, DY float32 }

var identity = matrix{M11: 1, M22: 1}

type renderTargetProps struct {
	Type, Format, Alpha uint32
	DpiX, DpiY          float32
	Usage, MinLevel     uint32
}

type textMetrics struct {
	Left, Top, Width, WidthTrailing, Height, LayoutWidth, LayoutHeight float32
	MaxBidi, Lines                                                     uint32
}

type trimming struct{ Granularity, Delimiter, DelimiterCount uint32 }

type bitmapInfo struct {
	Size                       uint32
	Width, Height              int32
	Planes, BitCount           uint16
	Compression, SizeImage     uint32
	XPelsPerMeter, YPelsPerMtr int32
	ClrUsed, ClrImportant      uint32
	Colors                     [1]uint32
}

// font picks a text style. Text is centered vertically in its box; without
// Wrap it stays on one line and ends in "…" when it doesn't fit.
type font struct {
	Size   float32
	Weight int // 400 regular, 600 semibold, 700 bold
	Align  int // 0 left, 1 right, 2 center
	Wrap   bool
}

// gfx holds the factories; it belongs to the UI thread.
type gfx struct {
	d2d, dw *com
	formats map[font]*com
}

func newGfx() (*gfx, error) {
	if err := procD2D1CreateFactory.Find(); err != nil {
		return nil, err
	}
	if err := procDWriteCreateFactory.Find(); err != nil {
		return nil, err
	}
	g := &gfx{formats: map[font]*com{}}
	if hr, _, _ := procD2D1CreateFactory.Call(0, uintptr(unsafe.Pointer(&iidD2D1Factory)), 0, uintptr(unsafe.Pointer(&g.d2d))); hr != 0 {
		return nil, fmt.Errorf("D2D1CreateFactory: 0x%08x", uint32(hr))
	}
	if hr, _, _ := procDWriteCreateFactory.Call(0, uintptr(unsafe.Pointer(&iidDWriteFactory)), uintptr(unsafe.Pointer(&g.dw))); hr != 0 {
		g.d2d.release()
		return nil, fmt.Errorf("DWriteCreateFactory: 0x%08x", uint32(hr))
	}
	return g, nil
}

func (g *gfx) format(f font) *com {
	if tf, ok := g.formats[f]; ok {
		return tf
	}
	family, _ := windows.UTF16PtrFromString("Segoe UI") // emoji fall back to Segoe UI Emoji, in color
	locale, _ := windows.UTF16PtrFromString("en-us")
	var tf *com
	if hr := g.dw.call(slotCreateTextFormat, uintptr(unsafe.Pointer(family)), 0, uintptr(f.Weight), 0, 5, f32(f.Size),
		uintptr(unsafe.Pointer(locale)), uintptr(unsafe.Pointer(&tf))); hr != 0 || tf == nil {
		return nil
	}
	tf.call(slotSetTextAlignment, uintptr(f.Align))
	tf.call(slotSetParagraphAlignment, 2) // center
	if !f.Wrap {
		tf.call(slotSetWordWrapping, 1) // no wrap
		var sign *com
		g.dw.call(slotCreateEllipsisTrimmingSign, uintptr(unsafe.Pointer(tf)), uintptr(unsafe.Pointer(&sign)))
		t := trimming{Granularity: 1} // by character
		tf.call(slotSetTrimming, uintptr(unsafe.Pointer(&t)), uintptr(unsafe.Pointer(sign)))
	}
	g.formats[f] = tf
	return tf
}

// measure returns the size s takes up in font f.
func (g *gfx) measure(s string, f font, maxWidth float32) (w, h float32) {
	tf := g.format(f)
	u, _ := windows.UTF16FromString(s)
	if tf == nil || len(u) < 2 {
		return 0, f.Size * 1.33
	}
	var tl *com
	if hr := g.dw.call(slotCreateTextLayout, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(tf)),
		f32(maxWidth), f32(10000), uintptr(unsafe.Pointer(&tl))); hr != 0 || tl == nil {
		return 0, f.Size * 1.33
	}
	defer tl.release()
	var m textMetrics
	tl.call(slotGetMetrics, uintptr(unsafe.Pointer(&m)))
	return m.WidthTrailing, m.Height
}

// canvas is a w×h bitmap with premultiplied alpha that Direct2D draws into.
type canvas struct {
	g            *gfx
	w, h         int32
	dc, bmp, old uintptr
	bits         unsafe.Pointer // the DIB section's pixels, owned by Windows
	rt, brush    *com
}

func (g *gfx) newCanvas(w, h int32) (*canvas, error) {
	c := &canvas{g: g, w: max(w, 1), h: max(h, 1)}
	c.dc, _, _ = procCreateCompatibleDC.Call(0)
	if c.dc == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	bi := bitmapInfo{Size: 40, Width: c.w, Height: -c.h, Planes: 1, BitCount: 32} // top-down
	c.bmp, _, _ = procCreateDIBSection.Call(c.dc, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&c.bits)), 0, 0)
	if c.bmp == 0 {
		c.close()
		return nil, fmt.Errorf("CreateDIBSection %dx%d failed", c.w, c.h)
	}
	c.old, _, _ = procSelectObject.Call(c.dc, c.bmp)
	props := renderTargetProps{Format: 87, Alpha: 1, DpiX: 96, DpiY: 96} // B8G8R8A8, premultiplied; 1 unit = 1 pixel
	if hr := g.d2d.call(slotCreateDCRenderTarget, uintptr(unsafe.Pointer(&props)), uintptr(unsafe.Pointer(&c.rt))); hr != 0 || c.rt == nil {
		c.close()
		return nil, fmt.Errorf("CreateDCRenderTarget: 0x%08x", uint32(hr))
	}
	r := struct{ L, T, R, B int32 }{0, 0, c.w, c.h}
	if hr := c.rt.call(slotBindDC, c.dc, uintptr(unsafe.Pointer(&r))); hr != 0 {
		c.close()
		return nil, fmt.Errorf("BindDC: 0x%08x", uint32(hr))
	}
	c.rt.call(slotSetTextAntialiasMode, 2) // grayscale: ClearType needs an opaque background
	white := color{1, 1, 1, 1}
	if hr := c.rt.call(slotCreateSolidColorBrush, uintptr(unsafe.Pointer(&white)), 0, uintptr(unsafe.Pointer(&c.brush))); hr != 0 {
		c.close()
		return nil, fmt.Errorf("CreateSolidColorBrush: 0x%08x", uint32(hr))
	}
	return c, nil
}

func (c *canvas) close() {
	c.brush.release()
	c.rt.release()
	c.brush, c.rt = nil, nil
	if c.old != 0 {
		procSelectObject.Call(c.dc, c.old)
	}
	if c.bmp != 0 {
		procDeleteObject.Call(c.bmp)
	}
	if c.dc != 0 {
		procDeleteDC.Call(c.dc)
	}
	c.dc, c.bmp, c.old = 0, 0, 0
}

// begin starts a frame on a fully transparent bitmap.
func (c *canvas) begin() {
	c.rt.call(slotBeginDraw)
	c.rt.call(slotSetTransform, uintptr(unsafe.Pointer(&identity)))
	clear := color{}
	c.rt.call(slotClear, uintptr(unsafe.Pointer(&clear)))
}

// end finishes the frame; an error means the canvas must be made again.
func (c *canvas) end() error {
	if hr := c.rt.call(slotEndDraw, 0, 0); hr != 0 {
		return fmt.Errorf("EndDraw: 0x%08x", uint32(hr))
	}
	return nil
}

func (c *canvas) paint(col color) {
	c.brush.call(slotSetColor, uintptr(unsafe.Pointer(&col)))
}

// clip limits drawing to r, rounded to whole pixels, until unclip. Clips
// that share an edge split pixels cleanly between them.
func (c *canvas) clip(r rectF) {
	r = rectF{round(r.L), round(r.T), round(r.R), round(r.B)}
	c.rt.call(slotPushAxisAlignedClip, uintptr(unsafe.Pointer(&r)), 1) // aliased
}

func round(v float32) float32 { return float32(math.Round(float64(v))) }

func (c *canvas) unclip() { c.rt.call(slotPopAxisAlignedClip) }

func (c *canvas) fillRect(r rectF, col color) {
	c.paint(col)
	c.rt.call(slotFillRectangle, uintptr(unsafe.Pointer(&r)), uintptr(unsafe.Pointer(c.brush)))
}

func (c *canvas) fillRound(r rectF, radius float32, col color) {
	c.paint(col)
	rr := roundedRect{r, radius, radius}
	c.rt.call(slotFillRoundedRectangle, uintptr(unsafe.Pointer(&rr)), uintptr(unsafe.Pointer(c.brush)))
}

func (c *canvas) strokeRound(r rectF, radius, width float32, col color) {
	c.paint(col)
	rr := roundedRect{r, radius, radius}
	c.rt.call(slotDrawRoundedRectangle, uintptr(unsafe.Pointer(&rr)), uintptr(unsafe.Pointer(c.brush)), f32(width), 0)
}

func (c *canvas) fillCircle(x, y, radius float32, col color) {
	c.paint(col)
	e := ellipse{x, y, radius, radius}
	c.rt.call(slotFillEllipse, uintptr(unsafe.Pointer(&e)), uintptr(unsafe.Pointer(c.brush)))
}

func (c *canvas) strokeCircle(x, y, radius, width float32, col color) {
	c.paint(col)
	e := ellipse{x, y, radius, radius}
	c.rt.call(slotDrawEllipse, uintptr(unsafe.Pointer(&e)), uintptr(unsafe.Pointer(c.brush)), f32(width), 0)
}

func (c *canvas) line(x0, y0, x1, y1, width float32, col color) {
	c.paint(col)
	c.rt.call(slotDrawLine, point(x0, y0), point(x1, y1), uintptr(unsafe.Pointer(c.brush)), f32(width), 0)
}

// rotate turns what is drawn next by deg degrees (clockwise) around x, y,
// until unrotate.
func (c *canvas) rotate(deg, x, y float32) {
	sin, cos := math.Sincos(float64(deg) * math.Pi / 180)
	sn, cs := float32(sin), float32(cos)
	m := matrix{M11: cs, M12: sn, M21: -sn, M22: cs, DX: x - x*cs + y*sn, DY: y - x*sn - y*cs}
	c.rt.call(slotSetTransform, uintptr(unsafe.Pointer(&m)))
}

func (c *canvas) unrotate() { c.rt.call(slotSetTransform, uintptr(unsafe.Pointer(&identity))) }

// text draws s in r, emoji in color.
func (c *canvas) text(s string, r rectF, f font, col color) {
	tf := c.g.format(f)
	u, _ := windows.UTF16FromString(s)
	if tf == nil || len(u) < 2 {
		return
	}
	c.paint(col)
	const colorFont = 4 // D2D1_DRAW_TEXT_OPTIONS_ENABLE_COLOR_FONT
	c.rt.call(slotDrawText, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(tf)),
		uintptr(unsafe.Pointer(&r)), uintptr(unsafe.Pointer(c.brush)), colorFont, 0)
}

// pixels returns the bitmap's premultiplied BGRA bytes (for previews).
func (c *canvas) pixels() []byte {
	return unsafe.Slice((*byte)(c.bits), int(c.w)*int(c.h)*4)
}
