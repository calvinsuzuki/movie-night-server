// Package ui draws Movie night's own windows on Windows: the popcorn menu
// that opens from the tray icon, and the overlay in the corner of the shared
// screen (who's watching, emojis thrown by friends, short notes). They are
// painted in the viewer page's colors, never take focus from the movie, and
// are left out of the screen capture, so friends never see them.
//
// Other systems keep the system menu: Supported reports false, Toast is a
// desktop notification and the rest do nothing. X11 can't leave a window out
// of the capture, so an overlay there would be streamed to friends.
package ui

import "time"

// Kind is what a menu row looks like and how it behaves.
type Kind int

const (
	Header  Kind = iota // the striped title card: Label, Detail (status line), Tone (its dot)
	Primary             // the big pill button (Tone ToneBad for Stop); Children are round icon buttons beside it
	Action              // a plain row; Detail shows on the right
	Toggle              // a row with a switch (Checked)
	Radio               // one choice of several (Checked)
	Check               // a checkbox (Checked)
	Section             // opens and closes its Children; Detail is the current choice
	Request             // someone knocking: OnClick lets them in, OnAlt turns them away
	Viewer              // someone watching (Detail: for how long); OnClick kicks them
	Label               // a small heading
	Note                // greyed text, not clickable
	Separator
)

// Tone colors a status dot or the primary button.
type Tone int

const (
	ToneNone Tone = iota
	ToneLive      // green
	ToneWarn      // butter
	ToneBad       // bucket red
)

// Icon is a small drawn symbol, for the round buttons beside Primary.
type Icon int

const (
	IconNone  Icon = iota
	IconLink       // a chain link; Checked shows a tick instead (just copied)
	IconEye        // an open eye
	IconPause      // two bars; Checked shows a play triangle instead (paused)
)

// Item is one menu row. The menu is rebuilt from scratch on every refresh,
// so ID must stay the same for the same row: it keeps the mouse highlight
// and which sections are open.
type Item struct {
	Kind          Kind
	ID            string
	Label, Detail string
	Tone          Tone
	Icon          Icon // for buttons beside Primary; Label is the button's tooltip
	Checked       bool
	Disabled      bool
	Children      []Item
	OnClick       func()
	OnAlt         func()
	// OnOpen (sections) hears true while the section is open on screen, again
	// on every redraw, and false once it closes or the menu does. It runs on
	// the UI thread, so it should be quick.
	OnOpen func(open bool)
	// KeepOpen leaves the menu open after a click (switches and choices),
	// so several settings can be changed in a row.
	KeepOpen bool
}

// Status is the overlay's watcher pill.
type Status struct {
	Visible bool
	Text    string // e.g. "2 watching"
	Tone    Tone   // the dot
	// Adapter and Output pick the shared monitor (DXGI indexes, as ddagrab
	// uses them), so the overlay sits where the movie plays.
	Adapter, Output int
}

// Mark is a big number shown in the middle of a monitor, so you can tell
// which is which (Adapter and Output as in Status).
type Mark struct {
	Label, Detail   string // "2", "2560×1440"
	Selected        bool   // the monitor being shared
	Adapter, Output int
}

// BubbleTime is how long a thrown emoji's bubble stays up.
const BubbleTime = 2 * time.Second
