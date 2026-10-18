// genicon draws the Movie night popcorn icon and writes the PNG/ICO assets.
//
//	go run ./tools/genicon
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

const (
	S  = 1024 // master canvas; downscaled with a box filter for anti-aliasing
	SS = 4    // supersampling factor per output pixel at 256px
)

type rgba struct{ r, g, b, a float64 }

var (
	red       = rgba{0.86, 0.16, 0.18, 1}
	redDark   = rgba{0.62, 0.08, 0.12, 1}
	white     = rgba{0.99, 0.97, 0.94, 1}
	whiteDark = rgba{0.86, 0.82, 0.78, 1}
	outline   = rgba{0.28, 0.07, 0.09, 1}
	kernel    = rgba{1.00, 0.96, 0.84, 1}
	kernelSh  = rgba{0.95, 0.80, 0.50, 1}
	butter    = rgba{1.00, 0.80, 0.25, 1}
)

type canvas struct {
	w, h int
	px   []rgba
}

func newCanvas(w, h int) *canvas { return &canvas{w, h, make([]rgba, w*h)} }

func (c *canvas) blend(x, y int, col rgba) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	p := &c.px[y*c.w+x]
	a := col.a
	p.r = col.r*a + p.r*(1-a)
	p.g = col.g*a + p.g*(1-a)
	p.b = col.b*a + p.b*(1-a)
	p.a = a + p.a*(1-a)
}

func (c *canvas) circle(cx, cy, r float64, col rgba) {
	for y := int(cy - r); y <= int(cy+r)+1; y++ {
		for x := int(cx - r); x <= int(cx+r)+1; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				c.blend(x, y, col)
			}
		}
	}
}

// bucket coordinates (master canvas units)
const (
	rimTop, rimBot = 430.0, 500.0
	bodyBot        = 960.0
	topHalf        = 370.0 // half-width at the rim
	botHalf        = 255.0 // half-width at the bottom
	cxMid          = 512.0
)

func halfWidthAt(y float64) float64 {
	t := (y - rimBot) / (bodyBot - rimBot)
	return topHalf - 20 + t*(botHalf-(topHalf-20))
}

func drawBucket(c *canvas) {
	const stripes = 7
	ow := 18.0 // outline width
	// outline + body
	for y := int(rimBot - 4); y <= int(bodyBot+ow); y++ {
		fy := float64(y) + 0.5
		hw := halfWidthAt(math.Min(fy, bodyBot))
		for x := int(cxMid - hw - ow); x <= int(cxMid+hw+ow); x++ {
			fx := float64(x) + 0.5
			d := fx - cxMid
			inside := math.Abs(d) <= hw && fy <= bodyBot
			if !inside {
				if math.Abs(d) <= hw+ow && fy <= bodyBot+ow {
					c.blend(x, y, outline)
				}
				continue
			}
			u := (d/hw + 1) / 2 // 0..1 across the bucket, follows the slant
			idx := int(u * stripes)
			col := white
			if idx%2 == 0 {
				col = red
			}
			// soft cylindrical shading
			shade := 0.78 + 0.22*math.Sin(u*math.Pi)
			if idx%2 == 0 && u > 0.78 {
				col = redDark
			}
			if idx%2 == 1 && u > 0.78 {
				col = whiteDark
			}
			c.blend(x, y, rgba{col.r * shade, col.g * shade, col.b * shade, 1})
		}
	}
	// rim band
	for y := int(rimTop - ow); y <= int(rimBot+ow/2); y++ {
		fy := float64(y) + 0.5
		for x := int(cxMid - topHalf - ow); x <= int(cxMid+topHalf+ow); x++ {
			fx := float64(x) + 0.5
			d := math.Abs(fx - cxMid)
			r := 34.0
			// rounded rectangle test
			ex := math.Max(d-(topHalf-r), 0)
			ey := math.Max(math.Max(rimTop+r-fy, fy-(rimBot-r)), 0)
			dist := math.Hypot(ex, ey)
			switch {
			case dist <= r:
				t := (fy - rimTop) / (rimBot - rimTop)
				col := red
				if t > 0.6 {
					col = redDark
				}
				c.blend(x, y, col)
			case dist <= r+ow:
				c.blend(x, y, outline)
			}
		}
	}
}

type puff struct{ x, y, r float64 }

