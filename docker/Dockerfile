# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Stage 1: build the web client demo.
FROM node:24-trixie-slim AS web
WORKDIR /occamy/client/occamy-web
COPY client/occamy-web/package.json client/occamy-web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY client/occamy-web ./
RUN npm run build

# Stage 2: build libguac with its protocol plugins, then occamyd against it.
#
# RDP is disabled: the vendored guacamole-server (1.0.0) only supports
# FreeRDP 1.x, which no current distribution ships. VNC and SSH are built.
FROM golang:1.27-trixie AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        autoconf                \
        automake                \
        libtool                 \
        make                    \
        pkg-config              \
        libcairo2-dev           \
        libpango1.0-dev         \
        libpng-dev              \
        libssh2-1-dev           \
        libssl-dev              \
        libvncserver-dev     && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /occamy
COPY guacamole ./guacamole
RUN ./guacamole/src/build-libguac.sh /occamy/guacamole /usr/local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o occamyd .

# Stage 3: runtime image.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        fontconfig              \
        fonts-dejavu-core       \
        libcairo2               \
        libpango-1.0-0          \
        libpangocairo-1.0-0     \
        libpng16-16t64          \
        libssh2-1t64            \
        libssl3t64              \
        libvncclient1        && \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /usr/local/lib/libguac* /usr/local/lib/
# libguac dlopen()s plugins by their unversioned name (libguac-client-vnc.so),
# which ldconfig does not cache, so the directory must be on the search path.
ENV LD_LIBRARY_PATH=/usr/local/lib
WORKDIR /occamy
COPY --from=build /occamy/occamyd ./
COPY conf.yaml ./
COPY LICENSE ./licenses/
COPY guacamole/LICENSE guacamole/NOTICE ./licenses/guacamole/
COPY --from=web /occamy/client/occamy-web/dist ./client/occamy-web/dist

EXPOSE 5636
CMD ["/occamy/occamyd"]
