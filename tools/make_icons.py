#!/usr/bin/env python3
"""Render CellBridge icons from art/devil.png (black line art).

App icons get a Liquid Glass look: glossy gradient tile, the devil as frosted
glass with a specular rim and soft shadow. Tray icons are the flat outline
tinted per connection state.
"""
from pathlib import Path

from PIL import Image, ImageChops, ImageDraw, ImageFilter

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "art" / "devil.png"
S = 1024

src = Image.open(SRC).convert("RGBA")
lum = src.convert("L")
alpha = src.getchannel("A")
lines = ImageChops.multiply(lum.point(lambda v: 255 if v < 128 else 0), alpha.point(lambda a: 255 if a >= 128 else 0))
fill = ImageChops.multiply(lum.point(lambda v: 255 if v >= 128 else 0), alpha.point(lambda a: 255 if a >= 128 else 0))


def fit(mask, box, size=S):
    """Scale mask to fit a box x box square, centred on a size x size canvas."""
    w, h = mask.size
    k = box / max(w, h)
    m = mask.resize((round(w * k), round(h * k)), Image.LANCZOS)
    out = Image.new("L", (size, size), 0)
    out.paste(m, ((size - m.width) // 2, (size - m.height) // 2))
    return out


def gradient(size, top, bottom):
    g = Image.new("RGBA", (1, 256))
    for y in range(256):
        t = y / 255
        g.putpixel((0, y), tuple(round(a + (b - a) * t) for a, b in zip(top, bottom)))
    return g.resize(size, Image.BICUBIC)


def solid(rgba, size=(S, S)):
    return Image.new("RGBA", size, rgba)


def glass_tile(glyph_box, rounded):
    # Background: diagonal ember-red → violet gradient with a soft light bloom.
    # Rotate an oversized gradient and crop, so no corners are left uncovered.
    big = gradient((S * 2, S * 2), (255, 94, 98, 255), (92, 24, 150, 255)).rotate(-20, resample=Image.BICUBIC)
    bg = big.crop((S // 2, S // 2, S // 2 + S, S // 2 + S))
    bloom = Image.new("L", (S, S), 0)
    ImageDraw.Draw(bloom).ellipse((-200, -260, 700, 520), fill=150)
    bg = Image.composite(solid((255, 170, 150, 255)), bg, bloom.filter(ImageFilter.GaussianBlur(160)))

    ln = fit(lines, glyph_box).filter(ImageFilter.MaxFilter(5))
    fl = fit(fill, glyph_box)

    # Soft shadow under the glass.
    sh = ln.filter(ImageFilter.GaussianBlur(22)).point(lambda v: v * 0.45)
    shadow = Image.new("L", (S, S), 0)
    shadow.paste(sh, (0, 22))
    img = Image.composite(solid((40, 0, 40, 255)), bg, shadow)

    # Frosted face: blurred, brightened background inside the head.
    frost = Image.alpha_composite(bg.filter(ImageFilter.GaussianBlur(30)), solid((255, 255, 255, 70)))
    img = Image.composite(frost, img, fl.point(lambda v: v * 0.55))

    # Glass strokes: white, brighter at the top, translucent at the bottom.
    body = gradient((S, S), (255, 255, 255, 245), (255, 236, 245, 200))
    img = Image.composite(body, img, ln)

    # Specular rim: light on top-left edges, tint on bottom-right edges.
    hi = ImageChops.subtract(ln, ImageChops.offset(ln, 6, 6)).filter(ImageFilter.GaussianBlur(1.5))
    lo = ImageChops.subtract(ln, ImageChops.offset(ln, -6, -6)).filter(ImageFilter.GaussianBlur(1.5))
    img = Image.composite(solid((255, 255, 255, 255)), img, hi)
    img = Image.composite(solid((200, 120, 200, 255)), img, lo.point(lambda v: v * 0.7))

    # Glossy top reflection across the tile.
    gloss = Image.new("L", (S, S), 0)
    ImageDraw.Draw(gloss).ellipse((-S * 0.3, -S * 0.75, S * 1.3, S * 0.42), fill=46)
    img = Image.composite(solid((255, 255, 255, 255)), img, gloss.filter(ImageFilter.GaussianBlur(8)))

    if rounded:
        # macOS-style tile: 824 px rounded square on a 1024 canvas.
        pad, r = 100, 185
        m = Image.new("L", (S, S), 0)
        ImageDraw.Draw(m).rounded_rectangle((pad, pad, S - pad, S - pad), r, fill=255)
        edge = Image.new("L", (S, S), 0)
        ImageDraw.Draw(edge).rounded_rectangle((pad, pad, S - pad, S - pad), r, outline=120, width=4)
        img = Image.composite(solid((255, 255, 255, 255)), img, edge.filter(ImageFilter.GaussianBlur(1)))
        drop = m.filter(ImageFilter.GaussianBlur(18)).point(lambda v: v * 0.35)
        canvas = Image.new("RGBA", (S, S), (0, 0, 0, 0))
        canvas.putalpha(ImageChops.offset(drop, 0, 12))
        canvas = Image.composite(img, canvas, m)
        return canvas
    return img


def tray(color):
    n = 64
    m = fit(lines.filter(ImageFilter.MaxFilter(9)), 62 * 4, n * 4).resize((n, n), Image.LANCZOS)
    out = Image.new("RGBA", (n, n), color + (0,))
    out.putalpha(m)
    return out


def main():
    ios = glass_tile(720, rounded=False).convert("RGB")  # iOS masks the corners itself; no alpha allowed
    iconset = ROOT / "ios" / "Sources" / "Assets.xcassets" / "AppIcon.appiconset"
    iconset.mkdir(parents=True, exist_ok=True)
    ios.save(iconset / "icon-1024.png")
    (iconset / "Contents.json").write_text(
        '{\n  "images" : [ { "filename" : "icon-1024.png", "idiom" : "universal", "platform" : "ios", "size" : "1024x1024" } ],\n'
        '  "info" : { "author" : "xcode", "version" : 1 }\n}\n')
    (iconset.parent / "Contents.json").write_text('{\n  "info" : { "author" : "xcode", "version" : 1 }\n}\n')

    tile = glass_tile(600, rounded=True)
    assets = ROOT / "desktop" / "assets"
    tile.save(assets / "icon-1024.png")
    tile.resize((256, 256), Image.LANCZOS).save(assets / "icon-256.png")
    tile.save(ROOT / "desktop" / "macos" / "AppIcon.icns", sizes=[(16, 16), (32, 32), (64, 64), (128, 128), (256, 256), (512, 512), (1024, 1024)])

    for name, color in {"off": (150, 150, 150), "busy": (240, 170, 30), "on": (40, 200, 90), "err": (220, 60, 60)}.items():
        tray(color).save(assets / f"tray-{name}.png")


if __name__ == "__main__":
    main()
