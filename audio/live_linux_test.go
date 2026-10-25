//go:build linux

package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLive plays a quiet tone on the speakers and records it the way a
// share does, checking it arrives whole: MOVIENIGHT_LIVE=1 go test ./audio -run Live -v
func TestLive(t *testing.T) {
	if os.Getenv("MOVIENIGHT_LIVE") == "" {
		t.Skip("set MOVIENIGHT_LIVE=1 to play a tone")
	}
	wav := filepath.Join(t.TempDir(), "tone.wav")
	if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "sine=f=440:d=4",
		"-ac", "2", wav).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}

	m, err := NewManager(Policy{Mode: ModeAll, Blocklist: DefaultBlocklist})
	if err != nil {
		t.Fatal(err)
	}
	m.SetActive(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	var pcm bytes.Buffer
	pumpCtx, stop := context.WithTimeout(ctx, 4*time.Second)
	defer stop()
	go m.mixer.Pump(pumpCtx, &pcm)
	time.Sleep(200 * time.Millisecond)
	play := exec.Command("paplay", "--volume=20000", "--client-name=movienight-live-test", wav)
	if err := play.Start(); err != nil {
		t.Fatal(err)
	}
	defer play.Process.Kill()
	<-pumpCtx.Done()
	time.Sleep(50 * time.Millisecond)

	// Every 10 ms block from the first sound on should carry the tone.
	b := pcm.Bytes()
	block := bytesFor(10 * time.Millisecond)
	first, blocks, silent := -1, 0, 0
	for off := 0; off+block <= len(b); off += block {
		peak := 0
		for i := off; i+1 < off+block; i += 2 {
			peak = max(peak, abs(int(int16(binary.LittleEndian.Uint16(b[i:])))))
		}
		if first < 0 && peak > 0 {
			first = off / block
		}
		if first >= 0 {
			blocks++
			if peak == 0 {
				silent++
			}
		}
	}
	t.Logf("recorded %.2fs; tone from %d ms, %d of %d blocks silent",
		float64(len(b))/float64(bytesFor(time.Second)), first*10, silent, blocks)
	if first < 0 {
		t.Fatal("the tone never arrived")
	}
	if silent > 0 {
		t.Errorf("%d gaps in the tone", silent)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
