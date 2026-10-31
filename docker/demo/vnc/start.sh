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
# x11vnc 0.9.17 hangs for good, now and then, after an X error as a
# client connects under the parity matrix's sessions: it logs "caught X11
# error:", drops its polling images on its way to exit, and stops in a
# futex wait with connections waiting to be accepted. Without MIT-SHM
# (-noshm) it did so less often on a Mac, but still did in CI.
# X11VNC_PRINT_XERROR makes it log the failed request first. Each x11vnc
# runs under a watchdog, which kills one that logs an X error or leaves a
# connection unaccepted for ten seconds, and starts one that exits again,
# and says so in the log. Its log lines start with its port. If an Xvfb
# or a desktop exits, so does the container.

set -euo pipefail
trap 'exit 0' TERM INT
export X11VNC_PRINT_XERROR=1

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

# log port prints x11vnc's lines with its port in front, and creates the
# file /tmp/x11vnc-$port.failed when x11vnc reports an X error.
log() {
	local line
	while IFS= read -r line; do
		printf '[%s] %s\n' "$1" "$line"
		case $line in
		"caught X11 error:"* | "caught XIO error:"*) : >"/tmp/x11vnc-$1.failed" ;;
		esac
	done
}

# serve port display args… runs x11vnc for display on port, and starts it
# again whenever it exits, reports an X error, or leaves a connection
# unaccepted for 10 s. A live x11vnc accepts within a second, however
# often clients connect, so the watchdog follows each waiting connection
# rather than the queue's length.
serve() {
	local port=$1 display=$2 pid waited now looks
	shift 2
	while :; do
		rm -f "/tmp/x11vnc-$port.failed"
		x11vnc -display "$display" -rfbport "$port" -forever -shared -noshm "$@" \
			> >(log "$port") 2>&1 &
		pid=$!
		# waited holds the connections that have waited at every look
		# for looks looks.
		waited= looks=0
		while kill -0 "$pid" 2>/dev/null; do
			sleep 2
			if [ -e "/tmp/x11vnc-$port.failed" ]; then
				# x11vnc exits after an X error, when it does not hang.
				echo "[$port] watchdog: x11vnc caught an X error, killing it"
				kill -9 "$pid" 2>/dev/null || true
				break
			fi
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
				break
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
