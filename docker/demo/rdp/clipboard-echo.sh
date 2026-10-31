#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# clipboard-echo answers the end-to-end test's clipboard check
# (tests/v1_test.go). Text on the X session's CLIPBOARD that starts with
# occamy-e2e:, which a viewer pasted through xrdp's clipboard channel,
# comes back on the CLIPBOARD as echo: and the text, which xrdp then
# offers the viewers. One round trip so carries the clipboard both ways.
# It reads the CLIPBOARD every half second, and stops with the session's
# xterm, its parent, which startwm.sh became.
while kill -0 "$PPID" 2>/dev/null; do
	text=$(timeout 2 xclip -o -selection clipboard 2>/dev/null) || text=
	case $text in
	occamy-e2e:*)
		printf 'echo:%s' "$text" | xclip -i -selection clipboard
		;;
	esac
	sleep 0.5
done
