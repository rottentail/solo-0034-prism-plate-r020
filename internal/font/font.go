// Package font parses TrueType glyf-outline fonts and lays out horizontal text runs.
//
// Only uploaded font bytes are used; machine fonts and network resources are never
// consulted. The parser admits a bounded TrueType subset: cmap format 4/12, hmtx
// advances, kern format 0 pairs, simple and composite glyf contours. CFF (OTTO),
// bitmap and variable fonts are rejected. Layout is a deterministic horizontal
// single-line run: one cmap glyph per rune, no complex shaping.
package font

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"prismplate/internal/geom"
	"unicode/utf16"
)

// Admission bounds keep uploaded fonts and resolved outlines inside the shared
// rendering budget. They are structural limits, not quality judgments.
const (
	MaxBytes      = 2 << 20 // per font file
	maxGlyphs     = 16384
	maxContours   = 1024 // per simple glyph
	maxPoints     = 16384
	maxComponents = 128 // per composite expansion
	maxDepth      = 8   // composite nesting
)

type cmapGroup struct{ start, end, gid uint32 }

type cmapFormat4 struct {
	data []byte // subtable slice, length already verified
	seg  int
}

// Face is a parsed, immutable TrueType font.
type Face struct {
	digest     string
	unitsPerEm int
	numGlyphs  int
	family     string
	license    string
	advances   []uint16
	loca       []uint32
	glyf       []byte
	cmap4      *cmapFormat4
	cmap12     []cmapGroup
	kern       map[uint32]int16
}

func u16(b []byte, off int) uint16 { return binary.BigEndian.Uint16(b[off:]) }
func i16(b []byte, off int) int16  { return int16(u16(b, off)) }
func u32(b []byte, off int) uint32 { return binary.BigEndian.Uint32(b[off:]) }

// Parse validates the sfnt structure and required tables. Bytes are retained,
// not copied; callers must not mutate them afterwards.
func Parse(data []byte) (*Face, error) {
	if len(data) < 12 || len(data) > MaxBytes {
		return nil, fmt.Errorf("font size must be 12 bytes..%d", MaxBytes)
	}
	scaler := u32(data, 0)
	if scaler != 0x00010000 && scaler != 0x74727565 { // 1.0 or 'true'
		return nil, fmt.Errorf("unsupported sfnt scaler %#08x: only TrueType glyf outlines are admitted (CFF/OTTO and bitmap fonts rejected)", scaler)
	}
	numTables := int(u16(data, 4))
	if numTables == 0 || 12+16*numTables > len(data) {
		return nil, fmt.Errorf("truncated table directory")
	}
	tables := map[string][]byte{}
	for i := 0; i < numTables; i++ {
		rec := 12 + 16*i
		tag := string(data[rec : rec+4])
		off, ln := u32(data, rec+8), u32(data, rec+12)
		if uint64(off)+uint64(ln) > uint64(len(data)) {
			return nil, fmt.Errorf("table %q out of bounds", tag)
		}
		if _, dup := tables[tag]; !dup {
			tables[tag] = data[off : off+ln]
		}
	}
	f := &Face{digest: digestOf(data)}
	head, ok := tables["head"]
	if !ok || len(head) < 54 {
		return nil, fmt.Errorf("missing or truncated head table")
	}
	if u32(head, 12) != 0x5F0F3CF5 {
		return nil, fmt.Errorf("bad head magic number")
	}
	f.unitsPerEm = int(u16(head, 18))
	if f.unitsPerEm < 16 || f.unitsPerEm > 16384 {
		return nil, fmt.Errorf("unitsPerEm out of range")
	}
	locFormat := int(i16(head, 50))
	if locFormat != 0 && locFormat != 1 {
		return nil, fmt.Errorf("unsupported loca format %d", locFormat)
	}
	maxp, ok := tables["maxp"]
	if !ok || len(maxp) < 6 {
		return nil, fmt.Errorf("missing or truncated maxp table")
	}
	f.numGlyphs = int(u16(maxp, 4))
	if f.numGlyphs < 1 || f.numGlyphs > maxGlyphs {
		return nil, fmt.Errorf("glyph count out of admitted range")
	}
	hhea, ok := tables["hhea"]
	if !ok || len(hhea) < 36 {
		return nil, fmt.Errorf("missing or truncated hhea table")
	}
	numH := int(u16(hhea, 34))
	if numH < 1 || numH > f.numGlyphs {
		return nil, fmt.Errorf("bad horizontal metric count")
	}
	hmtx, ok := tables["hmtx"]
	if !ok || len(hmtx) < 4*numH+2*(f.numGlyphs-numH) {
		return nil, fmt.Errorf("missing or truncated hmtx table")
	}
	f.advances = make([]uint16, f.numGlyphs)
	last := uint16(0)
	for g := 0; g < f.numGlyphs; g++ {
		if g < numH {
			last = u16(hmtx, 4*g)
		}
		f.advances[g] = last
	}
	glyf, ok := tables["glyf"]
	if !ok {
		return nil, fmt.Errorf("missing glyf table")
	}
	f.glyf = glyf
	locaRaw, ok := tables["loca"]
	if !ok {
		return nil, fmt.Errorf("missing loca table")
	}
	f.loca = make([]uint32, f.numGlyphs+1)
	if locFormat == 0 {
		if len(locaRaw) < 2*(f.numGlyphs+1) {
			return nil, fmt.Errorf("truncated loca table")
		}
		for i := range f.loca {
			f.loca[i] = uint32(u16(locaRaw, 2*i)) * 2
		}
	} else {
		if len(locaRaw) < 4*(f.numGlyphs+1) {
			return nil, fmt.Errorf("truncated loca table")
		}
		for i := range f.loca {
			f.loca[i] = u32(locaRaw, 4*i)
		}
	}
	for i := 1; i < len(f.loca); i++ {
		if f.loca[i] < f.loca[i-1] {
			return nil, fmt.Errorf("loca offsets not ascending")
		}
	}
	if f.loca[f.numGlyphs] > uint32(len(glyf)) {
		return nil, fmt.Errorf("loca exceeds glyf table")
	}
	if err := f.parseCmap(tables); err != nil {
		return nil, err
	}
	f.parseKern(tables)
	f.parseName(tables)
	return f, nil
}

