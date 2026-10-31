#!/bin/bash
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# start.sh [demo|scene]: see the Dockerfile. The tests fail to
# authenticate on purpose, so the blacklist takes many failures. If an
# Xvnc or the scene exits, so does the container.

set -euo pipefail

dir=/etc/vnc-test
mkdir -p "$dir"
printf '%s\n' "$VNC_PASSWORD" | vncpasswd -f >"$dir/passwd"
common=(-depth 24 -AlwaysShared -BlacklistThreshold 1000 -nolisten tcp)

# xvnc n args… starts Xvnc's screen :n, on port 5900+n, and waits for
# its socket. One at a time: X servers that start together race to make
# /tmp/.X11-unix, and one of them can lose its socket to another.
xvnc() {
	local n=$1
	shift
	# What a run before a restart of the container left.
	rm -f "/tmp/.X$n-lock" "/tmp/.X11-unix/X$n"
	Xvnc ":$n" "$@" "${common[@]}" &
	while [ ! -e "/tmp/.X11-unix/X$n" ]; do sleep 0.1; done
}

# certificates makes the CA and the server certificate.
certificates() {
	local san="DNS:localhost,IP:127.0.0.1" name
	for name in ${TLS_NAMES//,/ }; do
		san="$san,DNS:$name"
	done
	cd "$dir"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout ca.key -out ca.pem -days 365 -subj "/CN=Occamy test CA" \
		-addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign 2>/dev/null
	openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout server.key -out server.csr -subj "/CN=localhost" 2>/dev/null
	printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature\n' "$san" >ext
	openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial \
		-out server.pem -days 365 -extfile ext 2>/dev/null
	rm ca.key ca.srl server.csr ext
	chmod 644 ca.pem server.pem
	cd - >/dev/null
}

case "${1:-demo}" in
demo)
	echo "occamy:$PLAIN_PASSWORD" | chpasswd
	certificates
	if [ -n "${CA_DIR:-}" ]; then
		cp "$dir/ca.pem" "$CA_DIR/ca.pem.new"
		mv "$CA_DIR/ca.pem.new" "$CA_DIR/ca.pem"
	fi
	xvnc 0 -geometry 1024x768 -SecurityTypes VncAuth -PasswordFile "$dir/passwd"
	xvnc 1 -geometry 1024x768 -SecurityTypes X509Vnc,X509Plain -PlainUsers occamy \
		-X509Cert "$dir/server.pem" -X509Key "$dir/server.key" -PasswordFile "$dir/passwd"
	xvnc 2 -geometry 1024x768 -SecurityTypes None
	xvnc 3 -geometry 1024x768 -SecurityTypes VncAuth -PasswordFile "$dir/passwd"
	xvnc 4 -geometry 1024x768 -SecurityTypes VncAuth -PasswordFile "$dir/passwd"
	layouts=(us us us de fr)
	for n in 0 1 2 3 4; do
		DISPLAY=:$n LAYOUT=${layouts[n]} /usr/local/bin/desktop.sh &
	done
	wait -n
	exit 1
	;;
scene)
	xvnc 0 -geometry "${GEOMETRY:-1024x768}" -SecurityTypes VncAuth -PasswordFile "$dir/passwd"
	DISPLAY=:0 /usr/local/bin/scene &
	wait -n
	exit 1
	;;
*)
	echo "usage: start.sh [demo|scene]" >&2
	exit 2
	;;
esac
