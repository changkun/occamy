#!/bin/sh
# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

set -e

echo "occamy:$SSH_PASSWORD" | chpasswd
ssh-keygen -A
mkdir -p /run/sshd
exec /usr/sbin/sshd -D -e
