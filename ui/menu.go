//go:build windows || linux

package ui

import (
	"strings"
	"time"
)

// The popcorn menu (Windows and Linux): a dark card in the viewer page's colors that opens from
// the tray icon, with a striped bucket-red title, a big Start/Stop button
// and settings folded into sections. Switches and choices leave it open;
// anything else closes it, as does clicking elsewhere or Esc.

// Sizes before scaling, in pixels at 100%.
const (
	mMargin   = 14  // room around the card for its shadow
	mWidth    = 304 // the card
	mPad      = 8
	mIndent   = 14 // per section level
	sideGap   = 8  // between the primary button and its icon buttons
	headerGap = 8  // under the striped header card
	// cardRadius rounds the header card, and the primary button and its
	// icon buttons to match it.
	cardRadius = 12
)

type hit struct {
	r       rectF // in content coordinates
	id      string
	fn      func()
	keep    bool
	section string // opens or closes this section instead of calling fn
	top     bool   // section is top level: opening it closes the others
	off     bool
	inert   bool // only hovered (to show buttons), never clicked
}

type row struct {
	it    Item
	depth int
	r     rectF
	yes   rectF   // Request: "Let in"
	no    rectF   // Request: "✕"
	side  []rectF // Primary: its icon buttons, one per child
	kick  rectF   // Viewer: the Kick button, shown while the row is hovered
}

type menu struct {
	menuWin        // the window itself, per system
	w, h     int32 // its size
	items    func() []Item
	open     map[string]bool // open sections, kept while the app runs
	rows     []row
	blocks   []rectF // backgrounds of open sections
	hits     []hit
	hot      string
	focus    string // keyboard selection (a hit id)
	scroll   float32
	contentH float32
	ar       area
	x, y     int32
	anchorY  int32 // window bottom (menus above the taskbar) or top
	fromTop  bool
	visible  bool
	alpha    int
	closedAt time.Time
	watching map[string]func(bool) // open sections that want to know (OnOpen)
}

var mn = menu{open: map[string]bool{}}

// ShowMenu opens the menu by the mouse pointer, or closes it if it is open.
// items is called on every redraw, so it should be quick.
func ShowMenu(items func() []Item) { run(func() { mn.toggle(items) }) }

// RefreshMenu redraws the menu if it is open, with fresh items.
func RefreshMenu() {
	run(func() {
		if mn.visible {
			mn.relayout()
			mn.render()
		}
	})
}

func (m *menu) anchored(h int32) int32 {
	if m.fromTop {
		return m.anchorY
	}
	return max(m.ar.work.Top-int32(mMargin*m.ar.scale), m.anchorY-h)
}

// relayout rebuilds the rows from fresh items and resizes the window.
func (m *menu) relayout() {
	s := m.ar.scale
	m.rows, m.hits, m.blocks = m.rows[:0], m.hits[:0], m.blocks[:0]
	y := mPad * s
	m.lay(m.items(), 0, &y)
	m.contentH = y + mPad*s
	w := int32((mWidth + 2*mMargin) * s)
	maxH := m.ar.work.Bottom - m.ar.work.Top - int32(16*s) + int32(2*mMargin*s)
	h := min(int32(m.contentH+2*mMargin*s), maxH)
	if !m.resize(w, h) {
		return
	}
	m.scroll = max(0, min(m.scroll, m.maxScroll()))
	if m.visible {
		m.y = m.anchored(h)
	}
	m.watchSections()
}

// watchSections tells sections with OnOpen whether they're on screen.
func (m *menu) watchSections() {
	now := map[string]func(bool){}
	if m.visible {
		for _, rw := range m.rows {
			if rw.it.Kind == Section && rw.it.OnOpen != nil && m.open[rw.it.ID] {
				now[rw.it.ID] = rw.it.OnOpen
				rw.it.OnOpen(true)
			}
		}
	}
	for id, f := range m.watching {
		if now[id] == nil {
			f(false)
		}
	}
	m.watching = now
}

func (m *menu) maxScroll() float32 {
	return max(0, m.contentH-(float32(m.h)-2*mMargin*m.ar.scale))
}

func rowHeight(k Kind, depth int) float32 {
	switch k {
	case Header:
		return 66 + headerGap
	case Primary:
		return 48
	case Request:
		return 46
	case Label:
		return 26
	case Separator:
		return 11
	}
	if depth > 0 {
		return 30
	}
	return 34
}

