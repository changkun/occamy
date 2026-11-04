# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/protocol.md."""

import math

from .draw import FONT, MONO, SVG, esc


def frame(t):
    """A frame of a desktop: the screen in tiles, the tiles that changed
    by how each travels, and the frame's messages in the order sent."""
    T = 52                    # a tile, standing for 64 pixels
    COLS, ROWS = 8, 5
    X0, Y0 = 24, 24           # the screen's top left
    W, H = COLS * T, ROWS * T
    s = SVG(760, 444, t)

    def at(c, r):
        return X0 + c * T, Y0 + r * T

    # The screen: a text window on the left, a video and a dock on the right.
    s.rect(X0, Y0, W, H, fill=t["screen"])
    wx, wy = at(0, 0)
    s.rect(wx + 6, wy + 6, 5 * T - 12, H - 12, fill=t["window"], stroke=t["bar"], rx=4)
    s.rect(wx + 6, wy + 6, 5 * T - 12, 14, fill=t["bar"], rx=4)
    widths = [0.55, 0.8, 0.4, 0.7, 0.62, 0.3, 0.75, 0.5, 0.66, 0.45, 0.58, 0.72, 0.38, 0.6]
    for i, f in enumerate(widths):
        y = wy + 28 + i * 16
        if y > wy + H - 24:
            break
        s.rect(wx + 16, y, (5 * T - 40) * f, 6, fill=t["ink"], rx=2)
    # The line typed last, at the bottom of the window.
    s.rect(wx + 16, wy + 4 * T + 22, 120, 6, fill=t["line"], rx=2)

    vx, vy = at(5, 1)
    a, b, c = t["photo"]
    s.add(f'<defs><linearGradient id="video" x1="0" y1="0" x2="1" y2="1">'
          f'<stop offset="0" stop-color="{a}"/><stop offset="0.55" stop-color="{b}"/>'
          f'<stop offset="1" stop-color="{c}"/></linearGradient></defs>')
    s.rect(vx + 4, vy + 4, 3 * T - 8, 2 * T - 8, fill="url(#video)", rx=3)
    for i in range(3):
        dx, dy = at(5 + i, 4)
        s.rect(dx + 14, dy + 14, T - 28, T - 28, fill=t["bar"], stroke=t["ink"], rx=6)

    # The tile grid over it.
    for cidx in range(COLS + 1):
        s.line(X0 + cidx * T, Y0, X0 + cidx * T, Y0 + H, t["grid"], 0.6)
    for ridx in range(ROWS + 1):
        s.line(X0, Y0 + ridx * T, X0 + W, Y0 + ridx * T, t["grid"], 0.6)

    # What changed, and how it travels.
    cx, cy = at(0, 0)
    s.rect(cx + 2, cy + 24, 5 * T - 4, 4 * T - 26, stroke=t["copy"], sw=2, dash="6 4", rx=3)
    ax = cx + 5 * T - 22
    s.line(ax, cy + 4 * T - 14, ax, cy + 40, t["copy"], 2)
    s.add(f'<path d="M {ax - 6} {cy + 48} L {ax} {cy + 36} L {ax + 6} {cy + 48} Z" fill="{t["copy"]}"/>')
    for i in range(3):
        x, y = at(i, 4)
        s.rect(x + 1.5, y + 1.5, T - 3, T - 3, fill=t["lossless"], stroke=t["lossless"], sw=2, opacity=0.22, rx=2)
    for r in (1, 2):
        x, y = at(5, r)
        s.rect(x + 1.5, y + 1.5, 3 * T - 3, T - 3, fill=t["lossy"], stroke=t["lossy"], sw=2, opacity=0.25, rx=2)
    x, y = at(6, 4)
    s.rect(x + 1.5, y + 1.5, T - 3, T - 3, fill=t["cached"], stroke=t["cached"], sw=2, opacity=0.25, rx=2)

    # A numbered marker on each change, and its label to the right.
    def badge(x, y, n, color):
        s.add(f'<circle cx="{x}" cy="{y}" r="9" fill="{color}"/>')
        s.text(x, y + 4, str(n), 11, "#ffffff", weight=700, anchor="middle")

    badge(X0 + 16, Y0 + 38, 1, t["copy"])
    badge(X0 + 14, Y0 + 4 * T + 14, 2, t["lossless"])
    badge(X0 + 5 * T + 14, Y0 + T + 14, 3, t["lossy"])
    badge(X0 + 6 * T + 14, Y0 + 4 * T + 14, 4, t["cached"])

    LX = X0 + W + 40
    labels = [
        (t["copy"], "copy-rect", "The window scrolled up a line, so", "the client moves pixels it has."),
        (t["lossless"], "tile, deflate", "The new line of text, exact:", "one message a tile."),
        (t["lossy"], "tile, webp", "A video in motion, lossy: two", "messages of 3 tiles side by side."),
        (t["cached"], "tile, cached", "An icon the client holds in", "slot 5. No pixels are sent."),
    ]
    for i, (color, name, l1, l2) in enumerate(labels):
        y = Y0 + 12 + i * 66
        badge(LX + 9, y, i + 1, color)
        s.text(LX + 26, y + 4, name, 12.5, weight=600, family=MONO)
        s.text(LX + 26, y + 22, l1, 12, t["text2"])
        s.text(LX + 26, y + 38, l2, 12, t["text2"])
    s.text(X0, Y0 + H + 22, "Each square is a tile of 64 pixels. Tiles without colour did not change and are not sent.",
           12, t["text2"])

    # The frame's messages, in the order occamyd sends them.
    my = Y0 + H + 52
    s.text(X0, my, "The frame's messages, in the order they are sent", 12.5, weight=600)
    chips = [("copy-rect", "", t["copy"]), ("tile", "deflate", t["lossless"]), ("tile", "deflate", t["lossless"]),
             ("tile", "deflate", t["lossless"]), ("tile", "webp", t["lossy"]), ("tile", "webp", t["lossy"]),
             ("tile", "cached", t["cached"]), ("frame-end", "", t["muted"])]
    x = X0
    for name, codec, color in chips:
        w = 92 if not codec else 72
        s.rect(x, my + 12, w, 40, fill=color, stroke=color, sw=1.5, opacity=0.18, rx=5)
        if codec:
            s.text(x + w / 2, my + 29, name, 12, weight=500, anchor="middle", family=MONO)
            s.text(x + w / 2, my + 45, codec, 11, t["text2"], anchor="middle", family=MONO)
        else:
            s.text(x + w / 2, my + 36, name, 12, weight=500, anchor="middle", family=MONO)
        x += w + 8
    s.text(X0, my + 74, "The client applies them in order and shows the result at frame-end, then sends frame-ack.",
           12, t["text2"])
    return s


