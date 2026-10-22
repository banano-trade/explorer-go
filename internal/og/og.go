// Package og draws the 1200×630 share images that chat apps and social sites
// show when someone pastes an explorer link.
package og

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

//go:embed fonts/outfit-500.ttf
var outfit500 []byte

//go:embed fonts/outfit-700.ttf
var outfit700 []byte

//go:embed fonts/overpassmono-500.ttf
var mono500 []byte

// Card is what goes on a share image.
type Card struct {
	Kicker string   // small line above the title, e.g. "Send block"
	Title  string   // big white line
	Mono   string   // address or hash, in the monospace font
	Big    string   // yellow amount line
	Facts  []string // bottom row
}

const W, H = 1200, 630

var (
	fontsOnce sync.Once
	fonts     struct{ medium, bold, mono *opentype.Font }
	fontsErr  error

	bg     = color.RGBA{0x2A, 0x2A, 0x2E, 0xFF}
	white  = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	yellow = color.RGBA{0xFB, 0xDD, 0x11, 0xFF}
	grey   = color.RGBA{0xC9, 0xCA, 0xD0, 0xFF}
	soft   = color.RGBA{0xE4, 0xE4, 0xE7, 0xFF}
	deco   = color.RGBA{0x33, 0x33, 0x38, 0xFF}
)

func load() error {
	fontsOnce.Do(func() {
		if fonts.medium, fontsErr = opentype.Parse(outfit500); fontsErr != nil {
			return
		}
		if fonts.bold, fontsErr = opentype.Parse(outfit700); fontsErr != nil {
			return
		}
		fonts.mono, fontsErr = opentype.Parse(mono500)
	})
	return fontsErr
}

func face(f *opentype.Font, size float64) font.Face {
	fc, _ := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	return fc
}

// PNG renders c.
func PNG(c Card) ([]byte, error) {
	if err := load(); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	mark(img, 900, -60, 2.2, deco) // big faint banana in the corner

	// Brand line.
	mark(img, 72, 62, 0.19, yellow)
	x := text(img, face(fonts.bold, 30), yellow, 128, 92, "BANANO", 0)
	text(img, face(fonts.medium, 30), white, x+10, 92, "Explorer", 0)

	y := 290
	if c.Kicker != "" {
		text(img, face(fonts.medium, 30), yellow, 72, y-70, c.Kicker, W-144)
	}
	text(img, face(fonts.bold, 72), white, 72, y, c.Title, W-144)
	if c.Mono != "" {
		y += 56
		text(img, face(fonts.mono, 26), grey, 72, y, c.Mono, W-144)
		y += 70
	} else {
		y += 84
	}
	if c.Big != "" {
		text(img, face(fonts.bold, 48), yellow, 72, y, c.Big, W-144)
	}
	fx := 72
	for _, f := range c.Facts {
		fx = text(img, face(fonts.medium, 26), soft, fx, H-64, f, W-72-fx) + 40
		if fx > W-150 {
			break
		}
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// text draws s with its baseline at (x, y), shortened with "…" to fit maxW
// (0 = no limit), and returns the x where it ended.
func text(img draw.Image, f font.Face, c color.Color, x, y int, s string, maxW int) int {
	if maxW > 0 {
		for font.MeasureString(f, s).Ceil() > maxW && len(s) > 1 {
			r := []rune(strings.TrimSuffix(s, "…"))
			s = string(r[:len(r)-1]) + "…"
		}
	}
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Ceil()
}

// banana is the Banano mark (its 256×135 SVG path): two shapes, each a start
// point followed by cubic curves (three points each).
var banana = [][]float64{
	{184.1, 133.9, 103.5, 144, 66.3, 72.9, 73, 65.9, 77.4, 61.3, 84.1, 61, 90, 60.9, 117, 60.1, 155.1, 91.7, 162.9, 91.5,
		177.9, 91.2, 200.4, 67.3, 215.2, 47.6, 221.4, 39.4, 260.1, 27, 255.8, 48.6, 241.8, 118.8, 194.5, 132.6, 184.1, 133.9},
	{72, 0.9, 152.5, -9, 189.7, 62, 183, 69, 178.6, 73.7, 171.9, 74, 166, 74, 139, 74.8, 100.9, 43.2, 93.1, 43.4,
		78.1, 43.7, 55.6, 67.6, 40.8, 87.3, 34.6, 95.5, -4.1, 108, 0.2, 86.3, 14.3, 16, 61.6, 2.2, 72, 1},
}

// mark draws the banana at (x, y) scaled by k.
func mark(img *image.RGBA, x, y, k float64, c color.Color) {
	r := vector.NewRasterizer(W, H)
	pt := func(v []float64, i int) (float32, float32) { return float32(x + v[i]*k), float32(y + v[i+1]*k) }
	for _, sh := range banana {
		r.MoveTo(pt(sh, 0))
		for i := 2; i+5 < len(sh); i += 6 {
			ax, ay := pt(sh, i)
			bx, by := pt(sh, i+2)
			cx, cy := pt(sh, i+4)
			r.CubeTo(ax, ay, bx, by, cx, cy)
		}
		r.ClosePath()
	}
	r.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{})
}
