This folder contains a self-maintained [guacamole-server](https://github.com/apache/guacamole-server) source code.

This modified copy fixed a few issues which was identified from guacamole-server
code base, as well as many on demand simplification. It is based on
guacamole-server 1.0.0 and changes parts of the libguac API (for example
`guac_client_add_user` and `guac_user_input_thread`) so that `occamyd` can drive
users and connections from Go.

Protocol support with current distributions:

- VNC: builds against libvncserver 0.9.15.
- SSH: builds against libssh2 and OpenSSL 3. `key.c` still uses the RSA/DSA APIs
  deprecated in OpenSSL 3.0 and is compiled at the 1.1.0 API level.
- RDP: requires FreeRDP 1.x and is disabled automatically when it is missing,
  which is the case on every current distribution.

To build the libguac on Debian or Ubuntu:

```
sudo apt install -y  \
    autoconf         \
    automake         \
    libtool          \
    make             \
    pkg-config       \
    libcairo2-dev    \
    libpango1.0-dev  \
    libpng-dev       \
    libssh2-1-dev    \
    libssl-dev       \
    libvncserver-dev
sudo ./src/build-libguac.sh "$PWD" /usr/local
```

libguac loads protocol plugins with `dlopen("libguac-client-<protocol>.so")`.
When installing outside `/usr/lib`, add the library directory to
`LD_LIBRARY_PATH`.
