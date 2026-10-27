package stream

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCastLive encodes a test picture and tone the way a share does, sends
// them through the hub, and checks ffmpeg can decode what a viewer watching
// through the link receives: MOVIENIGHT_LIVE=1 go test ./stream -run CastLive -v
func TestCastLive(t *testing.T) {
	if os.Getenv("MOVIENIGHT_LIVE") == "" {
		t.Skip("set MOVIENIGHT_LIVE=1 to run ffmpeg")
	}
	ffmpeg := os.Getenv("MOVIENIGHT_FFMPEG")
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	h, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Forward(ctx, 15004, "video")
	go h.Forward(ctx, 15006, "audio")

	enc := exec.CommandContext(ctx, ffmpeg, "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24", "-f", "lavfi", "-i", "sine=f=440",
		"-map", "0:v", "-c:v", "libx264", "-profile:v", "baseline", "-tune", "zerolatency", "-g", "48", "-bf", "0",
		"-bsf:v", "dump_extra=freq=keyframe", "-f", "rtp", "rtp://127.0.0.1:15004?pkt_size=1200",
		"-map", "1:a", "-ac", "2", "-ar", "48000", "-c:a", "libopus", "-frame_duration", "20",
		"-f", "rtp", "rtp://127.0.0.1:15006?pkt_size=1200")
	if err := enc.Start(); err != nil {
		t.Skipf("no ffmpeg: %v", err)
	}
	defer enc.Process.Kill()

	time.Sleep(2500 * time.Millisecond) // a keyframe has gone by: start mid-GOP
	id, v := h.Watch("test")
	var video, audio bytes.Buffer
	codec := ""
	deadline := time.After(4 * time.Second)
collect:
	for {
		select {
		case m := <-v.out:
			switch m[0] {
			case 'V', 'A':
				n := int(m[1])
				if m[0] == 'V' {
					codec = string(m[2 : 2+n])
					video.Write(m[2+n:])
				} else {
					audio.Write(m[2+n:])
				}
			case 'v':
				video.Write(m[1:])
			case 'a':
				audio.Write(m[1:])
			}
		case <-deadline:
			break collect
		}
	}
	h.Remove(id)

	if !strings.HasPrefix(codec, "avc1.42") {
		t.Fatalf("video codec %q, want constrained baseline avc1.42…", codec)
	}
	dir := t.TempDir()
	for name, b := range map[string][]byte{"video.mp4": video.Bytes(), "audio.mp4": audio.Bytes()} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, b, 0o644)
		out, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
		if err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Fatalf("%s (%d bytes) doesn't decode cleanly: %v\n%s", name, len(b), err, out)
		}
		probe, _ := exec.Command(ffmpeg, "-hide_banner", "-i", path).CombinedOutput()
		if name == "video.mp4" && !strings.Contains(string(probe), "1280x720") {
			t.Fatalf("video isn't 1280x720:\n%s", probe)
		}
	}
}
