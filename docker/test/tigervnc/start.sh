#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Xvnc with VeNCrypt's X509Vnc and X509Plain only, and a loop on its
# display that answers the client's clipboard: text that starts
# "owner: " comes back as "echo: " and the rest. The tests fail to
# authenticate on purpose, so the blacklist takes many failures.

set -e

Xvnc :1 -rfbport 5901 -geometry 1024x768 -depth 24 \
    -SecurityTypes X509Vnc,X509Plain -PlainUsers occamy \
    -X509Cert /etc/vnc-test/server.pem -X509Key /etc/vnc-test/server.key \
    -PasswordFile /etc/vnc-test/passwd -AlwaysShared \
    -BlacklistThreshold 1000 &
while [ ! -e /tmp/.X11-unix/X1 ]; do sleep 0.1; done

export DISPLAY=:1
while true; do
    t=$(timeout 2 xclip -o -selection clipboard -t UTF8_STRING 2>/dev/null || true)
    case "$t" in
    "owner: "*)
        printf 'echo: %s' "${t#owner: }" |
            timeout 5 xclip -quiet -selection clipboard -t UTF8_STRING -i >/dev/null 2>&1 || true
        ;;
    esac
    sleep 0.3
done
