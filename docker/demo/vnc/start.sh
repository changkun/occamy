#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# The demo desktop (desktop.sh) on three Xvfb screens shared by x11vnc:
# screen :0 with the us keyboard layout on port 5900 with VNC
# Authentication and on port 5901 with none, for the parity matrix's
# None security type; screen :1 with the de layout on port 5902, and :2
# with fr on 5903, both with VNC Authentication, for its keyboard rows.
# No port is published, as no service of the stack is but occamyd's. If
# an x11vnc exits, so does the container.

set -e

x11vnc -storepasswd "$VNC_PASSWORD" /tmp/vncpasswd >/dev/null
n=0
for layout in us de fr; do
	# What a run before a restart of the container left.
	rm -f "/tmp/.X$n-lock" "/tmp/.X11-unix/X$n"
	Xvfb ":$n" -screen 0 1024x768x24 -nolisten tcp &
	while [ ! -e "/tmp/.X11-unix/X$n" ]; do sleep 0.1; done
	DISPLAY=":$n" LAYOUT=$layout /usr/local/bin/desktop.sh &
	n=$((n + 1))
done

serve() {
	(x11vnc -forever -shared "$@"; kill $$) &
}
serve -display :0 -rfbport 5901 -nopw -quiet
serve -display :1 -rfbport 5902 -rfbauth /tmp/vncpasswd
serve -display :2 -rfbport 5903 -rfbauth /tmp/vncpasswd
exec x11vnc -display :0 -rfbport 5900 -rfbauth /tmp/vncpasswd -forever -shared
