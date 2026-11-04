# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/try-it.md."""

from .deploy import arrow, box, chip, heads, inks, label
from .draw import SVG


def stack(t):
    """The demo stack of docker/docker-compose.yaml: the browser reaches
    occamy-v1 alone, on the loopback interface; occamy-v1 reaches the
    desktop and the SSH server by name on the stack's network; the debug
    port opens only with OCCAMY_DEBUG=1; and three servers are there for
    the end-to-end tests only."""
    c = inks(t)
    s = SVG(760, 452, t)
    heads(s, c)

    # The stack, with the browser and the shell outside it.
    s.rect(206, 40, 538, 360, stroke=t["grid"], sw=1.2, rx=12, dash="5 4")
    s.text(222, 64, "The demo stack: the compose project occamy", 12.5, t["text2"], weight=600)
    box(s, 16, 122, 134, 72, c["client"], "Browser", ["the web client"])
    box(s, 16, 256, 134, 72, c["private"], "Your shell", ["curl, go tool", "pprof"], dash="4 3", opacity=0.07)

    box(s, 250, 110, 172, 218, c["occamy"], "occamy-v1", ["occamyd and", "the web client", "",
                                                           "`docker/", "`occamy-v1.yaml"], mono_title=True)
    s.line(336, 328, 336, 344, c["occamy"], 1.2)
    chip(s, 250, 344, 172, c["occamy"], "occamy-v1-data", "signing key")

    # What the host reaches: occamy-v1's port, and its debug port while
    # the debug listener is on.
    arrow(s, 150, 158, 248, 158, c["client"], "client")
    label(s, 199, 150, "127.0.0.1:5637")
    arrow(s, 150, 292, 248, 292, c["private"], "private", dash="5 4")
    label(s, 199, 284, "127.0.0.1:5638")
    label(s, 199, 310, "OCCAMY_DEBUG=1", size=10.5, fill=t["text2"])

    # What occamy-v1 reaches, by service name on the stack's network.
    box(s, 528, 104, 200, 116, c["remote"], "desktop", ["XFCE with Firefox", "a session for each port",
                                                         "`:5900 VNC, :3389 RDP"], mono_title=True)
    box(s, 528, 234, 200, 76, c["remote"], "ssh", ["an OpenSSH server", "`:22"], mono_title=True)
    for y, name in ((150, "desktop:5900"), (190, "desktop:3389"), (271, "ssh:22")):
        arrow(s, 422, y, 526, y, c["remote"], "remote")
        label(s, 474, y - 8, name)

    # The servers that only the end-to-end tests connect to.
    s.rect(448, 322, 280, 62, stroke=c["infra"], sw=1.2, rx=8, dash="4 3")
    s.text(460, 342, "For the end-to-end tests only", 11.5, t["text2"])
    for x, w, name in ((460, 64, "vnc"), (532, 104, "tigervnc"), (644, 72, "rdp")):
        chip(s, x, 350, w, c["infra"], name)

    s.text(16, 436, "Only occamy-v1 is published, on the loopback interface. Inside the stack, "
                    "the containers reach each other by service name.", 12, t["text2"])
    return s


FIGURES = {"try-it-stack": stack}
