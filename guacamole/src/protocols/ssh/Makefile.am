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

lib_LTLIBRARIES = libguac-client-ssh.la

libguac_client_ssh_la_SOURCES = \
    client.c                    \
    clipboard.c                 \
    input.c                     \
    settings.c                  \
    ssh.c                       \
    ttymode.c                   \
    user.c                      \
    key.c                       \
    _ssh.c                      \
    dsa-compat.c                \
    rsa-compat.c                \
    terminal_buffer.c           \
    terminal_char_mappings.c    \
    terminal_common.c           \
    terminal_display.c          \
    terminal_named-colors.c     \
    terminal_palette.c          \
    terminal_scrollbar.c        \
    terminal.c                  \
    terminal_handlers.c         \
    terminal_typescript.c       \
    terminal_xparsecolor.c

noinst_HEADERS =                \
    client.h                    \
    clipboard.h                 \
    input.h                     \
    settings.h                  \
    ssh.h                       \
    ttymode.h                   \
    user.h                      \
    key.h                       \
    _ssh.h                      \
    dsa-compat.h                \
    rsa-compat.h                \
    terminal_buffer.h           \
    terminal_char_mappings.h    \
    terminal_common.h           \
    terminal_display.h          \
    terminal_named-colors.h     \
    terminal_palette.h          \
    terminal_scrollbar.h        \
    terminal.h                  \
    terminal_handlers.h         \
    terminal_types.h            \
    terminal_typescript.h       \
    terminal_xparsecolor.h

# Add agent sources if enabled
if ENABLE_SSH_AGENT
libguac_client_ssh_la_SOURCES += ssh_agent.c
noinst_HEADERS += ssh_agent.h
endif

libguac_client_ssh_la_CFLAGS = \
    -Werror -Wall -Iinclude    \
    @COMMON_INCLUDE@           \
    @LIBGUAC_INCLUDE@          \
    @PANGO_CFLAGS@             \
    @PANGOCAIRO_CFLAGS@

libguac_client_ssh_la_LIBADD = \
    @COMMON_LTLIB@             \
    @LIBGUAC_LTLIB@

libguac_client_ssh_la_LDFLAGS = \
    -version-info 0:0:0         \
    @PTHREAD_LIBS@              \
    @SSH_LIBS@                  \
    @SSL_LIBS@                  \
    @CAIRO_LIBS@              \
    @MATH_LIBS@               \
    @PANGO_LIBS@              \
    @PANGOCAIRO_LIBS@         \
    @PTHREAD_LIBS@