func (m *menu) lay(items []Item, depth int, y *float32) {
	s := m.ar.scale
	for _, it := range items {
		l := (mPad + float32(depth)*mIndent) * s
		r := rectF{l, *y, (mWidth - mPad) * s, *y + rowHeight(it.Kind, depth)*s}
		if it.Kind == Note {
			_, th := gfxDev.measure(it.Label, noteFont(s), r.w()-24*s)
			r.B = r.T + th + 12*s
		}
		rw := row{it: it, depth: depth, r: r}
		off := it.Disabled || it.OnClick == nil
		switch it.Kind {
		case Header:
			if it.OnClick != nil { // the status line, e.g. the link to copy
				m.hits = append(m.hits, hit{r: headerLine(r, s), id: it.ID, fn: it.OnClick, keep: it.KeepOpen})
			}
		case Primary:
			rw.r = r.cut(0, 2*s, 0, 6*s)
			main := rw.r
			main.R -= float32(len(it.Children)) * (rw.r.h() + sideGap*s)
			m.hits = append(m.hits, hit{r: main, id: it.ID, fn: it.OnClick, keep: it.KeepOpen, off: off})
			for i, b := range it.Children {
				l := main.R + sideGap*s + float32(i)*(rw.r.h()+sideGap*s)
				br := rectF{l, rw.r.T, l + rw.r.h(), rw.r.B}
				rw.side = append(rw.side, br)
				m.hits = append(m.hits, hit{r: br, id: b.ID, fn: b.OnClick, keep: b.KeepOpen, off: b.Disabled || b.OnClick == nil})
			}
		case Action, Toggle, Radio, Check:
			m.hits = append(m.hits, hit{r: r, id: it.ID, fn: it.OnClick, keep: it.KeepOpen, off: off})
		case Viewer:
			// Only the Kick button kicks; the row itself just shows it.
			cy := (r.T + r.B) / 2
			rw.kick = rectF{r.R - 10*s - 46*s, cy - 11*s, r.R - 10*s, cy + 11*s}
			m.hits = append(m.hits,
				hit{r: rw.kick, id: it.ID + "/kick", fn: it.OnClick, keep: it.KeepOpen, off: off},
				hit{r: r, id: it.ID, inert: true})
		case Section:
			m.hits = append(m.hits, hit{r: r, id: it.ID, section: it.ID, top: depth == 0, off: it.Disabled})
		case Request:
			cy := (r.T + r.B) / 2
			rw.no = rectF{r.R - 10*s - 26*s, cy - 13*s, r.R - 10*s, cy + 13*s}
			rw.yes = rectF{rw.no.L - 8*s - 68*s, rw.no.T, rw.no.L - 8*s, rw.no.B}
			m.hits = append(m.hits,
				hit{r: rw.yes, id: it.ID + "/yes", fn: it.OnClick, keep: true, off: it.OnClick == nil},
				hit{r: rw.no, id: it.ID + "/no", fn: it.OnAlt, keep: true, off: it.OnAlt == nil})
		}
		m.rows = append(m.rows, rw)
		*y = r.B
		if it.Kind == Section && m.open[it.ID] && len(it.Children) > 0 {
			top := *y
			*y += 2 * s
			m.lay(it.Children, depth+1, y)
			*y += 4 * s
			m.blocks = append(m.blocks, rectF{l, top, r.R, *y - 2*s})
		}
	}
}

func noteFont(s float32) font { return font{Size: 12 * s, Weight: 400, Wrap: true} }

func (m *menu) key(vk uintptr) {
	switch vk {
	case 0x1B: // Esc
		m.hide()
	case 0x26, 0x28: // up, down: move between the rows that do something
		var usable []int
		cur := -1
		for i, h := range m.hits {
			if !h.off && !h.inert {
				if h.id == m.focus {
					cur = len(usable)
				}
				usable = append(usable, i)
			}
		}
		if len(usable) == 0 {
			return
		}
		next := 0
		switch {
		case cur < 0 && vk == 0x26:
			next = len(usable) - 1
		case cur >= 0 && vk == 0x26:
			next = (cur - 1 + len(usable)) % len(usable)
		case cur >= 0:
			next = (cur + 1) % len(usable)
		}
		h := m.hits[usable[next]]
		m.focus = h.id
		m.scrollTo(h.r)
		m.render()
	case 0x0D, 0x20: // Enter, space
		if h := m.hitByID(m.focus); h != nil {
			m.activate(*h)
		}
	}
}

