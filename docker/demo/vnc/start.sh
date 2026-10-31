#!/bin/bash
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# The demo desktop (desktop.sh) on three Xvfb screens shared by x11vnc:
# screen :0 with the us keyboard layout on port 5900 with VNC
# Authentication and on port 5901 with none, for the parity matrix's
# None security type; screen :1 with the de layout on port 5902, and :2
# with fr on 5903, both with VNC Authentication, for its keyboard rows.
# No port is published, as no service of the stack is but occamyd's.
#
# x11vnc 0.9.17 with MIT-SHM hung for good, now and then, after an X
# error as a client connected under the parity matrix's sessions, in a
# futex wait with connections waiting to be accepted; it right after
# dropped its shared-memory polling images. Without MIT-SHM (-noshm), it
# has not. Each x11vnc also runs under a watchdog: one that leaves a
# connection unaccepted for six seconds is killed, and one that exits is
# started again, which the log says. Its log lines start with its port.
# If an Xvfb or a desktop exits, so does the container.

set -euo pipefail
trap 'exit 0' TERM INT

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

# backlog port prints how many connections wait for x11vnc to accept
# them on port: the receive queue of its listening socket.
backlog() {
	local line
	line=$(grep -m1 ":$(printf '%04X' "$1") 00000000:0000 0A " /proc/net/tcp || true)
	if [ -z "$line" ]; then
		echo 0
		return
	fi
	# shellcheck disable=SC2086 # split the line into its fields
	set -- $line
	echo $((16#${5#*:}))
}

# serve port display args… runs x11vnc for display on port, and starts it
# again whenever it exits or stops accepting connections.
serve() {
	local port=$1 display=$2 pid stuck
	shift 2
	while :; do
		x11vnc -display "$display" -rfbport "$port" -forever -shared -noshm "$@" \
			> >(sed -u "s/^/[$port] /") 2>&1 &
		pid=$!
		stuck=0
		while kill -0 "$pid" 2>/dev/null; do
			sleep 2
			if [ "$(backlog "$port")" -gt 0 ]; then
				stuck=$((stuck + 1))
			else
				stuck=0
			fi
			if [ "$stuck" -ge 3 ]; then
				echo "[$port] watchdog: x11vnc has accepted no connection for 6 s, killing it"
				kill -9 "$pid" 2>/dev/null || true
			fi
		done
		wait "$pid" || true
		echo "[$port] watchdog: x11vnc exited, starting it again"
		sleep 1
	done
}

serve 5900 :0 -rfbauth /tmp/vncpasswd &
serve 5901 :0 -nopw -quiet &
serve 5902 :1 -rfbauth /tmp/vncpasswd &
serve 5903 :2 -rfbauth /tmp/vncpasswd &
wait -n
exit 1
