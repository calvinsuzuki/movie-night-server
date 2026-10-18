package capture

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"

	"movienight/platform"
)

type Monitor struct {
	Adapter, Output int    // Windows: DXGI adapter and output index for ddagrab
	Device          string // macOS avfoundation index / X11 display+offset
	Name            string
	Width, Height   int
}

func (m Monitor) String() string {
	if m.Name != "" {
		return fmt.Sprintf("%s · %d×%d", m.Name, m.Width, m.Height)
	}
	return fmt.Sprintf("%d×%d", m.Width, m.Height)
}

var (
	sizeRe   = regexp.MustCompile(`Video: .*?(\d{3,5})x(\d{3,5})`)
	deviceRe = regexp.MustCompile(`Using device ([0-9a-f]{4}):[0-9a-f]{4}`)
	// xrandr --listmonitors: " 0: +*HDMI-1 1920/530x1080/300+0+0  HDMI-1"
	xrandrRe = regexp.MustCompile(`(?m)^\s*\d+:\s+\S+\s+(\d+)/\d+x(\d+)/\d+\+(\d+)\+(\d+)\s+(\S+)`)
)

// ListMonitors finds capturable monitors by asking ffmpeg to grab one frame
// from each candidate, which also tells us the native resolution.
func ListMonitors(ffmpeg string) ([]Monitor, error) {
	switch runtime.GOOS {
	case "windows":
		var mons []Monitor
		for adapter := 0; adapter < 4; adapter++ {
			for output := 0; output < 6; output++ {
				w, h, out, ok := probe(ffmpeg, "-loglevel", "verbose",
					"-init_hw_device", fmt.Sprintf("d3d11va=cap:%d", adapter), "-filter_hw_device", "cap",
					"-filter_complex", fmt.Sprintf("ddagrab=output_idx=%d:framerate=5", output))
				// The Microsoft Basic Render Driver (vendor 1414) mirrors the real
				// GPU's outputs; only keep adapters that report real hardware.
				dev := deviceRe.FindSubmatch(out)
				if !ok || dev == nil || string(dev[1]) == "1414" {
					break
				}
				mons = append(mons, Monitor{Adapter: adapter, Output: output, Width: w, Height: h,
					Name: fmt.Sprintf("Monitor %d", len(mons)+1)})
			}
		}
		if len(mons) == 0 {
			return nil, fmt.Errorf("ffmpeg could not capture any monitor (is ddagrab supported?)")
		}
		return mons, nil
	case "darwin":
		// avfoundation numbers screens after cameras; try the usual range.
		var mons []Monitor
		for i := 0; i < 8; i++ {
			dev := strconv.Itoa(i)
			if w, h, _, ok := probe(ffmpeg, "-f", "avfoundation", "-capture_cursor", "0", "-i", dev+":none"); ok {
				mons = append(mons, Monitor{Device: dev, Width: w, Height: h,
					Name: fmt.Sprintf("Screen %d", len(mons)+1)})
			}
		}
		return mons, nil
	default:
		display := os.Getenv("DISPLAY")
		if display == "" {
			display = ":0"
		}
		var mons []Monitor
		if out, err := exec.Command("xrandr", "--listmonitors").Output(); err == nil {
			for _, m := range xrandrRe.FindAllStringSubmatch(string(out), -1) {
				w, _ := strconv.Atoi(m[1])
				h, _ := strconv.Atoi(m[2])
				mons = append(mons, Monitor{Device: fmt.Sprintf("%s+%s,%s", display, m[3], m[4]),
					Width: w, Height: h, Name: m[5]})
			}
		}
		if len(mons) == 0 {
			w, h, _, ok := probe(ffmpeg, "-f", "x11grab", "-i", display)
			if !ok {
				return nil, fmt.Errorf("x11grab of %s failed (on Ubuntu, log in with \"Ubuntu on Xorg\")", display)
			}
			mons = append(mons, Monitor{Device: display, Width: w, Height: h, Name: "Screen"})
		}
		return mons, nil
	}
}

func probe(ffmpeg string, input ...string) (w, h int, out []byte, ok bool) {
	args := append([]string{"-hide_banner"}, input...)
	args = append(args, "-frames:v", "1", "-f", "null", "-")
	out, err := platform.Command(context.Background(), ffmpeg, args...).CombinedOutput()
	if err != nil {
		return 0, 0, out, false
	}
	m := sizeRe.FindSubmatch(out)
	if m == nil {
		return 0, 0, out, false
	}
	w, _ = strconv.Atoi(string(m[1]))
	h, _ = strconv.Atoi(string(m[2]))
	return w, h, out, true
}
