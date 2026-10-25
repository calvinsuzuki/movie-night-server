//go:build windows

package ui

import (
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One UI thread owns every window here and the Direct2D factories; other
// goroutines hand it work through run.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	dxgi     = windows.NewLazySystemDLL("dxgi.dll")

	procRegisterClassEx          = user32.NewProc("RegisterClassExW")
	procCreateWindowEx           = user32.NewProc("CreateWindowExW")
	procDefWindowProc            = user32.NewProc("DefWindowProcW")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetMessage               = user32.NewProc("GetMessageW")
	procTranslateMessage         = user32.NewProc("TranslateMessage")
	procDispatchMessage          = user32.NewProc("DispatchMessageW")
	procPostMessage              = user32.NewProc("PostMessageW")
	procUpdateLayeredWindow      = user32.NewProc("UpdateLayeredWindow")
	procSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	procSetThreadDpiAwarenessCtx = user32.NewProc("SetThreadDpiAwarenessContext")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procLoadCursor               = user32.NewProc("LoadCursorW")
	procSetCursor                = user32.NewProc("SetCursor")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo           = user32.NewProc("GetMonitorInfoW")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetFocus                 = user32.NewProc("SetFocus")
	procGetAsyncKeyState         = user32.NewProc("GetAsyncKeyState")
	procTrackMouseEvent          = user32.NewProc("TrackMouseEvent")
	procGetModuleHandle          = kernel32.NewProc("GetModuleHandleW")
	procGetDpiForMonitor         = shcore.NewProc("GetDpiForMonitor")
	procCreateDXGIFactory1       = dxgi.NewProc("CreateDXGIFactory1")

	iidDXGIFactory1 = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
)

const (
	wsPopup = 0x80000000

	exTopmost     = 0x8
	exTransparent = 0x20 // clicks go through to the window below
	exToolWindow  = 0x80 // no taskbar button
	exLayered     = 0x80000
	exNoActivate  = 0x08000000

	wmActivate      = 0x0006
	wmSetCursor     = 0x0020
	wmMouseActivate = 0x0021
	wmKeyDown       = 0x0100
	wmTimer         = 0x0113
	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	wmMouseWheel    = 0x020A
	wmMouseLeave    = 0x02A3
	wmRun           = 0x8001 // WM_APP+1: run queued work

	swHide           = 0
	swShow           = 5
	swShowNoActivate = 4

	// Slots of IDXGIFactory1, IDXGIAdapter1 and IDXGIOutput (dxgi.h).
	slotEnumAdapters1 = 12
	slotEnumOutputs   = 7
	slotOutputGetDesc = 7
)

type winRect struct{ Left, Top, Right, Bottom int32 }

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

type monitorInfo struct {
	Size          uint32
	Monitor, Work winRect
	Flags         uint32
}

type outputDesc struct {
	DeviceName [32]uint16
	Desktop    winRect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

type trackMouseEvent struct {
	Size, Flags uint32
	Track       uintptr
	HoverTime   uint32
}

// handler is a window's message handler; ok=false falls through to the
// default handling.
type handler func(hwnd uintptr, message uint32, wparam, lparam uintptr) (result uintptr, ok bool)

var (
	startOnce sync.Once
	queueMu   sync.Mutex
	queue     []func()
	wakeHwnd  uintptr
	gfxDev    *gfx // nil if Direct2D is unavailable
	handlers  = map[uintptr]handler{}
	className = windows.StringToUTF16Ptr("MovieNightUI")
	instance  uintptr
	wndProc   = syscall.NewCallback(dispatch)
)

// Supported reports whether this system gets Movie night's own menu and
// overlay (Direct2D is there on Windows 7 and later).
func Supported() bool {
	startOnce.Do(start)
	return gfxDev != nil
}

// run queues fn for the UI thread without waiting.
func run(fn func()) {
	startOnce.Do(start)
	if gfxDev == nil {
		return
	}
	queueMu.Lock()
	queue = append(queue, fn)
	queueMu.Unlock()
	procPostMessage.Call(wakeHwnd, wmRun, 0, 0)
}

func start() {
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread() // the windows belong to this thread
		// Sharp at any scaling, on every monitor: coordinates are real pixels.
		if procSetThreadDpiAwarenessCtx.Find() == nil {
			procSetThreadDpiAwarenessCtx.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
		}
		g, err := newGfx()
		if err != nil {
			log.Printf("ui: %v; using the system menu", err)
			close(ready)
			return
		}
		instance, _, _ = procGetModuleHandle.Call(0)
		cursor, _, _ := procLoadCursor.Call(0, 32512) // IDC_ARROW
		wc := wndClassEx{WndProc: wndProc, Instance: instance, Cursor: cursor, ClassName: className}
		wc.Size = uint32(unsafe.Sizeof(wc))
		procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
		const hwndMessage = ^uintptr(2) // HWND_MESSAGE (-3): never shown
		wakeHwnd, _, _ = procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, instance, 0)
		if wakeHwnd == 0 {
			log.Print("ui: can't create the UI thread's window; using the system menu")
			close(ready)
			return
		}
		gfxDev = g
		close(ready)
		var m msg
		for {
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	<-ready
}

func dispatch(hwnd, message, wparam, lparam uintptr) uintptr {
	if hwnd == wakeHwnd && message == wmRun {
		queueMu.Lock()
		work := queue
		queue = nil
		queueMu.Unlock()
		for _, fn := range work {
			fn()
		}
		return 0
	}
	if h := handlers[hwnd]; h != nil {
		if r, ok := h(hwnd, uint32(message), wparam, lparam); ok {
			return r
		}
	}
	r, _, _ := procDefWindowProc.Call(hwnd, message, wparam, lparam)
	return r
}

var affinityWarned bool

// newLayered makes a hidden, always-on-top window that shows a canvas. It
// is left out of screen capture (Windows 10 2004+), so friends watching the
// shared monitor never see it.
func newLayered(ex uintptr, h handler) uintptr {
	title, _ := windows.UTF16PtrFromString("Movie night")
	hwnd, _, _ := procCreateWindowEx.Call(ex|exLayered|exToolWindow|exTopmost, uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)), wsPopup, 0, 0, 1, 1, 0, 0, instance, 0)
	if hwnd == 0 {
		return 0
	}
	handlers[hwnd] = h
	const excludeFromCapture = 0x11 // WDA_EXCLUDEFROMCAPTURE
	if r, _, err := procSetWindowDisplayAffinity.Call(hwnd, excludeFromCapture); r == 0 && !affinityWarned {
		affinityWarned = true
		log.Printf("ui: can't hide Movie night's windows from the capture (%v); friends may see them", err)
	}
	return hwnd
}

