// Command genicon renders the CK Karaoke icon to every raster the ports need.
//
//	go run ./internal/cmd/genicon
//
// Run it from the repository root. The geometry here is the same as
// assets/icon/icon.svg and the Android vector drawables in
// ports/android/app/src/main/res/drawable; change all three together.
//
// All shapes live on Android's 108-unit adaptive-icon canvas, where only the
// central 72 units are guaranteed to survive the launcher's mask. Other
// platforms crop far less, so their rasters scale the artwork up about its
// centre.
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
)

const canvas = 108

// Gradient endpoints, top-left to bottom-right.
var (
	gradientFrom = color.NRGBA{0x37, 0x30, 0xA3, 0xFF} // indigo
	gradientTo   = color.NRGBA{0x0D, 0x94, 0x88, 0xFF} // teal
)

// stroke is the width of every line in the artwork.
const stroke = 4

// The microphone.
const (
	micX       = 54.0
	headTop    = 40.0 // centre of the head's upper cap
	headBottom = 48.0 // centre of the head's lower cap
	headRadius = 10.0
	cradleY    = 48.0
	cradleR    = 16.0
	stemTop    = 64.0
	stemBottom = 76.0
	baseHalf   = 10.0
)

// The sound waves: arcs about (micX, waveY) on both sides.
const (
	waveY     = 44.0
	waveSpan  = 40 * math.Pi / 180 // half-angle of each arc
	waveInner = 26.0
	waveOuter = 33.0
)

type target struct {
	path          string
	width, height int
	scale         float64 // artwork scale about the centre
	rgba          bool    // write 32-bit RGBA instead of 24-bit RGB
}

var targets = []target{
	{"ports/ios/CKKaraoke/Assets.xcassets/AppIcon.appiconset/icon-1024.png", 1024, 1024, 1.2, false},
	{"ports/desktop/build/appicon.png", 1024, 1024, 1.2, false},
	{"webui/static/icon-180.png", 180, 180, 1.2, false},
	{"webui/static/icon-192.png", 192, 192, 1.2, false},
	{"webui/static/icon-512.png", 512, 512, 1.2, false},
	// Google Play asks for 32-bit PNGs: the app icon and the feature graphic.
	{"ports/android/play/icon-512.png", 512, 512, 1.2, true},
	{"ports/android/play/feature-graphic.png", 1024, 500, 1.1, true},
}

func main() {
	for _, t := range targets {
		if err := write(t); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%dx%d)\n", t.path, t.width, t.height)
	}
}

func write(t target) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(t.path)
	if err != nil {
		return err
	}
	defer f.Close()
	// Fully opaque: an iOS app icon may not have transparency.
	img := render(t.width, t.height, t.scale)
	if t.rgba {
		return encodeRGBA(f, img)
	}
	return png.Encode(f, img)
}

// samples per axis, for anti-aliasing.
const samples = 4

// render draws the gradient over the whole image and the artwork at its centre,
// sized by the image's height.
func render(width, height int, scale float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	unit := float64(canvas) / float64(height)
	aspect := float64(width) / float64(height)
	const c = canvas / 2.0
	cx := c * aspect

	for py := range height {
		for px := range width {
			var covered, t float64
			for sy := range samples {
				for sx := range samples {
					x := (float64(px) + (float64(sx)+0.5)/samples) * unit
					y := (float64(py) + (float64(sy)+0.5)/samples) * unit
					t += (x/aspect + y) / (2 * canvas)
					// Map the sample back into artwork space.
					if foreground(c+(x-cx)/scale, c+(y-c)/scale) {
						covered++
					}
				}
			}
			const n = samples * samples
			a, t := covered/n, t/n
			img.SetNRGBA(px, py, color.NRGBA{
				R: blend(lerp(gradientFrom.R, gradientTo.R, t), a),
				G: blend(lerp(gradientFrom.G, gradientTo.G, t), a),
				B: blend(lerp(gradientFrom.B, gradientTo.B, t), a),
				A: 0xFF,
			})
		}
	}
	return img
}

