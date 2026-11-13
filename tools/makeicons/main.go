// makeicons draws the app icon and writes it in the forms each platform uses:
//
//	winres/icon.ico                       the Windows icon, built into the .exe by go-winres
//	packaging/linux/kamkmdirstat.png      the Linux icon named by kamkmdirstat.desktop
//	packaging/macos/KamkmDirStat.icns     the macOS icon in the app bundle
//	assets/icon.png                       a large copy for the README
//
// The icon is a small treemap on a dark tile, shaded the way the app shades its map. Smaller
// sizes use fewer blocks and whole pixels so they stay sharp. Run it from the repository root
// after changing the design: go run ./tools/makeicons
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	var ico []icoImage
	for _, size := range []int{16, 20, 24, 32, 40, 48, 64, 128, 256} {
		ico = append(ico, icoImage{size, encode(render(size, false))})
	}
	write("winres/icon.ico", icoFile(ico))
	write("packaging/linux/kamkmdirstat.png", encode(render(256, false)))
	write("assets/icon.png", encode(render(512, false)))

	icns := []icnsImage{
		{kind: "icp4", size: 16}, {kind: "icp5", size: 32}, {kind: "ic11", size: 32}, {kind: "ic12", size: 64}, {kind: "ic07", size: 128},
		{kind: "ic13", size: 256}, {kind: "ic08", size: 256}, {kind: "ic14", size: 512}, {kind: "ic09", size: 512}, {kind: "ic10", size: 1024},
	}
	drawn := map[int][]byte{}
	for i, e := range icns {
		if drawn[e.size] == nil {
			drawn[e.size] = encode(render(e.size, true))
		}
		icns[i].data = drawn[e.size]
	}
	write("packaging/macos/KamkmDirStat.icns", icnsFile(icns))
}

func write(name string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(name, data, 0o644); err != nil {
		fail(err)
	}
	fmt.Println(name)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func encode(img image.Image) []byte {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, img); err != nil {
		fail(err)
	}
	return b.Bytes()
}

// ---- Design ----

type rgb struct{ r, g, b float64 }

func hex(v uint32) rgb {
	return rgb{float64(v >> 16 & 255), float64(v >> 8 & 255), float64(v & 255)}
}

var (
	tileTop    = hex(0x343841)
	tileBottom = hex(0x1a1d23)
	blue       = hex(0x4392f7)
	coral      = hex(0xf07554)
	green      = hex(0x58c97d)
	amber      = hex(0xf6c343)
	violet     = hex(0xa17adf)
)

type rect struct{ x0, y0, x1, y1 float64 }

// block is one rectangle of the treemap. ridges are the rectangles it sits in, itself last,
// each with the height of its cushion, as in the app's map.
type block struct {
	rect
	color  rgb
	ridges []ridge
}

type ridge struct {
	rect
	h float64
}

// node is a rectangle split in two, in fractions of its parent.
type node struct {
	color      rgb
	vertical   bool    // split left and right instead of top and bottom
	at         float64 // where the split falls, as a fraction
	first, sec *node
}

func leaf(c rgb) *node { return &node{color: c} }

// The designs, from the smallest icons to the largest. They share the large blue block on
// the left and the coral block at the top right, so they read as the same icon.
var (
	small = &node{vertical: true, at: 0.54, first: leaf(blue),
		sec: &node{at: 0.5, first: leaf(coral), sec: leaf(green)}}
	medium = &node{vertical: true, at: 0.54, first: leaf(blue),
		sec: &node{at: 0.56, first: leaf(coral),
			sec: &node{vertical: true, at: 0.56, first: leaf(green), sec: leaf(amber)}}}
	large = &node{vertical: true, at: 0.54, first: leaf(blue),
		sec: &node{at: 0.56, first: leaf(coral),
			sec: &node{vertical: true, at: 0.56, first: leaf(green),
				sec: &node{at: 0.5, first: leaf(amber), sec: leaf(violet)}}}}
)

