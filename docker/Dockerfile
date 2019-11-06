# Copyright 2019 Changkun Ou. All rights reserved.
# Use of this source code is governed by a MIT
# license that can be found in the LICENSE file.

# FYI: debian has connection issue
FROM centos:centos7
ENV \
    LC_ALL=en_US.UTF-8                \
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
        libjpeg-turbo                 \
        libssh2                       \
        liberation-mono-fonts         \
        libvncserver                  \
        libwebp                       \
        pango                         \
        terminus-fonts                \
        git                           \
        uuid"                         \
    BUILD_DEPENDENCIES="              \
        autoconf                      \
        automake                      \
        cairo-devel                   \
        freerdp-devel                 \
        libjpeg-turbo-devel           \
        uuid-devel                    \
        pango-devel                   \
        libssh2-devel                 \
        libtool                       \
        libvncserver-devel            \
        libwebp-devel                 \
        make"                         \
    GO_VERSION=1.13.4
RUN yum -y update                                                         && \
    yum -y install epel-release $RUNTIME_DEPENDENCIES $BUILD_DEPENDENCIES
# see: https://github.com/Zer0CoolX/guacamole-install-rhel/issues/78#issuecomment-534620524
RUN yum -y remove freerdp-devel && \
    yum -y remove freerdp-libs && \
    yum -y install freerdp-devel-1.0.2-15.el7 \
                freerdp-plugins-1.0.2-15.el7 \
                --enablerepo=C7.6.1810-base \
                --disablerepo=base \
                --disablerepo=updates
WORKDIR /go/src/github.com/changkun/occamy
RUN mkdir /go-download                                      && \
    cd /go-download                                         && \
    wget https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -xvf go${GO_VERSION}.linux-amd64.tar.gz                      && \
    mv go /usr/local && \
    cp /usr/lib64/glib-2.0/include/glibconfig.h /usr/include/glib-2.0/
ENV GOROOT=/usr/local/go
ENV GOPATH=/go
ENV PATH=$GOPATH/bin:$GOROOT/bin:$PATH

COPY . .

RUN ./guacamole/src/build-libguac.sh /go/src/github.com/changkun/occamy/guacamole

RUN go build -mod vendor -x -ldflags \
"-X github.com/changkun/occamy/config.Version=v$(git describe --always --tags) \
-X github.com/changkun/occamy/config.BuildTime=$(date +%F) \
-X github.com/changkun/occamy/config.GitCommit=$(git rev-parse HEAD)" \
-o occamyd occamy.go

EXPOSE 5636
CMD ["/go/src/github.com/changkun/occamy/occamyd"]