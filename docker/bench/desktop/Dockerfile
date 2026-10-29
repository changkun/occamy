# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Bench desktop: the scene program (bench/scene) with no window manager,
# served by one protocol per container. `start.sh vnc` runs it on Xvfb
# shared by x11vnc on port 5900; `start.sh rdp` runs xrdp on port 3389,
# whose sessions for the user occamy are the scene program on xorgxrdp.
# docker-compose.bench.yaml builds it from the repository root.

FROM golang:1.27-trixie AS build
WORKDIR /occamy
COPY go.mod go.sum ./
RUN go mod download
COPY bench/ bench/
RUN CGO_ENABLED=0 go build -trimpath -o /scene ./bench/scene

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        x11vnc                  \
        xorgxrdp                \
        xrdp                    \
        xvfb                 && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --create-home --shell /bin/bash occamy && \
    sed -i 's/^#EnableConsole=false/EnableConsole=true/; s/^EnableSyslog=true/EnableSyslog=false/' \
        /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini && \
    printf '#!/bin/sh\nexec /usr/local/bin/scene\n' > /etc/xrdp/startwm.sh
COPY --from=build /scene /usr/local/bin/scene
COPY docker/bench/desktop/start.sh /usr/local/bin/start.sh
ENV VNC_PASSWORD=vncpassword RDP_PASSWORD=occamy
EXPOSE 3389 5900
ENTRYPOINT ["/usr/local/bin/start.sh"]
CMD ["vnc"]