// layout places the blocks of a design inside r, leaving gap pixels between them. snap
// rounds edges to whole pixels.
func layout(n *node, r rect, gap float64, snap bool, h float64, ridges []ridge, out *[]block) {
	own := append(append([]ridge{}, ridges...), ridge{r, h})
	if n.first == nil {
		*out = append(*out, block{r, n.color, own})
		return
	}
	round := func(v float64) float64 {
		if snap {
			return math.Round(v)
		}
		return v
	}
	a, b := r, r
	if n.vertical {
		split := round(r.x0 + (r.x1-r.x0-gap)*n.at)
		a.x1, b.x0 = split, split+gap
	} else {
		split := round(r.y0 + (r.y1-r.y0-gap)*n.at)
		a.y1, b.y0 = split, split+gap
	}
	layout(n.first, a, gap, snap, h*0.91, own, out)
	layout(n.sec, b, gap, snap, h*0.91, own, out)
}

type design struct {
	tile         rect
	radius       float64
	blocks       []block
	blockRadius  float64
	highlight    float64 // width of the light edge inside the tile, 0 for none
	shadow       bool
	shadowOffset float64
	shadowBlur   float64
}

func makeDesign(size int, mac bool) design {
	s := float64(size)
	var d design
	if mac {
		// macOS icons sit on an 824 by 824 tile in a 1024 by 1024 canvas, with a shadow.
		m := s * 100 / 1024
		d.tile = rect{m, m, s - m, s - m}
		d.shadow, d.shadowOffset, d.shadowBlur = true, s*12/1024, s*28/1024
	} else {
		m := math.Floor(s / 32)
		d.tile = rect{m, m, s - m, s - m}
	}
	t := d.tile.x1 - d.tile.x0
	d.radius = t * 0.225
	if size <= 24 {
		d.radius = t * 0.2
	}

	shape, snap := large, false
	switch {
	case size <= 24:
		shape, snap = small, true
	case size <= 48:
		shape, snap = medium, true
	case size < 64:
		shape = medium
	}
	inset := t * 0.15
	gap := t * 0.03
	if snap {
		inset = math.Round(t * 0.16)
		gap = 1
		if size >= 40 {
			gap = 2
		}
	}
	content := rect{d.tile.x0 + inset, d.tile.y0 + inset, d.tile.x1 - inset, d.tile.y1 - inset}
	if snap {
		content = rect{math.Round(content.x0), math.Round(content.y0), math.Round(content.x1), math.Round(content.y1)}
	}
	layout(shape, content, gap, snap, 0.38, nil, &d.blocks)
	// The whole map is not raised, as in the app, so drop its ridge.
	for i := range d.blocks {
		d.blocks[i].ridges = d.blocks[i].ridges[1:]
	}
	switch {
	case size <= 24:
		d.blockRadius = 0
	case snap:
		d.blockRadius = 1
	default:
		d.blockRadius = t * 0.018
	}
	if size >= 32 {
		d.highlight = math.Max(1, t/160)
	}
	return d
}

// ---- Drawing ----

func render(size int, mac bool) *image.NRGBA {
	d := makeDesign(size, mac)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			var c premul
			if d.shadow {
				dist := roundRect(px, py-d.shadowOffset, d.tile, d.radius)
				a := 0.32 * (1 - smoothstep(-d.shadowBlur, d.shadowBlur, dist))
				c = c.over(rgb{}, a)
			}
			tileCover := coverage(roundRect(px, py, d.tile, d.radius))
			if tileCover == 0 {
				img.SetNRGBA(x, y, c.nrgba())
				continue
			}
			f := (py - d.tile.y0) / (d.tile.y1 - d.tile.y0)
			fill := mix(tileTop, tileBottom, f)
			if d.highlight > 0 {
				dist := roundRect(px, py, d.tile, d.radius)
				band := coverage(math.Abs(dist+d.highlight) - d.highlight/2)
				fill = mix(fill, rgb{255, 255, 255}, band*0.14*(1-f*0.5))
			}
			for _, b := range d.blocks {
				cover := coverage(roundRect(px, py, b.rect, d.blockRadius))
				if cover > 0 {
					fill = mix(fill, shade(b, px, py), cover)
				}
			}
			c = c.over(fill, tileCover)
			img.SetNRGBA(x, y, c.nrgba())
		}
	}
	return img
}