// present shows the canvas at (x, y) on screen, at the given opacity.
func present(hwnd uintptr, c *canvas, x, y int32, alpha byte) {
	pos := struct{ X, Y int32 }{x, y}
	size := struct{ CX, CY int32 }{c.w, c.h}
	src := struct{ X, Y int32 }{}
	blend := [4]byte{0, 0, alpha, 1} // AC_SRC_OVER, per-pixel alpha
	procUpdateLayeredWindow.Call(hwnd, 0, uintptr(unsafe.Pointer(&pos)), uintptr(unsafe.Pointer(&size)), c.dc,
		uintptr(unsafe.Pointer(&src)), 0, uintptr(unsafe.Pointer(&blend)), 2) // ULW_ALPHA
}

// keepOnTop puts the window back above others that went on top since.
func keepOnTop(hwnd uintptr) {
	const noMove, noSize, noActivate = 0x2, 0x1, 0x10
	procSetWindowPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, noMove|noSize|noActivate) // HWND_TOPMOST
}

// area is a monitor: its work area (without the taskbar) and its scale.
type area struct {
	work  winRect
	scale float32
}

func monitorArea(hmon uintptr) area {
	a := area{scale: 1}
	mi := monitorInfo{}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := procGetMonitorInfo.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
		a.work = mi.Work
	}
	if procGetDpiForMonitor.Find() == nil {
		var dx, dy uint32
		if hr, _, _ := procGetDpiForMonitor.Call(hmon, 0, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); hr == 0 && dx > 0 {
			a.scale = float32(dx) / 96
		}
	}
	return a
}

func monitorAt(x, y int32) uintptr {
	const nearest = 2
	m, _, _ := procMonitorFromPoint.Call(uintptr(uint32(x))|uintptr(uint32(y))<<32, nearest)
	return m
}

func cursorPos() (int32, int32) {
	var p struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return p.X, p.Y
}

type dxgiKey struct{ adapter, output int }

var (
	dxgiCache   = map[dxgiKey]uintptr{}
	dxgiCacheAt time.Time
)

// sharedMonitor finds the monitor ddagrab captures (DXGI adapter and output
// indexes), or the primary monitor.
func sharedMonitor(adapter, output int) uintptr {
	if time.Since(dxgiCacheAt) > 10*time.Second { // monitors come and go
		dxgiCache, dxgiCacheAt = map[dxgiKey]uintptr{}, time.Now()
	}
	k := dxgiKey{adapter, output}
	if m, ok := dxgiCache[k]; ok {
		return m
	}
	m := dxgiMonitor(adapter, output)
	if m == 0 {
		const primary = 1
		m, _, _ = procMonitorFromPoint.Call(0, primary)
	}
	dxgiCache[k] = m
	return m
}

func dxgiMonitor(adapter, output int) uintptr {
	if procCreateDXGIFactory1.Find() != nil {
		return 0
	}
	var f, a, o *com
	if hr, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidDXGIFactory1)), uintptr(unsafe.Pointer(&f))); hr != 0 || f == nil {
		return 0
	}
	defer f.release()
	if f.call(slotEnumAdapters1, uintptr(adapter), uintptr(unsafe.Pointer(&a))) != 0 || a == nil {
		return 0
	}
	defer a.release()
	if a.call(slotEnumOutputs, uintptr(output), uintptr(unsafe.Pointer(&o))) != 0 || o == nil {
		return 0
	}
	defer o.release()
	var d outputDesc
	if o.call(slotOutputGetDesc, uintptr(unsafe.Pointer(&d))) != 0 {
		return 0
	}
	return d.Monitor
}

func handCursor() {
	hand, _, _ := procLoadCursor.Call(0, 32649) // IDC_HAND
	procSetCursor.Call(hand)
}

func lparamXY(lp uintptr) (float32, float32) {
	return float32(int16(lp & 0xFFFF)), float32(int16(lp >> 16 & 0xFFFF))
}
