#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

set -e

echo "occamy:$RDP_PASSWORD" | chpasswd

# xrdp and xrdp-sesman log to stdout through stdio and fork per
# connection, so without line buffering their log lines show up late and
# once more in every child.
stdbuf -oL xrdp-sesman --nodaemon &
exec stdbuf -oL xrdp --nodaemon
