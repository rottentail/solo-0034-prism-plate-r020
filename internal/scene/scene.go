// Package scene defines the portable prism-plate-v1 document contract.
package scene

import "prismplate/internal/geom"

type Color struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
	A float64 `json:"a"`
}
type Stop struct {
	At    float64 `json:"at"`
	Color Color   `json:"color"`
}
type Brush struct {
	Kind  string     `json:"kind"`
	Color Color      `json:"color"`
	From  geom.Point `json:"from"`
	To    geom.Point `json:"to"`
	Stops []Stop     `json:"stops,omitempty"`
}
type Shape struct {
	ID        string      `json:"id"`
	Path      []geom.Op   `json:"path"`
	FillRule  string      `json:"fill_rule"`
	Brush     Brush       `json:"brush"`
	Transform geom.Matrix `json:"transform"`
}

// Text is an editable text object. The source string and font reference are
// the stored facts; outlines are resolved deterministically at compile time
// from the uploaded font bytes. FontDigest optionally pins the exact font the
// author laid out with. Origin is the baseline start in local coordinates.
type Text struct {
	ID            string      `json:"id"`
	Source        string      `json:"source"`
	Font          string      `json:"font"`
	FontDigest    string      `json:"font_digest,omitempty"`
	Size          float64     `json:"size"`
	Origin        geom.Point  `json:"origin"`
	LetterSpacing float64     `json:"letter_spacing"`
	Brush         Brush       `json:"brush"`
	Transform     geom.Matrix `json:"transform"`
}
type Layer struct {
	ID      string    `json:"id"`
	Opacity float64   `json:"opacity"`
	Blend   string    `json:"blend"`
	Clip    []geom.Op `json:"clip,omitempty"`
	Mask    []geom.Op `json:"mask,omitempty"`
	Shapes  []Shape   `json:"shapes"`
	Texts   []Text    `json:"texts,omitempty"`
}
type Document struct {
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Background Color   `json:"background"`
	Layers     []Layer `json:"layers"`
}
type CompiledShape struct {
	Shape   Shape
	Path    geom.Path
	Inverse geom.Matrix
}
type CompiledLayer struct {
	Layer  Layer
	Clip   geom.Path
	Mask   geom.Path
	Shapes []CompiledShape
}
type Compiled struct {
	Document Document
	Layers   []CompiledLayer
	Segments int
}
