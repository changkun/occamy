# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Demo VNC server: the demo desktop (desktop.sh) on Xvfb, shared by x11vnc
# on port 5900 with the password VNC_PASSWORD and on port 5901 without
# one, and with the de and fr keyboard layouts on ports 5902 and 5903
# (start.sh).
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        x11-xkb-utils           \
        x11vnc                  \
        xclip                   \
        xterm                   \
        xvfb                 && \
    rm -rf /var/lib/apt/lists/*
COPY desktop.sh start.sh /usr/local/bin/
ENV VNC_PASSWORD=vncpassword
EXPOSE 5900 5901 5902 5903
CMD ["/usr/local/bin/start.sh"]
