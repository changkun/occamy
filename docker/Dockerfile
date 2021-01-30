# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# FYI: debian has connection issue
FROM centos:centos7
ENV LC_ALL=en_US.UTF-8                \
    RUNTIME_DEPENDENCIES="            \
        gcc                           \
        gdb                           \
        wget                          \
        glib                          \
        cairo                         \
        dejavu-sans-mono-fonts        \
        freerdp                       \
        freerdp-plugins               \
        ghostscript                   \
        libssh2                       \
        liberation-mono-fonts         \
        libvncserver                  \
        pango                         \
        terminus-fonts                \
        git"                          \
    BUILD_DEPENDENCIES="              \
        autoconf                      \
        automake                      \
        cairo-devel                   \
        freerdp-devel                 \
        pango-devel                   \
        libssh2-devel                 \
        libtool                       \
        libvncserver-devel            \
        make"                         \
    GO_VERSION=1.15.7
RUN yum -y update                                                         && \
    yum -y install epel-release $RUNTIME_DEPENDENCIES $BUILD_DEPENDENCIES && \
    # see: https://github.com/Zer0CoolX/guacamole-install-rhel/issues/78#issuecomment-534620524
    yum -y remove freerdp-devel && \
    yum -y remove freerdp-libs && \
    yum -y install freerdp-devel-1.0.2-15.el7 \
                freerdp-plugins-1.0.2-15.el7 \
                --enablerepo=C7.6.1810-base \
                --disablerepo=base \
                --disablerepo=updates
WORKDIR /occamy
RUN mkdir /golang                                      && \
    cd /golang                                         && \
    wget https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -xvf go${GO_VERSION}.linux-amd64.tar.gz
ADD . .
RUN ./guacamole/src/build-libguac.sh /occamy/guacamole
RUN /golang/go/bin/go build -mod vendor -x -o occamyd

EXPOSE 5636
CMD ["/occamy/occamyd"]