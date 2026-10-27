package stream

import (
	"encoding/binary"
	"errors"
)

// Just enough fragmented MP4 for Media Source Extensions: one track per
// stream, an init segment (ftyp+moov) and one moof+mdat per fragment.

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := make([]byte, 8, n)
	binary.BigEndian.PutUint32(b, uint32(n))
	copy(b[4:], typ)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func fullBox(typ string, version byte, flags uint32, parts ...[]byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append([][]byte{head}, parts...)...)
}

func u16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
func zeros(n int) []byte  { return make([]byte, n) }

var identity = func() []byte {
	var b []byte
	for _, v := range []uint32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000} {
		b = append(b, u32(v)...)
	}
	return b
}()

// initSegment describes one track: video ("vide", 90 kHz) or audio ("soun", 48 kHz).
func initSegment(handler string, timescale uint32, width, height uint16, entry []byte) []byte {
	ftyp := box("ftyp", []byte("isom"), u32(0x200), []byte("isomiso6avc1mp41"))
	mvhd := fullBox("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(0), u32(0x10000), u16(0x100), zeros(10), identity, zeros(24), u32(2))
	volume := uint16(0)
	if handler == "soun" {
		volume = 0x100
	}
	tkhd := fullBox("tkhd", 0, 7, u32(0), u32(0), u32(1), u32(0), u32(0), zeros(8), u16(0), u16(0), u16(volume), u16(0), identity,
		u32(uint32(width)<<16), u32(uint32(height)<<16))
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(timescale), u32(0), u16(0x55c4), u16(0)) // "und"
	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte(handler), zeros(12), []byte("Movie night\x00"))
	media := fullBox("smhd", 0, 0, u16(0), u16(0))
	if handler == "vide" {
		media = fullBox("vmhd", 0, 1, zeros(8))
	}
	dinf := box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1)))
	stbl := box("stbl",
		fullBox("stsd", 0, 0, u32(1), entry),
		fullBox("stts", 0, 0, u32(0)),
		fullBox("stsc", 0, 0, u32(0)),
		fullBox("stsz", 0, 0, u32(0), u32(0)),
		fullBox("stco", 0, 0, u32(0)))
	trak := box("trak", tkhd, box("mdia", mdhd, hdlr, box("minf", media, dinf, stbl)))
	mvex := box("mvex", fullBox("trex", 0, 0, u32(1), u32(1), u32(0), u32(0), u32(0)))
	return append(ftyp, box("moov", mvhd, trak, mvex)...)
}

// avc1Entry is the H.264 sample entry, built from the stream's SPS and PPS.
func avc1Entry(sps, pps []byte, width, height uint16) []byte {
	avcC := box("avcC", []byte{1, sps[1], sps[2], sps[3], 0xff, 0xe1}, u16(uint16(len(sps))), sps,
		[]byte{1}, u16(uint16(len(pps))), pps)
	return box("avc1", zeros(6), u16(1), zeros(16), u16(width), u16(height),
		u32(0x480000), u32(0x480000), u32(0), u16(1), zeros(32), u16(0x18), u16(0xffff), avcC)
}

// opusEntry is the Opus sample entry (stereo, 48 kHz).
func opusEntry() []byte {
	dOps := box("dOps", []byte{0, 2}, u16(312), u32(48000), u16(0), []byte{0})
	return box("Opus", zeros(6), u16(1), zeros(8), u16(2), u16(16), zeros(4), u32(48000<<16), dOps)
}

type sample struct {
	duration uint32
	data     []byte
	key      bool
}

// fragment is one moof+mdat with samples starting at decode time t.
func fragment(seq uint32, t uint64, samples []sample) []byte {
	moof := func(dataOffset uint32) []byte {
		trun := []byte{}
		trun = append(trun, u32(uint32(len(samples)))...)
		trun = append(trun, u32(dataOffset)...)
		for _, s := range samples {
			flags := uint32(0x01010000) // depends on others, not a sync sample
			if s.key {
				flags = 0x02000000
			}
			trun = append(trun, u32(s.duration)...)
			trun = append(trun, u32(uint32(len(s.data)))...)
			trun = append(trun, u32(flags)...)
		}
		return box("moof",
			fullBox("mfhd", 0, 0, u32(seq)),
			box("traf",
				fullBox("tfhd", 0, 0x020000, u32(1)), // default-base-is-moof
				fullBox("tfdt", 1, 0, u64(t)),
				fullBox("trun", 0, 0x000701, trun))) // data offset, durations, sizes, flags
	}
	head := moof(0)
	head = moof(uint32(len(head) + 8))
	var payload [][]byte
	for _, s := range samples {
		payload = append(payload, s.data)
	}
	return append(head, box("mdat", payload...)...)
}

// spsSize reads the picture size from an H.264 sequence parameter set.
func spsSize(sps []byte) (width, height int, err error) {
	// Drop emulation prevention bytes (00 00 03 → 00 00).
	var rbsp []byte
	zeros := 0
	for _, b := range sps[1:] {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		rbsp = append(rbsp, b)
	}
	r := &bits{b: rbsp}
	profile := r.u(8)
	r.u(16) // constraint flags, level
	r.ue()  // sps id
	chroma := 1
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma = r.ue()
		if chroma == 3 {
			r.u(1)
		}
		r.ue()
		r.ue()
		r.u(1)
		if r.u(1) == 1 { // scaling matrices
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				if r.u(1) == 0 {
					continue
				}
				size := 16
				if i >= 6 {
					size = 64
				}
				last, next := 8, 8
				for j := 0; j < size; j++ {
					if next != 0 {
						next = (last + r.se() + 256) % 256
					}
					if next != 0 {
						last = next
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num
	switch r.ue() {
	case 0:
		r.ue()
	case 1:
		r.u(1)
		r.se()
		r.se()
		for n := r.ue(); n > 0 && r.err == nil; n-- {
			r.se()
		}
	}
	r.ue()
	r.u(1)
	w := r.ue() + 1
	h := r.ue() + 1
	frameMbsOnly := r.u(1)
	if frameMbsOnly == 0 {
		r.u(1)
	}
	r.u(1)
	width, height = w*16, (2-frameMbsOnly)*h*16
	if r.u(1) == 1 { // cropping
		cx, cy := 1, 2-frameMbsOnly
		if chroma == 1 {
			cx, cy = 2, 2*(2-frameMbsOnly)
		} else if chroma == 2 {
			cx = 2
		}
		l, rt, t, b := r.ue(), r.ue(), r.ue(), r.ue()
		width -= (l + rt) * cx
		height -= (t + b) * cy
	}
	if r.err != nil || width <= 0 || height <= 0 || width > 16384 || height > 16384 {
		return 0, 0, errors.New("unreadable SPS")
	}
	return width, height, nil
}

type bits struct {
	b   []byte
	pos int
	err error
}

func (r *bits) u(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b)*8 {
			r.err = errors.New("short")
			return 0
		}
		v = v<<1 | int(r.b[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return v
}

func (r *bits) ue() int {
	n := 0
	for r.u(1) == 0 && r.err == nil && n < 32 {
		n++
	}
	return 1<<n - 1 + r.u(n)
}

func (r *bits) se() int {
	v := r.ue()
	if v%2 == 1 {
		return (v + 1) / 2
	}
	return -v / 2
}