def layout(t):
    """A message byte by byte: the text message that sends 日本 as the
    client's third input, with its header, a fixed-size field and a
    variable-size one."""
    B = 40                    # a byte
    X0, Y0 = 24, 92           # the first byte's top left
    data = "03 01 00 00 03 00 00 00 06 00 00 00 e6 97 a5 e6 9c ac".split()
    fields = [  # first byte, bytes, name, type, value, kind
        (0, 2, "type", "u16", "0x0103: text", "header"),
        (2, 2, "flags", "u16", "0", "header"),
        (4, 4, "seq", "u32", "3", "fixed"),
        (8, 4, "length", "u32", "6 bytes follow", "variable"),
        (12, 6, "text", "UTF-8", "日本", "variable"),
    ]
    color = {"header": t["copy"], "fixed": t["lossless"], "variable": t["cached"]}
    s = SVG(X0 * 2 + 18 * B, 262, t)

    # The two parts of every message, above the fields.
    def brace(x1, x2, y, label, ink):
        s.add(f'<path d="M {x1 + 2} {y + 8} L {x1 + 2} {y} L {x2 - 2} {y} L {x2 - 2} {y + 8}" '
              f'fill="none" stroke="{ink}" stroke-width="1.5"/>')
        s.text((x1 + x2) / 2, y - 7, label, 12.5, weight=600, anchor="middle")

    brace(X0, X0 + 4 * B, Y0 - 62, "Header, 4 bytes", t["text2"])
    brace(X0 + 4 * B, X0 + 18 * B, Y0 - 62, "Body: the fields of text, in order", t["text2"])

    for first, n, name, typ, value, kind in fields:
        x1, x2 = X0 + first * B, X0 + (first + n) * B
        ink = color[kind]
        s.text((x1 + x2) / 2, Y0 - 24, name, 12.5, weight=600, anchor="middle", family=MONO)
        s.text((x1 + x2) / 2, Y0 - 9, typ, 11, t["text2"], anchor="middle")
        for i in range(n):
            x = x1 + i * B
            s.rect(x + 1, Y0, B - 2, 34, fill=ink, stroke=ink, sw=1.2, opacity=0.16, rx=3)
            s.text(x + B / 2, Y0 + 22, data[first + i], 13, weight=500, anchor="middle", family=MONO)
        s.text(x1 + 3, Y0 + 50, str(first), 10.5, t["muted"])
        s.text((x1 + x2) / 2, Y0 + 72, value, 12.5, anchor="middle")
    s.text(X0 + 18 * B - 3, Y0 + 50, "17", 10.5, t["muted"], anchor="end")

    # A key to the colours, and how to read the numbers.
    ky = Y0 + 120
    for i, (kind, label) in enumerate([("header", "The header"), ("fixed", "A fixed-size field"),
                                       ("variable", "A variable-size field: its length, then its bytes")]):
        x = X0 + [0, 140, 310][i]
        s.rect(x, ky - 10, 12, 12, fill=color[kind], stroke=color[kind], opacity=0.3, rx=2)
        s.text(x + 18, ky, label, 12, t["text2"])
    s.text(X0, ky + 28, "Integers are little-endian, so 03 01 is 0x0103. The small numbers are byte offsets.",
           12, t["text2"])
    return s


