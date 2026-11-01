#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# An XFCE session on $DISPLAY, until it ends. xrdp-sesman runs this as the
# user when it starts an X session, and start.sh for the VNC session.
export LANG=C.UTF-8 NO_AT_BRIDGE=1
exec dbus-run-session -- xfce4-session
