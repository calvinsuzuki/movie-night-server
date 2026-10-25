//go:build linux

package ui

import (
	"fmt"
	"log"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
)

// GTK 3, cairo and pango are loaded at run time with purego, so the build
// stays free of cgo and Movie night still starts (with the system menu)
// where they're missing. GTK 3 rather than 4: only 3 can place a popup
// window by the tray on X11.

var (
	gtkInitCheck          func(argc, argv uintptr) bool
	gtkMain               func()
	gtkWindowNew          func(kind int32) uintptr
	gtkWidgetGetScreen    func(w uintptr) uintptr
	gtkWidgetSetVisual    func(w, visual uintptr)
	gtkWidgetSetPaintable func(w uintptr, on bool)
	gtkWidgetAddEvents    func(w uintptr, mask int32)
	gtkWidgetSetSize      func(w uintptr, width, height int32)
	gtkWindowResize       func(w uintptr, width, height int32)
	gtkWindowMove         func(w uintptr, x, y int32)
	gtkWidgetShow         func(w uintptr)
	gtkWidgetHide         func(w uintptr)
	gtkWidgetQueueDraw    func(w uintptr)
	gtkWidgetGetWindow    func(w uintptr) uintptr
	gtkWidgetSetOpacity   func(w uintptr, opacity float64)

	gdkSetAllowedBackends    func(backends string)
	gdkScreenGetRGBAVisual   func(screen uintptr) uintptr
	gdkDisplayGetDefault     func() uintptr
	gdkDisplayGetDefaultSeat func(d uintptr) uintptr
	gdkDisplayMonitorAt      func(d uintptr, x, y int32) uintptr
	gdkMonitorGetWorkarea    func(mon uintptr, r *gdkRect)
	gdkSeatGetPointer        func(seat uintptr) uintptr
	gdkSeatGrab              func(seat, window uintptr, caps int32, ownerEvents bool, cursor, event, prepare, data uintptr) int32
	gdkSeatUngrab            func(seat uintptr)
	gdkDeviceGetPosition     func(dev, screen uintptr, x, y *int32)
	gdkEventGetCoords        func(ev uintptr, x, y *float64) bool
	gdkEventGetButton        func(ev uintptr, button *uint32) bool
	gdkEventGetKeyval        func(ev uintptr, keyval *uint32) bool
	gdkEventGetScrollDir     func(ev uintptr, dir *int32) bool
	gdkEventGetScrollDeltas  func(ev uintptr, dx, dy *float64) bool
	gdkCursorNewFromName     func(display uintptr, name string) uintptr
	gdkWindowSetCursor       func(window, cursor uintptr)

	gSignalConnectData func(instance uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64
	gObjectUnref       func(obj uintptr)
	gIdleAdd           func(fn, data uintptr) uint32
	gTimeoutAdd        func(ms uint32, fn, data uintptr) uint32

	cairoImageSurfaceCreate func(format, width, height int32) uintptr
	cairoCreate             func(surface uintptr) uintptr
	cairoSave               func(cr uintptr)
	cairoRestore            func(cr uintptr)
	cairoSetSourceRGBA      func(cr uintptr, r, g, b, a float64)
	cairoSetOperator        func(cr uintptr, op int32)
	cairoPaint              func(cr uintptr)
	cairoRectangle          func(cr uintptr, x, y, w, h float64)
	cairoArc                func(cr uintptr, xc, yc, radius, angle1, angle2 float64)
	cairoNewPath            func(cr uintptr)
	cairoNewSubPath         func(cr uintptr)
	cairoClosePath          func(cr uintptr)
	cairoMoveTo             func(cr uintptr, x, y float64)
	cairoLineTo             func(cr uintptr, x, y float64)
	cairoSetLineWidth       func(cr uintptr, w float64)
	cairoSetLineCap         func(cr uintptr, cap int32)
	cairoFill               func(cr uintptr)
	cairoStroke             func(cr uintptr)
	cairoClip               func(cr uintptr)
	cairoTranslate          func(cr uintptr, x, y float64)
	cairoRotate             func(cr uintptr, rad float64)

	pangoCairoCreateLayout  func(cr uintptr) uintptr
	pangoCairoShowLayout    func(cr, layout uintptr)
	pangoLayoutSetText      func(l uintptr, text string, length int32)
	pangoLayoutSetFont      func(l, desc uintptr)
	pangoLayoutSetWidth     func(l uintptr, width int32)
	pangoLayoutSetWrap      func(l uintptr, wrap int32)
	pangoLayoutSetEllipsize func(l uintptr, mode int32)
	pangoLayoutSetAlignment func(l uintptr, align int32)
	pangoLayoutSetSinglePar func(l uintptr, on bool)
	pangoLayoutGetPixelSize func(l uintptr, w, h *int32)
	pangoFontDescFromString func(s string) uintptr
	pangoFontDescSetAbsSize func(desc uintptr, size float64)
	pangoFontDescSetWeight  func(desc uintptr, weight int32)
)

type gdkRect struct{ X, Y, W, H int32 }

// winRect is a screen rectangle, as on Windows.
type winRect struct{ Left, Top, Right, Bottom int32 }

// area is a monitor's work area (without the top bar or a dock).
type area struct {
	work  winRect
	scale float32 // always 1: GTK scales for us
}

var (
	startOnce sync.Once
	gtkReady  bool
	display   uintptr
	seat      uintptr

	queueMu sync.Mutex
	queue   []func()

	idleCB, timerCB, eventCB uintptr
)

// Supported reports whether this system gets Movie night's own menu (GTK 3
// is there); elsewhere the tray keeps the system menu. There's no overlay
// on Linux: X11 can't leave a window out of the capture, so friends would
// see it.
func Supported() bool {
	startOnce.Do(start)
	return gtkReady
}

// run queues fn for the UI thread without waiting.
func run(fn func()) {
	if !Supported() {
		return
	}
	queueMu.Lock()
	queue = append(queue, fn)
	queueMu.Unlock()
	gIdleAdd(idleCB, 0)
}

func start() {
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread() // GTK belongs to this thread
		if err := loadGTK(); err != nil {
			log.Printf("ui: %v; using the system menu", err)
			close(ready)
			return
		}
		// The menu is placed by hand next to the tray, which only X11 allows
		// (screen capture needs Xorg anyway).
		gdkSetAllowedBackends("x11")
		if !gtkInitCheck(0, 0) {
			log.Print("ui: GTK can't open the display; using the system menu")
			close(ready)
			return
		}
		display = gdkDisplayGetDefault()
		seat = gdkDisplayGetDefaultSeat(display)
		idleCB = purego.NewCallback(func(data uintptr) uintptr {
			queueMu.Lock()
			fns := queue
			queue = nil
			queueMu.Unlock()
			for _, fn := range fns {
				fn()
			}
			return 0 // G_SOURCE_REMOVE
		})
		timerCB = purego.NewCallback(func(data uintptr) uintptr {
			if timers[data] != nil && timers[data]() {
				return 1 // G_SOURCE_CONTINUE
			}
			return 0
		})
		eventCB = purego.NewCallback(func(widget, arg, data uintptr) uintptr {
			if events[data] != nil && events[data](arg) {
				return 1
			}
			return 0
		})
		gtkReady = true
		close(ready)
		gtkMain()
	}()
	<-ready
}

