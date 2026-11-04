# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

"""The figures of docs/debugging.md."""

from .deploy import arrow, box, heads, inks, label
from .draw import MONO, SVG


def sources(t):
    """Where to look at a running occamyd: its log on standard error,
    the health check on the public listener, which users and probes
    reach from the network, and the debug listener on the loopback
    interface, which only someone on the host reaches."""
    c = inks(t)
    s = SVG(760, 396, t)
    heads(s, c)

    s.rect(16, 36, 536, 318, stroke=t["grid"], sw=1.2, rx=12, dash="5 4")
    s.text(32, 60, "The host, or occamyd's pod", 12.5, t["text2"], weight=600)

    box(s, 150, 86, 190, 116, c["occamy"], "occamyd", ["serves two listeners", "and writes one log"])

    # Its two listeners, as ports on its edge.
    def port(y, ink, name, note, dash=None):
        s.rect(340, y, 168, 30, fill=ink, stroke=ink, sw=1.5, rx=5, dash=dash, opacity=0.16)
        s.text(352, y + 20, name, 11.5, weight=600, family=MONO)
        s.text(498, y + 20, note, 10.5, t["text2"], anchor="end")

    port(98, c["client"], ":5636", "public")
    port(156, c["private"], "localhost:6060", "debug", dash="4 3")

    # Users and probes reach the public listener from the network.
    box(s, 590, 70, 156, 92, c["client"], "Users, probes", ["`GET /healthz", "the API, streams", "and web client"])
    arrow(s, 588, 113, 510, 113, c["client"], "client")

    # Only someone on the host reaches the debug listener.
    box(s, 330, 232, 206, 100, c["infra"], "You, on the host", ["`curl /debug/vars", "`go tool pprof",
                                                                 "or kubectl port-forward"])
    arrow(s, 400, 232, 400, 188, c["private"], "private", dash="5 4")

    # The log, which the container engine or journald keeps.
    box(s, 36, 232, 250, 100, c["occamy"], "The log", ["`podman logs occamy", "`journalctl -u occamy",
                                                         "`kubectl logs"], opacity=0.06)
    arrow(s, 210, 202, 210, 230, c["occamy"], "occamy")
    label(s, 222, 220, "standard error", mono=False, anchor="start", size=10.5, fill=t["text2"])

    s.text(16, 382, "Users and probes reach the public listener. The debug listener stays on the loopback "
                    "interface, so only someone on the host can read it.", 12, t["text2"])
    return s


FIGURES = {"debugging-sources": sources}
