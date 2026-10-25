//go:build windows

package ui

import (
	"time"
)

// The overlay sits in the bottom left corner of the shared monitor: a pill
// with how many friends are watching and, stacked above it, a bubble for
// each emoji a friend throws ("🍿 Ana") and the odd note ("Invite link
// copied"). Clicks go straight through it to the movie.

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

const (
	popIn   = 220 * time.Millisecond
	fadeOut = 350 * time.Millisecond
	maxPops = 5
	ovW     = 380 // window size before scaling; it's see-through
	ovH     = 320
	ovEdge  = 20 // from the corner of the work area
)

type pop struct {
	emoji, text string
	born        time.Time
	life        time.Duration
	note        bool // a dark note rather than a butter throw bubble
}

type overlay struct {
	hwnd     uintptr
	cv       *canvas
	status   Status
	pillA    float32 // the pill's opacity, eased towards 1 or 0
	pops     []*pop
	ticking  bool
	scale    float32
	x, y     int32
	shownNow bool
}

var ov overlay

// SetStatus updates the watcher pill (and which monitor the overlay is on).
func SetStatus(s Status) { run(func() { ov.status = s; ov.render() }) }

// Bubble shows "emoji text" above the pill for d, if the pill is showing.
func Bubble(emoji, text string, d time.Duration) {
	run(func() {
		if ov.status.Visible {
			ov.add(&pop{emoji: emoji, text: text, life: d})
		}
	})
}

// Toast shows a short note in the overlay's corner even when the pill is off.
func Toast(emoji, text string, d time.Duration) {
	run(func() { ov.add(&pop{emoji: emoji, text: text, life: d, note: true}) })
}

func (o *overlay) add(p *pop) {
	p.born = time.Now()
	o.pops = append(o.pops, p)
	if len(o.pops) > maxPops {
		o.pops = o.pops[len(o.pops)-maxPops:]
	}
	o.render()
}

func (o *overlay) handle(hwnd uintptr, message uint32, wparam, lparam uintptr) (uintptr, bool) {
	switch message {
	case wmTimer:
		o.render()
		return 0, true
	case wmMouseActivate:
		return 3, true // MA_NOACTIVATE
	}
	return 0, false
}

// render draws the current frame, and keeps a frame timer running while
// anything is moving.
func (o *overlay) render() {
	now := time.Now()
	alive := o.pops[:0]
	for _, p := range o.pops {
		if now.Sub(p.born) < p.life+fadeOut {
			alive = append(alive, p)
		}
	}
	o.pops = alive
	target := float32(0)
	if o.status.Visible {
		target = 1
	}
	if o.pillA < target {
		o.pillA = min(target, o.pillA+0.12)
	} else if o.pillA > target {
		o.pillA = max(target, o.pillA-0.12)
	}
	animating := len(o.pops) > 0 || o.pillA != target
	if o.hwnd == 0 {
		o.hwnd = newLayered(exTransparent|exNoActivate, o.handle)
	}
	if o.hwnd == 0 {
		return
	}
	if animating != o.ticking {
		if animating {
			procSetTimer.Call(o.hwnd, 1, 16, 0)
		} else {
			procKillTimer.Call(o.hwnd, 1)
		}
		o.ticking = animating
	}
	if o.pillA == 0 && len(o.pops) == 0 {
		if o.shownNow {
			procShowWindow.Call(o.hwnd, swHide)
			o.shownNow = false
		}
		return
	}

	ar := monitorArea(sharedMonitor(o.status.Adapter, o.status.Output))
	if o.cv == nil || ar.scale != o.scale {
		if o.cv != nil {
			o.cv.close()
		}
		var err error
		if o.cv, err = gfxDev.newCanvas(int32(ovW*ar.scale), int32(ovH*ar.scale)); err != nil {
			o.cv = nil
			return
		}
		o.scale = ar.scale
	}
	s := o.scale
	o.x = ar.work.Left + int32(ovEdge*s)
	o.y = ar.work.Bottom - int32(ovEdge*s) - o.cv.h

	if o.draw(o.cv, now) != nil {
		o.cv.close()
		o.cv = nil
		return
	}
	present(o.hwnd, o.cv, o.x, o.y, 255)
	if !o.shownNow {
		procShowWindow.Call(o.hwnd, swShowNoActivate)
		o.shownNow = true
	}
	keepOnTop(o.hwnd)
}

