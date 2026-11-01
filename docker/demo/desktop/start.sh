#!/bin/bash
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# start.sh serves the demo desktop: an XFCE session on Xvnc, on port 5900,
# which runs from the start and outlives its clients, and xrdp, on port
# 3389, which starts a session of its own at the first connection. If
# Xvnc or xrdp exits, so does the container; a session that ends, as by
# logging out of it, starts again.

set -euo pipefail
trap 'exit 0' TERM INT

echo "occamy:$RDP_PASSWORD" | chpasswd

# The VNC session has a home of its own. Firefox refuses a profile that
# another session has open, and XFCE keeps each session's settings in its
# home, so one home for both sessions would tie them together.
home=/home/occamy/vnc
install -d -o occamy -g occamy "$home"
install -o occamy -g occamy -m 644 /etc/skel/.bashrc /etc/skel/.profile "$home/"
printf '%s\n' "$VNC_PASSWORD" | vncpasswd -f | install -o occamy -g occamy -m 600 /dev/stdin "$home/.vncpasswd"
cd "$home"

# runuser keeps the environment, and no session needs the passwords.
unset VNC_PASSWORD RDP_PASSWORD

# occamy runs commands as the user of the VNC session.
occamy() {
	runuser -u occamy -- env HOME="$home" DISPLAY=:1 "$@"
}

# Xvnc and XFCE's session manager, as a user, cannot make these.
mkdir -p -m 1777 /tmp/.X11-unix /tmp/.ICE-unix
# What a run before a restart of the container left, of Xvnc and of xrdp's
# X sessions, or a restart would move xrdp's session to the next display.
rm -f /tmp/.X*-lock /tmp/.X11-unix/X*
occamy Xvnc :1 -rfbport 5900 -geometry 1280x800 -depth 24 -AlwaysShared \
	-desktop "Desktop (XFCE)" -SecurityTypes VncAuth -PasswordFile "$home/.vncpasswd" \
	-nolisten tcp &
xvnc=$!
while [ ! -e /tmp/.X11-unix/X1 ]; do
	kill -0 "$xvnc" # set -e ends the script if Xvnc could not start
	sleep 0.1
done
(while :; do
	occamy /etc/xrdp/startwm.sh || true
	sleep 1
done) &

# xrdp and xrdp-sesman log to stdout through stdio and fork per
# connection, so without line buffering their log lines show up late and
# once more in every child.
stdbuf -oL xrdp-sesman --nodaemon &
stdbuf -oL xrdp --nodaemon &
wait -n
exit 1
