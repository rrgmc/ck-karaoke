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
	"fmt"
	"image"
	"image/color"
	"image/png"
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
	path  string
	size  int
	scale float64 // artwork scale about the centre
}

var targets = []target{
	{"ports/ios/CKKaraoke/Assets.xcassets/AppIcon.appiconset/icon-1024.png", 1024, 1.2},
	{"ports/desktop/build/appicon.png", 1024, 1.2},
	{"webui/static/icon-180.png", 180, 1.2},
	{"webui/static/icon-192.png", 192, 1.2},
	{"webui/static/icon-512.png", 512, 1.2},
}

func main() {
	for _, t := range targets {
		if err := write(t); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%dx%d)\n", t.path, t.size, t.size)
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
	return png.Encode(f, render(t.size, t.scale))
}

// samples per axis, for anti-aliasing.
const samples = 4

func render(size int, scale float64) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	unit := float64(canvas) / float64(size)
	const c = canvas / 2.0

	for py := range size {
		for px := range size {
			var covered, t float64
			for sy := range samples {
				for sx := range samples {
					x := (float64(px) + (float64(sx)+0.5)/samples) * unit
					y := (float64(py) + (float64(sy)+0.5)/samples) * unit
					t += (x + y) / (2 * canvas)
					// Map the sample back into artwork space.
					if foreground(c+(x-c)/scale, c+(y-c)/scale) {
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
