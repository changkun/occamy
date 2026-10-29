#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

set -e

Xvfb :0 -screen 0 1024x768x24 -nolisten tcp &
while [ ! -e /tmp/.X11-unix/X0 ]; do sleep 0.1; done

export DISPLAY=:0
xterm -geometry 120x40+0+0 &

x11vnc -storepasswd "$VNC_PASSWORD" /tmp/vncpasswd >/dev/null
exec x11vnc -display :0 -rfbport 5900 -rfbauth /tmp/vncpasswd -forever -shared