// timers and events are what the C callbacks run, by their data number.
// purego callbacks can't be freed, so there's one of each, for good.
var (
	timers = map[uintptr]func() bool{}
	events = map[uintptr]func(arg uintptr) bool{}
)

// every calls fn every ms milliseconds on the UI thread for as long as it
// returns true.
func every(id uintptr, ms uint32, fn func() bool) {
	timers[id] = fn
	gTimeoutAdd(ms, timerCB, id)
}

// connect runs fn on signal (on the UI thread); fn gets the signal's
// second argument (the event, or the cairo context for "draw").
func connect(obj uintptr, signal string, id uintptr, fn func(arg uintptr) bool) {
	events[id] = fn
	gSignalConnectData(obj, signal, eventCB, id, 0, 0)
}

func loadGTK() (err error) {
	defer func() {
		if r := recover(); r != nil { // a missing function
			err = fmt.Errorf("%v", r)
		}
	}()
	libs := map[string]uintptr{}
	for _, name := range []string{"libgtk-3.so.0", "libgdk-3.so.0", "libgobject-2.0.so.0", "libglib-2.0.so.0",
		"libcairo.so.2", "libpango-1.0.so.0", "libpangocairo-1.0.so.0"} {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return fmt.Errorf("can't load %s: %w", name, err)
		}
		libs[name] = h
	}
	gtk, gdk, gobj, glib := libs["libgtk-3.so.0"], libs["libgdk-3.so.0"], libs["libgobject-2.0.so.0"], libs["libglib-2.0.so.0"]
	cairo, pango, pc := libs["libcairo.so.2"], libs["libpango-1.0.so.0"], libs["libpangocairo-1.0.so.0"]
	for _, f := range []struct {
		fn   any
		lib  uintptr
		name string
	}{
		{&gtkInitCheck, gtk, "gtk_init_check"},
		{&gtkMain, gtk, "gtk_main"},
		{&gtkWindowNew, gtk, "gtk_window_new"},
		{&gtkWidgetGetScreen, gtk, "gtk_widget_get_screen"},
		{&gtkWidgetSetVisual, gtk, "gtk_widget_set_visual"},
		{&gtkWidgetSetPaintable, gtk, "gtk_widget_set_app_paintable"},
		{&gtkWidgetAddEvents, gtk, "gtk_widget_add_events"},
		{&gtkWidgetSetSize, gtk, "gtk_widget_set_size_request"},
		{&gtkWindowResize, gtk, "gtk_window_resize"},
		{&gtkWindowMove, gtk, "gtk_window_move"},
		{&gtkWidgetShow, gtk, "gtk_widget_show"},
		{&gtkWidgetHide, gtk, "gtk_widget_hide"},
		{&gtkWidgetQueueDraw, gtk, "gtk_widget_queue_draw"},
		{&gtkWidgetGetWindow, gtk, "gtk_widget_get_window"},
		{&gtkWidgetSetOpacity, gtk, "gtk_widget_set_opacity"},

		{&gdkSetAllowedBackends, gdk, "gdk_set_allowed_backends"},
		{&gdkScreenGetRGBAVisual, gdk, "gdk_screen_get_rgba_visual"},
		{&gdkDisplayGetDefault, gdk, "gdk_display_get_default"},
		{&gdkDisplayGetDefaultSeat, gdk, "gdk_display_get_default_seat"},
		{&gdkDisplayMonitorAt, gdk, "gdk_display_get_monitor_at_point"},
		{&gdkMonitorGetWorkarea, gdk, "gdk_monitor_get_workarea"},
		{&gdkSeatGetPointer, gdk, "gdk_seat_get_pointer"},
		{&gdkSeatGrab, gdk, "gdk_seat_grab"},
		{&gdkSeatUngrab, gdk, "gdk_seat_ungrab"},
		{&gdkDeviceGetPosition, gdk, "gdk_device_get_position"},
		{&gdkEventGetCoords, gdk, "gdk_event_get_coords"},
		{&gdkEventGetButton, gdk, "gdk_event_get_button"},
		{&gdkEventGetKeyval, gdk, "gdk_event_get_keyval"},
		{&gdkEventGetScrollDir, gdk, "gdk_event_get_scroll_direction"},
		{&gdkEventGetScrollDeltas, gdk, "gdk_event_get_scroll_deltas"},
		{&gdkCursorNewFromName, gdk, "gdk_cursor_new_from_name"},
		{&gdkWindowSetCursor, gdk, "gdk_window_set_cursor"},

		{&gSignalConnectData, gobj, "g_signal_connect_data"},
		{&gObjectUnref, gobj, "g_object_unref"},
		{&gIdleAdd, glib, "g_idle_add"},
		{&gTimeoutAdd, glib, "g_timeout_add"},

		{&cairoImageSurfaceCreate, cairo, "cairo_image_surface_create"},
		{&cairoCreate, cairo, "cairo_create"},
		{&cairoSave, cairo, "cairo_save"},
		{&cairoRestore, cairo, "cairo_restore"},
		{&cairoSetSourceRGBA, cairo, "cairo_set_source_rgba"},
		{&cairoSetOperator, cairo, "cairo_set_operator"},
		{&cairoPaint, cairo, "cairo_paint"},
		{&cairoRectangle, cairo, "cairo_rectangle"},
		{&cairoArc, cairo, "cairo_arc"},
		{&cairoNewPath, cairo, "cairo_new_path"},
		{&cairoNewSubPath, cairo, "cairo_new_sub_path"},
		{&cairoClosePath, cairo, "cairo_close_path"},
		{&cairoMoveTo, cairo, "cairo_move_to"},
		{&cairoLineTo, cairo, "cairo_line_to"},
		{&cairoSetLineWidth, cairo, "cairo_set_line_width"},
		{&cairoSetLineCap, cairo, "cairo_set_line_cap"},
		{&cairoFill, cairo, "cairo_fill"},
		{&cairoStroke, cairo, "cairo_stroke"},
		{&cairoClip, cairo, "cairo_clip"},
		{&cairoTranslate, cairo, "cairo_translate"},
		{&cairoRotate, cairo, "cairo_rotate"},

		{&pangoCairoCreateLayout, pc, "pango_cairo_create_layout"},
		{&pangoCairoShowLayout, pc, "pango_cairo_show_layout"},
		{&pangoLayoutSetText, pango, "pango_layout_set_text"},
		{&pangoLayoutSetFont, pango, "pango_layout_set_font_description"},
		{&pangoLayoutSetWidth, pango, "pango_layout_set_width"},
		{&pangoLayoutSetWrap, pango, "pango_layout_set_wrap"},
		{&pangoLayoutSetEllipsize, pango, "pango_layout_set_ellipsize"},
		{&pangoLayoutSetAlignment, pango, "pango_layout_set_alignment"},
		{&pangoLayoutSetSinglePar, pango, "pango_layout_set_single_paragraph_mode"},
		{&pangoLayoutGetPixelSize, pango, "pango_layout_get_pixel_size"},
		{&pangoFontDescFromString, pango, "pango_font_description_from_string"},
		{&pangoFontDescSetAbsSize, pango, "pango_font_description_set_absolute_size"},
		{&pangoFontDescSetWeight, pango, "pango_font_description_set_weight"},
	} {
		purego.RegisterLibFunc(f.fn, f.lib, f.name)
	}
	return nil
}

// pointer is where the mouse is, in screen coordinates.
func pointer() (int32, int32) {
	var x, y int32
	gdkDeviceGetPosition(gdkSeatGetPointer(seat), 0, &x, &y)
	return x, y
}

func monitorArea(x, y int32) area {
	var r gdkRect
	gdkMonitorGetWorkarea(gdkDisplayMonitorAt(display, x, y), &r)
	return area{work: winRect{r.X, r.Y, r.X + r.W, r.Y + r.H}, scale: 1}
}
