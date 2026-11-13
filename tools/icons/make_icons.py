"""Draws the PropBook candlestick icon and writes every platform's icon file.

usage: python3 make_icons.py <out_dir>
writes: icon.png (256, window/taskbar), PropBook.ico (Windows), AppIcon.icns (macOS),
        ext-16/32/48/128.png (browser extension)
"""
import sys
from pathlib import Path
from PIL import Image, ImageDraw, ImageFilter

GREEN = (63, 207, 142, 255)
RED = (240, 96, 90, 255)
TILE = (17, 21, 19, 255)
EDGE = (47, 56, 51, 255)
# (x, wick top, wick bottom, body top, body bottom, colour) on a 256 grid
CANDLES = [(64, 150, 214, 166, 204, RED), (108, 118, 196, 128, 176, GREEN),
           (152, 92, 170, 104, 146, GREEN), (196, 46, 130, 58, 112, GREEN)]


def draw(size: int) -> Image.Image:
    """Full-bleed tile, used for Windows and the browser extension."""
    im = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(im)
    s = size / 256
    d.rounded_rectangle([8 * s, 8 * s, 248 * s, 248 * s], radius=52 * s, fill=TILE, outline=EDGE, width=max(1, int(4 * s)))
    for x, wt, wb, bt, bb, c in CANDLES:
        d.line([(x * s, wt * s), (x * s, wb * s)], fill=c, width=max(1, int(6 * s)))
        d.rounded_rectangle([(x - 15) * s, bt * s, (x + 15) * s, bb * s], radius=4 * s, fill=c)
    return im


def smooth(size: int) -> Image.Image:
    return draw(size * 4).resize((size, size), Image.LANCZOS)


def mac_icon(size: int = 1024) -> Image.Image:
    """Apple's icon grid: the tile occupies ~824/1024 with a soft drop shadow."""
    tile = smooth(round(size * 0.84))
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    shadow = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    off = (size - tile.width) // 2
    alpha = tile.split()[3].point(lambda a: int(a * 0.45))
    shadow.paste((0, 0, 0, 255), (off, off + size // 64), alpha)
    canvas = Image.alpha_composite(canvas, shadow.filter(ImageFilter.GaussianBlur(size / 80)))
    canvas.alpha_composite(tile, (off, off))
    return canvas


def main(out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    smooth(256).save(out / "icon.png")
    smooth(256).save(out / "PropBook.ico", sizes=[(n, n) for n in (16, 24, 32, 48, 64, 128, 256)])
    mac_icon(1024).save(out / "AppIcon.icns")
    mac_icon(512).save(out / "mac-preview.png")
    for n in (16, 32, 48, 128):
        smooth(n).save(out / f"ext-{n}.png")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    main(Path(sys.argv[1]))
