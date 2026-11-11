"""Vördr brand images: the rune shield (Algiz, the rune of protection, on a shield) and the logo with the wordmark.

One geometry, two outputs: SVG sources in this folder and the PNGs Home Assistant reads from the integration's brand/
folder (HA 2026.3+: icon 256x256 and @2x 512, logo with the shortest side 256 and @2x 512, transparent, no padding;
dark_ variants for dark backgrounds). PNGs are drawn at 4x and scaled down for smooth edges.

    python assets/brand/render.py [--font <Ubuntu Sans ttf>]

Needs Pillow. The wordmark uses Ubuntu Sans (Ubuntu Font Licence), Bold.
"""

import argparse
import os
import sys

from PIL import Image, ImageDraw, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(os.path.dirname(HERE))
HA_BRAND = os.path.join(REPO, "ha", "custom_components", "vordr", "brand")

# Design space: the shield spans x 20..80, y 14..88 (as in the first sketch); the rune sits inside.
SHIELD = [
    ("M", 50, 14),
    ("L", 80, 24),
    ("L", 80, 50),
    ("C", 80, 70, 66, 82, 50, 88),
    ("C", 34, 82, 20, 70, 20, 50),
    ("L", 20, 24),
]
BOX = (20, 14, 80, 88)
CENTRE = (50, 51)
RING_OUT, RING_IN = 0.86, 0.80  # the light ring between these scales of the shield (around CENTRE)
RUNE = [((50, 32), (50, 74)), ((50, 52), (37, 36)), ((50, 52), (63, 36))]
RUNE_WIDTH = 7.0

THEMES = {  # shield, ring, rune, wordmark
    "light": ("#085041", "#9FE1CB", "#FAC775", "#085041"),
    "dark": ("#0F6E56", "#5DCAA5", "#FAC775", "#E1F5EE"),
}
WORD = "Vördr"
WEIGHT = 700  # Bold: SemiBold looked thin next to the solid shield
FONTS = [
    "/usr/share/fonts/truetype/ubuntu/UbuntuSans[wdth,wght].ttf",
]


def shield_points(steps=48):
    """The shield outline as points in design space (cubic segments sampled)."""
    pts, cur = [], None
    for seg in SHIELD:
        if seg[0] in ("M", "L"):
            cur = (seg[1], seg[2])
            pts.append(cur)
        else:
            x0, y0 = cur
            x1, y1, x2, y2, x3, y3 = seg[1:]
            for i in range(1, steps + 1):
                t = i / steps
                a, b, c, d = (1 - t) ** 3, 3 * (1 - t) ** 2 * t, 3 * (1 - t) * t**2, t**3
                pts.append((a * x0 + b * x1 + c * x2 + d * x3, a * y0 + b * y1 + c * y2 + d * y3))
            cur = (x3, y3)
    return pts


def scaled(pts, s):
    cx, cy = CENTRE
    return [(cx + (x - cx) * s, cy + (y - cy) * s) for x, y in pts]


def svg_path():
    out = []
    for seg in SHIELD:
        out.append(seg[0] + " " + " ".join(f"{v:g}" for v in seg[1:]))
    return " ".join(out) + " Z"


def svg_mark(theme):
    shield, ring, rune, _ = THEMES[theme]
    d = svg_path()
    cx, cy = CENTRE

    def at(s):
        return f'transform="translate({cx} {cy}) scale({s}) translate({-cx} {-cy})"'

    lines = " ".join(f"M{a[0]} {a[1]} L{b[0]} {b[1]}" for a, b in RUNE)
    return (
        f'<path d="{d}" fill="{shield}"/>'
        f'<path d="{d}" fill="{ring}" {at(RING_OUT)}/>'
        f'<path d="{d}" fill="{shield}" {at(RING_IN)}/>'
        f'<path d="{lines}" fill="none" stroke="{rune}" stroke-width="{RUNE_WIDTH}" stroke-linecap="round" '
        f'stroke-linejoin="round"/>'
    )


