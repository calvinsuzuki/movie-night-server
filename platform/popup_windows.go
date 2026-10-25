//go:build windows

package platform

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The "Ana wants to watch" popup: a small dark card with Movie night's
// colors, just above the taskbar on the side of the clock (where the popcorn
// icon lives). It stays on top but never takes focus from the movie, several
// stack upwards, and it's left out of the screen capture. Choose adds a
// third, quieter answer as a link ("Don't ask again"), and ChooseTimed a
// countdown on the yes button.

var (
	gdi32  = windows.NewLazySystemDLL("gdi32.dll")
	dwmapi = windows.NewLazySystemDLL("dwmapi.dll")

	procRegisterClassEx            = user32.NewProc("RegisterClassExW")
	procCreateWindowEx             = user32.NewProc("CreateWindowExW")
	procDefWindowProc              = user32.NewProc("DefWindowProcW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procGetMessage                 = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessage            = user32.NewProc("DispatchMessageW")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procBeginPaint                 = user32.NewProc("BeginPaint")
	procEndPaint                   = user32.NewProc("EndPaint")
	procInvalidateRect             = user32.NewProc("InvalidateRect")
	procFillRect                   = user32.NewProc("FillRect")
	procDrawText                   = user32.NewProc("DrawTextW")
	procDrawIconEx                 = user32.NewProc("DrawIconEx")
	procLoadCursor                 = user32.NewProc("LoadCursorW")
	procSetCursor                  = user32.NewProc("SetCursor")
	procSetTimer                   = user32.NewProc("SetTimer")
	procKillTimer                  = user32.NewProc("KillTimer")
	procTrackMouseEvent            = user32.NewProc("TrackMouseEvent")
	procSystemParametersInfo       = user32.NewProc("SystemParametersInfoW")
	procGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	procGetDpiForSystem            = user32.NewProc("GetDpiForSystem")
	procSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	procPrivateExtractIcons        = user32.NewProc("PrivateExtractIconsW")
	procSetWindowDisplayAffinity   = user32.NewProc("SetWindowDisplayAffinity")
	procGetModuleHandle            = kernel32.NewProc("GetModuleHandleW")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	procCreateFont             = gdi32.NewProc("CreateFontW")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procRoundRect              = gdi32.NewProc("RoundRect")
	procGetStockObject         = gdi32.NewProc("GetStockObject")

	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	wmDestroy       = 0x0002
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmEraseBkgnd    = 0x0014
	wmSetCursor     = 0x0020
	wmMouseActivate = 0x0021
	wmTimer         = 0x0113
	wmMouseMove     = 0x0200
	wmLButtonUp     = 0x0202
	wmMouseLeave    = 0x02A3
)

// Colors as Windows COLORREFs (0x00BBGGRR), from the viewer page's palette.
const (
	colBack      = 0x00100D0D // near-black
	colRed       = 0x001313AB // bucket red
	colButter    = 0x008DDEF8
	colButterHot = 0x00B0EBFF
	colText      = 0x00FFFFFF
	colMuted     = 0x00B4B4BE
	colDeny      = 0x002A2626
	colDenyHot   = 0x003C3636
)

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) has(x, y int32) bool { return x >= r.Left && x < r.Right && y >= r.Top && y < r.Bottom }

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

type paintStruct struct {
	Hdc                uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}

type trackMouseEvent struct {
	Size, Flags uint32
	Track       uintptr
	HoverTime   uint32
}

type popup struct {
	title, text       string
	answer            chan int
	dismissed         *atomic.Bool
	result            int // 0 none, 1 allow, 2 deny, 3 link, 4 time ran out
	hot               int // button under the mouse: 0 none, 1 allow, 2 deny, 3 link
	tracking          bool
	alpha             int
	slot              int
	scale             float64
	w, h              int32
	allow, deny, link rect
	fontSmall         uintptr
	fontBold          uintptr
	fontButton        uintptr
	icon              uintptr
	iconSize          int32
	denyLabel         string
	allowLabel        string
	linkLabel         string    // optional
	deadline          time.Time // optional: the countdown on the yes button ends here
	shown             string    // the countdown last painted, e.g. "2:59"
	pad, iconGap      int32
}

var (
	popupMu    sync.Mutex
	popups     = map[uintptr]*popup{}
	slots      = map[int]bool{}
	classOnce  sync.Once
	className  = windows.StringToUTF16Ptr("MovieNightPopup")
	wndProcPtr = syscall.NewCallback(wndProc)
	appIcon    uintptr
	iconOnce   sync.Once
)

