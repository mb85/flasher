# Builds disc.ttf, a one-glyph icon font with a CD at U+E000, drawn on the
# same grid and stroke weight as Shirei's Microns icons so it sits beside
# them in buttons. Needs fontTools:
#
#   python3 disc.py
import math

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen

UPM, ADVANCE = 480, 440
CX, CY = 220, 180  # centre, as for Microns' round glyphs
STROKE = 40


def arc(pen, r, a0, a1, start):
    """Quadratic segments along a circle of radius r from angle a0 to a1."""
    steps = max(2, math.ceil(abs(a1 - a0) / (math.pi / 8)))
    da = (a1 - a0) / steps
    pt = lambda a, rr=r: (round(CX + rr * math.cos(a)), round(CY + rr * math.sin(a)))
    if start:
        pen.moveTo(pt(a0))
    for i in range(steps):
        a = a0 + da * (i + 1)
        mid = a - da / 2
        pen.qCurveTo(pt(mid, r / math.cos(da / 2)), pt(a))


def ring(pen, r):
    """A stroked circle: outer contour clockwise, inner anticlockwise."""
    arc(pen, r + STROKE / 2, math.pi / 2, math.pi / 2 - 2 * math.pi, True)
    pen.closePath()
    arc(pen, r - STROKE / 2, math.pi / 2, math.pi / 2 + 2 * math.pi, True)
    pen.closePath()


def stroke_arc(pen, r, a0, a1):
    """A stroked arc with round caps, clockwise from a1 back to a0."""
    h = STROKE / 2
    arc(pen, r + h, a1, a0, True)
    cap = lambda a: (CX + r * math.cos(a), CY + r * math.sin(a))
    x, y = cap(a0)
    cap_arc(pen, x, y, h, a0, a0 - math.pi)
    arc(pen, r - h, a0, a1, False)
    x, y = cap(a1)
    cap_arc(pen, x, y, h, a1 + math.pi, a1)
    pen.closePath()


def cap_arc(pen, x, y, h, a0, a1):
    steps = 4
    da = (a1 - a0) / steps
    for i in range(steps):
        a = a0 + da * (i + 1)
        mid = a - da / 2
        k = h / math.cos(da / 2)
        pen.qCurveTo((round(x + k * math.cos(mid)), round(y + k * math.sin(mid))),
                     (round(x + h * math.cos(a)), round(y + h * math.sin(a))))


pen = TTGlyphPen(None)
ring(pen, 170)  # the disc's edge
ring(pen, 50)   # the hole
stroke_arc(pen, 110, math.radians(105), math.radians(165))  # a reflection
disc = pen.glyph()

fb = FontBuilder(UPM, isTTF=True)
fb.setupGlyphOrder([".notdef", "disc"])
fb.setupCharacterMap({0xE000: "disc"})
fb.setupGlyf({".notdef": TTGlyphPen(None).glyph(), "disc": disc})
fb.setupHorizontalMetrics({".notdef": (ADVANCE, 0), "disc": (ADVANCE, 30)})
fb.setupHorizontalHeader(ascent=420, descent=-60)
fb.setupNameTable({"familyName": "FlasherIcons", "styleName": "Regular"})
fb.setupOS2(sTypoAscender=420, sTypoDescender=-60, usWinAscent=420, usWinDescent=60)
fb.setupPost()
fb.save("disc.ttf")