// shade lights a block like a cushion, with the light from the top left, as the app's map
// does, but gentler, since an icon is seen small.
func shade(b block, x, y float64) rgb {
	var nx, ny float64
	for _, r := range b.ridges {
		if w := r.x1 - r.x0; w > 0 {
			nx += 2.4 * r.h * (2*x - r.x0 - r.x1) / w
		}
		if h := r.y1 - r.y0; h > 0 {
			ny += 2.4 * r.h * (2*y - r.y0 - r.y1) / h
		}
	}
	ld := math.Sqrt(102)
	lx, ly, lz := -1/ld, -1/ld, 10/ld
	const ambient = 0.2
	cos := math.Min(1, (nx*lx+ny*ly+lz)/math.Sqrt(nx*nx+ny*ny+1))
	p := 1.08 * ((1-ambient)*math.Max(0, cos) + ambient) / ((1-ambient)*lz + ambient)
	return rgb{math.Min(255, b.color.r*p), math.Min(255, b.color.g*p), math.Min(255, b.color.b*p)}
}

// roundRect is the signed distance from a point to a rounded rectangle, negative inside.
func roundRect(x, y float64, r rect, radius float64) float64 {
	cx, cy := (r.x0+r.x1)/2, (r.y0+r.y1)/2
	hx, hy := (r.x1-r.x0)/2, (r.y1-r.y0)/2
	radius = math.Min(radius, math.Min(hx, hy))
	qx := math.Abs(x-cx) - hx + radius
	qy := math.Abs(y-cy) - hy + radius
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return outside + math.Min(math.Max(qx, qy), 0) - radius
}

// coverage is how much of a pixel a shape covers, from its distance at the pixel's centre.
func coverage(dist float64) float64 { return clamp(0.5-dist, 0, 1) }

func smoothstep(a, b, x float64) float64 {
	t := clamp((x-a)/(b-a), 0, 1)
	return t * t * (3 - 2*t)
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func mix(a, b rgb, t float64) rgb {
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

// premul is a colour with alpha, its channels already multiplied by alpha.
type premul struct{ r, g, b, a float64 }

func (p premul) over(c rgb, a float64) premul {
	return premul{c.r*a + p.r*(1-a), c.g*a + p.g*(1-a), c.b*a + p.b*(1-a), a + p.a*(1-a)}
}

func (p premul) nrgba() color.NRGBA {
	if p.a <= 0 {
		return color.NRGBA{}
	}
	ch := func(v float64) uint8 { return uint8(clamp(math.Round(v/p.a), 0, 255)) }
	return color.NRGBA{ch(p.r), ch(p.g), ch(p.b), uint8(math.Round(p.a * 255))}
}

// ---- Files ----

type icoImage struct {
	size int
	data []byte
}

// icoFile stores PNG images in an .ico file, which Windows Vista and later read.
func icoFile(images []icoImage) []byte {
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint16(0))
	le(uint16(1))
	le(uint16(len(images)))
	offset := 6 + 16*len(images)
	for _, im := range images {
		dim := uint8(im.size)
		if im.size >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})
		le(uint16(1))
		le(uint16(32))
		le(uint32(len(im.data)))
		le(uint32(offset))
		offset += len(im.data)
	}
	for _, im := range images {
		b.Write(im.data)
	}
	return b.Bytes()
}

type icnsImage struct {
	kind string
	size int
	data []byte
}

func icnsFile(images []icnsImage) []byte {
	var body bytes.Buffer
	for _, im := range images {
		body.WriteString(im.kind)
		binary.Write(&body, binary.BigEndian, uint32(8+len(im.data)))
		body.Write(im.data)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes()
}