// Ask shows the "let them in?" popup without blocking. answer gets true
// (Let in) or false (Not now) when the user clicks; dismiss closes the popup
// without an answer.
func Ask(title, text string) (answer <-chan bool, dismiss func()) {
	choice, dismiss := Choose(title, text, "Let them in", "Not now", "")
	ch := make(chan bool, 1)
	go func() {
		if c, ok := <-choice; ok {
			ch <- c == 0
		}
		close(ch)
	}()
	return ch, dismiss
}

// Choose shows a popup with a yes button, a no button and, if extra isn't
// empty, a link, without blocking. answer gets 0 (yes), 1 (no) or 2 (the
// link) when the user clicks; dismiss closes the popup without an answer.
func Choose(title, text, yes, no, extra string) (answer <-chan int, dismiss func()) {
	return ChooseTimed(title, text, yes, no, extra, 0)
}

// ChooseTimed is Choose with a countdown on the yes button, "End stream
// (2:59)": when it runs out the popup closes and answer gets 3. Zero means
// no countdown.
func ChooseTimed(title, text, yes, no, extra string, after time.Duration) (answer <-chan int, dismiss func()) {
	ch := make(chan int, 1)
	var dismissed atomic.Bool
	hwndCh := make(chan uintptr, 1)
	go func() {
		runtime.LockOSThread() // the window belongs to this thread
		defer runtime.UnlockOSThread()
		p := &popup{title: title, text: text, answer: ch, dismissed: &dismissed,
			allowLabel: yes, denyLabel: no, linkLabel: extra}
		if after > 0 {
			p.deadline = time.Now().Add(after)
		}
		hwnd := showPopup(p)
		hwndCh <- hwnd
		if hwnd == 0 {
			close(ch)
			return
		}
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
	hwnd := <-hwndCh
	return ch, func() {
		if !dismissed.Swap(true) && hwnd != 0 {
			procPostMessage.Call(hwnd, wmClose, 0, 0)
		}
	}
}

func showPopup(p *popup) uintptr {
	inst, _, _ := procGetModuleHandle.Call(0)
	classOnce.Do(func() {
		cursor, _, _ := procLoadCursor.Call(0, 32512) // IDC_ARROW
		wc := wndClassEx{WndProc: wndProcPtr, Instance: inst, Cursor: cursor, ClassName: className}
		wc.Size = uint32(unsafe.Sizeof(wc))
		procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
	})

	p.scale = 1
	if procGetDpiForSystem.Find() == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi > 0 {
			p.scale = float64(dpi) / 96
		}
	}
	px := func(v float64) int32 { return int32(v*p.scale + 0.5) }
	p.w, p.h, p.pad, p.iconSize, p.iconGap = px(356), px(128), px(16), px(40), px(12)
	if p.linkLabel != "" {
		p.w = px(420)
	}
	bh, bw, dw := px(34), px(132), px(100)
	if !p.deadline.IsZero() {
		bw = px(150) // room for "(2:59)"
	}
	p.allow = rect{p.w - p.pad - bw, p.h - p.pad - bh, p.w - p.pad, p.h - p.pad}
	p.deny = rect{p.allow.Left - px(8) - dw, p.allow.Top, p.allow.Left - px(8), p.allow.Bottom}
	if p.linkLabel != "" {
		p.link = rect{p.pad + px(4), p.allow.Top, p.deny.Left - px(8), p.allow.Bottom}
	}
	p.fontSmall = font(px(12), 400)
	p.fontBold = font(px(15), 600)
	p.fontButton = font(px(13), 600)
	p.icon = exeIcon(p.iconSize)

	popupMu.Lock()
	for slots[p.slot] {
		p.slot++
	}
	slots[p.slot] = true
	popupMu.Unlock()
	x, y := popupPosition(p.w, p.h, p.slot, px(12))

	const wsPopup = 0x80000000
	const exTopmost, exToolWindow, exLayered, exNoActivate = 0x8, 0x80, 0x80000, 0x08000000
	title, _ := windows.UTF16PtrFromString(AppName)
	hwnd, _, _ := procCreateWindowEx.Call(exTopmost|exToolWindow|exLayered|exNoActivate,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)), wsPopup,
		uintptr(x), uintptr(y), uintptr(p.w), uintptr(p.h), 0, 0, inst, 0)
	if hwnd == 0 {
		releaseSlot(p.slot)
		return 0
	}
	popupMu.Lock()
	popups[hwnd] = p
	popupMu.Unlock()
	if procSetWindowDisplayAffinity.Find() == nil {
		procSetWindowDisplayAffinity.Call(hwnd, 0x11) // WDA_EXCLUDEFROMCAPTURE: friends don't see it
	}

	// Windows 11: rounded corners and a butter outline. Older Windows just
	// ignores these.
	if procDwmSetWindowAttribute.Find() == nil {
		corner, border := uint32(2), uint32(colButter) // DWMWCP_ROUND
		procDwmSetWindowAttribute.Call(hwnd, 33, uintptr(unsafe.Pointer(&corner)), 4)
		procDwmSetWindowAttribute.Call(hwnd, 34, uintptr(unsafe.Pointer(&border)), 4)
	}
	procSetLayeredWindowAttributes.Call(hwnd, 0, 0, 2) // LWA_ALPHA: start invisible, fade in
	procShowWindow.Call(hwnd, 4)                       // SW_SHOWNOACTIVATE
	procSetTimer.Call(hwnd, 1, 15, 0)
	if !p.deadline.IsZero() {
		procSetTimer.Call(hwnd, 2, 250, 0)
	}
	return hwnd
}

