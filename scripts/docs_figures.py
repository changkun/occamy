# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""Draws the figures of the documents that are not measurements.

Each figure is an SVG in a light and a dark version, which a page shows
with <picture> as the benchmark figures are: GitHub picks one by the
system's colour scheme, and scripts/docs_site.py by the website's.
Mermaid draws the sequences and flowcharts; these draw what Mermaid
cannot, such as a message's bytes or a screen of tiles. Each page's
figures are a module of scripts/figures/, named after the page.

    python3 scripts/docs_figures.py
"""

import importlib
import os
import pkgutil
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "..", "docs", "figures")
sys.path.insert(0, HERE)

import figures  # noqa: E402
from figures.draw import THEMES  # noqa: E402


def main():
    os.makedirs(OUT, exist_ok=True)
    for info in pkgutil.iter_modules(figures.__path__):
        if info.name == "draw":
            continue
        module = importlib.import_module(f"figures.{info.name}")
        for name, draw in module.FIGURES.items():
            for theme, t in THEMES.items():
                path = os.path.join(OUT, name + ("" if theme == "light" else "-dark") + ".svg")
                draw(t).save(path)
                print(os.path.relpath(path))


if __name__ == "__main__":
    main()
