# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/architecture.md."""

from .draw import FONT, MONO, SVG

# The versions of a screen of 6 by 4 tiles, and those that viewer A was
# last sent. A's tiles that are older than the framebuffer's are dirty:
# a video at the right moved on, and a key A pressed drew at the bottom
# right.
SCREEN = [
    [12, 12, 12, 12, 12, 12],
    [12, 27, 27, 12, 38, 38],
    [12, 27, 27, 12, 38, 38],
    [12, 12, 12, 12, 12, 44],
]
SENT = [
    [12, 12, 12, 12, 12, 12],
    [12, 27, 27, 12, 30, 30],
    [12, 27, 27, 12, 30, 30],
    [12, 12, 12, 12, 12, 12],
]


def pipeline(t):
    """The path from the remote's screen to a viewer's: the framebuffer's
    versions against what the viewer was sent, the stages of the
    viewer's writer, the encode cache the viewers share, and the frame
    and its acknowledgement."""
    s = SVG(940, 566, t)
    ink, muted = t["text"], t["muted"]

    # An arrowhead for each ink, since not every renderer fills one with
    # its line's colour (context-stroke).
    heads = {ink: "arch-head-ink", muted: "arch-head-muted"}
    s.add("<defs>" + "".join(
        f'<marker id="{name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        f'orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="{c}"/></marker>'
        for c, name in heads.items()) + "</defs>")

    def arrow(points, color=ink, dash=None, sw=1.6):
        d = "M " + " L ".join(f"{x} {y}" for x, y in points)
        extra = f' stroke-dasharray="{dash}"' if dash else ""
        s.add(f'<path d="{d}" fill="none" stroke="{color}" stroke-width="{sw}"{extra} '
              f'marker-end="url(#{heads[color]})"/>')

    def box(x, y, w, h, title, sub=None, accent=None):
        s.rect(x, y, w, h, fill=t["window"], stroke=accent or t["grid"], sw=2 if accent else 1.2, rx=6)
        if sub:
            s.text(x + w / 2, y + h / 2 - 3, title, 13, weight=600, anchor="middle")
            s.text(x + w / 2, y + h / 2 + 14, sub, 11.5, t["text2"], anchor="middle")
        else:
            s.text(x + w / 2, y + h / 2 + 5, title, 13, weight=600, anchor="middle")

    # The remote and the backend, which draws into the framebuffer.
    box(24, 24, 120, 48, "Remote", "VNC or RDP server")
    box(24, 112, 120, 48, "Backend", "decodes updates")
    arrow([(84, 72), (84, 108)])
    s.text(92, 94, "updates", 11.5, t["text2"])

    T = 30                  # a tile
    GY = 40                 # the grids' top

    def grid(x0, versions, label, dirty_ink):
        s.text(x0, GY - 12, label, 12.5, weight=600)
        for r, row in enumerate(versions):
            for c, v in enumerate(row):
                x, y = x0 + c * T, GY + r * T
                dirty = SCREEN[r][c] > SENT[r][c]
                s.rect(x, y, T, T, fill=t["window"] if dirty else t["screen"], stroke=t["grid"], sw=0.8)
                s.text(x + T / 2, y + T / 2 + 4, str(v), 11, ink if dirty else muted,
                       weight=600 if dirty else 400, anchor="middle", family=MONO)
        for r in range(4):
            for c in range(6):
                if SCREEN[r][c] > SENT[r][c]:
                    s.rect(x0 + c * T + 1.5, GY + r * T + 1.5, T - 3, T - 3, stroke=dirty_ink, sw=2, rx=2)

    FX, AX = 196, 444       # the framebuffer's grid and viewer A's
    grid(FX, SCREEN, "The framebuffer's versions", ink)
    grid(AX, SENT, "What viewer A was sent", ink)
    arrow([(144, 136), (FX - 6, 136)])
    s.text(150, 128, "draws", 11.5, t["text2"])
    s.text((FX + 6 * T + AX) / 2, GY + 2 * T + 5, "vs", 13, t["text2"], weight=600, anchor="middle")

    # What the grids say.
    nx = AX + 6 * T + 36
    for i, line in enumerate(["Each change gives a tile the next", "version of one counter for the screen.",
                              "", "The outlined tiles are newer than", "what A has, so they are A's dirty tiles."]):
        if line:
            s.text(nx, GY + 10 + i * 17, line, 12, t["text2"])
    kx, ky = AX + 5 * T + T, GY + 3 * T + T / 2
    s.line(kx + 4, ky, nx - 6, ky, t["text2"], 1)
    s.text(nx, ky - 3, "A key that A pressed drew this one,", 12, t["text2"])
    s.text(nx, ky + 13, "so it goes first, in a frame of its own.", 12, t["text2"])

    # Viewer A's writer: its stages, in order. The arrow from the grids
    # comes down on the left, so the title starts beside it.
    CY = 214                # the container's top
    RW = 712                # the containers' width
    s.rect(16, CY, RW, 164, fill="none", stroke=t["grid"], sw=1.2, rx=10, dash="5 4")
    s.text(174, CY + 22, "Viewer A's writer", 13, weight=600)
    s.text(296, CY + 22, "one for each viewer, on a goroutine of its own", 12, t["text2"])
    mid = (FX + AX + 6 * T) / 2
    arrow([(mid, GY + 4 * T + 6), (mid, 194), (91, 194), (91, CY + 44)])

    SY, SW, SH, GAP = CY + 48, 118, 52, 24
    stages = [
        ("Dirty tiles", None, [("Newer than A has,", None)], [("an input's first", None)]),
        ("Scrolls", t["copy"], [("Rows A has, moved:", None)], [("copy-rect", t["copy"])]),
        ("Tile cache", t["cached"], [("Tiles A holds:", None)], [("tile, cached", t["cached"])]),
        ("Encoder", None, [("Exact", t["lossless"]), (", or ", None), ("lossy", t["lossy"])],
         [("in runs of tiles", None)]),
        ("Pacer", None, [("At A's rate, a few", None)], [("frames in flight", None)]),
    ]

    def spans(x, y, parts):
        # A line of text whose parts may each have a colour of their own:
        # a part in colour names a message or a kind of tile, in its hue.
        out = []
        for text, color in parts:
            if color:
                fam = MONO if text in ("copy-rect", "tile, cached") else FONT
                out.append(f'<tspan fill="{color}" font-weight="600" font-family="{fam}">{text}</tspan>')
            else:
                out.append(f"<tspan>{text}</tspan>")
        s.add(f'<text x="{x}" y="{y}" font-family="{FONT}" font-size="11.5" fill="{t["text2"]}" '
              f'text-anchor="middle" xml:space="preserve">{"".join(out)}</text>')

    xs = [32 + i * (SW + GAP) for i in range(len(stages))]
    for x, (name, accent, line1, line2) in zip(xs, stages):
        box(x, SY, SW, SH, name, accent=accent)
        spans(x + SW / 2, SY + SH + 20, line1)
        spans(x + SW / 2, SY + SH + 36, line2)
    for a, b in zip(xs, xs[1:]):
        arrow([(a + SW, SY + SH / 2), (b - 4, SY + SH / 2)])
    # The encoder's two kinds of tile: a bar of each colour along its foot.
    ex = xs[3]
    s.rect(ex + 8, SY + SH - 9, SW / 2 - 10, 4, fill=t["lossless"], rx=2)
    s.rect(ex + SW / 2 + 2, SY + SH - 9, SW / 2 - 10, 4, fill=t["lossy"], rx=2)

    # Client A, the frame it gets, and its acknowledgement.
    CX, CW = 816, 108
    s.rect(CX, SY - 14, CW, 80, fill=t["window"], stroke=t["grid"], sw=1.2, rx=6)
    s.text(CX + CW / 2, SY + 6, "Client A", 13, weight=600, anchor="middle")
    s.text(CX + CW / 2, SY + 26, "decodes, and", 11.5, t["text2"], anchor="middle")
    s.text(CX + CW / 2, SY + 42, "shows the frame", 11.5, t["text2"], anchor="middle")
    s.text(CX + CW / 2, SY + 58, "at frame-end", 11.5, t["text2"], anchor="middle")
    px = xs[4] + SW
    arrow([(px, SY + 16), (CX - 4, SY + 16)])
    s.text((px + CX) / 2, SY + 10, "frame", 11.5, weight=500, anchor="middle")
    arrow([(CX, SY + 40), (px + 4, SY + 40)], color=muted, dash="5 4")
    s.text((px + CX) / 2, SY + 58, "frame-ack", 11.5, muted, weight=500, anchor="middle", family=MONO)

    # The encode cache and workers, which the viewers' encoders share.
    EY = CY + 186
    ecx = xs[3] + SW / 2
    box(ecx - 120, EY, 240, 48, "Encode cache and workers", "each encoding made once, for every viewer")
    s.line(ecx, SY + SH + 44, ecx, EY, t["grid"], 1.4, dash="3 3")

    # Viewer B, whose writer runs the same stages against its own record.
    BY = EY + 72
    s.rect(16, BY, RW, 48, fill="none", stroke=t["grid"], sw=1.2, rx=10, dash="5 4")
    s.text(32, BY + 29, "Viewer B's writer", 13, weight=600)
    s.text(156, BY + 29, "the same stages, against what B was sent, at B's pace", 12, t["text2"])
    s.line(ecx, EY + 48, ecx, BY, t["grid"], 1.4, dash="3 3")
    box(CX, BY, CW, 48, "Client B")
    arrow([(16 + RW, BY + 18), (CX - 4, BY + 18)])
    arrow([(CX, BY + 34), (20 + RW, BY + 34)], color=muted, dash="5 4")

    s.text(16, 556, "The framebuffer is the session's. What each viewer was sent, its writer and its pace are "
                    "the viewer's own.", 12, t["text2"])
    return s


FIGURES = {"architecture-pipeline": pipeline}
