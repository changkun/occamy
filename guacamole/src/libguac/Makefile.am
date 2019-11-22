#
# Licensed to the Apache Software Foundation (ASF) under one
# or more contributor license agreements.  See the NOTICE file
# distributed with this work for additional information
# regarding copyright ownership.  The ASF licenses this file
# to you under the Apache License, Version 2.0 (the
# "License"); you may not use this file except in compliance
# with the License.  You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.
#

AUTOMAKE_OPTIONS = foreign 
ACLOCAL_AMFLAGS = -I m4

lib_LTLIBRARIES = libguac.la

libguacincdir = $(includedir)/guacamole

libguacinc_HEADERS =                  \
    guacamole/client.h                \
    guacamole/client-types.h          \
    guacamole/error.h                 \
    guacamole/error-types.h           \
    guacamole/layer.h                 \
    guacamole/layer-types.h           \
    guacamole/object.h                \
    guacamole/object-types.h          \
    guacamole/parser.h                \
    guacamole/pool.h                  \
    guacamole/protocol.h              \
    guacamole/protocol-types.h        \
    guacamole/socket-constants.h      \
    guacamole/socket.h                \
    guacamole/socket-fntypes.h        \
    guacamole/socket-types.h          \
    guacamole/stream.h                \
    guacamole/stream-types.h          \
    guacamole/timestamp.h             \
    guacamole/timestamp-types.h       \
    guacamole/unicode.h               \
    guacamole/user.h                  \
    guacamole/user-fntypes.h          \
    guacamole/user-types.h

noinst_HEADERS =      \
    encode-png.h      \
    palette.h         \
    user-handlers.h

libguac_la_SOURCES =   \
    client.c           \
    encode-png.c       \
    error.c            \
    palette.c          \
    parser.c           \
    pool.c             \
    protocol.c         \
    socket.c           \
    socket-broadcast.c \
    socket-fd.c        \
    timestamp.c        \
    unicode.c          \
    user.c             \
    user-handlers.c

libguac_la_CFLAGS = \
    -Werror -Wall -pedantic -I$(srcdir)/guacamole

libguac_la_LDFLAGS =     \
    -version-info 16:0:0 \
    -no-undefined        \
    @CAIRO_LIBS@         \
    @DL_LIBS@            \
    @PNG_LIBS@           \
    @PTHREAD_LIBS@       \
    @SSL_LIBS@

