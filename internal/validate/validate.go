// Package validate owns structural admission and the shared compilation budget.
package validate

import (
	"fmt"
	"math"
	"prismplate/internal/font"
	"prismplate/internal/geom"
	"prismplate/internal/scene"
	"regexp"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func ID(s string) bool         { return identifier.MatchString(s) }
func finite(x float64) bool    { return !math.IsNaN(x) && !math.IsInf(x, 0) && math.Abs(x) <= 1e6 }
func Unit(x float64) bool      { return finite(x) && x >= 0 && x <= 1 }
func Color(c scene.Color) bool { return Unit(c.R) && Unit(c.G) && Unit(c.B) && Unit(c.A) }
func path(ops []geom.Op) (geom.Path, error) {
	if len(ops) == 0 || len(ops) > 256 {
		return nil, fmt.Errorf("contour operation count must be 1..256")
	}
	open := false
	vertices := 0
	for _, o := range ops {
		n, ok := map[string]int{"M": 1, "L": 1, "Q": 2, "C": 3, "Z": 0}[o.Kind]
		if !ok || len(o.Points) != n {
			return nil, fmt.Errorf("invalid contour verb or arity")
		}
		for _, p := range o.Points {
			if !finite(p.X) || !finite(p.Y) {
				return nil, fmt.Errorf("non-finite contour")
			}
		}
		if o.Kind == "M" {
			if open {
				return nil, fmt.Errorf("close each contour before another M")
			}
			open = true
			vertices = 1
		} else {
			if !open {
				return nil, fmt.Errorf("contour must begin with M")
			}
			if o.Kind == "Z" {
				if vertices < 3 {
					return nil, fmt.Errorf("contour requires three vertices")
				}
				open = false
			} else {
				vertices++
			}
		}
	}
	if open {
		return nil, fmt.Errorf("contour must terminate with Z")
	}
	return geom.Flatten(ops), nil
}
func brush(b scene.Brush) error {
	if b.Kind == "solid" {
		if !Color(b.Color) {
			return fmt.Errorf("invalid solid color")
		}
		return nil
	}
	if b.Kind != "linear" {
		return fmt.Errorf("unknown brush")
	}
	for _, v := range []float64{b.From.X, b.From.Y, b.To.X, b.To.Y} {
		if !finite(v) {
			return fmt.Errorf("invalid gradient axis")
		}
	}
	if math.Hypot(b.To.X-b.From.X, b.To.Y-b.From.Y) < 1e-6 {
		return fmt.Errorf("degenerate gradient axis")
	}
	if len(b.Stops) < 2 || len(b.Stops) > 16 {
		return fmt.Errorf("gradient requires 2..16 stops")
	}
	prev := -1.0
	for _, s := range b.Stops {
		if !Unit(s.At) || s.At <= prev || !Color(s.Color) {
			return fmt.Errorf("invalid gradient stops")
		}
		prev = s.At
	}
	if b.Stops[0].At != 0 || prev != 1 {
		return fmt.Errorf("gradient endpoints must be 0 and 1")
	}
	return nil
}

// transform checks a shape or text transform for finiteness and invertibility.
func transform(m geom.Matrix) (geom.Matrix, error) {
	for _, v := range m {
		if !finite(v) {
			return geom.Matrix{}, fmt.Errorf("invalid transform")
		}
	}
	inv, ok := m.Inverse()
	if !ok {
		return geom.Matrix{}, fmt.Errorf("singular transform")
	}
	for _, v := range inv {
		if !finite(v) {
			return geom.Matrix{}, fmt.Errorf("ill-conditioned transform")
		}
	}
	return inv, nil
}

// text compiles one editable text object into a resolved, orientation-normalized
// contour shape. Missing glyphs and font mismatches are admission errors that
// name the text node so the gap can be located.
func text(t scene.Text, fonts font.Set) (scene.CompiledShape, geom.Path, error) {
	var out scene.CompiledShape
	if n := utf8.RuneCountInString(t.Source); n < 1 || n > 256 {
		return out, nil, fmt.Errorf("text %q: source must be 1..256 runes", t.ID)
	}
	if !finite(t.Size) || t.Size <= 0 || t.Size > 1024 {
		return out, nil, fmt.Errorf("text %q: invalid size", t.ID)
	}
	if !finite(t.LetterSpacing) || math.Abs(t.LetterSpacing) > 256 {
		return out, nil, fmt.Errorf("text %q: invalid letter spacing", t.ID)
	}
	if !finite(t.Origin.X) || !finite(t.Origin.Y) {
		return out, nil, fmt.Errorf("text %q: invalid origin", t.ID)
	}
	inv, err := transform(t.Transform)
	if err != nil {
		return out, nil, fmt.Errorf("text %q: %w", t.ID, err)
	}
	if err := brush(t.Brush); err != nil {
		return out, nil, fmt.Errorf("text %q: %w", t.ID, err)
	}
	face, ok := fonts[t.Font]
	if !ok {
		return out, nil, fmt.Errorf("text %q: unknown font %q", t.ID, t.Font)
	}
	if t.FontDigest != "" && t.FontDigest != face.Digest() {
		return out, nil, fmt.Errorf("text %q: font %q digest mismatch", t.ID, t.Font)
	}
	p, err := face.Layout(t.Source, t.Size, t.Origin, t.LetterSpacing)
	if err != nil {
		return out, nil, fmt.Errorf("text %q: %w", t.ID, err)
	}
	out = scene.CompiledShape{
		Shape:   scene.Shape{ID: t.ID, FillRule: "nonzero", Brush: t.Brush, Transform: t.Transform},
		Path:    p,
		Inverse: inv,
	}
	return out, p, nil
}
func Compile(d scene.Document, fonts font.Set) (scene.Compiled, error) {
	c := scene.Compiled{Document: d}
	if d.Width < 1 || d.Height < 1 || d.Width > 512 || d.Height > 512 || !Color(d.Background) {
		return c, fmt.Errorf("invalid canvas (maximum 512 square)")
	}
	if len(d.Layers) < 1 || len(d.Layers) > 16 {
		return c, fmt.Errorf("requires 1..16 layers")
	}
	ids := map[string]bool{}
	add := func(id string) error {
		if !ID(id) || ids[id] {
			return fmt.Errorf("invalid or duplicate node id %q", id)
		}
		ids[id] = true
		return nil
	}
	count := func(p geom.Path) {
		for _, r := range p {
			c.Segments += len(r)
		}
	}
	for _, l := range d.Layers {
		if err := add(l.ID); err != nil {
			return c, err
		}
		if !Unit(l.Opacity) || (l.Blend != "normal" && l.Blend != "multiply" && l.Blend != "screen") {
			return c, fmt.Errorf("invalid layer compositing")
		}
		if n := len(l.Shapes) + len(l.Texts); n < 1 || n > 32 {
			return c, fmt.Errorf("requires 1..32 shapes and texts per layer")
		}
		cl := scene.CompiledLayer{Layer: l}
		if len(l.Clip) > 0 {
			p, e := path(l.Clip)
			if e != nil {
				return c, e
			}
			cl.Clip = p
			count(p)
		}
		if len(l.Mask) > 0 {
			p, e := path(l.Mask)
			if e != nil {
				return c, e
			}
			cl.Mask = p
			count(p)
		}
		for _, s := range l.Shapes {
			if err := add(s.ID); err != nil {
				return c, err
			}
			if s.FillRule != "nonzero" && s.FillRule != "evenodd" {
				return c, fmt.Errorf("invalid fill rule")
			}
			inv, err := transform(s.Transform)
			if err != nil {
				return c, err
			}
			p, e := path(s.Path)
			if e != nil {
				return c, e
			}
			if e = brush(s.Brush); e != nil {
				return c, e
			}
			count(p)
			cl.Shapes = append(cl.Shapes, scene.CompiledShape{Shape: s, Path: p, Inverse: inv})
		}
		for _, t := range l.Texts {
			if err := add(t.ID); err != nil {
				return c, err
			}
			cs, p, err := text(t, fonts)
			if err != nil {
				return c, err
			}
			count(p)
			cl.Shapes = append(cl.Shapes, cs)
		}
		c.Layers = append(c.Layers, cl)
	}
	// Four samples per pixel; every fill is bounded even under adversarial input.
	if int64(c.Segments)*int64(d.Width)*int64(d.Height)*4 > 160_000_000 {
		return c, fmt.Errorf("scene exceeds bounded rendering work")
	}
	return c, nil
}
