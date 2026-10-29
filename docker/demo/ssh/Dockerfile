# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Demo SSH server: OpenSSH with password login for the user occamy.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        openssh-server       && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --create-home --shell /bin/bash occamy && \
    printf 'PasswordAuthentication yes\nKbdInteractiveAuthentication yes\n' \
        > /etc/ssh/sshd_config.d/demo.conf
COPY start.sh /usr/local/bin/start.sh
ENV SSH_PASSWORD=occamy
EXPOSE 22
CMD ["/usr/local/bin/start.sh"]
