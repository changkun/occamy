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
# connection unaccepted for ten seconds is killed, and one that exits is
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

# waiting port prints, sorted, the remote addresses of the connections
# that wait for x11vnc to accept them on port. In /proc/net/tcp, such a
# connection belongs to no socket yet (inode 0), and is established, or
# closing if its client gave up (CLOSE_WAIT); one that x11vnc accepted and
# closed has an inode of 0 too, but is in neither state.
waiting() {
	awk -v port=":$(printf '%04X' "$1")" \
		'substr($2, length($2) - 4) == port && ($4 == "01" || $4 == "08") && $10 == "0" { print $3 }' \
		/proc/net/tcp | sort
}

# serve port display args… runs x11vnc for display on port, and starts it
# again whenever it exits or leaves a connection unaccepted for 10 s. A
# live x11vnc accepts within a second, however often clients connect, so
# the watchdog follows each waiting connection rather than the queue's
# length.
serve() {
	local port=$1 display=$2 pid waited now looks
	shift 2
	while :; do
		x11vnc -display "$display" -rfbport "$port" -forever -shared -noshm "$@" \
			> >(sed -u "s/^/[$port] /") 2>&1 &
		pid=$!
		# waited holds the connections that have waited at every look
		# for looks looks.
		waited= looks=0
		while kill -0 "$pid" 2>/dev/null; do
			sleep 2
			now=$(waiting "$port")
			if [ -n "$waited" ]; then
				waited=$(comm -12 <(printf '%s\n' "$waited") <(printf '%s\n' "$now"))
			fi
			if [ -n "$waited" ]; then
				looks=$((looks + 1))
			else
				waited=$now looks=0
			fi
			if [ "$looks" -ge 5 ]; then
				echo "[$port] watchdog: x11vnc has left a connection unaccepted for 10 s, killing it"
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