func (o *overlay) draw(c *canvas, now time.Time) error {
	c.begin()
	bottom := float32(c.h)
	if o.pillA > 0 {
		bottom = o.drawPill(c, bottom) - 10*o.scale
	}
	for i := len(o.pops) - 1; i >= 0; i-- { // newest at the bottom, nearest the pill
		bottom = o.drawPop(c, o.pops[i], now, bottom) - 8*o.scale
	}
	return c.end()
}

// drawPill draws "● 2 watching" with its bottom at y and returns its top.
func (o *overlay) drawPill(c *canvas, y float32) float32 {
	s, a := o.scale, o.pillA
	f := font{Size: 13.5 * s, Weight: 600}
	tw, _ := c.g.measure(o.status.Text, f, 600*s)
	h := 34 * s
	r := rectF{2 * s, y - h - 2*s, 2*s + 14*s + 9*s + 8*s + tw + 16*s, y - 2*s}
	shadow(c, r, h/2, s, a)
	c.fillRound(r, h/2, theater.with(0.72*a))
	c.strokeRound(r.inset(0.5, 0.5), h/2, 1, butter.with(0.35*a))
	cx, cy := r.L+14*s+4.5*s, (r.T+r.B)/2
	c.fillCircle(cx, cy, 4.5*s, toneColor(o.status.Tone).with(a))
	c.text(o.status.Text, rectF{cx + 4.5*s + 8*s, r.T, r.R, r.B}, f, salt.with(a))
	return r.T
}

// drawPop draws a bubble with its bottom at y and returns its top.
func (o *overlay) drawPop(c *canvas, p *pop, now time.Time, y float32) float32 {
	s := o.scale
	age := now.Sub(p.born)
	a, rise := float32(1), float32(0)
	switch {
	case age < popIn:
		t := float32(age) / float32(popIn)
		t = 1 - (1-t)*(1-t) // ease out
		a, rise = t, (1-t)*12*s
	case age > p.life:
		t := min(1, float32(age-p.life)/float32(fadeOut))
		a, rise = 1-t, -t*6*s
	}
	ef := font{Size: 20 * s, Weight: 400}
	tf := font{Size: 14 * s, Weight: 600}
	if p.note {
		ef.Size, tf.Weight = 16*s, 400
	}
	ew, _ := c.g.measure(p.emoji, ef, 100*s)
	tw, _ := c.g.measure(p.text, tf, 300*s)
	h := 40 * s
	w := min(14*s+ew+8*s+tw+16*s, float32(c.w)-6*s)
	r := rectF{2 * s, y - h, 2*s + w, y}.shift(0, rise)

	shadow(c, r, 16*s, s, a)
	textColor := theater
	if p.note {
		c.fillRound(r, 14*s, panelBg.with(0.94*a))
		c.strokeRound(r.inset(0.5, 0.5), 14*s, 1, butter.with(0.4*a))
		textColor = salt
	} else {
		// A speech bubble: rounded, except the corner pointing at the pill.
		// Drawn in pieces that don't overlap, so it fades evenly.
		k, out := 16*s, 2*s // out: past the edges, keeping them smooth
		corner := rectF{r.L - out, r.B - k, r.L + k, r.B + out}
		for _, part := range []rectF{{r.L + k, r.T - out, r.R + out, r.B + out}, {r.L - out, r.T - out, r.L + k, r.B - k}} {
			c.clip(part)
			c.fillRound(r, k, butter.with(a))
			c.unclip()
		}
		c.clip(corner)
		c.fillRound(rectF{r.L, r.B - 2*k, r.L + 2*k, r.B}, 4*s, butter.with(a))
		c.unclip()
	}
	c.text(p.emoji, rectF{r.L + 14*s, r.T, r.L + 14*s + ew + 2*s, r.B}, ef, salt.with(a))
	c.text(p.text, rectF{r.L + 14*s + ew + 8*s, r.T, r.R - 12*s, r.B}, tf, textColor.with(a))
	return y - h // the stack doesn't jiggle while a bubble animates
}

// shadow fakes a soft drop shadow with a few widening, fading outlines.
func shadow(c *canvas, r rectF, radius, s, a float32) {
	for i := float32(1); i <= 4; i++ {
		c.fillRound(r.shift(0, 3*s).inset(-i*s, -i*s), radius+i*s, theater.with(0.07*a))
	}
}
