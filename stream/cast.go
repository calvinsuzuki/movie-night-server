package stream

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// cast repackages the RTP going to WebRTC viewers into fragmented MP4 for
// friends with no direct path to us (strict NAT on both ends). Their page
// plays it with Media Source Extensions, fed through the link's tunnel over a
// WebSocket. Nothing is re-encoded.
//
// Messages start with one byte: 'V'/'A' an init segment (then the codec
// string's length and the codec string), 'v'/'a' a fragment, 'k' keepalive.
type cast struct {
	start time.Time
	mu    sync.Mutex
	subs  map[*castViewer]struct{}

	depack   codecs.H264Packet
	frame    []byte // length-prefixed NAL units of the frame being received
	frameTS  uint32
	inFrame  bool
	sps, pps []byte
	vinit    []byte
	vclock   clock
	vlast    int64
	vdur     uint32
	vseq     uint32
	gop      [][]byte // the fragments since the last keyframe, for newcomers

	ainit  []byte
	aclock clock
	aseq   uint32
	alast  time.Time
}

// castViewer is one friend watching through the link.
type castViewer struct {
	name    string
	since   time.Time
	out     chan []byte
	done    chan struct{}
	once    sync.Once
	audio   bool // the page set up sound (it was being shared when they joined)
	needKey bool // fell behind: skip to the next keyframe
}

func (v *castViewer) stop() { v.once.Do(func() { close(v.done) }) }

// clock turns 32-bit RTP timestamps into a decode time that starts at the
// wall-clock moment the stream was first seen, so separately encoded audio
// and video line up.
type clock struct {
	rate     int64
	init     bool
	last     uint32
	base, ex int64
}

func (c *clock) at(ts uint32, since time.Duration) int64 {
	if !c.init {
		c.init, c.last, c.base = true, ts, int64(since)*c.rate/int64(time.Second)
		return c.base
	}
	c.ex += int64(int32(ts - c.last))
	c.last = ts
	return max(0, c.base+c.ex)
}

func newCast() *cast {
	c := &cast{start: time.Now(), subs: map[*castViewer]struct{}{}, vdur: 90000 / 24}
	c.depack.IsAVC = true
	c.vclock.rate, c.aclock.rate = 90000, 48000
	c.ainit = initMessage('A', "opus", initSegment("soun", 48000, 0, 0, opusEntry()))
	return c
}

func initMessage(kind byte, codec string, seg []byte) []byte {
	return append(append([]byte{kind, byte(len(codec))}, codec...), seg...)
}

func (c *cast) write(kind string, p *rtp.Packet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kind == "audio" {
		c.writeAudio(p)
	} else {
		c.writeVideo(p)
	}
}

func (c *cast) writeVideo(p *rtp.Packet) {
	if c.inFrame && p.Timestamp != c.frameTS {
		c.flushVideo() // the last packet of a frame went missing
	}
	if nal, err := c.depack.Unmarshal(p.Payload); err == nil {
		c.frame = append(c.frame, nal...)
	}
	c.frameTS, c.inFrame = p.Timestamp, true
	if p.Marker {
		c.flushVideo()
	}
}

func (c *cast) flushVideo() {
	frame := c.frame
	c.frame, c.inFrame = nil, false
	var data []byte
	key := false
	sps, pps := c.sps, c.pps
	for len(frame) >= 4 {
		n := int(binary.BigEndian.Uint32(frame))
		if n == 0 || n > len(frame)-4 {
			break
		}
		nal := frame[4 : 4+n]
		frame = frame[4+n:]
		switch nal[0] & 0x1f {
		case 7:
			sps = nal
		case 8:
			pps = nal
		case 9: // access unit delimiter: not needed in MP4
		default:
			if nal[0]&0x1f == 5 {
				key = true
			}
			data = binary.BigEndian.AppendUint32(data, uint32(n))
			data = append(data, nal...)
		}
	}
	if sps != nil && pps != nil && (!bytes.Equal(sps, c.sps) || !bytes.Equal(pps, c.pps) || c.vinit == nil) {
		if w, h, err := spsSize(sps); err == nil {
			c.sps, c.pps = sps, pps
			codec := fmt.Sprintf("avc1.%02x%02x%02x", sps[1], sps[2], sps[3])
			c.vinit = initMessage('V', codec, initSegment("vide", 90000, uint16(w), uint16(h), avc1Entry(sps, pps, uint16(w), uint16(h))))
			c.gop = nil
			c.send(c.vinit, false)
		}
	}
	if c.vinit == nil || len(data) == 0 || (c.gop == nil && !key) {
		return
	}
	t := c.vclock.at(c.frameTS, time.Since(c.start))
	if d := t - c.vlast; c.vlast > 0 && d > 0 && d < 90000 {
		c.vdur = uint32(d)
	}
	c.vlast = t
	c.vseq++
	msg := append([]byte{'v'}, fragment(c.vseq, uint64(t), []sample{{c.vdur, data, key}})...)
	if key {
		c.gop = [][]byte{msg}
	} else if len(c.gop) < 500 {
		c.gop = append(c.gop, msg)
	}
	c.send(msg, key)
}

func (c *cast) writeAudio(p *rtp.Packet) {
	if len(p.Payload) == 0 {
		return
	}
	c.alast = time.Now()
	t := c.aclock.at(p.Timestamp, time.Since(c.start))
	c.aseq++
	data := append([]byte(nil), p.Payload...)
	msg := append([]byte{'a'}, fragment(c.aseq, uint64(t), []sample{{960, data, true}})...) // 20 ms frames
	c.send(msg, false)
}

// send queues msg for every viewer. One who can't keep up skips ahead to the
// next keyframe (getting the init segments again) instead of falling behind.
func (c *cast) send(msg []byte, key bool) {
	for v := range c.subs {
		if v.needKey {
			if !key || !v.restart(c) {
				continue
			}
		}
		if !v.offer(msg) {
			v.needKey = true
		}
	}
}

// restart queues the init segments before a keyframe.
func (v *castViewer) restart(c *cast) bool {
	if v.audio && !v.offer(c.ainit) {
		return false
	}
	if !v.offer(c.vinit) {
		return false
	}
	v.needKey = false
	return true
}

// offer queues msg without ever blocking the stream.
func (v *castViewer) offer(msg []byte) bool {
	select {
	case v.out <- msg:
		return true
	default:
		return false
	}
}

// audioLive reports whether sound is being shared right now.
func (c *cast) audioLive() bool { return time.Since(c.alast) < 3*time.Second }

// subscribe starts a viewer off with the init segments and the current
// group of pictures, so the picture shows up at once. The first message
// says whether sound is being shared, so the page knows what to expect.
func (c *cast) subscribe(v *castViewer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[v] = struct{}{}
	v.audio = c.audioLive()
	hello := []byte{'H', 0}
	if v.audio {
		hello[1] = 1
	}
	v.offer(hello)
	if c.vinit == nil || c.gop == nil || !v.restart(c) {
		v.needKey = true
		return
	}
	for _, m := range c.gop {
		v.offer(m)
	}
}

func (c *cast) unsubscribe(v *castViewer) {
	c.mu.Lock()
	delete(c.subs, v)
	c.mu.Unlock()
}
