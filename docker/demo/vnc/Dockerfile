# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Demo VNC server: an xterm on Xvfb, shared by x11vnc on port 5900.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        x11vnc                  \
        xterm                   \
        xvfb                 && \
    rm -rf /var/lib/apt/lists/*
COPY start.sh /usr/local/bin/start.sh
ENV VNC_PASSWORD=vncpassword
EXPOSE 5900
CMD ["/usr/local/bin/start.sh"]
