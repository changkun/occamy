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
        libvncserver                  \
        git"                          \
    BUILD_DEPENDENCIES="              \
        autoconf                      \
        automake                      \
        cairo-devel                   \
        libtool                       \
        libvncserver-devel            \
        make"                         \
    GO_VERSION=1.13.8
RUN yum -y update                                                         && \
    yum -y install epel-release $RUNTIME_DEPENDENCIES $BUILD_DEPENDENCIES
WORKDIR /occamy
RUN mkdir /golang                                      && \
    cd /golang                                         && \
    wget https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -xvf go${GO_VERSION}.linux-amd64.tar.gz
ADD . .
RUN ./guacamole/src/build-libguac.sh /occamy/guacamole
RUN /golang/go/bin/go build -mod vendor -x -ldflags \
    "-X /occamy/config.Version=v$(git describe --always --tags) \
    -X /occamy/config.BuildTime=$(date +%F) \
    -X /occamy/config.GitCommit=$(git rev-parse HEAD)" \
    -o occamyd occamy.go

EXPOSE 5636
CMD ["/occamy/occamyd"]