def write_svgs():
    x0, y0, x1, y1 = BOX
    w, h = x1 - x0, y1 - y0
    for theme in THEMES:
        suffix = "" if theme == "light" else "-dark"
        word = THEMES[theme][3]
        icon = (
            f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{x0 - (h - w) / 2:g} {y0} {h} {h}">'
            f"<title>Vördr</title>{svg_mark(theme)}</svg>\n"
        )
        logo = (
            f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{x0} {y0} {w + 13 + 160} {h}">'
            f"<title>Vördr</title>{svg_mark(theme)}"
            f'<text x="{x1 + 13}" y="{y0 + h * 0.70:g}" font-family="Ubuntu Sans, Ubuntu, sans-serif" '
            f'font-weight="700" font-size="{h * 0.62:g}" fill="{word}">{WORD}</text></svg>\n'
        )
        for name, text in ((f"icon{suffix}.svg", icon), (f"logo{suffix}.svg", logo)):
            with open(os.path.join(HERE, name), "w", encoding="utf-8", newline="\n") as f:
                f.write(text)


def draw_mark(theme, height, factor=4):
    """The rune shield as an RGBA image of the given height (shield trimmed: top and bottom touch the edges)."""
    shield, ring, rune, _ = THEMES[theme]
    x0, y0, x1, y1 = BOX
    k = height * factor / (y1 - y0)
    w = round((x1 - x0) * k)
    img = Image.new("RGBA", (w, height * factor), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def px(pts):
        return [((x - x0) * k, (y - y0) * k) for x, y in pts]

    base = shield_points()
    d.polygon(px(base), fill=shield)
    d.polygon(px(scaled(base, RING_OUT)), fill=ring)
    d.polygon(px(scaled(base, RING_IN)), fill=shield)
    r = RUNE_WIDTH * k / 2
    for a, b in RUNE:
        (ax, ay), (bx, by) = px([a, b])
        d.line([(ax, ay), (bx, by)], fill=rune, width=round(2 * r))
        for x, y in ((ax, ay), (bx, by)):
            d.ellipse([x - r, y - r, x + r, y + r], fill=rune)
    return img.resize((round(w / factor), height), Image.LANCZOS)


def find_font(arg):
    for path in [arg] if arg else FONTS:
        if path and os.path.isfile(path):
            return path
    sys.exit("Ubuntu Sans not found - pass --font <path to UbuntuSans[wdth,wght].ttf>")


def draw_logo(theme, height, font_path, factor=4):
    """Shield plus wordmark, trimmed to the content."""
    mark = draw_mark(theme, height * factor, factor=1)
    font = ImageFont.truetype(font_path, round(height * factor * 0.62))
    font.set_variation_by_axes([100, WEIGHT])  # width 100 %, weight; by axes (instance names differ per Pillow)
    left, top, right, bottom = font.getbbox(WORD)
    gap = round(height * factor * 0.17)
    w = mark.width + gap + (right - left)
    img = Image.new("RGBA", (w, mark.height), (0, 0, 0, 0))
    img.paste(mark, (0, 0), mark)
    # The wordmark's body (without descenders, "Vördr" has none) sits centred on the shield's middle.
    y = (mark.height - (bottom - top)) / 2 - top
    ImageDraw.Draw(img).text((mark.width + gap - left, y), WORD, font=font, fill=THEMES[theme][3])
    return img.resize((round(w / factor), height), Image.LANCZOS)


def square(img, size):
    """The mark centred in a square of the given size (the shield is taller than wide)."""
    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    out.paste(img, ((size - img.width) // 2, (size - img.height) // 2), img)
    return out


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--font")
    font = find_font(p.parse_args().font)
    write_svgs()
    os.makedirs(HA_BRAND, exist_ok=True)
    for theme in THEMES:
        pre = "" if theme == "light" else "dark_"
        for scale, suffix in ((1, ""), (2, "@2x")):
            square(draw_mark(theme, 256 * scale), 256 * scale).save(
                os.path.join(HA_BRAND, f"{pre}icon{suffix}.png"), optimize=True
            )
            draw_logo(theme, 256 * scale, font).save(os.path.join(HA_BRAND, f"{pre}logo{suffix}.png"), optimize=True)
    for name in sorted(os.listdir(HA_BRAND)):
        with Image.open(os.path.join(HA_BRAND, name)) as im:
            print(f"{name:18} {im.size[0]:4} x {im.size[1]:4}")


if __name__ == "__main__":
    main()
