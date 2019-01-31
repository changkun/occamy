# FYI: debian has connection issue
FROM centos:centos7
ENV \
    LC_ALL=en_US.UTF-8                \
    RUNTIME_DEPENDENCIES="            \
        htop                          \
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
        libtelnet                     \
        libvorbis                     \
        libvncserver                  \
        libwebp                       \
        pango                         \
        terminus-fonts                \
        pulseaudio-libs               \
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
        pulseaudio-libs-devel         \
        libssh2-devel                 \
        libtelnet-devel               \
        libtool                       \
        libvncserver-devel            \
        libwebp-devel                 \
        make"
RUN yum -y update                                                         && \
    yum -y install epel-release $RUNTIME_DEPENDENCIES $BUILD_DEPENDENCIES
WORKDIR /go/src/github.com/changkun/occamy

RUN mkdir /go-download                                      && \
    cd /go-download                                         && \
    wget https://dl.google.com/go/go1.11.5.linux-amd64.tar.gz && \
    tar -xvf go1.11.5.linux-amd64.tar.gz                      && \
    mv go /usr/local && \
    cp /usr/lib64/glib-2.0/include/glibconfig.h /usr/include/glib-2.0/
ENV GOROOT=/usr/local/go
ENV GOPATH=/go
ENV PATH=$GOPATH/bin:$GOROOT/bin:$PATH

COPY . .

# build guacamole-server from local source
RUN cd /go/src/github.com/changkun/occamy/guacamole && \
    # fix https://issues.apache.org/jira/plugins/servlet/mobile#issue/GUACAMOLE-213
    echo "/usr/local/lib" >> /etc/ld.so.conf             && \
    ./src/guacd-docker/bin/build-guacd.sh /go/src/github.com/changkun/occamy/guacamole

RUN go build -x -ldflags \
"-X github.com/changkun/occamy/config.Version=v$(git describe --always --tags) \
-X github.com/changkun/occamy/config.BuildTime=$(date +%F) \
-X github.com/changkun/occamy/config.GitCommit=$(git rev-parse HEAD)" \
-o occamyd occamy.go

EXPOSE 5636
CMD ["/go/src/github.com/changkun/occamy/occamyd"]