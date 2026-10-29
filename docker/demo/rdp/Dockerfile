# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Demo RDP server: xrdp on port 3389 with password login for the user
# occamy, whose session is an xterm on xorgxrdp. Logs go to the console.
# TLS uses the self-signed certificate made at build time, so clients must
# ignore the certificate.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        xorgxrdp                \
        xrdp                    \
        xterm                && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --create-home --shell /bin/bash occamy && \
    sed -i 's/^#EnableConsole=false/EnableConsole=true/; s/^EnableSyslog=true/EnableSyslog=false/' \
        /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini && \
    printf '#!/bin/sh\nexec xterm -geometry 120x40+0+0\n' > /etc/xrdp/startwm.sh
COPY start.sh /usr/local/bin/start.sh
ENV RDP_PASSWORD=occamy
EXPOSE 3389
CMD ["/usr/local/bin/start.sh"]
