#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# start.sh x11vnc|tigervnc serves the bench desktop's scene program from
# one VNC server, on a screen of GEOMETRY (1024x768 by default), with the
# password VNC_PASSWORD.

set -e
geometry=${GEOMETRY:-1024x768}

case "$1" in
x11vnc)
	# As docker/bench/desktop/start.sh vnc.
	Xvfb :0 -screen 0 "${geometry}x24" -nolisten tcp &
	while [ ! -e /tmp/.X11-unix/X0 ]; do sleep 0.1; done
	(DISPLAY=:0 /usr/local/bin/scene; kill $$) &
	x11vnc -storepasswd "$VNC_PASSWORD" /tmp/vncpasswd >/dev/null
	exec x11vnc -display :0 -rfbport 5900 -rfbauth /tmp/vncpasswd \
		-forever -shared -threads -wait 1 -defer 0 -nonap -nowf -noscr
	;;
tigervnc)
	# Xvnc is the X server and the VNC server in one. The scene exits
	# when Xvnc does, and stops the container.
	printf '%s\n' "$VNC_PASSWORD" | vncpasswd -f >/tmp/vncpasswd
	Xvnc :0 -geometry "$geometry" -depth 24 -rfbport 5900 \
		-rfbauth /tmp/vncpasswd -SecurityTypes VncAuth -AlwaysShared \
		-nolisten tcp &
	while [ ! -e /tmp/.X11-unix/X0 ]; do sleep 0.1; done
	DISPLAY=:0 exec /usr/local/bin/scene
	;;
*)
	echo "usage: start.sh x11vnc|tigervnc" >&2
	exit 2
	;;
esac
