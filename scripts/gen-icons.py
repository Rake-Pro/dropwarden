#!/usr/bin/env python3
"""Generate dropwarden's app icons from the loot-chest logo.

The logo is the 24x24 SVG in frontend/index.html, redrawn here with Pillow
(no SVG rasterizer needed) on a dark rounded-square plate so it reads on both
light and dark backgrounds:

  lid     #ffd685  x 3..21, y 3..10, top corners rounded (r 5.5)
  body    #f2b24e  x 3..21, y 12..21, bottom corners rounded (r 2.5)
  clasp   #7a4c14  x 10..14, y 8.6..15, rounded (r 1.2)
  keyhole #ffd685  circle at (12, 11.4), r 1.1

Outputs (commit them; the release workflow consumes them):
  build/icons/dropwarden_icon.png  1024x1024 master (macOS icns is built from
                                    it on the CI runner by scripts/bundle.sh)
  build/icons/dropwarden.ico       16/24/32/48/64/128/256 (Windows exe + installer)
  build/icons/tray.png             44x44 (for future tray use)

Run from the repo root: python3 scripts/gen-icons.py
"""
import os

from PIL import Image, ImageDraw

OUT = "build/icons"
SIZE = 1024
SS = 4  # supersampling factor; drawn at SIZE*SS and downsampled

PLATE = (27, 31, 39, 255)        # #1b1f27
PLATE_EDGE = (46, 52, 64, 255)   # faint rim so the plate edge reads on dark UIs
LID = (0xFF, 0xD6, 0x85, 255)
BODY = (0xF2, 0xB2, 0x4E, 255)
CLASP = (0x7A, 0x4C, 0x14, 255)


def draw_master():
    s = SIZE * SS
    img = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    # Plate: rounded square with a small margin, macOS-style proportions.
    margin = 64 * SS
    radius = 200 * SS
    d.rounded_rectangle([margin, margin, s - margin, s - margin], radius=radius, fill=PLATE_EDGE)
    rim = 6 * SS
    d.rounded_rectangle([margin + rim, margin + rim, s - margin - rim, s - margin - rim],
                        radius=radius - rim, fill=PLATE)

    # SVG units -> pixels: the 18-unit chest spans ~63% of the canvas,
    # centered on the SVG's (12, 12).
    unit = 36 * SS
    off = s / 2 - 12 * unit

    def p(x, y):
        return (off + x * unit, off + y * unit)

    def box(x0, y0, x1, y1):
        return [*p(x0, y0), *p(x1, y1)]

    d.rounded_rectangle(box(3, 3, 21, 10), radius=5.5 * unit, fill=LID,
                        corners=(True, True, False, False))
    d.rounded_rectangle(box(3, 12, 21, 21), radius=2.5 * unit, fill=BODY,
                        corners=(False, False, True, True))
    d.rounded_rectangle(box(10, 8.6, 14, 15), radius=1.2 * unit, fill=CLASP)
    d.ellipse(box(12 - 1.1, 11.4 - 1.1, 12 + 1.1, 11.4 + 1.1), fill=LID)

    return img.resize((SIZE, SIZE), Image.LANCZOS)


def main():
    os.makedirs(OUT, exist_ok=True)
    master = draw_master()
    master.save(os.path.join(OUT, "dropwarden_icon.png"))

    sizes = [16, 24, 32, 48, 64, 128, 256]
    master.save(os.path.join(OUT, "dropwarden.ico"), sizes=[(n, n) for n in sizes])

    master.resize((44, 44), Image.LANCZOS).save(os.path.join(OUT, "tray.png"))

    for name in sorted(os.listdir(OUT)):
        print("wrote", os.path.join(OUT, name), os.path.getsize(os.path.join(OUT, name)), "bytes")


if __name__ == "__main__":
    main()
