# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""What the figures share: fonts, the two themes, and a small SVG
writer."""

FONT = "Inter, 'Helvetica Neue', Helvetica, Arial, sans-serif"
MONO = "'SF Mono', Menlo, Consolas, monospace"

# The benchmark figures' surfaces and inks (bench/report.py), and a
# hue for each way a tile can travel, from the same palette.
THEMES = {
    "light": dict(
        surface="#fcfcfb", text="#0b0b0b", text2="#52514e", muted="#898781",
        grid="#c3c2b7", screen="#f3f2ee", window="#ffffff", bar="#e1e0d9",
        ink="#b5b3aa", line="#2a78d6", photo=("#f6c37a", "#e8875a", "#8b5fbf"),
        copy="#6250d6", lossless="#2a78d6", lossy="#eb6834", cached="#1baf7a",
    ),
    "dark": dict(
        surface="#1a1a19", text="#f0efec", text2="#c3c2b7", muted="#898781",
        grid="#4a4a46", screen="#232322", window="#2c2c2a", bar="#383835",
        ink="#6b6a65", line="#3987e5", photo=("#b88a45", "#a85a3a", "#5e3f8a"),
        copy="#9085e9", lossless="#3987e5", lossy="#d95926", cached="#199e70",
    ),
}


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


class SVG:
    def __init__(self, w, h, t):
        self.w, self.h, self.t = w, h, t
        self.parts = []

    def add(self, s):
        self.parts.append(s)

    def rect(self, x, y, w, h, fill="none", stroke="none", sw=1, rx=0, dash=None, opacity=None):
        extra = f' stroke-dasharray="{dash}"' if dash else ""
        extra += f' fill-opacity="{opacity}"' if opacity is not None else ""
        self.add(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" '
                 f'stroke="{stroke}" stroke-width="{sw}"{extra}/>')

    def line(self, x1, y1, x2, y2, stroke, sw=1, dash=None):
        extra = f' stroke-dasharray="{dash}"' if dash else ""
        self.add(f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" stroke="{stroke}" stroke-width="{sw}"{extra}/>')

    def text(self, x, y, s, size=12, fill=None, weight=400, anchor="start", family=FONT):
        fill = fill or self.t["text"]
        self.add(f'<text x="{x}" y="{y}" font-family="{family}" font-size="{size}" font-weight="{weight}" '
                 f'fill="{fill}" text-anchor="{anchor}">{esc(s)}</text>')

    def save(self, path):
        body = "\n".join(self.parts)
        with open(path, "w", encoding="utf-8") as f:
            f.write(f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" '
                    f'viewBox="0 0 {self.w} {self.h}">\n'
                    f'<rect width="{self.w}" height="{self.h}" fill="{self.t["surface"]}"/>\n{body}\n</svg>\n')