func (m *menu) scrollTo(r rectF) {
	view := float32(m.h) - 2*mMargin*m.ar.scale
	if r.T < m.scroll {
		m.scroll = r.T
	} else if r.B > m.scroll+view {
		m.scroll = r.B - view
	}
	m.scroll = max(0, min(m.scroll, m.maxScroll()))
}

func (m *menu) activate(h hit) {
	if h.off || h.inert {
		return
	}
	if h.section != "" {
		opening := !m.open[h.section]
		if opening && h.top {
			for _, o := range m.hits {
				if o.top {
					delete(m.open, o.section)
				}
			}
		}
		m.open[h.section] = opening
		m.relayout()
		if opening { // show what just opened
			for _, b := range m.blocks {
				if b.T >= h.r.B-1 && b.T <= h.r.B+4*m.ar.scale {
					m.scrollTo(rectF{h.r.L, h.r.T, h.r.R, b.B})
				}
			}
		}
		m.render()
		return
	}
	if !h.keep {
		m.hide()
	}
	fn := h.fn
	go func() {
		fn()
		RefreshMenu()
	}()
}

// toContent turns window coordinates into content coordinates.
func (m *menu) toContent(x, y float32) (float32, float32) {
	off := mMargin * m.ar.scale
	return x - off, y - off + m.scroll
}

func (m *menu) hitAt(x, y float32) *hit {
	view := rectF{mMargin * m.ar.scale, mMargin * m.ar.scale, float32(m.w) - mMargin*m.ar.scale, float32(m.h) - mMargin*m.ar.scale}
	if !view.has(x, y) {
		return nil
	}
	cx, cy := m.toContent(x, y)
	for i := range m.hits {
		if m.hits[i].r.has(cx, cy) {
			return &m.hits[i]
		}
	}
	return nil
}

func (m *menu) hitByID(id string) *hit {
	if id == "" {
		return nil
	}
	for i := range m.hits {
		if m.hits[i].id == id {
			return &m.hits[i]
		}
	}
	return nil
}

func (m *menu) hover(x, y float32) {
	id := ""
	if h := m.hitAt(x, y); h != nil && !h.off {
		id = h.id
	}
	if id != m.hot {
		m.hot = id
		m.render()
	}
}

// paint draws the whole menu: the card, its rows and the scroll bar.
func (m *menu) paint(c *canvas) {
	s := m.ar.scale
	card := rectF{mMargin * s, mMargin * s, float32(c.w) - mMargin*s, float32(c.h) - mMargin*s}
	for i := float32(1); i <= 6; i++ {
		c.fillRound(card.shift(0, 4*s).inset(-i*1.5*s, -i*1.5*s), 14*s+i*1.5*s, theater.with(0.05))
	}
	c.fillRound(card, 14*s, panelBg.with(0.98))
	c.clip(card)
	dx, dy := card.L, card.T-m.scroll
	for _, b := range m.blocks {
		c.fillRound(b.shift(dx, dy), 9*s, salt.with(0.04))
	}
	for _, rw := range m.rows {
		r := rw.r.shift(dx, dy)
		if r.B < card.T || r.T > card.B {
			continue
		}
		m.drawRow(c, rw, r)
	}
	m.drawTip(c, dx, dy)
	if h := m.hitByID(m.focus); h != nil {
		radius := 8 * s
		if h.r.w() == h.r.h() { // an icon button
			radius = cardRadius * s
		}
		c.strokeRound(h.r.shift(dx, dy).inset(1*s, 1*s), radius, 1.5*s, butter.with(0.8))
	}
	c.unclip()
	c.strokeRound(card.inset(0.5, 0.5), 14*s, 1, butter.with(0.22))
	if ms := m.maxScroll(); ms > 0 { // a thin scroll bar
		view := card.h()
		bar := view * view / m.contentH
		top := card.T + (view-bar)*m.scroll/ms
		c.fillRound(rectF{card.R - 5*s, top + 4*s, card.R - 2*s, top + bar - 4*s}, 1.5*s, salt.with(0.25))
	}
}

