# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/benchmarks/codecs.md."""

from .draw import SVG

# The measurement's numbers that the figure labels (docs/benchmarks/codecs.md,
# Tiles of 64 pixels): a key, and a line scrolled into view after a
# copy-rect, at each tile size.
CHANGES = {
    64: ("A key: 1 tile, 0.1 to 1.7 KiB", "A scrolled line: 15 tiles, 4.6 KiB"),
    128: ("A key: 1 tile, 0.2 to 2.9 KiB", "A scrolled line: 8 tiles, 8.7 KiB"),
}


def tile_size(t):
    """The bottom of a 1024-pixel screen of scrolling text, at 64- and
    128-pixel tiles: the tiles that a key and a scrolled line send at each
    size, and the copy-rect that moves the rest of the text."""
    K = 0.6                       # drawn pixels per screen pixel
    SW, SH = 1024, 256            # the part of the screen shown: its bottom four rows of 64
    W, H = SW * K, SH * K
    X0 = 24
    PANEL = H + 76                # a panel: its title, the screen, its labels
    s = SVG(int(X0 * 2 + W), int(16 + 2 * PANEL + 24), t)

    def badge(x, y, n, color):
        s.add(f'<circle cx="{x}" cy="{y}" r="9" fill="{color}"/>')
        s.text(x, y + 4, str(n), 11, "#ffffff", weight=700, anchor="middle")

    # A screen pixel (x, y) of the part shown, where y counts from its top.
    def at(x, y, y0):
        return X0 + x * K, y0 + y * K

    # The key is a glyph typed in the text, and the scrolled line the
    # screen's last 13 rows, below the copy's bottom at 755 of 768, so from
    # 243 of the 256 shown.
    key = (300, 92)
    line_top = 243

    for i, size in enumerate((64, 128)):
        y0 = 16 + i * PANEL + 26
        s.text(X0, y0 - 10, f"Tiles of {size} pixels", 13, t["text"], weight=600)

        # The screen: a page of text, with the line that scrolled into view.
        s.rect(X0, y0, W, H, fill=t["window"], stroke=t["bar"])
        for r, f in enumerate([0.62, 0.8, 0.45, 0.74, 0.58, 0.9, 0.36, 0.7, 0.66, 0.52, 0.84, 0.4, 0.76, 0.6]):
            y = 10 + r * 16
            if y + 6 > line_top:
                break
            x, yy = at(96, y, y0)
            s.rect(x, yy, (SW - 160) * K * f, 4, fill=t["ink"], rx=2)
        x, yy = at(96, line_top + 3, y0)
        s.rect(x, yy, (SW - 160) * K * 0.7, 4, fill=t["line"], rx=2)
        kx, ky = at(*key, y0)
        s.rect(kx, ky - 2, 7, 9, fill=t["line"], rx=1)

        # The tile grid.
        for c in range(SW // size + 1):
            x = X0 + c * size * K
            s.line(x, y0, x, y0 + H, t["grid"], 0.6)
        for r in range(SH // size + 1):
            y = y0 + r * size * K
            s.line(X0, y, X0 + W, y, t["grid"], 0.6)

        # The copy-rect: the text above the new line moves up a line, from
        # x 64 on, the same at either size.
        cx, cy = at(64, 0, y0)
        s.rect(cx + 2, cy + 2, (SW - 64) * K - 4, line_top * K - 4, stroke=t["copy"], sw=1.6, dash="6 4", rx=3)
        ax = X0 + W - 18
        s.line(ax, cy + line_top * K - 12, ax, cy + 14, t["copy"], 1.6)
        s.add(f'<path d="M {ax - 5} {cy + 20} L {ax} {cy + 10} L {ax + 5} {cy + 20} Z" fill="{t["copy"]}"/>')

        # The tiles each change sends: the key's one, and the bottom row,
        # from the copy's left edge.
        def sent(c, r):
            x, y = X0 + c * size * K, y0 + r * size * K
            s.rect(x + 1.5, y + 1.5, size * K - 3, size * K - 3, fill=t["lossless"], stroke=t["lossless"],
                   sw=2, opacity=0.22, rx=2)

        sent(key[0] // size, key[1] // size)
        bottom = (SH - 1) // size
        for c in range(64 // size, SW // size):
            sent(c, bottom)
        badge(X0 + (key[0] // size) * size * K + 12, y0 + (key[1] // size) * size * K + 12, 1, t["lossless"])
        badge(X0 + (64 // size) * size * K + (size * K) / 2, y0 + bottom * size * K + 12, 2, t["lossless"])

        # What each change costs at this size.
        ly = y0 + H + 26
        a, b = CHANGES[size]
        badge(X0 + 9, ly - 4, 1, t["lossless"])
        s.text(X0 + 24, ly, a, 12, t["text2"])
        badge(X0 + 290, ly - 4, 2, t["lossless"])
        s.text(X0 + 305, ly, b, 12, t["text2"])

    # The copy-rect, the same at either size.
    ly = 16 + 2 * PANEL + 8
    s.rect(X0 + 1, ly - 11, 18, 12, stroke=t["copy"], sw=1.6, dash="4 3", rx=2)
    s.text(X0 + 26, ly, "copy-rect: the text above the new line moves up a line, the same at either size",
           12, t["text2"])
    return s


FIGURES = {"codecs-tile-size": tile_size}
