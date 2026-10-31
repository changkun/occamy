#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# xrdp-sesman runs this as the user when it starts an X session: the
# clipboard echo for the end-to-end test beside an xterm, which is the
# whole desktop.
/usr/local/bin/clipboard-echo.sh &
exec xterm -geometry 120x40+0+0