// encodeRGBA writes img as an 8-bit RGBA PNG. image/png writes a fully opaque
// image as RGB, with no alpha channel.
func encodeRGBA(w io.Writer, img *image.NRGBA) error {
	b := img.Bounds()
	var raw bytes.Buffer
	for y := b.Min.Y; y < b.Max.Y; y++ {
		raw.WriteByte(0) // filter type: none
		i := img.PixOffset(b.Min.X, y)
		raw.Write(img.Pix[i : i+4*b.Dx()])
	}
	var idat bytes.Buffer
	zw := zlib.NewWriter(&idat)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	// Width, height, bit depth 8 and colour type 6 (RGBA); compression,
	// filter and interlace methods stay 0.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(b.Dx()))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(b.Dy()))
	ihdr[8], ihdr[9] = 8, 6

	if _, err := io.WriteString(w, "\x89PNG\r\n\x1a\n"); err != nil {
		return err
	}
	for _, c := range []struct {
		name string
		data []byte
	}{{"IHDR", ihdr}, {"IDAT", idat.Bytes()}, {"IEND", nil}} {
		if err := writeChunk(w, c.name, c.data); err != nil {
			return err
		}
	}
	return nil
}

// writeChunk writes one PNG chunk: length, name, data and CRC.
func writeChunk(w io.Writer, name string, data []byte) error {
	var head [8]byte
	binary.BigEndian.PutUint32(head[:4], uint32(len(data)))
	copy(head[4:], name)
	crc := crc32.NewIEEE()
	crc.Write(head[4:])
	crc.Write(data)
	var tail [4]byte
	binary.BigEndian.PutUint32(tail[:], crc.Sum32())
	for _, p := range [][]byte{head[:], data, tail[:]} {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

func lerp(a, b uint8, t float64) float64 {
	return float64(a) + (float64(b)-float64(a))*t
}

// blend mixes a background channel towards white by coverage.
func blend(under, a float64) uint8 {
	v := under*(1-a) + 255*a
	return uint8(math.Round(math.Max(0, math.Min(255, v))))
}

// foreground reports whether a point in artwork space is white.
func foreground(x, y float64) bool {
	const half = stroke / 2.0
	switch {
	// Head: a capsule.
	case segment(x, y, micX, headTop, micX, headBottom) <= headRadius:
		return true
	// Cradle: the lower half of a circle.
	case y >= cradleY && math.Abs(math.Hypot(x-micX, y-cradleY)-cradleR) <= half:
		return true
	// Ends of the cradle, rounded.
	case math.Hypot(x-(micX-cradleR), y-cradleY) <= half,
		math.Hypot(x-(micX+cradleR), y-cradleY) <= half:
		return true
	// Stem and base.
	case segment(x, y, micX, stemTop, micX, stemBottom) <= half:
		return true
	case segment(x, y, micX-baseHalf, stemBottom, micX+baseHalf, stemBottom) <= half:
		return true
	}
	for _, r := range []float64{waveInner, waveOuter} {
		if arc(x, y, r, half) {
			return true
		}
	}
	return false
}

// arc reports whether a point lies on either wave arc of radius r, caps
// included.
func arc(x, y, r, half float64) bool {
	dx, dy := x-micX, y-waveY
	angle := math.Atan2(dy, math.Abs(dx)) // mirrored: both sides at once
	if math.Abs(angle) <= waveSpan && math.Abs(math.Hypot(dx, dy)-r) <= half {
		return true
	}
	for _, s := range []float64{-1, 1} {
		ex := r * math.Cos(waveSpan)
		ey := s * r * math.Sin(waveSpan)
		if math.Hypot(math.Abs(dx)-ex, dy-ey) <= half {
			return true
		}
	}
	return false
}

// segment is the distance from (x, y) to the segment (ax, ay)-(bx, by).
func segment(x, y, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	t := ((x-ax)*vx + (y-ay)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(ax+t*vx), y-(ay+t*vy))
}
