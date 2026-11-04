# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/deploy.md, and the boxes and arrows that the
topologies of docs/try-it.md and docs/debugging.md share with it."""

from .draw import FONT, MONO, SVG, esc


def inks(t):
    """The hue of each part of a deployment, the same on every figure
    of the three pages: the user's side, occamyd, the remotes, what
    must stay private, and what only carries traffic."""
    return dict(client=t["lossless"], occamy=t["copy"], remote=t["cached"],
                private=t["lossy"], infra=t["muted"])


def heads(s, colors):
    """Adds an arrowhead marker for each colour, named head-<key>, since
    not every renderer fills a marker with its line's colour."""
    s.add("<defs>" + "".join(
        f'<marker id="head-{key}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" '
        f'orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="{ink}"/></marker>'
        for key, ink in colors.items()) + "</defs>")


def arrow(s, x1, y1, x2, y2, ink, key, sw=1.6, dash=None, both=False):
    """A line from (x1, y1) to (x2, y2) with a head of marker key."""
    extra = f' stroke-dasharray="{dash}"' if dash else ""
    start = f' marker-start="url(#head-{key})"' if both else ""
    s.add(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{ink}" stroke-width="{sw}"{extra}{start} '
          f'marker-end="url(#head-{key})"/>')


def box(s, x, y, w, h, ink, title, lines=(), mono_title=False, dash=None, opacity=0.13, size=13):
    """A part of the topology: a tinted box with a title and lines of
    detail under it. A line that starts with "`" is set in monospace."""
    s.rect(x, y, w, h, fill=ink, stroke=ink, sw=1.5, rx=8, dash=dash, opacity=opacity)
    ty = y + 22
    s.text(x + 12, ty, title, size, weight=600, family=MONO if mono_title else FONT)
    for i, line in enumerate(lines):
        mono = line.startswith("`")
        s.text(x + 12, ty + 19 + i * 17, line.strip("`"), 11 if mono else 11.5, s.t["text2"],
               family=MONO if mono else FONT)


def label(s, x, y, text, mono=True, anchor="middle", size=11, fill=None):
    """A label on an arrow, with a halo of the surface behind it so
    that it stays legible over lines."""
    fam = MONO if mono else FONT
    s.add(f'<text x="{x}" y="{y}" font-family="{fam}" font-size="{size}" font-weight="500" '
          f'fill="{fill or s.t["text"]}" text-anchor="{anchor}" paint-order="stroke" '
          f'stroke="{s.t["surface"]}" stroke-width="4">{esc(text)}</text>')


def chip(s, x, y, w, ink, name, note="", dash=None, opacity=0.07):
    """Something a part keeps beside it, such as a file: its name in
    monospace and a note after it."""
    s.rect(x, y, w, 26, fill=ink, stroke=ink, rx=5, dash=dash, opacity=opacity)
    s.text(x + 10, y + 17, name, 11, s.t["text"], family=MONO)
    if note:
        s.text(x + w - 9, y + 17, note, 10.5, s.t["text2"], anchor="end")


def topology(t):
    """A deployment: browsers and Occamy for macOS reach a TLS proxy,
    which alone reaches occamyd on the loopback interface; occamyd
    reaches each remote itself, and keeps its configuration, its
    signing key and an optional debug listener beside it."""
    c = inks(t)
    s = SVG(760, 424, t)
    heads(s, c)

    # The users' side.
    box(s, 16, 118, 134, 62, c["client"], "Browser", ["the web client"])
    box(s, 16, 198, 134, 62, c["client"], "Occamy", ["for macOS"])

    # The server: the proxy and occamyd, on one host.
    s.rect(204, 80, 408, 300, stroke=t["grid"], sw=1.2, rx=12, dash="5 4")
    s.text(220, 102, "Your server", 12.5, t["text2"], weight=600)
    box(s, 222, 148, 122, 82, c["infra"], "TLS proxy", ["Caddy or nginx", "ends TLS"])
    box(s, 432, 128, 162, 122, c["occamy"], "occamyd", ["`listen:", "`127.0.0.1:5636", "sessions live", "in its memory"])
    # What occamyd keeps beside it.
    s.line(513, 250, 513, 266, c["occamy"], 1.2)
    chip(s, 432, 266, 162, c["occamy"], "occamy.yaml", "read at start")
    chip(s, 432, 298, 162, c["occamy"], "signing.key", "on a volume")
    chip(s, 432, 330, 162, c["private"], "localhost:6060", "debug", dash="4 3")

    # Users reach the proxy over HTTPS; the proxy reaches occamyd.
    arrow(s, 150, 149, 220, 180, c["client"], "client")
    arrow(s, 150, 229, 220, 198, c["client"], "client")
    label(s, 182, 186, "HTTPS")
    label(s, 182, 202, ":443")
    arrow(s, 344, 189, 430, 189, c["infra"], "infra")
    label(s, 387, 180, "HTTP/1.1")
    label(s, 387, 207, "Upgrade")

    # An identity provider, for OIDC sign-in only.
    box(s, 432, 14, 162, 50, c["infra"], "Identity provider", ["OIDC, optional"], dash="4 3", opacity=0.05, size=12)
    arrow(s, 513, 128, 513, 66, c["infra"], "infra", dash="4 3")

    # The remotes, each reached by occamyd.
    remotes = [("VNC server", "`:5900"), ("RDP host", "`:3389, TLS"), ("SSH server", "`:22"),
               ("Kubernetes API", "`:443 or :6443")]
    for i, (name, port) in enumerate(remotes):
        y = 96 + i * 64
        box(s, 636, y, 118, 50, c["remote"], name, [port], size=12)
        arrow(s, 594, 189, 634, y + 25, c["remote"], "remote")
    s.text(695, 86, "Remotes", 12.5, t["text2"], weight=600, anchor="middle")

    s.text(16, 408, "Users reach occamyd only through the proxy. occamyd connects to each remote itself, "
                    "with the credentials its configuration names.", 12, t["text2"])
    return s


FIGURES = {"deploy-topology": topology}
