# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# TigerVNC's Xvnc offering only VeNCrypt's X509Vnc, with the password
# vncpassword, and X509Plain, for the user occamy with the password
# plainpassword, for the VNC backend's integration test
# (internal/backend/vnc/vencrypt_integration_test.go). The image makes a
# CA of its own and a server certificate for localhost and 127.0.0.1
# that it signs, keeps ca.pem and server.pem in /etc/vnc-test for the
# test to copy out, and drops the CA's key.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        openssl                       \
        tigervnc-standalone-server    \
        tigervnc-tools                \
        xclip                      && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /etc/vnc-test
RUN openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -keyout ca.key -out ca.pem -days 365 -subj "/CN=Occamy test CA" \
        -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign && \
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -keyout server.key -out server.csr -subj "/CN=localhost" && \
    printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature\n' > ext && \
    openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial \
        -out server.pem -days 365 -extfile ext && \
    rm ca.key ca.srl server.csr ext && \
    echo vncpassword | vncpasswd -f > passwd && \
    useradd --no-create-home occamy && \
    echo occamy:plainpassword | chpasswd
COPY start.sh /usr/local/bin/start.sh
EXPOSE 5901
CMD ["/usr/local/bin/start.sh"]