func (m *menu) drawRow(c *canvas, rw row, r rectF) {
	s := m.ar.scale
	it := rw.it
	hot := m.hot == it.ID || (it.Kind == Viewer && m.hot == it.ID+"/kick")
	label := font{Size: 13.5 * s, Weight: 400}
	if rw.depth > 0 {
		label.Size = 13 * s
	}
	small := font{Size: 12.5 * s, Weight: 400, Align: 1}
	text, a := salt, float32(1)
	if it.Disabled {
		text, a = salt.with(0.4), 0.4
	}
	if hot && it.Kind != Primary && it.Kind != Header {
		c.fillRound(r, 8*s, butter.with(0.12))
	}
	cy := (r.T + r.B) / 2
	switch it.Kind {
	case Header:
		r = r.cut(0, 0, 0, headerGap*s)
		c.fillRound(r, cardRadius*s, red)
		// The popcorn bucket's stripes, clear of the rounded corners.
		for x := r.L + 12*s; x+11*s <= r.R-12*s; x += 22 * s {
			c.fillRect(rectF{x, r.T, x + 11*s, r.B}, redDeep)
		}
		c.text("🍿", rectF{r.L + 12*s, r.T, r.L + 48*s, r.B}, font{Size: 28 * s, Weight: 400}, salt)
		c.text(it.Label, rectF{r.L + 56*s, r.T + 10*s, r.R - 12*s, r.T + 34*s}, font{Size: 16 * s, Weight: 700}, salt)
		dot := r.L + 56*s + 4*s
		c.fillCircle(dot, r.T+46*s, 5*s, salt.with(0.9))
		c.fillCircle(dot, r.T+46*s, 3.6*s, toneColor(it.Tone))
		f, fg := font{Size: 12.5 * s, Weight: 600}, butter
		line := rectF{dot + 10*s, r.T + 36*s, r.R - 10*s, r.T + 56*s}
		if hot { // clickable: a dark chip behind the text
			tw, _ := gfxDev.measure(it.Detail, f, line.w())
			c.fillRound(rectF{line.L - 5*s, line.T, min(line.L+tw+6*s, line.R+4*s), line.B}, 6*s, theater.with(0.3))
			fg = salt
		}
		c.text(it.Detail, line, f, fg)
	case Primary:
		bg, fg := butter, theater
		if hot {
			bg = butterHot
		}
		if it.Tone == ToneBad {
			bg, fg = redDeep, salt
			if hot {
				bg = red
			}
		}
		if it.Disabled {
			bg, fg = bg.with(0.45), fg.with(0.6)
		}
		pill := r
		if len(rw.side) > 0 {
			pill.R = r.L + (rw.side[0].L - rw.r.L) - sideGap*s
		}
		c.fillRound(pill, cardRadius*s, bg)
		if it.Tone == ToneBad {
			c.strokeRound(pill.inset(0.5, 0.5), cardRadius*s, 1, salt.with(0.18))
		}
		c.text(it.Label, pill, font{Size: 14.5 * s, Weight: 600, Align: 2}, fg)
		dx, dy := r.L-rw.r.L, r.T-rw.r.T // the buttons were laid out unscrolled
		for i, b := range it.Children {
			br := rw.side[i].shift(dx, dy)
			bg, fg := salt.with(0.1), salt
			switch {
			case b.Disabled:
				bg, fg = salt.with(0.05), salt.with(0.35)
			case b.Checked:
				fg = toneColor(b.Tone)
				if b.Tone == ToneNone {
					fg = live
				}
				bg = fg.with(0.22)
			case m.hot == b.ID:
				bg, fg = butter.with(0.22), butter
			}
			bx, by := (br.L+br.R)/2, (br.T+br.B)/2
			c.fillRound(br, cardRadius*s, bg)
			drawIcon(c, b.Icon, b.Checked, bx, by, s, fg)
		}
	case Action:
		c.text(it.Label, rectF{r.L + 12*s, r.T, r.R - 12*s, r.B}, label, text)
		if it.Detail != "" {
			c.text(it.Detail, rectF{r.L + 12*s, r.T, r.R - 12*s, r.B}, small, butter)
		}
	case Toggle:
		c.text(it.Label, rectF{r.L + 12*s, r.T, r.R - 56*s, r.B}, label, text)
		track := rectF{r.R - 12*s - 32*s, cy - 9*s, r.R - 12*s, cy + 9*s}
		if it.Checked {
			c.fillRound(track, 9*s, butter.with(a))
			c.fillCircle(track.R-9*s, cy, 6.5*s, theater.with(a))
		} else {
			c.fillRound(track, 9*s, salt.with(0.2*a))
			c.fillCircle(track.L+9*s, cy, 6.5*s, salt.with(0.85*a))
		}
	case Radio:
		x := r.L + 12*s + 6*s
		if it.Checked {
			c.strokeCircle(x, cy, 6.5*s, 1.6*s, butter)
			c.fillCircle(x, cy, 3.5*s, butter)
		} else {
			c.strokeCircle(x, cy, 6.5*s, 1.4*s, muted)
		}
		c.text(it.Label, rectF{x + 14*s, r.T, r.R - 12*s, r.B}, label, text)
	case Check:
		box := rectF{r.L + 12*s, cy - 7*s, r.L + 26*s, cy + 7*s}
		if it.Checked {
			c.fillRound(box, 3.5*s, butter.with(a))
			c.line(box.L+3.2*s, cy, box.L+6*s, cy+3*s, 1.8*s, theater)
			c.line(box.L+6*s, cy+3*s, box.R-3*s, cy-3.5*s, 1.8*s, theater)
		} else {
			c.strokeRound(box.inset(0.7*s, 0.7*s), 3*s, 1.4*s, muted.with(0.68*a))
		}
		c.text(it.Label, rectF{box.R + 10*s, r.T, r.R - 12*s, r.B}, label, text)
	case Section:
		c.text(it.Label, rectF{r.L + 12*s, r.T, r.R - 120*s, r.B}, label, text)
		if it.Detail != "" {
			c.text(it.Detail, rectF{r.L + 100*s, r.T, r.R - 30*s, r.B}, small, muted)
		}
		x := r.R - 16*s
		if m.open[it.ID] {
			c.line(x-4*s, cy-2*s, x, cy+2*s, 1.6*s, muted)
			c.line(x, cy+2*s, x+4*s, cy-2*s, 1.6*s, muted)
		} else {
			c.line(x-2*s, cy-4*s, x+2*s, cy, 1.6*s, muted)
			c.line(x+2*s, cy, x-2*s, cy+4*s, 1.6*s, muted)
		}
	case Request:
		c.fillRound(r.cut(0, 3*s, 0, 3*s), 10*s, butter.with(0.1))
		bold := font{Size: 13.5 * s, Weight: 700}
		yesL := r.L + (rw.yes.L - rw.r.L)
		nw, _ := gfxDev.measure(it.Label, bold, yesL-r.L-20*s)
		nameEnd := min(r.L+12*s+nw+2*s, yesL-10*s)
		c.text(it.Label, rectF{r.L + 12*s, r.T, nameEnd, r.B}, bold, salt)
		c.text(" "+it.Detail, rectF{nameEnd, r.T, yesL - 8*s, r.B}, font{Size: 12.5 * s, Weight: 400}, muted)
		yes, no := butter, salt.with(0.1)
		if m.hot == it.ID+"/yes" {
			yes = butterHot
		}
		if m.hot == it.ID+"/no" {
			no = salt.with(0.22)
		}
		dx, dy := r.L-rw.r.L, r.T-rw.r.T // the buttons were laid out unscrolled
		yr, nr := rw.yes.shift(dx, dy), rw.no.shift(dx, dy)
		c.fillRound(yr, 13*s, yes)
		c.text("Let in", yr, font{Size: 12.5 * s, Weight: 600, Align: 2}, theater)
		c.fillCircle((nr.L+nr.R)/2, cy, 13*s, no)
		c.text("✕", nr, font{Size: 12 * s, Weight: 600, Align: 2}, salt)
	case Viewer:
		c.fillCircle(r.L+16*s, cy, 3.5*s, live)
		c.text(it.Label, rectF{r.L + 28*s, r.T, r.R - 90*s, r.B}, label, text)
		onKick := m.hot == it.ID+"/kick" || m.focus == it.ID+"/kick"
		if hot || onKick {
			kick := rw.kick.shift(r.L-rw.r.L, r.T-rw.r.T) // laid out unscrolled
			bg := redDeep
			if onKick {
				bg = red
			}
			c.fillRound(kick, 11*s, bg)
			c.text("Kick", kick, font{Size: 12 * s, Weight: 600, Align: 2}, salt)
		} else if it.Detail != "" {
			c.text(it.Detail, rectF{r.L + 28*s, r.T, r.R - 12*s, r.B}, small, muted)
		}
	case Label:
		c.text(strings.ToUpper(it.Label), rectF{r.L + 12*s, r.T + 4*s, r.R - 12*s, r.B}, font{Size: 11 * s, Weight: 600}, muted.with(0.8))
	case Note:
		c.text(it.Label, r.inset(12*s, 6*s), noteFont(s), muted)
	case Separator:
		c.fillRect(rectF{r.L + 4*s, cy - 0.5*s, r.R - 4*s, cy + 0.5*s}, salt.with(0.09))
	}
}

