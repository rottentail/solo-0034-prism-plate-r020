#!/usr/bin/env python3
"""Build the prism-plate demo font: a deterministic TrueType glyf font.

The font is generated from schematic hand-defined contours so text workflows
run without machine fonts or network access. It exercises the admitted font
subset: cmap format 4, hmtx advances, a kern pair, one composite glyph, and
quadratic curves. Several contours deliberately use non-standard winding so
orientation normalization is covered. Glyph shapes are schematic placeholders,
not calligraphy. The font declares SIL OFL 1.1 in its name table; it is an
original generated artifact, so the license grant is the project's own.

Usage: python3 scripts/make_demo_font.py OUT.ttf  (prints sha256 of the file)
"""
import struct, sys, hashlib

UPM = 1000
FAMILY = 'Prism Demo Sans'
LICENSE = ('SIL Open Font License 1.1 - schematic demonstration outlines '
           'generated for prism-plate verification; no calligraphic intent')
LICENSE_URL = 'https://openfontlicense.org/'


def circle(cx, cy, r):
    """Four quadratic arcs approximating a circle (control points at corners)."""
    return [(cx + r, cy, 1), (cx + r, cy + r, 0), (cx, cy + r, 1), (cx, cy + r, 0),
            (cx - r, cy, 1), (cx - r, cy - r, 0), (cx, cy - r, 1), (cx + r, cy - r, 0)]


# (advance, contours) or (advance, ('composite', [(gid, dx, dy), ...])).
# Contours are lists of (x, y, on_curve). Inner rings intentionally repeat the
# outer winding; the ling bar is intentionally opposite.
GLYPHS = [
    (500, [[(50, 0, 1), (450, 0, 1), (450, 700, 1), (50, 700, 1)]]),               # 0 .notdef
    (300, []),                                                                     # 1 space
    (700, [circle(350, 575, 165), circle(350, 575, 95),                            # 2 '8' two disjoint rings
           circle(350, 195, 185), circle(350, 195, 105)]),
    (700, [[(60, 0, 1), (640, 0, 1), (350, 700, 1)],                               # 3 'A' with counter
           [(290, 170, 1), (410, 170, 1), (350, 380, 1)]]),
    (680, [[(40, 700, 1), (340, 60, 1), (640, 700, 1),                             # 4 'V'
            (510, 700, 1), (340, 310, 1), (170, 700, 1)]]),
    (760, [circle(380, 350, 330), circle(380, 350, 190)]),                         # 5 'O'
    (600, [[(130, 760, 1), (290, 760, 1), (290, 920, 1), (130, 920, 1)],           # 6 dieresis
           [(310, 760, 1), (470, 760, 1), (470, 920, 1), (310, 920, 1)]]),
    (700, ('composite', [(3, 0, 0), (6, 50, 0)])),                                 # 7 'Ä' = A + dieresis
    (1000, [[(80, 60, 1), (920, 60, 1), (920, 880, 1), (80, 880, 1)],              # 8 U+68F1 棱
            [(260, 240, 1), (740, 240, 1), (740, 700, 1), (260, 700, 1)],
            [(120, 490, 1), (880, 530, 1), (880, 470, 1), (120, 430, 1)]]),
    (1000, [[(60, 160, 1), (460, 160, 1), (460, 860, 1), (60, 860, 1)],            # 9 U+7248 版
            [(170, 300, 1), (350, 300, 1), (350, 720, 1), (170, 720, 1)],
            [(540, 160, 1), (940, 160, 1), (940, 860, 1), (540, 860, 1)]]),
]
CMAP = {0x20: 1, 0x38: 2, 0x41: 3, 0x4F: 5, 0x56: 4, 0xA8: 6, 0xC4: 7, 0x68F1: 8, 0x7248: 9}
KERN = {(3, 4): -90}  # A,V


