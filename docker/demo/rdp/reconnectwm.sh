#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# xrdp-sesman runs this each time a connection reconnects to the X
# session, as the session's user and with the environment it starts the
# session's channel server with. That channel server, xrdp-chansrv,
# carries the clipboard. xrdp 0.10.1's can exit when a connection
# closes just after attaching to it, and sesman starts it only with the
# session, so every later connection would wait 10 seconds for it and get
# no clipboard. If the user runs no channel server, this starts one
# again; xrdp, which retries for 10 seconds, then connects to it.
#
# Only then, and one at a time: a second channel server would delete
# the first one's socket to listen on it. Two reconnections at once take
# the lock in turn, and the first holds it until its channel server
# runs, so that the second finds it.
uid=$(id -u)

# running reports whether a channel server of the user runs.
running() {
	for p in /proc/[0-9]*; do
		[ "$(cat "$p/comm" 2>/dev/null)" = xrdp-chansrv ] || continue
		[ "$(stat -c %u "$p" 2>/dev/null)" = "$uid" ] || continue
		read -r _ _ state _ 2>/dev/null < "$p/stat" || continue
		[ "$state" != Z ] && return 0
	done
	return 1
}

exec 9> "/tmp/.xrdp-chansrv-restart-$uid.lock"
flock 9
running && exit 0
echo "reconnectwm.sh: no channel server on display $DISPLAY: starting xrdp-chansrv again"
/usr/sbin/xrdp-chansrv 9>&- &
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
	running && exit 0
	sleep 0.1
done
