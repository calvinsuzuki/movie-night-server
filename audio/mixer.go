package audio

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"time"
)

// Mixer output format: what ffmpeg is told to expect on stdin.
const (
	MixRate     = 48000
	MixChannels = 2
	frameBytes  = 2 * MixChannels // s16le

	cushion  = 40 * time.Millisecond  // buffered audio kept to absorb capture jitter
	maxQueue = 200 * time.Millisecond // beyond this, drop old audio to cap latency
)

func bytesFor(d time.Duration) int { return int(d.Seconds()*MixRate) * frameBytes }

// Source is one captured stream (one app process).
type Source struct {
	mu      sync.Mutex
	buf     []byte
	starved bool
}

func (s *Source) Push(pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, pcm...)
	// Device clock running faster than ours: drop old audio back to the cushion.
	if len(s.buf) > bytesFor(maxQueue) {
		s.buf = append([]byte(nil), s.buf[len(s.buf)-bytesFor(cushion):]...)
	}
}

// take returns up to n bytes. After an underrun it returns nothing until a
// cushion has built up again, so capture jitter doesn't cause constant gaps.
func (s *Source) take(n int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starved && len(s.buf) < bytesFor(cushion) {
		return nil
	}
	s.starved = false
	if n > len(s.buf) {
		n = len(s.buf)
		s.starved = true
	}
	out := s.buf[:n:n]
	s.buf = s.buf[n:]
	return out
}

type Mixer struct {
	mu      sync.Mutex
	sources map[string]*Source
}

func NewMixer() *Mixer { return &Mixer{sources: map[string]*Source{}} }

func (m *Mixer) Add(key string) *Source {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Source{starved: true}
	m.sources[key] = s
	return s
}

func (m *Mixer) Remove(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sources, key)
}

// Pump writes the mix to w at exactly real-time rate. Sources deliver nothing
// while their app is silent, so gaps become silence; that keeps ffmpeg's
// sample-count timestamps aligned with the wall clock.
func (m *Mixer) Pump(ctx context.Context, w io.Writer) error {
	m.mu.Lock()
	for _, s := range m.sources { // a fresh ffmpeg starts from "now"
		s.mu.Lock()
		s.buf, s.starved = s.buf[:0], true
		s.mu.Unlock()
	}
	m.mu.Unlock()

	start := time.Now()
	var written int64 // frames
	var acc []int32
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		need := int(int64(time.Since(start).Seconds()*MixRate) - written)
		if need <= 0 {
			continue
		}
		samples := need * MixChannels
		if cap(acc) < samples {
			acc = make([]int32, samples)
		}
		acc = acc[:samples]
		clear(acc)

		m.mu.Lock()
		for _, s := range m.sources {
			pcm := s.take(need * frameBytes)
			for i := 0; i+1 < len(pcm); i += 2 {
				acc[i/2] += int32(int16(binary.LittleEndian.Uint16(pcm[i:])))
			}
		}
		m.mu.Unlock()

		out := make([]byte, samples*2)
		for i, v := range acc {
			v = max(-32768, min(32767, v))
			binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v)))
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		written += int64(need)
	}
}
