// Package capture builds and supervises the ffmpeg processes that grab the
// screen/audio, encode them and emit RTP to localhost for the WebRTC hub.
package capture

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"runtime"
	"strconv"
	"strings"
	"time"

	"movienight/platform"
)

type Config struct {
	FFmpeg    string
	Monitor   Monitor
	FPS       int
	Height    int // 0 = native resolution
	Bitrate   int // bits per second
	Encoder   string
	Cursor    bool
	VideoPort int
	AudioPort int

	// Camera, if set, is shared instead of the monitor.
	Camera *Camera
	// Bubble, if set, is a camera shown small in a corner of the monitor
	// (BubbleCorner, one of BottomRight...).
	Bubble       *Camera
	BubbleCorner string
}

// Encoders in preference order; "auto" picks the first that can actually open.
var encoderOrder = map[string][]string{
	"windows": {"h264_nvenc", "h264_qsv", "h264_amf", "libx264"},
	"darwin":  {"h264_videotoolbox", "libx264"},
	"linux":   {"h264_nvenc", "libx264"},
}

var encoderAliases = map[string]string{
	"nvenc": "h264_nvenc", "qsv": "h264_qsv", "amf": "h264_amf",
	"videotoolbox": "h264_videotoolbox", "x264": "libx264",
}

// DetectEncoder returns a working ffmpeg H.264 encoder. Being listed by
// `ffmpeg -encoders` is not enough (NVENC with an outdated driver, AMF next to
// an NVIDIA capture device...), so each candidate encodes a few frames through
// the real capture pipeline.
func DetectEncoder(c Config, pref string) (string, error) {
	candidates := encoderOrder[runtime.GOOS]
	if pref != "" && pref != "auto" {
		name := pref
		if alias, ok := encoderAliases[pref]; ok {
			name = alias
		}
		candidates = []string{name}
	}
	for _, enc := range candidates {
		args := append([]string{"-hide_banner", "-loglevel", "error"}, captureArgs(c)...)
		args = append(args, encoderArgs(c, enc)...)
		args = append(args, "-frames:v", "5", "-f", "null", "-")
		out, err := platform.Command(context.Background(), c.FFmpeg, args...).CombinedOutput()
		if err == nil {
			return enc, nil
		}
		log.Printf("encoder %s unavailable: %s", enc, firstLine(out))
	}
	return "", fmt.Errorf("no usable H.264 encoder among %v", candidates)
}

// VideoArgs returns the ffmpeg arguments for screen capture -> H.264 -> RTP.
func VideoArgs(c Config, encoder string) []string {
	args := []string{"-hide_banner", "-loglevel", "warning", "-stats", "-stats_period", "10"}
	args = append(args, captureArgs(c)...)
	args = append(args, encoderArgs(c, encoder)...)
	// SPS/PPS in-band before every keyframe: viewers can join at any keyframe.
	return append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "rtp", rtpURL(c.VideoPort))
}

func captureArgs(c Config) []string {
	if c.Camera != nil {
		// The camera alone, never larger than it really is.
		vf := fmt.Sprintf("fps=%d,", c.FPS)
		if c.Height > 0 {
			vf += fmt.Sprintf(`scale=-2:min(ih\,%d):flags=bicubic,`, c.Height)
		}
		return append(cameraInput(*c.Camera), "-vf", vf+"format=nv12")
	}
	if c.Bubble != nil && runtime.GOOS != "darwin" {
		return bubbleArgs(c)
	}
	var args []string
	scale := ""
	if c.Height > 0 && c.Height != c.Monitor.Height {
		scale = fmt.Sprintf("scale=-2:%d:flags=bicubic,", c.Height)
	}
	switch runtime.GOOS {
	case "windows":
		args = append(args, ddagrabArgs(c)...)
		args = append(args, "-filter_complex", ddagrab(c)+scale+"format=nv12[v]", "-map", "[v]")
	case "darwin":
		args = append(args, "-f", "avfoundation", "-framerate", strconv.Itoa(c.FPS),
			"-capture_cursor", strconv.Itoa(boolInt(c.Cursor)),
			"-i", c.Monitor.Device+":none", "-vf", scale+"format=nv12")
	default:
		args = append(args, x11grabInput(c)...)
		args = append(args, "-vf", scale+"format=nv12")
	}
	return args
}

// ddagrabArgs open the GPU that owns the monitor, for ddagrab.
func ddagrabArgs(c Config) []string {
	return []string{"-init_hw_device", fmt.Sprintf("d3d11va=cap:%d", c.Monitor.Adapter), "-filter_hw_device", "cap"}
}

// ddagrab is Desktop Duplication on the GPU that owns the monitor. Frames
// are copied to system memory once (~200 MB/s at 1080p) so any encoder on
// any GPU can be used.
func ddagrab(c Config) string {
	return fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:draw_mouse=%d,hwdownload,format=bgra,",
		c.Monitor.Output, c.FPS, boolInt(c.Cursor))
}

func x11grabInput(c Config) []string {
	return []string{"-f", "x11grab", "-framerate", strconv.Itoa(c.FPS),
		"-draw_mouse", strconv.Itoa(boolInt(c.Cursor)),
		"-video_size", fmt.Sprintf("%dx%d", c.Monitor.Width, c.Monitor.Height),
		"-i", c.Monitor.Device}
}