def simple_glyph(contours):
    pts = [(x, y, on) for c in contours for (x, y, on) in c]
    xs = [p[0] for p in pts]
    ys = [p[1] for p in pts]
    out = struct.pack('>hhhhh', len(contours), min(xs), min(ys), max(xs), max(ys))
    ends, acc = [], 0
    for c in contours:
        acc += len(c)
        ends.append(acc - 1)
    out += struct.pack('>%dH' % len(ends), *ends)
    out += struct.pack('>H', 0)  # instruction length
    out += bytes(0x01 if on else 0x00 for (_, _, on) in pts)  # on-curve flag, int16 coords
    for vals in (xs, ys):
        prev = 0
        for v in vals:
            out += struct.pack('>h', v - prev)
            prev = v
    return out


def composite_glyph(components, boxes):
    xs = [b[0] for b in boxes]
    ys = [b[1] for b in boxes]
    xe = [b[2] for b in boxes]
    ye = [b[3] for b in boxes]
    out = struct.pack('>hhhhh', -1, min(xs), min(ys), max(xe), max(ye))
    for i, (gid, dx, dy) in enumerate(components):
        flags = 0x0001 | 0x0002  # word args, xy values
        if i < len(components) - 1:
            flags |= 0x0020  # more components
        out += struct.pack('>HHhh', flags, gid, dx, dy)
    return out


def checksum(data):
    pad = (-len(data)) % 4
    data = data + b'\0' * pad
    total = 0
    for i in range(0, len(data), 4):
        total = (total + struct.unpack('>I', data[i:i + 4])[0]) & 0xFFFFFFFF
    return total