// drawTip names the icon button under the mouse (or keyboard), just above it.
func (m *menu) drawTip(c *canvas, dx, dy float32) {
	s := m.ar.scale
	for _, rw := range m.rows {
		for i, b := range rw.it.Children {
			if rw.it.Kind != Primary || b.Label == "" || (m.hot != b.ID && m.focus != b.ID) {
				continue
			}
			br := rw.side[i].shift(dx, dy)
			f := font{Size: 12 * s, Weight: 600, Align: 2}
			tw, _ := gfxDev.measure(b.Label, f, 260*s)
			w := tw + 20*s
			l := min((br.L+br.R)/2-w/2, float32(c.w)-mMargin*s-6*s-w)
			tip := rectF{l, br.T - 30*s, l + w, br.T - 6*s}
			c.fillRound(tip, tip.h()/2, theater.with(0.92))
			c.strokeRound(tip.inset(0.5, 0.5), tip.h()/2, 1, butter.with(0.4))
			c.text(b.Label, tip, f, salt)
		}
	}
}

// drawIcon draws a small symbol about 18 px wide, centered on x, y.
func drawIcon(c *canvas, ic Icon, checked bool, x, y, s float32, col color) {
	w := 1.8 * s
	switch {
	case checked && ic == IconLink:
		c.line(x-6*s, y+0.5*s, x-2*s, y+4.5*s, w, col)
		c.line(x-2*s, y+4.5*s, x+6.5*s, y-4.5*s, w, col)
	case ic == IconLink:
		// Two chain links, side by side and tilted up to the right.
		c.rotate(-45, x, y)
		c.strokeRound(rectF{x - 9.5*s, y - 3.6*s, x + 1.5*s, y + 3.6*s}, 3.6*s, w, col)
		c.strokeRound(rectF{x - 1.5*s, y - 3.6*s, x + 9.5*s, y + 3.6*s}, 3.6*s, w, col)
		c.unrotate()
	case ic == IconPause && checked:
		// Play: a triangle pointing right, filled with thin upright strokes.
		l, r, hh := x-4.5*s, x+6.5*s, 7*s
		for px := l; px <= r; px += 0.5 * s {
			h := hh * (1 - (px-l)/(r-l))
			c.line(px, y-h, px, y+h, 0.9*s, col)
		}
	case ic == IconPause:
		c.fillRound(rectF{x - 6*s, y - 7*s, x - 1.5*s, y + 7*s}, 1.5*s, col)
		c.fillRound(rectF{x + 1.5*s, y - 7*s, x + 6*s, y + 7*s}, 1.5*s, col)
	case ic == IconEye:
		// The lids are arcs of one circle each, cut where they meet.
		hw, hh := 9*s, 5.5*s
		rad := (hw*hw + hh*hh) / (2 * hh)
		c.clip(rectF{x - hw - 2*s, y - hh - 2*s, x + hw + 2*s, y})
		c.strokeCircle(x, y-hh+rad, rad, w, col)
		c.unclip()
		c.clip(rectF{x - hw - 2*s, y, x + hw + 2*s, y + hh + 2*s})
		c.strokeCircle(x, y+hh-rad, rad, w, col)
		c.unclip()
		c.fillCircle(x, y, 2.8*s, col)
	}
}

// headerLine is where the header's status line sits in row r.
func headerLine(r rectF, s float32) rectF {
	return rectF{r.L + 60*s, r.T + 34*s, r.R - 8*s, r.T + 58*s}
}