func digestOf(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func (f *Face) parseCmap(tables map[string][]byte) error {
	cmap, ok := tables["cmap"]
	if !ok || len(cmap) < 4 {
		return fmt.Errorf("missing cmap table")
	}
	n := int(u16(cmap, 2))
	if len(cmap) < 4+8*n {
		return fmt.Errorf("truncated cmap directory")
	}
	best4, best12 := -1, -1
	for i := 0; i < n; i++ {
		off := int(u32(cmap, 4+8*i+4))
		if off < 0 || off+2 > len(cmap) {
			continue
		}
		switch u16(cmap, off) {
		case 4:
			if best4 < 0 {
				best4 = off
			}
		case 12:
			if best12 < 0 {
				best12 = off
			}
		}
	}
	if best12 >= 0 {
		st := cmap[best12:]
		if len(st) < 16 {
			return fmt.Errorf("truncated cmap format 12")
		}
		ln := int(u32(st, 4))
		ng := int(u32(st, 12))
		if ln < 16 || ln > len(st) || ng < 1 || 16+12*ng > ln {
			return fmt.Errorf("bad cmap format 12 structure")
		}
		groups := make([]cmapGroup, ng)
		prevEnd := uint32(0)
		for i := 0; i < ng; i++ {
			o := 16 + 12*i
			g := cmapGroup{u32(st, o), u32(st, o+4), u32(st, o+8)}
			if g.end < g.start || (i > 0 && g.start <= prevEnd) {
				return fmt.Errorf("cmap format 12 groups not ascending")
			}
			prevEnd = g.end
			groups[i] = g
		}
		f.cmap12 = groups
		return nil
	}
	if best4 >= 0 {
		st := cmap[best4:]
		if len(st) < 16 {
			return fmt.Errorf("truncated cmap format 4")
		}
		ln := int(u16(st, 2))
		if ln < 16 || ln > len(st) {
			return fmt.Errorf("bad cmap format 4 length")
		}
		st = st[:ln]
		seg := int(u16(st, 6)) / 2
		if seg < 1 || 16+8*seg > ln {
			return fmt.Errorf("bad cmap format 4 segments")
		}
		f.cmap4 = &cmapFormat4{data: st, seg: seg}
		return nil
	}
	return fmt.Errorf("no admitted cmap subtable (need format 4 or 12)")
}

func (f *Face) parseKern(tables map[string][]byte) {
	k, ok := tables["kern"]
	if !ok || len(k) < 4 || u16(k, 0) != 0 {
		return
	}
	n := int(u16(k, 2))
	off := 4
	for t := 0; t < n && off+6 <= len(k); t++ {
		ln := int(u16(k, off+2))
		coverage := u16(k, off+4)
		if ln < 6 || off+ln > len(k) {
			return
		}
		if coverage>>8 == 0 && f.kern == nil {
			np := int(u16(k, off+6))
			if 14+6*np <= ln {
				m := map[uint32]int16{}
				for i := 0; i < np; i++ {
					o := off + 14 + 6*i
					m[uint32(u16(k, o))<<16|uint32(u16(k, o+2))] = i16(k, o+4)
				}
				f.kern = m
			}
		}
		off += ln
	}
}

func (f *Face) parseName(tables map[string][]byte) {
	nm, ok := tables["name"]
	if !ok || len(nm) < 6 {
		return
	}
	count := int(u16(nm, 2))
	storage := int(u16(nm, 4))
	if 6+12*count > len(nm) || storage > len(nm) {
		return
	}
	pick := func(want int) string {
		best := ""
		bestScore := -1
		for i := 0; i < count; i++ {
			o := 6 + 12*i
			if int(u16(nm, o+6)) != want {
				continue
			}
			off, ln := storage+int(u16(nm, o+10)), int(u16(nm, o+8))
			if off < storage || off+ln > len(nm) {
				continue
			}
			platform, lang := int(u16(nm, o)), int(u16(nm, o+4))
			score := 0
			switch platform {
			case 0, 3:
				score = 2
				if lang == 0x409 {
					score = 3
				}
			case 1:
				score = 1
			default:
				continue
			}
			if s := decodeName(platform, nm[off:off+ln]); s != "" && score > bestScore {
				best, bestScore = s, score
			}
		}
		return best
	}
	f.family = pick(1)
	f.license = pick(13)
	if f.license == "" {
		f.license = pick(14)
	}
}

func decodeName(platform int, b []byte) string {
	var s string
	if platform == 1 {
		rs := make([]rune, len(b))
		for i, c := range b {
			if c < 0x20 || c > 0x7E {
				rs[i] = '?'
			} else {
				rs[i] = rune(c)
			}
		}
		s = string(rs)
	} else {
		if len(b)%2 != 0 {
			b = b[:len(b)-1]
		}
		u := make([]uint16, len(b)/2)
		for i := range u {
			u[i] = u16(b, 2*i)
		}
		s = string(utf16.Decode(u))
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			continue
		}
		out = append(out, r)
		if len(out) >= 200 {
			break
		}
	}
	return string(out)
}

