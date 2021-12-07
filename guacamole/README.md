This folder contains a self-maintained [guacamole-server](https://github.com/apache/guacamole-server) source code.

This modified copy fixed a few issues which was identified from guacamole-server
code base, as well as many on demand simplification.

To build the libguac:


```
sudo apt install -y  \
    libtool          \
    libcairo-dev     \
    libvncserver-dev \
    libssl-dev       \
    libssh2-1-dev
```