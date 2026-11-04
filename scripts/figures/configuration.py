# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/configuration.md.

Each part of the configuration file keeps one hue on every figure of
the page: the listener blue, sign-in violet, connections green and
secrets orange."""

import math

from .draw import MONO, SVG


def _hues(t):
    return dict(listen=t["lossless"], auth=t["copy"], conn=t["cached"], secret=t["lossy"])


def _arrow(s, x1, y1, x2, y2, color, sw=1.6, head=7):
    """A line from (x1, y1) to (x2, y2) with a filled head at the end."""
    a = math.atan2(y2 - y1, x2 - x1)
    bx, by = x2 - head * math.cos(a), y2 - head * math.sin(a)
    s.line(x1, y1, bx, by, color, sw)
    px, py = -math.sin(a) * head * 0.55, math.cos(a) * head * 0.55
    s.add(f'<path d="M {x2:.1f} {y2:.1f} L {bx + px:.1f} {by + py:.1f} L {bx - px:.1f} {by - py:.1f} Z" '
          f'fill="{color}"/>')


def _box(s, x, w, y, h, color, title, lines, mono=True, opacity=0.10):
    """A stage of the request: a tinted box with a title and lines. A
    line's leading spaces indent it, as in the file."""
    t = s.t
    s.rect(x, y, w, h, fill=color, opacity=opacity, rx=8)
    s.rect(x, y, w, h, stroke=color, sw=1.5, rx=8)
    s.text(x + 12, y + 24, title, 13, weight=650)
    for i, line in enumerate(lines):
        indent = (len(line) - len(line.lstrip(" "))) * 7
        if mono:
            s.text(x + 12 + indent, y + 48 + i * 16, line.lstrip(" "), 11.5, t["text2"], family=MONO)
        else:
            s.text(x + 12, y + 48 + i * 16, line, 12, t["text2"])