def build():
    glyphs = []
    boxes = []
    for adv, body in GLYPHS:
        if isinstance(body, tuple):
            comp_boxes = []
            for gid, dx, dy in body[1]:
                b = boxes[gid]
                comp_boxes.append((b[0] + dx, b[1] + dy, b[2] + dx, b[3] + dy))
            glyphs.append(composite_glyph(body[1], comp_boxes))
            boxes.append((min(b[0] for b in comp_boxes), min(b[1] for b in comp_boxes),
                          max(b[2] for b in comp_boxes), max(b[3] for b in comp_boxes)))
        else:
            if body:
                pts = [(x, y) for c in body for (x, y, _) in c]
                xs = [p[0] for p in pts]
                ys = [p[1] for p in pts]
                boxes.append((min(xs), min(ys), max(xs), max(ys)))
                glyphs.append(simple_glyph(body))
            else:
                boxes.append((0, 0, 0, 0))
                glyphs.append(b'')
    num = len(glyphs)

    loca = [0]
    glyf = b''
    for g in glyphs:
        glyf += g
        glyf += b'\0' * ((-len(glyf)) % 4)
        loca.append(len(glyf))
    loca_tab = struct.pack('>%dI' % (num + 1), *loca)

    bbox = (min(b[0] for b in boxes), min(b[1] for b in boxes),
            max(b[2] for b in boxes), max(b[3] for b in boxes))
    head = struct.pack('>IIIIHHQQhhhhHHhhh',
                       0x00010000, 0x00010000, 0, 0x5F0F3CF5, 0, UPM, 0, 0,
                       bbox[0], bbox[1], bbox[2], bbox[3], 0, 8, 2, 1, 0)
    advances = [a for a, _ in GLYPHS]
    hhea = struct.pack('>IhhhHhhhhhhhhhhhH', 0x00010000, 950, -250, 0,
                       max(advances), 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, num)
    hmtx = b''.join(struct.pack('>Hh', a, 0) for a in advances)
    simple = [b for _, b in GLYPHS if not isinstance(b, tuple)]
    composites = [b for _, b in GLYPHS if isinstance(b, tuple)]
    max_points = max(len([p for c in b for p in c]) for b in simple)
    max_contours = max(len(b) for b in simple)
    max_comp_points = 0
    max_comp_contours = 0
    max_comp_elements = 0
    for _, comps in composites:
        max_comp_elements = max(max_comp_elements, len(comps))
        pts = 0
        cts = 0
        for gid, _, _ in comps:
            ref = GLYPHS[gid][1]
            pts += len([p for c in ref for p in c])
            cts += len(ref)
        max_comp_points = max(max_comp_points, pts)
        max_comp_contours = max(max_comp_contours, cts)
    maxp = struct.pack('>I' + 'H' * 14, 0x00010000, num, max_points, max_contours,
                       max_comp_points, max_comp_contours, 1, 0, 0, 0, 0, 0, 0,
                       max_comp_elements, 1 if composites else 0)

    codes = sorted(CMAP) + [0xFFFF]
    seg = len(codes)
    ends = codes
    starts = codes
    deltas = [((CMAP.get(c, 0) - c) & 0xFFFF) for c in codes]
    seg2 = 2 * seg
    pow2 = 1
    entry = 0
    while pow2 * 2 <= seg:
        pow2 *= 2
        entry += 1
    cmap4 = struct.pack('>HHHHHHH', 4, 16 + 8 * seg, 0, seg2, 2 * pow2, entry, seg2 - 2 * pow2)
    cmap4 += struct.pack('>%dH' % seg, *ends)
    cmap4 += struct.pack('>H', 0)
    cmap4 += struct.pack('>%dH' % seg, *starts)
    cmap4 += struct.pack('>%dH' % seg, *deltas)
    cmap4 += struct.pack('>%dH' % seg, *([0] * seg))
    cmap = struct.pack('>HHHHI', 0, 1, 3, 1, 12) + cmap4

    names = [(1, FAMILY), (13, LICENSE), (14, LICENSE_URL)]
    recs = b''
    storage = b''
    for nid, s in names:
        enc = s.encode('utf-16-be')
        recs += struct.pack('>HHHHHH', 3, 1, 0x409, nid, len(enc), len(storage))
        storage += enc
    name = struct.pack('>HHH', 0, len(names), 6 + 12 * len(names)) + recs + storage

    pairs = b''.join(struct.pack('>HHh', l, r, v) for (l, r), v in sorted(KERN.items()))
    kern = struct.pack('>HH', 0, 1)
    kern += struct.pack('>HHH', 0, 14 + 6 * len(KERN), 0x0001)
    kern += struct.pack('>HHHH', len(KERN), 6 * len(KERN), 0, 0) + pairs

    tables = {'cmap': cmap, 'glyf': glyf, 'head': head, 'hhea': hhea, 'hmtx': hmtx,
              'kern': kern, 'loca': loca_tab, 'maxp': maxp, 'name': name}
    tags = sorted(tables)
    n = len(tags)
    pow2 = 1
    entry = 0
    while pow2 * 2 <= n:
        pow2 *= 2
        entry += 1
    header = struct.pack('>IHHHH', 0x00010000, n, 16 * pow2, entry, 16 * n - 16 * pow2)
    offset = 12 + 16 * n
    records = b''
    body = b''
    head_off = None
    for tag in tags:
        data = tables[tag]
        if tag == 'head':
            head_off = offset
        records += tag.encode('ascii') + struct.pack('>III', checksum(data), offset, len(data))
        body += data + b'\0' * ((-len(data)) % 4)
        offset += len(data) + ((-len(data)) % 4)
    font = header + records + body
    adj = (0xB1B0AFBA - checksum(font)) & 0xFFFFFFFF
    return font[:head_off + 8] + struct.pack('>I', adj) + font[head_off + 12:]


def main():
    if len(sys.argv) != 2:
        raise SystemExit('usage: make_demo_font.py OUT.ttf')
    data = build()
    with open(sys.argv[1], 'wb') as fh:
        fh.write(data)
    print(hashlib.sha256(data).hexdigest())


if __name__ == '__main__':
    main()