def window(t):
    """The frame window over a long path: a window of 2 frames, the
    frames and acknowledgements in flight, and an input's frame that
    goes one beyond the full window."""
    SX, CX = 190, 560         # the lanes of occamyd and the client
    TOP, D = 64, 92           # where time starts, and the one-way delay
    s = SVG(780, 560, t)

    for x, name in ((SX, "occamyd"), (CX, "Client")):
        s.rect(x - 56, 14, 112, 30, fill=t["window"], stroke=t["grid"], rx=6)
        s.text(x, 34, name, 13, weight=600, anchor="middle")
        s.line(x, 44, x, 530, t["grid"], 1.5)
    # An arrowhead for each ink, since not every renderer fills one
    # with its line's colour (context-stroke).
    frame_ink, ack_ink, key_ink = t["lossless"], t["muted"], t["lossy"]
    heads = {frame_ink: "head-frame", ack_ink: "head-ack", key_ink: "head-key"}
    s.add("<defs>" + "".join(
        f'<marker id="{name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        f'orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="{ink}"/></marker>'
        for ink, name in heads.items()) + "</defs>")
    s.add(f'<path d="M 40 70 L 40 510" stroke="{t["muted"]}" stroke-width="1" marker-end="url(#head-ack)"/>')
    s.text(52, 506, "time", 11, t["muted"])

    def msg(x1, y1, x2, label, ink, sw=1.8, dash=None, side=-1, at=0.5):
        y2 = y1 + D
        extra = f' stroke-dasharray="{dash}"' if dash else ""
        s.add(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{ink}" stroke-width="{sw}"{extra} '
              f'marker-end="url(#{heads[ink]})"/>')
        # The label sits along the line, a little above it.
        ang = math.degrees(math.atan2(y2 - y1, x2 - x1))
        if ang > 90:
            ang -= 180
        if ang < -90:
            ang += 180
        mx, my = x1 + (x2 - x1) * at, y1 + (y2 - y1) * at
        s.add(f'<text x="{mx}" y="{my}" transform="rotate({ang:.1f} {mx} {my}) translate(0 {-6 if side < 0 else 15})" '
              f'font-family="{FONT}" font-size="12" font-weight="500" fill="{t["text"]}" text-anchor="middle" '
              f'paint-order="stroke" stroke="{t["surface"]}" stroke-width="4">{esc(label)}</text>')
        return y2

    # Frames 1 and 2 fill the window; changes wait until frame 1's ack.
    msg(SX, TOP + 20, CX, "frame 1", frame_ink, at=0.42)
    msg(SX, TOP + 50, CX, "frame 2", frame_ink, at=0.42)
    ack1 = TOP + 20 + D + 14
    msg(CX, ack1, SX, "frame-ack 1", ack_ink, dash="5 4", at=0.62)
    ack2 = TOP + 50 + D + 14
    msg(CX, ack2, SX, "frame-ack 2", ack_ink, dash="5 4", at=0.78)
    f3 = ack1 + D
    msg(SX, f3, CX, "frame 3", frame_ink, at=0.62)
    f4 = ack2 + D
    msg(SX, f4, CX, "frame 4", frame_ink, at=0.7)
    # Each ack frees a place that the changes waiting fill at once, so
    # the window stays full from frame 2 on.
    s.rect(SX - 9, TOP + 50, 18, f4 + 160 - (TOP + 50), fill=t["lossless"], opacity=0.14, rx=4)
    key = ack2 + 26
    arrive = msg(CX, key, SX, "key, seq 42", key_ink, sw=2.2, at=0.22)
    f5 = arrive + 8
    shown = msg(SX, f5, CX, "frame 5, last_seq 42", key_ink, sw=2.2, side=1, at=0.5)

    # What each side sees.
    def note(x, y, lines, anchor="end"):
        for i, line in enumerate(lines):
            s.text(x, y + i * 16, line, 12, t["text2"], anchor=anchor)

    note(SX - 18, TOP + 66, ["Window full:", "changes wait"])
    note(SX - 18, f3 + 4, ["They go as one", "frame on the ack"])
    note(SX - 18, f5 - 2, ["The key's frame goes", "at once, one beyond", "the full window"])
    s.add(f'<path d="M {CX + 14} {key} L {CX + 22} {key} L {CX + 22} {shown + 4} L {CX + 14} {shown + 4}" '
          f'fill="none" stroke="{key_ink}" stroke-width="1.5"/>')
    note(CX + 30, (key + shown) / 2 - 6, ["The key's latency, which", "last_seq lets the client", "measure exactly"], "start")
    s.text(24, 548, "A window of 2 frames on a path whose one-way delay is longer than the time between frames. "
                    "Shaded: the window is full.", 12, t["text2"])
    return s


FIGURES = {"protocol-layout": layout, "protocol-frame": frame, "protocol-window": window}
