# Copyright 2026 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# Demo desktop: a real desktop for a first look at Occamy, XFCE with its
# panel, menu and file manager, a terminal, a text editor and Firefox,
# as the user occamy. Port 5900 serves one session with TigerVNC's Xvnc,
# with VNC Authentication (VNC_PASSWORD), and port 3389 another with
# xrdp and xorgxrdp, with password login (RDP_PASSWORD). Built from the
# repository's root, since it shares the RDP demo's reconnection script:
#
#	podman build -t occamy-demo-desktop -f docker/demo/desktop/Dockerfile .
#
# The tests' probes do not live here: tests/parity_test.go needs the bare
# X servers of the vnc, tigervnc and rdp images, with no window manager.
#
# --no-install-recommends keeps it to what a viewer sees, so a few of
# XFCE's recommendations are named here: librsvg2-common draws its default
# wallpaper, sensible-utils is how its panel finds the browser, and
# tango-icon-theme is the icon theme it asks for. No mail client is
# installed, so the menu's Mail Reader is hidden.
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        dbus-x11                      \
        firefox-esr                   \
        fonts-dejavu-core             \
        librsvg2-common               \
        mousepad                      \
        sensible-utils                \
        tango-icon-theme              \
        thunar                        \
        tigervnc-standalone-server    \
        tigervnc-tools                \
        x11-xserver-utils             \
        xfce4-panel                   \
        xfce4-session                 \
        xfce4-settings                \
        xfce4-terminal                \
        xfdesktop4                    \
        xfwm4                         \
        xorgxrdp                      \
        xrdp                       && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --create-home --shell /bin/bash occamy && \
    sed -i 's/^#EnableConsole=false/EnableConsole=true/; s/^EnableSyslog=true/EnableSyslog=false/' \
        /etc/xrdp/xrdp.ini /etc/xrdp/sesman.ini && \
    sed -i '/^\[Desktop Entry\]/a NoDisplay=true' /usr/share/applications/xfce4-mail-reader.desktop
COPY docker/demo/rdp/reconnectwm.sh docker/demo/desktop/startwm.sh /etc/xrdp/
COPY docker/demo/desktop/start.sh /usr/local/bin/
# Firefox opens on a page of its own, since the stack may have no network,
# without its first-run pages.
COPY docker/demo/desktop/policies.json /etc/firefox/policies/
COPY docker/demo/desktop/index.html /usr/local/share/occamy-demo/
ENV VNC_PASSWORD=vncpassword RDP_PASSWORD=occamy
EXPOSE 3389 5900
CMD ["/usr/local/bin/start.sh"]
