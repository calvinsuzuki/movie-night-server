//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The process puts itself in a job that kills every member when its last
// handle closes, so ffmpeg and cloudflared die with Movie night even if it
// crashes or is force-closed (child processes join the job automatically).
func init() {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	windows.AssignProcessToJobObject(job, windows.CurrentProcess()) // handle kept until exit
}

// Command runs a console program (ffmpeg) without flashing a console window.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

func Open(target string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

func CopyText(s string) error {
	text, err := windows.UTF16FromString(s)
	if err != nil {
		return err
	}
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		return fmt.Errorf("open clipboard: %w", err)
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	const gmemMoveable, cfUnicodeText = 0x0002, 13
	size := uintptr(len(text) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("alloc: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return fmt.Errorf("lock: %w", err)
	}
	procRtlMoveMemory.Call(p, uintptr(unsafe.Pointer(&text[0])), size)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		return fmt.Errorf("set clipboard: %w", err)
	}
	return nil
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "MovieNight"

func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func SetAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`"`)
}

// SingleInstance returns false if Movie night is already running.
func SingleInstance() bool {
	name, _ := windows.UTF16PtrFromString(`Local\MovieNight.SingleInstance`)
	_, err := windows.CreateMutex(nil, false, name) // handle intentionally kept until exit
	return err != windows.ERROR_ALREADY_EXISTS
}

func Alert(msg string) {
	title, _ := windows.UTF16PtrFromString(AppName)
	text, _ := windows.UTF16PtrFromString(msg)
	windows.MessageBox(0, text, title, windows.MB_OK|windows.MB_ICONINFORMATION|windows.MB_SETFOREGROUND)
}

var (
	winmm                 = windows.NewLazySystemDLL("winmm.dll")
	procPlaySound         = winmm.NewProc("PlaySoundW")
	procEnumThreadWindows = user32.NewProc("EnumThreadWindows")
	procPostMessage       = user32.NewProc("PostMessageW")
)

// PlaySound plays a WAV file held in memory, without waiting. wav must stay
// alive while it plays (embedded assets do). It plays inside our own process,
// whose sound is never shared.
func PlaySound(wav []byte) {
	if len(wav) == 0 {
		return
	}
	const sndAsync, sndNoDefault, sndMemory = 0x1, 0x2, 0x4
	procPlaySound.Call(uintptr(unsafe.Pointer(&wav[0])), 0, sndAsync|sndNoDefault|sndMemory)
}

const idYes, idNo = 6, 7

// closeDialog answers "No" on a popup window so its thread returns; Ask then
// reports nothing, because the popup was dismissed rather than answered.
var closeDialog = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	const wmCommand = 0x0111
	procPostMessage.Call(hwnd, wmCommand, idNo, 0)
	return 1
})

// Ask shows a Yes/No popup on top of everything without blocking. answer
// gets true (Yes) or false (No) when the user clicks; dismiss closes the
// popup without an answer.
func Ask(title, msg string) (answer <-chan bool, dismiss func()) {
	ch := make(chan bool, 1)
	tid := make(chan uint32, 1)
	var dismissed atomic.Bool
	go func() {
		runtime.LockOSThread() // the dialog belongs to this thread
		defer runtime.UnlockOSThread()
		tid <- windows.GetCurrentThreadId()
		t, _ := windows.UTF16PtrFromString(title)
		m, _ := windows.UTF16PtrFromString(msg)
		const mbYesNo, mbIconQuestion, mbSetForeground, mbTopmost = 0x4, 0x20, 0x10000, 0x40000
		r, _ := windows.MessageBox(0, m, t, mbYesNo|mbIconQuestion|mbSetForeground|mbTopmost)
		if !dismissed.Load() {
			ch <- r == idYes
		}
	}()
	thread := <-tid
	return ch, func() {
		if !dismissed.Swap(true) {
			procEnumThreadWindows.Call(uintptr(thread), closeDialog, 0)
		}
	}
}