// bubbleArgs put the camera, a quarter of the picture's height with a
// thin red frame, in a corner of the monitor.
func bubbleArgs(c Config) []string {
	h := c.Height
	if h <= 0 {
		h = c.Monitor.Height
	}
	scale := ""
	if h != c.Monitor.Height {
		scale = fmt.Sprintf("scale=-2:%d:flags=bicubic,", h)
	}
	size, border, margin := h/4&^1, max(2, h/540)&^1, max(2, h/108)&^1
	x, y := fmt.Sprintf("main_w-overlay_w-%d", margin), fmt.Sprintf("main_h-overlay_h-%d", margin)
	switch c.BubbleCorner {
	case BottomLeft:
		x = strconv.Itoa(margin)
	case TopRight:
		y = strconv.Itoa(margin)
	case TopLeft:
		x, y = strconv.Itoa(margin), strconv.Itoa(margin)
	}
	// Both are live sources with their own clocks: start both at zero.
	cam := fmt.Sprintf("fps=%d,scale=-2:%d,pad=iw+%d:ih+%d:%d:%d:color=0xAB1313,format=yuv420p,setpts=PTS-STARTPTS[b]",
		c.FPS, size-2*border, 2*border, 2*border, border, border)
	mix := fmt.Sprintf("[s][b]overlay=x=%s:y=%s:eof_action=pass:repeatlast=1,fps=%d,format=nv12[v]", x, y, c.FPS)

	var args []string
	if runtime.GOOS == "windows" {
		args = append(args, cameraInput(*c.Bubble)...)
		args = append(args, ddagrabArgs(c)...)
		screen := ddagrab(c) + scale + "format=yuv420p,setpts=PTS-STARTPTS[s]"
		return append(args, "-filter_complex", screen+";[0:v]"+cam+";"+mix, "-map", "[v]")
	}
	args = append(args, x11grabInput(c)...)
	args = append(args, cameraInput(*c.Bubble)...)
	screen := "[0:v]" + scale + "format=yuv420p,setpts=PTS-STARTPTS[s]"
	return append(args, "-filter_complex", screen+";[1:v]"+cam+";"+mix, "-map", "[v]")
}

func encoderArgs(c Config, encoder string) []string {
	gop := strconv.Itoa(c.FPS * 2) // new viewers / lost packets recover within 2s
	rate := strconv.Itoa(c.Bitrate)
	args := []string{"-c:v", encoder,
		"-b:v", rate, "-maxrate", rate, "-bufsize", strconv.Itoa(c.Bitrate / 2),
		"-g", gop, "-bf", "0"}
	switch encoder {
	case "h264_nvenc":
		args = append(args, "-preset", "p4", "-tune", "ll", "-rc", "cbr",
			"-zerolatency", "1", "-forced-idr", "1", "-profile:v", "baseline")
	case "h264_qsv":
		args = append(args, "-preset", "veryfast", "-look_ahead", "0",
			"-async_depth", "1", "-profile:v", "baseline")
	case "h264_amf":
		args = append(args, "-usage", "lowlatency", "-rc", "cbr",
			"-profile:v", "constrained_baseline")
	case "h264_videotoolbox":
		args = append(args, "-realtime", "1", "-profile:v", "constrained_baseline")
	case "libx264":
		args = append(args, "-preset", "veryfast", "-tune", "zerolatency",
			"-profile:v", "baseline", "-sc_threshold", "0", "-keyint_min", gop)
	}
	return args
}

// AudioArgs returns ffmpeg arguments for system audio -> Opus -> RTP; input
// comes from the audio manager (raw PCM on stdin or a PulseAudio monitor).
func AudioArgs(c Config, input []string) []string {
	args := append([]string{"-hide_banner", "-loglevel", "warning"}, input...)
	return append(args, "-ac", "2", "-ar", "48000",
		"-c:a", "libopus", "-b:a", "128k", "-application", "audio", "-frame_duration", "20",
		"-f", "rtp", rtpURL(c.AudioPort))
}

// rtpURL keeps packets under Tailscale's 1280-byte MTU after SRTP/WireGuard overhead.
func rtpURL(port int) string {
	return fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port)
}

// Supervise runs ffmpeg until ctx is cancelled, restarting it if it exits.
// feed, if non-nil, streams data into ffmpeg's stdin.
func Supervise(ctx context.Context, name, ffmpeg string, args []string,
	feed func(ctx context.Context, w io.Writer) error) {
	for ctx.Err() == nil {
		log.Printf("[%s] starting ffmpeg", name)
		err := runOnce(ctx, name, ffmpeg, args, feed)
		if ctx.Err() != nil {
			return
		}
		log.Printf("[%s] ffmpeg exited (%v), restarting in 2s", name, err)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func runOnce(ctx context.Context, name, ffmpeg string, args []string,
	feed func(ctx context.Context, w io.Writer) error) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := platform.Command(runCtx, ffmpeg, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	var stdin io.WriteCloser
	if feed != nil {
		if stdin, err = cmd.StdinPipe(); err != nil {
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go logLines(name, stderr)
	if feed != nil {
		go func() {
			err := feed(runCtx, stdin)
			stdin.Close()
			if err != nil && runCtx.Err() == nil {
				log.Printf("[%s] input stopped: %v", name, err)
				cancel()
			}
		}()
	}
	return cmd.Wait()
}

// logLines prefixes ffmpeg's stderr; progress lines end in \r, so split on both.
func logLines(name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			log.Printf("[%s] %s", name, line)
		}
	}
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
