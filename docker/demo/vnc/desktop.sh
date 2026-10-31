#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# desktop.sh [clipboard|typed] runs the demo desktop on $DISPLAY, which
# the demo VNC servers share (docker/demo/vnc, docker/demo/tigervnc), and
# the probes through which the end-to-end tests (tests/parity_test.go)
# check what reaches the server, over the session itself:
#
#   desktop.sh sets the keyboard layout to LAYOUT (us by default), and
#   starts an xterm with a shell, the typing probe in a small xterm at the
#   top left, and the clipboard probe, each again when it exits. No
#   window manager runs, so the keyboard goes to the window under the
#   pointer.
#   desktop.sh typed reads lines from its terminal and puts each on the
#   clipboard as "typed: " and the line's UTF-8 bytes in hex, which no
#   server's clipboard encoding can change.
#   desktop.sh clipboard answers the clipboard: text that starts
#   "owner: " comes back as "echo: " and the rest, and text that starts
#   "hex: " as "hexed: " and the rest's UTF-8 bytes in hex, which shows
#   what the server took whatever it sends back.
#
# The layout stays as it starts: x11vnc adds a keysym missing from the
# layout to an unused key when a client sends one, remembers where, and
# after setxkbmap has dropped the key, still presses it.
#
# Text is put on the clipboard as STRING, in Latin-1, when Latin-1 holds
# it, and as UTF8_STRING otherwise, as X clients that convert do: x11vnc
# reads STRING, and RFB's cut text is Latin-1.

export LANG=C.UTF-8

# get prints the clipboard, in UTF-8.
get() {
	if timeout 2 xclip -o -selection clipboard -t TARGETS 2>/dev/null | grep -qx UTF8_STRING; then
		timeout 2 xclip -o -selection clipboard -t UTF8_STRING 2>/dev/null
	else
		timeout 2 xclip -o -selection clipboard -t STRING 2>/dev/null | iconv -f LATIN1 -t UTF-8
	fi
}

# put puts $1, in UTF-8, on the clipboard. xclip serves it in the
# background until another client takes the clipboard.
put() {
	if latin1=$(printf '%s' "$1" | iconv -f UTF-8 -t LATIN1 2>/dev/null); then
		printf '%s' "$latin1" | xclip -selection clipboard -t STRING -i >/dev/null 2>&1
	else
		printf '%s' "$1" | xclip -selection clipboard -t UTF8_STRING -i >/dev/null 2>&1
	fi
}

# hex prints $1's bytes in hex.
hex() {
	printf '%s' "$1" | od -An -v -tx1 | tr -d ' \n'
}

case "${1:-}" in
"")
	setxkbmap -layout "${LAYOUT:-us}"
	(while :; do xterm -geometry 80x2+0+0 -T "typing probe" -e "$0" typed; sleep 1; done) &
	(while :; do xterm -geometry 120x40+0+50 -T shell; sleep 1; done) &
	exec "$0" clipboard
	;;
typed)
	echo "Lines typed here reach the clipboard as hex (tests/parity_test.go)."
	while IFS= read -r line; do
		put "typed: $(hex "$line")"
	done
	;;
clipboard)
	# It looks every 0.3 seconds for a minute after the clipboard last
	# changed, and every 2 seconds after that, so that an idle desktop
	# costs little, as in the benchmark stack.
	last= quiet=0
	while :; do
		t=$(get)
		if [ "$t" != "$last" ]; then
			last=$t quiet=0
			case "$t" in
			"owner: "*)
				last="echo: ${t#owner: }"
				put "$last"
				;;
			"hex: "*)
				last="hexed: $(hex "${t#hex: }")"
				put "$last"
				;;
			esac
		fi
		if [ "$quiet" -lt 200 ]; then
			quiet=$((quiet + 1))
			sleep 0.3
		else
			sleep 2
		fi
	done
	;;
*)
	echo "usage: desktop.sh [clipboard|typed]" >&2
	exit 2
	;;
esac
