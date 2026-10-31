# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# TigerVNC's Xvnc, for the demo stack and the VNC backend's integration
# tests. Built from the repository's root:
#
#	podman build -t occamy-demo-tigervnc -f docker/demo/tigervnc/Dockerfile .
#
# start.sh demo (the default) serves the demo desktop
# (docker/demo/vnc/desktop.sh) from five Xvnc screens, one for each
# security type with the us keyboard layout: port 5900 with VNC
# Authentication (VNC_PASSWORD), 5901 with VeNCrypt's X509Vnc and
# X509Plain only (the user occamy with PLAIN_PASSWORD), and 5902 with
# None; and with VNC Authentication, one with the de layout on 5903 and
# one with fr on 5904, for the parity matrix's keyboard rows
# (tests/parity_test.go). At start it makes a CA of its own
# and a server certificate signed by it, for localhost, 127.0.0.1 and the
# names in TLS_NAMES, keeps ca.pem and server.pem in /etc/vnc-test,
# copies ca.pem to CA_DIR if that is set, and drops the CA's key.
#
# start.sh scene serves the bench desktop's scene program (bench/scene)
# from one Xvnc screen of GEOMETRY (1024x768 by default) on port 5900
# with VNC Authentication, for TestEncodings
# (internal/backend/vnc/integration_encodings_test.go).
FROM golang:1.27-trixie AS build
WORKDIR /occamy
COPY go.mod go.sum ./
COPY bench/ bench/
RUN CGO_ENABLED=0 go build -trimpath -o /scene ./bench/scene

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        openssl                       \
        tigervnc-standalone-server    \
        tigervnc-tools                \
        x11-xkb-utils                 \
        xclip                         \
        xterm                      && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --no-create-home occamy
COPY --from=build /scene /usr/local/bin/scene
COPY docker/demo/vnc/desktop.sh docker/demo/tigervnc/start.sh /usr/local/bin/
ENV VNC_PASSWORD=vncpassword PLAIN_PASSWORD=plainpassword TLS_NAMES=tigervnc
EXPOSE 5900 5901 5902 5903 5904
ENTRYPOINT ["/usr/local/bin/start.sh"]
CMD ["demo"]
