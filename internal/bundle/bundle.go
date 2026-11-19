// Package bundle seals portable scene history without private runtime state.
// The envelope carries every font the plate's revisions reference, so a
// recipient recompiles text from identical font bytes and layout cannot drift.
package bundle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"prismplate/internal/font"
	"prismplate/internal/repository"
	"prismplate/internal/validate"
)

const Schema = "prism-plate-bundle-v2"

type Envelope struct {
	Schema string            `json:"schema"`
	Digest string            `json:"digest"`
	Plate  repository.Plate  `json:"plate"`
	Fonts  []repository.Font `json:"fonts,omitempty"`
}

// payload is the canonical digest input: plate history plus referenced fonts.
type payload struct {
	Plate repository.Plate  `json:"plate"`
	Fonts []repository.Font `json:"fonts,omitempty"`
}

func Seal(p repository.Plate, fonts []repository.Font) ([]byte, error) {
	b, err := json.Marshal(payload{Plate: p, Fonts: fonts})
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Schema: Schema, Digest: repository.Hash(b), Plate: p, Fonts: fonts})
}

// Open verifies the envelope digest and every font's self-digest, then
// recompiles all revisions against exactly the bundled fonts. Font metadata
// is re-derived from the font bytes; bundled metadata is not trusted.
func Open(raw []byte) (repository.Plate, []repository.Font, error) {
	var e Envelope
	if len(raw) > 8<<20 {
		return e.Plate, nil, fmt.Errorf("bundle too large")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return e.Plate, nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return e.Plate, nil, fmt.Errorf("trailing bundle content")
	}
	b, err := json.Marshal(payload{Plate: e.Plate, Fonts: e.Fonts})
	if err != nil {
		return e.Plate, nil, err
	}
	if e.Schema != Schema {
		return e.Plate, nil, fmt.Errorf("unsupported bundle schema")
	}
	if repository.Hash(b) != e.Digest {
		return e.Plate, nil, fmt.Errorf("bundle digest mismatch")
	}
	if len(e.Fonts) > 32 {
		return e.Plate, nil, fmt.Errorf("bundle font capacity exceeded")
	}
	blobs := map[string][]byte{}
	for _, f := range e.Fonts {
		if !validate.ID(f.ID) || blobs[f.ID] != nil || len(f.Data) == 0 || len(f.Data) > font.MaxBytes || repository.Hash(f.Data) != f.Digest {
			return e.Plate, nil, fmt.Errorf("invalid bundled font %q", f.ID)
		}
		blobs[f.ID] = f.Data
	}
	set, err := font.ParseAll(blobs)
	if err != nil {
		return e.Plate, nil, err
	}
	for i, f := range e.Fonts {
		face := set[f.ID]
		e.Fonts[i].Family = face.Family()
		e.Fonts[i].License = face.License()
		e.Fonts[i].Glyphs = face.NumGlyphs()
		e.Fonts[i].UnitsPerEm = face.UnitsPerEm()
	}
	s := repository.Fresh()
	s.Plates[e.Plate.ID] = e.Plate
	for _, f := range e.Fonts {
		s.Fonts[f.ID] = f
	}
	if err = repository.Check(s); err != nil {
		return e.Plate, nil, err
	}
	if !validate.ID(e.Plate.ID) || len(e.Plate.Revisions) > 64 || len(e.Plate.Title) > 160 {
		return e.Plate, nil, fmt.Errorf("invalid plate metadata")
	}
	for _, r := range e.Plate.Revisions {
		if _, err = validate.Compile(r.Document, set); err != nil {
			return e.Plate, nil, err
		}
	}
	return e.Plate, e.Fonts, nil
}
