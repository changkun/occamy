# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""MkDocs hooks for the website (mkdocs.yml).

The site's pages are docs/, and its home page is the repository's
README.md. The README is written for GitHub, from the repository's
root, and docs/README.md is the manual's index, which MkDocs would take
as the home page. So the hooks
- make index.md from README.md, and manual.md from docs/README.md;
- point each link that resolves inside docs/ at that page or file of
  the site, and each one that leaves docs/, such as specs/ or LICENSE,
  at the repository on GitHub.

MkDocs resolves a Markdown link from the page's file, but leaves raw
HTML alone, and the browser resolves it from the page's URL, which is a
directory: docs/benchmarks.md is served at benchmarks/. So an HTML src,
srcset or href is written relative to the page's URL instead.
"""

import os
import posixpath
import re

from mkdocs.structure.files import File

REPO = "https://github.com/changkun/occamy"
BRANCH = "master"

# A Markdown link or image target, or an HTML src, srcset or href.
LINK = re.compile(r'(\]\()([^)\s]+)(\))|((?:src|srcset|href)=")([^"]+)(")')


def _root(config):
    return os.path.dirname(os.path.abspath(config["config_file_path"]))


def on_files(files, config):
    root = _root(config)
    manual = files.get_file_from_path("README.md")
    if manual is not None:
        files.remove(manual)
        with open(manual.abs_src_path, encoding="utf-8") as f:
            files.append(File.generated(config, "manual.md", content=f.read()))
    with open(os.path.join(root, "README.md"), encoding="utf-8") as f:
        home = f.read()
    # Markdown inside the README's centred header, which GitHub renders.
    home = home.replace('<div align="center">', '<div align="center" markdown>', 1)
    files.append(File.generated(config, "index.md", content=home))
    return files


def on_page_markdown(markdown, page, config, files):
    root = _root(config)
    docs = os.path.relpath(config["docs_dir"], root).replace(os.sep, "/")
    src = page.file.src_uri
    # Where the page's links are written from, relative to the root.
    if src == "index.md":
        base = ""
    elif src == "manual.md":
        base = docs
    else:
        base = posixpath.join(docs, posixpath.dirname(src))

    # The directory of the page's URL, relative to the site's root.
    url_dir = page.url if page.url.endswith("/") else posixpath.dirname(page.url)

    def rewrite(target, html=False):
        if re.match(r"^[a-z][a-z0-9+.-]*:", target, re.I) or target.startswith(("#", "/")):
            return target
        path, sep, anchor = target.partition("#")
        if path == "":
            return target
        resolved = posixpath.normpath(posixpath.join(base, path))
        if resolved == docs or resolved.startswith(docs + "/"):
            inside = posixpath.relpath(resolved, docs)
            if inside == "README.md":
                inside = "manual.md"
            if not html:
                here = posixpath.dirname(src)
                return posixpath.relpath(inside, here or ".") + sep + anchor
            if inside.endswith(".md"):
                inside = "" if inside == "index.md" else inside[: -len(".md")] + "/"
            rel = posixpath.relpath(inside or ".", url_dir or ".")
            if inside.endswith("/") and rel != ".":
                rel += "/"
            return ("./" if rel == "." else rel) + sep + anchor
        kind = "tree" if os.path.isdir(os.path.join(root, resolved)) else "blob"
        return f"{REPO}/{kind}/{BRANCH}/{resolved}" + sep + anchor

    def replace(m):
        if m.group(1):
            return m.group(1) + rewrite(m.group(2)) + m.group(3)
        attr, value = m.group(4), m.group(5)
        if attr == 'srcset="':
            # Each candidate is a URL and an optional descriptor.
            value = ", ".join(
                " ".join([rewrite(parts[0], html=True)] + parts[1:])
                for parts in (c.split() for c in value.split(","))
                if parts
            )
        else:
            value = rewrite(value, html=True)
        return attr + value + m.group(6)

    return PICTURE.sub(_light_dark, LINK.sub(replace, markdown))


# A figure in a light and a dark version, as GitHub shows it: the
# system's colour scheme picks one.
PICTURE = re.compile(
    r'<picture>\s*<source media="\(prefers-color-scheme: dark\)" srcset="([^"]+)">\s*'
    r'<img alt="([^"]*)" src="([^"]+)">\s*</picture>'
)


def _light_dark(m):
    # The site has a toggle of its own, so show the version of its
    # scheme instead, with Material's #only-light and #only-dark.
    dark, alt, light = m.group(1), m.group(2), m.group(3)
    return (
        f'<img alt="{alt}" src="{light}#only-light">'
        f'<img alt="{alt}" src="{dark}#only-dark">'
    )
