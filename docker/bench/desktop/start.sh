#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# start.sh vnc|rdp serves the scene program over one protocol.

set -e

case "$1" in
vnc)
	Xvfb :0 -screen 0 1024x768x24 -nolisten tcp &
	while [ ! -e /tmp/.X11-unix/X0 ]; do sleep 0.1; done
	# If the scene exits, stop the container, so that the benchmark fails
	# instead of measuring an empty screen. x11vnc replaces this shell
	# below, so it has this shell's PID.
	(DISPLAY=:0 /usr/local/bin/scene; kill $$) &
	x11vnc -storepasswd "$VNC_PASSWORD" /tmp/vncpasswd >/dev/null
	# By default x11vnc takes 27 ms at the median and 125 ms at p95 from a
	# key to the update that shows it, measured with a VNC client of its
	# own. It polls the screen every 20 ms, defers updates by 20 ms, naps
	# when the screen is quiet, and after input looks for window moves and
	# scrolls before it sends. With threads, polls every millisecond and
	# none of the rest, it takes 2.8 ms and 3.4 ms, which keeps the VNC
	# server within the benchmark's 5 ms budget for the remote server.
	exec x11vnc -display :0 -rfbport 5900 -rfbauth /tmp/vncpasswd \
		-forever -shared -threads -wait 1 -defer 0 -nonap -nowf -noscr
	;;
rdp)
	echo "occamy:$RDP_PASSWORD" | chpasswd
	# As in docker/demo/rdp: line buffering, so that log lines show up
	# once and in time.
	stdbuf -oL xrdp-sesman --nodaemon &
	exec stdbuf -oL xrdp --nodaemon
	;;
*)
	echo "usage: start.sh vnc|rdp" >&2
	exit 2
	;;
esac