func drawPopcorn(c *canvas) {
	// Each kernel is a cluster of overlapping circles; drawn back-to-front.
	kernels := []puff{
		{230, 380, 95}, {794, 380, 95},
		{330, 300, 105}, {694, 300, 105},
		{512, 250, 120},
		{400, 175, 95}, {624, 175, 95},
		{512, 110, 85},
		{180, 420, 70}, {844, 420, 70},
		{430, 390, 90}, {594, 390, 90},
	}
	for _, k := range kernels {
		cluster := []puff{
			{k.x - k.r*0.45, k.y + k.r*0.10, k.r * 0.62},
			{k.x + k.r*0.45, k.y + k.r*0.12, k.r * 0.60},
			{k.x, k.y - k.r*0.35, k.r * 0.64},
			{k.x, k.y + k.r*0.15, k.r * 0.66},
		}
		for _, p := range cluster { // outline pass
			c.circle(p.x, p.y, p.r+18, outline)
		}
		for _, p := range cluster { // shadow side
			c.circle(p.x, p.y, p.r, kernelSh)
		}
		for _, p := range cluster { // lit side
			c.circle(p.x-p.r*0.12, p.y-p.r*0.14, p.r*0.84, kernel)
		}
		c.circle(k.x+k.r*0.18, k.y+k.r*0.05, k.r*0.16, butter)
	}
}

// downscale averages premultiplied colour over box-filter blocks.
func downscale(c *canvas, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	f := float64(c.w) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a, n float64
			for sy := int(float64(y) * f); sy < int(float64(y+1)*f); sy++ {
				for sx := int(float64(x) * f); sx < int(float64(x+1)*f); sx++ {
					p := c.px[sy*c.w+sx]
					r += p.r * p.a
					g += p.g * p.a
					b += p.b * p.a
					a += p.a
					n++
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				uint8(math.Round(r / a * 255)), uint8(math.Round(g / a * 255)),
				uint8(math.Round(b / a * 255)), uint8(math.Round(a / n * 255)),
			})
		}
	}
	return img
}

func grayscale(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		l := 0.3*float64(src.Pix[i]) + 0.59*float64(src.Pix[i+1]) + 0.11*float64(src.Pix[i+2])
		v := uint8(l*0.8 + 30)
		dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = v, v, v, src.Pix[i+3]
	}
	return dst
}

func pngBytes(img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		log.Fatal(err)
	}
	return b.Bytes()
}

// writeICO stores small sizes as 32-bit DIBs (most compatible, incl. tray
// icons) and 256px as PNG.
func writeICO(path string, imgs []*image.NRGBA) {
	var entries, data bytes.Buffer
	offset := 6 + 16*len(imgs)
	for _, img := range imgs {
		size := img.Rect.Dx()
		var blob []byte
		if size >= 256 {
			blob = pngBytes(img)
		} else {
			blob = dib(img)
		}
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		entries.Write([]byte{dim, dim, 0, 0})
		binary.Write(&entries, binary.LittleEndian, uint16(1))  // planes
		binary.Write(&entries, binary.LittleEndian, uint16(32)) // bpp
		binary.Write(&entries, binary.LittleEndian, uint32(len(blob)))
		binary.Write(&entries, binary.LittleEndian, uint32(offset))
		offset += len(blob)
		data.Write(blob)
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, uint16(len(imgs))})
	out.Write(entries.Bytes())
	out.Write(data.Bytes())
	must(os.WriteFile(path, out.Bytes(), 0o644))
}

func dib(img *image.NRGBA) []byte {
	n := img.Rect.Dx()
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint32{40, uint32(n), uint32(n * 2)})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&b, binary.LittleEndian, []uint32{0, uint32(n * n * 4), 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- { // bottom-up BGRA
		for x := 0; x < n; x++ {
			i := img.PixOffset(x, y)
			b.Write([]byte{img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]})
		}
	}
	maskRow := ((n + 31) / 32) * 4 // AND mask, unused with alpha but required
	b.Write(make([]byte, maskRow*n))
	return b.Bytes()
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	c := newCanvas(S, S)
	drawPopcorn(c)
	drawBucket(c)

	out := "assets"
	must(os.MkdirAll(out, 0o755))
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	var color, gray []*image.NRGBA
	for _, s := range sizes {
		img := downscale(c, s)
		color = append(color, img)
		gray = append(gray, grayscale(img))
	}
	for i, s := range sizes {
		if s == 48 || s == 128 || s == 256 {
			must(os.WriteFile(filepath.Join(out, "icon-"+itoa(s)+".png"), pngBytes(color[i]), 0o644))
		}
		if s == 64 {
			must(os.WriteFile(filepath.Join(out, "tray.png"), pngBytes(color[i]), 0o644))
			must(os.WriteFile(filepath.Join(out, "tray-paused.png"), pngBytes(gray[i]), 0o644))
		}
	}
	writeICO(filepath.Join(out, "icon.ico"), color)
	// Tray icons only need the small sizes.
	writeICO(filepath.Join(out, "tray.ico"), color[:6])
	writeICO(filepath.Join(out, "tray-paused.ico"), gray[:6])
	log.Printf("wrote icons to %s/", out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