// popupPosition puts the popup in the corner of the work area next to the
// notification area, whichever edge the taskbar is on.
func popupPosition(w, h int32, slot int, margin int32) (int32, int32) {
	var work rect
	procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&work)), 0) // SPI_GETWORKAREA
	sw, _, _ := procGetSystemMetrics.Call(0)                                    // SM_CXSCREEN
	stack := int32(slot) * (h + margin)
	x := work.Right - w - margin
	y := work.Bottom - h - margin - stack
	switch {
	case work.Top > 0: // taskbar on top: the tray is top right, stack downwards
		y = work.Top + margin + stack
	case work.Left > 0: // taskbar on the left: the tray is bottom left
		x = work.Left + margin
	case work.Right < int32(sw): // on the right: bottom right, as usual
	}
	return x, y
}

func wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	popupMu.Lock()
	p := popups[hwnd]
	popupMu.Unlock()
	if p == nil {
		r, _, _ := procDefWindowProc.Call(hwnd, message, wparam, lparam)
		return r
	}
	switch message {
	case wmMouseActivate:
		return 3 // MA_NOACTIVATE: clicking doesn't steal focus from the movie
	case wmEraseBkgnd:
		return 1
	case wmPaint:
		p.paint(hwnd)
		return 0
	case wmTimer:
		if wparam == 2 { // the countdown
			if !time.Now().Before(p.deadline) {
				p.result = 4
				procDestroyWindow.Call(hwnd)
			} else if p.countdown() != p.shown {
				procInvalidateRect.Call(hwnd, 0, 0)
			}
			return 0
		}
		p.alpha += 40
		if p.alpha >= 255 {
			p.alpha = 255
			procKillTimer.Call(hwnd, 1)
		}
		procSetLayeredWindowAttributes.Call(hwnd, 0, uintptr(p.alpha), 2)
		return 0
	case wmMouseMove:
		if !p.tracking {
			tme := trackMouseEvent{Flags: 0x2, Track: hwnd} // TME_LEAVE
			tme.Size = uint32(unsafe.Sizeof(tme))
			procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			p.tracking = true
		}
		p.setHot(hwnd, p.hit(lparam))
		return 0
	case wmMouseLeave:
		p.tracking = false
		p.setHot(hwnd, 0)
		return 0
	case wmSetCursor:
		if p.hot != 0 {
			hand, _, _ := procLoadCursor.Call(0, 32649) // IDC_HAND
			procSetCursor.Call(hand)
			return 1
		}
	case wmLButtonUp:
		if b := p.hit(lparam); b != 0 {
			p.result = b
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		popupMu.Lock()
		delete(popups, hwnd)
		popupMu.Unlock()
		releaseSlot(p.slot)
		for _, f := range []uintptr{p.fontSmall, p.fontBold, p.fontButton} {
			procDeleteObject.Call(f)
		}
		if p.result != 0 && !p.dismissed.Load() {
			p.answer <- p.result - 1
		}
		close(p.answer) // a dismissed popup answers nothing
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, message, wparam, lparam)
	return r
}

func (p *popup) hit(lparam uintptr) int {
	x, y := int32(int16(lparam&0xFFFF)), int32(int16(lparam>>16&0xFFFF))
	switch {
	case p.allow.has(x, y):
		return 1
	case p.deny.has(x, y):
		return 2
	case p.linkLabel != "" && p.link.has(x, y):
		return 3
	}
	return 0
}

func (p *popup) setHot(hwnd uintptr, b int) {
	if b != p.hot {
		p.hot = b
		procInvalidateRect.Call(hwnd, 0, 0)
	}
}