func (f *Face) lookup(r rune) (uint16, bool) {
	if f.cmap12 != nil {
		lo, hi := 0, len(f.cmap12)-1
		for lo <= hi {
			mid := (lo + hi) / 2
			g := f.cmap12[mid]
			switch {
			case uint32(r) < g.start:
				hi = mid - 1
			case uint32(r) > g.end:
				lo = mid + 1
			default:
				gid := g.gid + uint32(r) - g.start
				if gid > 0xFFFF {
					return 0, false
				}
				return uint16(gid), true
			}
		}
		return 0, false
	}
	if f.cmap4 != nil {
		return f.cmap4.lookup(r)
	}
	return 0, false
}

func (c *cmapFormat4) lookup(r rune) (uint16, bool) {
	if r > 0xFFFF {
		return 0, false
	}
	code := uint16(r)
	d := c.data
	lo, hi := 0, c.seg-1
	for lo < hi {
		mid := (lo + hi) / 2
		if u16(d, 14+2*mid) >= code {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	i := lo
	if u16(d, 14+2*i) < code || u16(d, 16+2*c.seg+2*i) > code {
		return 0, false
	}
	delta := int(i16(d, 16+4*c.seg+2*i))
	ro := int(u16(d, 16+6*c.seg+2*i))
	if ro == 0 {
		return uint16((int(code) + delta) & 0xFFFF), true
	}
	pos := 16 + 6*c.seg + 2*i + ro + 2*int(code-uint16(u16(d, 16+2*c.seg+2*i)))
	if pos+2 > len(d) {
		return 0, false
	}
	g := u16(d, pos)
	if g == 0 {
		return 0, false
	}
	return uint16((int(g) + delta) & 0xFFFF), true
}

func (f *Face) advance(gid int) int { return int(f.advances[gid]) }

type fpoint struct {
	x, y float64
	on   bool
}
type outline struct{ contours [][]fpoint }

func (f *Face) outline(gid int, depth int) (outline, error) {
	if gid < 0 || gid >= f.numGlyphs {
		return outline{}, fmt.Errorf("glyph id %d out of range", gid)
	}
	if depth > maxDepth {
		return outline{}, fmt.Errorf("composite glyph nesting too deep")
	}
	start, end := f.loca[gid], f.loca[gid+1]
	if start == end {
		return outline{}, nil
	}
	g := f.glyf[start:end]
	if len(g) < 10 {
		return outline{}, fmt.Errorf("truncated glyph %d", gid)
	}
	n := int(i16(g, 0))
	if n >= 0 {
		return simpleOutline(g, n)
	}
	if n == -1 {
		return f.compositeOutline(g, depth)
	}
	return outline{}, fmt.Errorf("glyph %d has invalid contour count", gid)
}

func simpleOutline(g []byte, n int) (outline, error) {
	if n == 0 {
		return outline{}, nil
	}
	if n > maxContours {
		return outline{}, fmt.Errorf("glyph contour count over budget")
	}
	if len(g) < 10+2*n+2 {
		return outline{}, fmt.Errorf("truncated glyph contours")
	}
	endPts := make([]int, n)
	for i := 0; i < n; i++ {
		endPts[i] = int(u16(g, 10+2*i))
	}
	total := endPts[n-1] + 1
	if total > maxPoints {
		return outline{}, fmt.Errorf("glyph point count over budget")
	}
	off := 10 + 2*n
	instr := int(u16(g, off))
	off += 2
	if off+instr > len(g) {
		return outline{}, fmt.Errorf("truncated glyph instructions")
	}
	off += instr
	flags := make([]byte, 0, total)
	for len(flags) < total {
		if off >= len(g) {
			return outline{}, fmt.Errorf("truncated glyph flags")
		}
		fl := g[off]
		off++
		flags = append(flags, fl)
		if fl&0x08 != 0 {
			if off >= len(g) {
				return outline{}, fmt.Errorf("truncated glyph flags")
			}
			rep := int(g[off])
			off++
			if len(flags)+rep > total {
				return outline{}, fmt.Errorf("glyph flag repeat overflow")
			}
			for i := 0; i < rep; i++ {
				flags = append(flags, fl)
			}
		}
	}
	xs := make([]int, total)
	ys := make([]int, total)
	acc := 0
	for i := 0; i < total; i++ {
		fl := flags[i]
		var dx int
		switch {
		case fl&0x02 != 0:
			if off >= len(g) {
				return outline{}, fmt.Errorf("truncated glyph coordinates")
			}
			v := int(g[off])
			off++
			if fl&0x10 != 0 {
				dx = v
			} else {
				dx = -v
			}
		case fl&0x10 != 0:
			dx = 0
		default:
			if off+2 > len(g) {
				return outline{}, fmt.Errorf("truncated glyph coordinates")
			}
			dx = int(i16(g, off))
			off += 2
		}
		acc += dx
		xs[i] = acc
	}
	acc = 0
	for i := 0; i < total; i++ {
		fl := flags[i]
		var dy int
		switch {
		case fl&0x04 != 0:
			if off >= len(g) {
				return outline{}, fmt.Errorf("truncated glyph coordinates")
			}
			v := int(g[off])
			off++
			if fl&0x20 != 0 {
				dy = v
			} else {
				dy = -v
			}
		case fl&0x20 != 0:
			dy = 0
		default:
			if off+2 > len(g) {
				return outline{}, fmt.Errorf("truncated glyph coordinates")
			}
			dy = int(i16(g, off))
			off += 2
		}
		acc += dy
		ys[i] = acc
	}
	out := outline{contours: make([][]fpoint, 0, n)}
	prev := 0
	for c := 0; c < n; c++ {
		if endPts[c] < prev {
			return outline{}, fmt.Errorf("glyph contour endpoints not ascending")
		}
		pts := make([]fpoint, 0, endPts[c]-prev+1)
		for i := prev; i <= endPts[c]; i++ {
			pts = append(pts, fpoint{float64(xs[i]), float64(ys[i]), flags[i]&0x01 != 0})
		}
		out.contours = append(out.contours, pts)
		prev = endPts[c] + 1
	}
	return out, nil
}

func (f *Face) compositeOutline(g []byte, depth int) (outline, error) {
	out := outline{}
	off := 10
	components := 0
	for {
		if off+4 > len(g) {
			return outline{}, fmt.Errorf("truncated composite glyph")
		}
		flags := int(u16(g, off))
		compGID := int(u16(g, off+2))
		off += 4
		var a1, a2 int
		if flags&0x0001 != 0 {
			if off+4 > len(g) {
				return outline{}, fmt.Errorf("truncated composite glyph")
			}
			a1, a2 = int(i16(g, off)), int(i16(g, off+2))
			off += 4
		} else {
			if off+2 > len(g) {
				return outline{}, fmt.Errorf("truncated composite glyph")
			}
			a1, a2 = int(int8(g[off])), int(int8(g[off+1]))
			off += 2
		}
		if flags&0x0002 == 0 {
			return outline{}, fmt.Errorf("composite point matching is not admitted")
		}
		dx, dy := float64(a1), float64(a2)
		m00, m01, m10, m11 := 1.0, 0.0, 0.0, 1.0
		switch {
		case flags&0x0008 != 0:
			if off+2 > len(g) {
				return outline{}, fmt.Errorf("truncated composite glyph")
			}
			s := float64(i16(g, off)) / 16384
			off += 2
			m00, m11 = s, s
		case flags&0x0040 != 0:
			if off+4 > len(g) {
				return outline{}, fmt.Errorf("truncated composite glyph")
			}
			m00 = float64(i16(g, off)) / 16384
			m11 = float64(i16(g, off+2)) / 16384
			off += 4
		case flags&0x0080 != 0:
			if off+8 > len(g) {
				return outline{}, fmt.Errorf("truncated composite glyph")
			}
			m00 = float64(i16(g, off)) / 16384
			m01 = float64(i16(g, off+2)) / 16384
			m10 = float64(i16(g, off+4)) / 16384
			m11 = float64(i16(g, off+6)) / 16384
			off += 8
		}
		child, err := f.outline(compGID, depth+1)
		if err != nil {
			return outline{}, err
		}
		for _, c := range child.contours {
			nc := make([]fpoint, len(c))
			for i, p := range c {
				nc[i] = fpoint{m00*p.x + m01*p.y + dx, m10*p.x + m11*p.y + dy, p.on}
			}
			out.contours = append(out.contours, nc)
		}
		components++
		if components > maxComponents || len(out.contours) > 2*maxContours {
			return outline{}, fmt.Errorf("composite glyph over budget")
		}
		if flags&0x0020 == 0 {
			break
		}
	}
	return out, nil
}

// contourOps expands one TrueType contour into M/L/Q/Z ops, inserting implied
// on-curve midpoints between consecutive off-curve points.
func contourOps(pts []fpoint) []geom.Op {
	n := len(pts)
	if n == 0 {
		return nil
	}
	expanded := make([]fpoint, 0, 2*n)
	for i := 0; i < n; i++ {
		a := pts[i]
		b := pts[(i+1)%n]
		expanded = append(expanded, a)
		if !a.on && !b.on {
			expanded = append(expanded, fpoint{(a.x + b.x) / 2, (a.y + b.y) / 2, true})
		}
	}
	start := -1
	for i, p := range expanded {
		if p.on {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	expanded = append(expanded[start:], expanded[:start]...)
	ops := []geom.Op{{Kind: "M", Points: []geom.Point{{X: expanded[0].x, Y: expanded[0].y}}}}
	for i := 1; i < len(expanded); {
		p := expanded[i]
		if p.on {
			ops = append(ops, geom.Op{Kind: "L", Points: []geom.Point{{X: p.x, Y: p.y}}})
			i++
			continue
		}
		if i+1 >= len(expanded) {
			break // trailing off-curve point: closed by the final Q below
		}
		e := expanded[i+1]
		ops = append(ops, geom.Op{Kind: "Q", Points: []geom.Point{{X: p.x, Y: p.y}, {X: e.x, Y: e.y}}})
		i += 2
	}
	if last := expanded[len(expanded)-1]; !last.on {
		ops = append(ops, geom.Op{Kind: "Q", Points: []geom.Point{{X: last.x, Y: last.y}, {X: expanded[0].x, Y: expanded[0].y}}})
	}
	return append(ops, geom.Op{Kind: "Z"})
}

// normalize rewinds each ring so nesting parity alone decides orientation:
// even depth rings get positive signed area, odd depth negative. With the
// nonzero fill rule this keeps counters (holes) stable regardless of the
// winding conventions inside the font, and keeps overlapping contours of
// different glyphs additive. Rings are already in text-local coordinates.
func normalize(rings geom.Path) {
	for i, ring := range rings {
		if len(ring) < 3 {
			continue
		}
		depth := 0
		for j, other := range rings {
			if i == j || len(other) < 3 {
				continue
			}
			if single := (geom.Path{other}); single.Contains(ring[0], "nonzero") {
				depth++
			}
		}
		area2 := 0.0
		for k, a := range ring {
			b := ring[(k+1)%len(ring)]
			area2 += a.X*b.Y - b.X*a.Y
		}
		if area2 == 0 {
			continue
		}
		if (area2 > 0) != (depth%2 == 0) {
			for l, r := 0, len(ring)-1; l < r; l, r = l+1, r-1 {
				ring[l], ring[r] = ring[r], ring[l]
			}
		}
	}
}

// Layout resolves src into flattened contours in text-local coordinates.
// origin is the baseline start; y grows downward to match the scene. Spacing
// between glyphs comes from hmtx advances plus kern pairs, with spacing added
// after every glyph (including the last). A missing glyph is an error that
// names the rune and its index so the gap can be located.
func (f *Face) Layout(src string, size float64, origin geom.Point, spacing float64) (geom.Path, error) {
	scale := size / float64(f.unitsPerEm)
	pen := 0.0
	prev := -1
	idx := 0
	var out geom.Path
	for _, r := range src {
		gid, ok := f.lookup(r)
		if !ok || gid == 0 || int(gid) >= f.numGlyphs {
			return nil, fmt.Errorf("missing glyph for U+%04X %q at rune index %d", r, string(r), idx)
		}
		if prev >= 0 && f.kern != nil {
			pen += float64(f.kern[uint32(prev)<<16|uint32(gid)]) * scale
		}
		o, err := f.outline(int(gid), 0)
		if err != nil {
			return nil, fmt.Errorf("glyph for U+%04X: %w", r, err)
		}
		var ops []geom.Op
		for _, c := range o.contours {
			ops = append(ops, contourOps(c)...)
		}
		for i := range ops {
			for j := range ops[i].Points {
				p := &ops[i].Points[j]
				p.X = origin.X + pen + p.X*scale
				p.Y = origin.Y - p.Y*scale
			}
		}
		rings := geom.Flatten(ops)
		normalize(rings)
		out = append(out, rings...)
		pen += float64(f.advance(int(gid)))*scale + spacing
		prev = int(gid)
		idx++
	}
	return out, nil
}

func (f *Face) Digest() string  { return f.digest }
func (f *Face) Family() string  { return f.family }
func (f *Face) License() string { return f.license }
func (f *Face) NumGlyphs() int  { return f.numGlyphs }
func (f *Face) UnitsPerEm() int { return f.unitsPerEm }

// Set maps font identities to parsed faces for one compilation pass.
type Set map[string]*Face

// ParseAll parses every uploaded font; any failure rejects the whole set so a
// compile never proceeds with a partial font inventory.
func ParseAll(blobs map[string][]byte) (Set, error) {
	set := Set{}
	for id, data := range blobs {
		face, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("font %q: %w", id, err)
		}
		set[id] = face
	}
	return set, nil
}