def overview(t):
    """The path of a request through the parts of the file, where a
    connection's secret comes from, and when occamyd reads each."""
    h = _hues(t)
    s = SVG(810, 500, t)

    # The request, from the user to the remote, through the parts of the
    # file that occamyd applies in turn.
    Y, H = 46, 122
    xs = [(22, 124), (168, 144), (334, 144), (500, 144), (666, 124)]
    _box(s, *xs[0], Y, H, t["muted"], "A user", ["web client, or", "Occamy for macOS"], mono=False, opacity=0.06)
    _box(s, *xs[1], Y, H, h["listen"], "Take the request", ["listen", "tls", "origins", "web_client"])
    _box(s, *xs[2], Y, H, h["auth"], "Sign the user in",
         ["auth:", "  users", "  oidc", "  trusted_issuers", "  signing_key_file"])
    _box(s, *xs[3], Y, H, h["conn"], "Open a connection",
         ["connections:", "  - target", "    allow", "    credentials"])
    x, w = xs[4]
    s.rect(x, Y, w, H, fill=t["muted"], opacity=0.06, rx=8)
    s.rect(x, Y, w, H, stroke=t["muted"], sw=1.5, rx=8)
    s.text(x + 12, Y + 24, "The remote", 13, weight=650)
    for i, line in enumerate(["SSH, VNC or RDP", "server, or a", "Kubernetes pod"]):
        s.text(x + 12, Y + 48 + i * 16, line, 12, t["text2"])
    s.text(x + 12, Y + 98, "checked with", 11.5, t["muted"])
    s.text(x + 12, Y + 113, "host_keys, tls", 11.5, t["muted"], family=MONO)
    for (x1, w1), (x2, _) in zip(xs, xs[1:]):
        _arrow(s, x1 + w1 + 3, Y + H / 2, x2 - 3, Y + H / 2, t["text2"])

    # occamyd is the three boxes in the middle: a bracket over them.
    bx1, bx2 = xs[1][0], xs[3][0] + xs[3][1]
    s.line(bx1, 32, bx2, 32, t["muted"], 1)
    s.line(bx1, 32, bx1, 40, t["muted"], 1)
    s.line(bx2, 32, bx2, 40, t["muted"], 1)
    s.text((bx1 + bx2) / 2, 24, "occamyd, as occamy.yaml configures it", 12, t["text2"], anchor="middle")

    # Where a connection's secret comes from: exactly one source.
    cx = xs[3][0] + xs[3][1] / 2
    bus, top = Y + H + 26, Y + H + 48
    cards = [
        ("A file on disk", ["password_file", "private_key_file, token_file"], "read as each session starts"),
        ("An environment variable", ["password_env, token_env"], "fixed when occamyd starts"),
        ("The session's owner", ["prompt: true"], "asked as each session starts, never kept"),
    ]
    CW, CH, gap = 236, 92, 18
    x0 = (810 - 3 * CW - 2 * gap) / 2
    centres = [x0 + i * (CW + gap) + CW / 2 for i in range(3)]
    s.line(centres[0], bus, centres[-1], bus, h["secret"], 1.6)
    for c in centres:
        s.line(c, bus, c, top, h["secret"], 1.6)
    _arrow(s, cx, bus, cx, Y + H + 2, h["secret"])
    s.text(centres[0] + 8, bus - 7, "credentials names exactly one source", 11.5, h["secret"], weight=600)
    for i, (title, lines, note) in enumerate(cards):
        x = x0 + i * (CW + gap)
        s.rect(x, top, CW, CH, fill=h["secret"], opacity=0.09, rx=8)
        s.rect(x, top, CW, CH, stroke=h["secret"], sw=1.5, rx=8)
        s.text(x + 12, top + 22, title, 12.5, weight=650)
        for j, line in enumerate(lines):
            s.text(x + 12, top + 42 + j * 15, line, 11.5, t["text2"], family=MONO)
        s.text(x + 12, top + CH - 12, note, 11.5, t["muted"])

    # When occamyd reads what: the file once at start, its certificate
    # and the issuers' keys at start and as they change, a session's
    # secret as it starts, and a changed file only at a restart.
    s.text(22, 348, "When occamyd reads what", 12.5, weight=650)
    axis, start, restart = 430, 60, 700
    _arrow(s, 30, axis, 790, axis, t["muted"], sw=1.2)
    s.text(790, axis - 8, "time", 11, t["muted"], anchor="end")
    for x in (start, restart):
        s.line(x, 360, x, axis, t["text"], 2)
        s.add(f'<circle cx="{x}" cy="{axis}" r="4.5" fill="{t["text"]}"/>')
    sessions = [(250, restart, 374), (330, 560, 390), (420, restart, 406)]
    for x1, x2, y in sessions:
        s.rect(x1, y, x2 - x1, 9, fill=h["conn"], opacity=0.35, rx=4)
        s.rect(x1, y, x2 - x1, 9, stroke=h["conn"], sw=1.2, rx=4)
        s.add(f'<circle cx="{x1}" cy="{y + 4.5}" r="5" fill="{h["secret"]}"/>')
    # An edit to the file changes nothing until the restart.
    ex = 620
    s.line(ex, 366, ex, axis, t["muted"], 1, dash="3 3")
    s.add(f'<path d="M {ex} {axis - 6} L {ex + 6} {axis} L {ex} {axis + 6} L {ex - 6} {axis} Z" '
          f'fill="{t["surface"]}" stroke="{t["text2"]}" stroke-width="1.5"/>')
    s.text(ex - 8, 364, "You edit occamy.yaml: nothing changes yet", 11.5, t["text2"], anchor="end")

    def label(x, lines, anchor):
        for i, line in enumerate(lines):
            s.text(x, axis + 22 + i * 15, line, 11.5 if i else 12, t["text2"] if i else t["text"],
                   weight=400 if i else 650, anchor=anchor)

    label(start - 6, ["occamyd starts", "It reads occamy.yaml and the signing key,", "and tls and issuers' keys as they change."],
          "start")
    label(440, ["Sessions", "Each reads its secret (orange)", "as it starts."], "middle")
    label(restart + 88, ["Restart", "occamyd reads the edited", "file; every session ends."], "end")
    return s


FIGURES = {
    "configuration-overview": overview,
}