// paint draws the whole card off-screen, then copies it in one go (no flicker).
func (p *popup) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	mem, _, _ := procCreateCompatibleDC.Call(hdc)
	bmp, _, _ := procCreateCompatibleBitmap.Call(hdc, uintptr(p.w), uintptr(p.h))
	old, _, _ := procSelectObject.Call(mem, bmp)
	defer func() {
		procBitBlt.Call(hdc, 0, 0, uintptr(p.w), uintptr(p.h), mem, 0, 0, 0x00CC0020) // SRCCOPY
		procSelectObject.Call(mem, old)
		procDeleteObject.Call(bmp)
		procDeleteDC.Call(mem)
	}()

	fill(mem, rect{0, 0, p.w, p.h}, colBack)
	fill(mem, rect{0, 0, int32(4 * p.scale), p.h}, colRed) // a stripe of the popcorn bucket
	procSetBkMode.Call(mem, 1)                             // TRANSPARENT

	left := p.pad + int32(4*p.scale)
	if p.icon != 0 {
		procDrawIconEx.Call(mem, uintptr(left), uintptr(p.pad), p.icon, uintptr(p.iconSize), uintptr(p.iconSize), 0, 0, 3)
	}
	tx := left + p.iconSize + p.iconGap
	text(mem, p.fontSmall, colMuted, p.title, rect{tx, p.pad, p.w - p.pad, p.pad + int32(18*p.scale)}, 0x20|0x800)
	text(mem, p.fontBold, colText, p.text, rect{tx, p.pad + int32(18*p.scale), p.w - p.pad, p.allow.Top - int32(6*p.scale)}, 0x10|0x800|0x8000)

	allow, deny := uint32(colButter), uint32(colDeny)
	if p.hot == 1 {
		allow = colButterHot
	}
	if p.hot == 2 {
		deny = colDenyHot
	}
	r := int32(10 * p.scale)
	roundRect(mem, p.allow, allow, r)
	roundRect(mem, p.deny, deny, r)
	const center = 0x1 | 0x4 | 0x20 | 0x800 // DT_CENTER | DT_VCENTER | DT_SINGLELINE | DT_NOPREFIX
	allowLabel := p.allowLabel
	if !p.deadline.IsZero() {
		p.shown = p.countdown()
		allowLabel += " (" + p.shown + ")"
	}
	text(mem, p.fontButton, 0x00000000, allowLabel, p.allow, center)
	text(mem, p.fontButton, colText, p.denyLabel, p.deny, center)
	if p.linkLabel != "" {
		link := uint32(colMuted)
		if p.hot == 3 {
			link = colText
		}
		text(mem, p.fontSmall, link, p.linkLabel, p.link, 0x4|0x20|0x800|0x8000) // DT_VCENTER | DT_SINGLELINE | DT_NOPREFIX | DT_END_ELLIPSIS
	}
}

// countdown is the time left on the yes button, e.g. "2:59".
func (p *popup) countdown() string {
	left := max(0, time.Until(p.deadline).Round(time.Second))
	return fmt.Sprintf("%d:%02d", int(left.Minutes()), int(left.Seconds())%60)
}

func fill(dc uintptr, r rect, color uint32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	procFillRect.Call(dc, uintptr(unsafe.Pointer(&r)), brush)
	procDeleteObject.Call(brush)
}

func roundRect(dc uintptr, r rect, color uint32, radius int32) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(color))
	nullPen, _, _ := procGetStockObject.Call(8) // NULL_PEN
	oldBrush, _, _ := procSelectObject.Call(dc, brush)
	oldPen, _, _ := procSelectObject.Call(dc, nullPen)
	procRoundRect.Call(dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right+1), uintptr(r.Bottom+1), uintptr(radius), uintptr(radius))
	procSelectObject.Call(dc, oldBrush)
	procSelectObject.Call(dc, oldPen)
	procDeleteObject.Call(brush)
}

func text(dc, font uintptr, color uint32, s string, r rect, format uintptr) {
	old, _, _ := procSelectObject.Call(dc, font)
	procSetTextColor.Call(dc, uintptr(color))
	u, _ := windows.UTF16FromString(s)
	procDrawText.Call(dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&r)), format)
	procSelectObject.Call(dc, old)
}

func font(height int32, weight int) uintptr {
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	f, _, _ := procCreateFont.Call(uintptr(-height), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face))) // DEFAULT_CHARSET, CLEARTYPE_QUALITY
	return f
}

// exeIcon is the popcorn icon from our own .exe, at the size asked for.
func exeIcon(size int32) uintptr {
	iconOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil || procPrivateExtractIcons.Find() != nil {
			return
		}
		path, _ := windows.UTF16PtrFromString(exe)
		var id uint32
		procPrivateExtractIcons.Call(uintptr(unsafe.Pointer(path)), 0, uintptr(size), uintptr(size),
			uintptr(unsafe.Pointer(&appIcon)), uintptr(unsafe.Pointer(&id)), 1, 0)
	})
	return appIcon
}

func releaseSlot(slot int) {
	popupMu.Lock()
	delete(slots, slot)
	popupMu.Unlock()
}
