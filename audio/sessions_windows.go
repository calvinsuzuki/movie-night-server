//go:build windows

package audio

import (
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca/pkg/wca"
	"golang.org/x/sys/windows"
)

type session struct {
	pid     uint32
	name    string // normalized exe name
	display string
}

// listSessions returns the processes that have an audio session on the
// default playback device. Must run on a COM-initialized thread.
func listSessions() ([]session, error) {
	var enum *wca.IMMDeviceEnumerator
	if err := wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL,
		wca.IID_IMMDeviceEnumerator, &enum); err != nil {
		return nil, fmt.Errorf("device enumerator: %w", err)
	}
	defer enum.Release()
	var dev *wca.IMMDevice
	if err := enum.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &dev); err != nil {
		return nil, fmt.Errorf("default output device: %w", err)
	}
	defer dev.Release()
	var mgr *wca.IAudioSessionManager2
	if err := dev.Activate(wca.IID_IAudioSessionManager2, wca.CLSCTX_ALL, nil, &mgr); err != nil {
		return nil, fmt.Errorf("session manager: %w", err)
	}
	defer mgr.Release()
	var sessEnum *wca.IAudioSessionEnumerator
	if err := mgr.GetSessionEnumerator(&sessEnum); err != nil {
		return nil, fmt.Errorf("session enumerator: %w", err)
	}
	defer sessEnum.Release()

	var count int
	if err := sessEnum.GetCount(&count); err != nil {
		return nil, err
	}
	var out []session
	for i := 0; i < count; i++ {
		var ctrl *wca.IAudioSessionControl
		if err := sessEnum.GetSession(i, &ctrl); err != nil {
			continue
		}
		dispatch, err := ctrl.QueryInterface(wca.IID_IAudioSessionControl2)
		ctrl.Release()
		if err != nil {
			continue
		}
		ctrl2 := (*wca.IAudioSessionControl2)(unsafe.Pointer(dispatch))
		var pid, state uint32
		ctrl2.GetProcessId(&pid)
		ctrl2.GetState(&state)
		ctrl2.Release()
		if pid == 0 || state == wca.AudioSessionStateExpired { // pid 0: system sounds
			continue
		}
		path := processPath(pid)
		if path == "" {
			continue
		}
		out = append(out, session{pid: pid, name: Normalize(path), display: fileDescription(path)})
	}
	return out, nil
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}

var descCache = map[string]string{}

// fileDescription returns e.g. "Google Chrome" for chrome.exe, falling back
// to the exe name.
func fileDescription(path string) string {
	if d, ok := descCache[path]; ok {
		return d
	}
	desc := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if size, err := windows.GetFileVersionInfoSize(path, nil); err == nil && size > 0 {
		info := make([]byte, size)
		if windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&info[0])) == nil {
			var trans *[2]uint16
			var n uint32
			if windows.VerQueryValue(unsafe.Pointer(&info[0]), `\VarFileInfo\Translation`,
				unsafe.Pointer(&trans), &n) == nil && n >= 4 {
				key := fmt.Sprintf(`\StringFileInfo\%04x%04x\FileDescription`, trans[0], trans[1])
				var p *uint16
				if windows.VerQueryValue(unsafe.Pointer(&info[0]), key, unsafe.Pointer(&p), &n) == nil && n > 1 {
					if s := strings.TrimSpace(windows.UTF16PtrToString(p)); s != "" {
						desc = s
					}
				}
			}
		}
	}
	descCache[path] = desc
	return desc
}

var _ = ole.IID_IUnknown
