//go:build windows

package audio

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

// Process loopback capture (Windows 10 2004+/11): records the audio of one
// process tree only, via ActivateAudioInterfaceAsync("VAD\Process_Loopback").

var (
	procActivateAudioInterfaceAsync = windows.NewLazySystemDLL("Mmdevapi.dll").NewProc("ActivateAudioInterfaceAsync")

	iidCompletionHandler = ole.NewGUID("{41D949AB-9862-444A-80F6-C261334DA5EB}")
	iidAgileObject       = ole.NewGUID("{94EA2B94-E9CC-49E0-C0FF-EE64CA8F5B90}")
)

const (
	activationTypeProcessLoopback = 1
	loopbackModeIncludeTree       = 0
	vtBlob                        = 65
	streamflagsSrcDefaultQuality  = 0x08000000
)

type activationParams struct {
	ActivationType  uint32
	TargetProcessID uint32
	LoopbackMode    uint32
}

// PROPVARIANT holding a BLOB, x64 layout.
type propVariantBlob struct {
	vt         uint16
	_, _, _    uint16
	cbSize     uint32
	_          uint32
	pBlobData  *activationParams
}

// A minimal COM object implementing IActivateAudioInterfaceCompletionHandler.
type completionVtbl struct {
	QueryInterface, AddRef, Release, ActivateCompleted uintptr
}

type completionHandler struct {
	vtbl *completionVtbl
	once sync.Once
	done chan struct{}
}

type comObject struct{ vtbl *[8]uintptr }

var (
	handlerVtbl = &completionVtbl{
		QueryInterface: syscall.NewCallback(func(this *completionHandler, riid *ole.GUID, out **completionHandler) uintptr {
			if ole.IsEqualGUID(riid, ole.IID_IUnknown) || ole.IsEqualGUID(riid, iidCompletionHandler) ||
				ole.IsEqualGUID(riid, iidAgileObject) {
				*out = this
				return 0
			}
			*out = nil
			return 0x80004002 // E_NOINTERFACE
		}),
		AddRef:  syscall.NewCallback(func(this *completionHandler) uintptr { return 1 }),
		Release: syscall.NewCallback(func(this *completionHandler) uintptr { return 1 }),
		ActivateCompleted: syscall.NewCallback(func(this *completionHandler, op uintptr) uintptr {
			this.once.Do(func() { close(this.done) })
			return 0
		}),
	}
	// Handlers are referenced from COM threads; keep them reachable until done.
	liveHandlers sync.Map
)

func activateProcessLoopback(pid uint32) (*wca.IAudioClient, error) {
	params := &activationParams{activationTypeProcessLoopback, pid, loopbackModeIncludeTree}
	pv := &propVariantBlob{vt: vtBlob, cbSize: uint32(unsafe.Sizeof(*params)), pBlobData: params}
	h := &completionHandler{vtbl: handlerVtbl, done: make(chan struct{})}
	liveHandlers.Store(h, true)
	defer liveHandlers.Delete(h)

	path, _ := windows.UTF16PtrFromString(`VAD\Process_Loopback`)
	var op *comObject
	hr, _, _ := procActivateAudioInterfaceAsync.Call(
		uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(wca.IID_IAudioClient)),
		uintptr(unsafe.Pointer(pv)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&op)))
	if hr != 0 {
		return nil, fmt.Errorf("ActivateAudioInterfaceAsync: 0x%08x", uint32(hr))
	}
	defer syscall.SyscallN(op.vtbl[2], uintptr(unsafe.Pointer(op))) // Release

	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("process loopback activation timed out")
	}
	runtime.KeepAlive(pv)

	var activateHR int32
	var client *wca.IAudioClient
	r, _, _ := syscall.SyscallN(op.vtbl[3], uintptr(unsafe.Pointer(op)), // GetActivateResult
		uintptr(unsafe.Pointer(&activateHR)), uintptr(unsafe.Pointer(&client)))
	if r != 0 || activateHR != 0 || client == nil {
		return nil, fmt.Errorf("process loopback activation failed: 0x%08x/0x%08x", uint32(r), uint32(activateHR))
	}
	return client, nil
}

// captureProcess records pid's process tree into src until ctx is cancelled.
func captureProcess(ctx context.Context, pid uint32, src *Source) error {
	runtime.LockOSThread() // COM objects are bound to this thread
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 { // S_FALSE: already initialized
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	}
	defer ole.CoUninitialize()

	client, err := activateProcessLoopback(pid)
	if err != nil {
		return err
	}
	defer client.Release()

	// Ask WASAPI to convert whatever the app plays into the mixer's format.
	wfx := &wca.WAVEFORMATEX{
		WFormatTag: 1, NChannels: MixChannels, NSamplesPerSec: MixRate, WBitsPerSample: 16,
		NBlockAlign: frameBytes, NAvgBytesPerSec: MixRate * frameBytes,
	}
	flags := uint32(wca.AUDCLNT_STREAMFLAGS_LOOPBACK | wca.AUDCLNT_STREAMFLAGS_EVENTCALLBACK |
		wca.AUDCLNT_STREAMFLAGS_AUTOCONVERTPCM | streamflagsSrcDefaultQuality)
	if err := client.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, flags, 200_000, 0, wfx, nil); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(event)
	if err := client.SetEventHandle(uintptr(event)); err != nil {
		return fmt.Errorf("set event: %w", err)
	}
	var capClient *wca.IAudioCaptureClient
	if err := client.GetService(wca.IID_IAudioCaptureClient, &capClient); err != nil {
		return fmt.Errorf("capture client: %w", err)
	}
	defer capClient.Release()
	if err := client.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer client.Stop()

	for ctx.Err() == nil {
		windows.WaitForSingleObject(event, 100)
		for {
			var frames uint32
			if err := capClient.GetNextPacketSize(&frames); err != nil || frames == 0 {
				break
			}
			var data *byte
			var bufFlags uint32
			var devPos, qpcPos uint64
			if err := capClient.GetBuffer(&data, &frames, &bufFlags, &devPos, &qpcPos); err != nil {
				break
			}
			n := int(frames) * frameBytes
			chunk := make([]byte, n)
			if bufFlags&wca.AUDCLNT_BUFFERFLAGS_SILENT == 0 {
				copy(chunk, unsafe.Slice(data, n))
			}
			capClient.ReleaseBuffer(frames)
			src.Push(chunk)
		}
	}
	return nil
}
