# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Bench link: shape.sh, which emulates a link in the kernel on the client
# connections of a server of the bench stack. The harness runs it in that
# server's network namespace, with NET_ADMIN, around each block of a loss
# study (docs/benchmarks.md, Loss). make bench-realtime-2 builds it.

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ethtool                 \
        iproute2                \
    && rm -rf /var/lib/apt/lists/*
COPY shape.sh /usr/local/bin/shape
ENTRYPOINT ["/usr/local/bin/shape"]
