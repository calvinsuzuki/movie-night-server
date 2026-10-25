package capture

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"movienight/platform"
)

// Camera is a webcam ffmpeg can open: Windows DirectShow or Linux V4L2.
type Camera struct {
	Name   string // as the system names it, e.g. "Logi C270 HD WebCam"
	Device string // ffmpeg's input: "video=<name>" (dshow) or "/dev/video0"
}

func (c Camera) String() string { return c.Name }

// Corners the camera bubble can sit in.
const (
	BottomRight = "bottom-right"
	BottomLeft  = "bottom-left"
	TopRight    = "top-right"
	TopLeft     = "top-left"
)

// dshow lists devices as `[dshow @ 0x..] "Logi C270 HD WebCam" (video)`.
var dshowVideoRe = regexp.MustCompile(`"([^"]+)" \(video\)`)

// ListCameras finds the webcams on this computer. macOS isn't supported yet.
func ListCameras(ffmpeg string) []Camera {
	var cams []Camera
	switch runtime.GOOS {
	case "windows":
		out, _ := platform.Command(context.Background(), ffmpeg, "-hide_banner",
			"-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
		for _, m := range dshowVideoRe.FindAllStringSubmatch(string(out), -1) {
			cams = append(cams, Camera{Name: m[1], Device: "video=" + m[1]})
		}
	case "linux":
		// Each webcam usually shows up twice (picture and metadata); the
		// metadata node has index 1.
		nodes, _ := filepath.Glob("/sys/class/video4linux/video*")
		sort.Strings(nodes)
		for _, n := range nodes {
			if idx, _ := os.ReadFile(filepath.Join(n, "index")); strings.TrimSpace(string(idx)) != "0" {
				continue
			}
			name, _ := os.ReadFile(filepath.Join(n, "name"))
			cams = append(cams, Camera{Name: strings.TrimSpace(string(name)), Device: "/dev/" + filepath.Base(n)})
		}
	}
	return cams
}

// cameraInput is the ffmpeg input for a camera.
func cameraInput(c Camera) []string {
	if runtime.GOOS == "windows" {
		// A big real-time buffer: webcams deliver in bursts.
		return []string{"-f", "dshow", "-rtbufsize", "64M", "-i", c.Device}
	}
	return []string{"-f", "v4l2", "-i", c.Device}
}
