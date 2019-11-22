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

lib_LTLIBRARIES = libguac-client-rdp.la

nodist_libguac_client_rdp_la_SOURCES = \
    _generated_keymaps.c

libguac_client_rdp_la_SOURCES = \
    client.c                    \
    decompose.c                 \
    dvc.c                       \
    error.c                     \
    input.c                     \
    json.c                      \
    keyboard.c                  \
    ptr_string.c                \
    rdp.c                       \
    rdp_bitmap.c                \
    rdp_cliprdr.c               \
    rdp_color.c                 \
    rdp_disp.c                  \
    rdp_fs.c                    \
    rdp_gdi.c                   \
    rdp_glyph.c                 \
    rdp_keymap.c                \
    rdp_print_job.c             \
    rdp_pointer.c               \
    rdp_rail.c                  \
    rdp_settings.c              \
    rdp_stream.c                \
    rdp_svc.c                   \
    resolution.c                \
    unicode.c                   \
    user.c                      \
    list.c

guacsvc_sources =          \
    guac_svc/svc_service.c \
    rdp_svc.c

guacdr_sources =                             \
    guac_rdpdr/rdpdr_fs_messages.c           \
    guac_rdpdr/rdpdr_fs_messages_dir_info.c  \
    guac_rdpdr/rdpdr_fs_messages_file_info.c \
    guac_rdpdr/rdpdr_fs_messages_vol_info.c  \
    guac_rdpdr/rdpdr_fs_service.c            \
    guac_rdpdr/rdpdr_messages.c              \
    guac_rdpdr/rdpdr_printer.c               \
    guac_rdpdr/rdpdr_service.c               \
    rdp_fs.c                                 \
    rdp_print_job.c                          \
    rdp_stream.c                             \
    unicode.c

noinst_HEADERS =                             \
    compat/client-cliprdr.h                  \
    compat/rail.h                            \
    guac_rdpdr/rdpdr_fs_messages.h           \
    guac_rdpdr/rdpdr_fs_messages_dir_info.h  \
    guac_rdpdr/rdpdr_fs_messages_file_info.h \
    guac_rdpdr/rdpdr_fs_messages_vol_info.h  \
    guac_rdpdr/rdpdr_fs_service.h            \
    guac_rdpdr/rdpdr_messages.h              \
    guac_rdpdr/rdpdr_printer.h               \
    guac_rdpdr/rdpdr_service.h               \
    guac_svc/svc_service.h                   \
    client.h                                 \
    decompose.h                              \
    dvc.h                                    \
    error.h                                  \
    input.h                                  \
    json.h                                   \
    keyboard.h                               \
    ptr_string.h                             \
    rdp.h                                    \
    rdp_bitmap.h                             \
    rdp_cliprdr.h                            \
    rdp_color.h                              \
    rdp_disp.h                               \
    rdp_fs.h                                 \
    rdp_gdi.h                                \
    rdp_glyph.h                              \
    rdp_keymap.h                             \
    rdp_pointer.h                            \
    rdp_print_job.h                          \
    rdp_rail.h                               \
    rdp_settings.h                           \
    rdp_status.h                             \
    rdp_stream.h                             \
    rdp_svc.h                                \
    resolution.h                             \
    unicode.h                                \
    user.h                                   \
    list.h

# Add compatibility layer for WinPR if not available
if ! ENABLE_WINPR
noinst_HEADERS  += compat/winpr-stream.h compat/winpr-wtypes.h
libguac_client_rdp_la_SOURCES += compat/winpr-stream.c
guacsvc_sources += compat/winpr-stream.c
guacdr_sources  += compat/winpr-stream.c
endif

#
# Main RDP client library
#

libguac_client_rdp_la_CFLAGS = \
    -Werror -Wall -Iinclude    \
    @COMMON_INCLUDE@           \
    @LIBGUAC_INCLUDE@

libguac_client_rdp_la_LDFLAGS = \
    -version-info 0:0:0         \
    @CAIRO_LIBS@                \
    @PTHREAD_LIBS@              \
    @RDP_LIBS@

libguac_client_rdp_la_LIBADD =     \
    @COMMON_LTLIB@                 \
    @LIBGUAC_LTLIB@

#
# RDPDR
#

guacdr_cflags                = \
    -Werror -Wall -Iinclude    \
    @COMMON_INCLUDE@           \
    @LIBGUAC_INCLUDE@

guacdr_ldflags =                   \
    -module -avoid-version -shared \
    @PTHREAD_LIBS@                 \
    @RDP_LIBS@

guacdr_libadd =     \
    @COMMON_LTLIB@  \
    @LIBGUAC_LTLIB@

#
# Static Virtual Channels
#

guacsvc_cflags               = \
    -Werror -Wall -Iinclude    \
    @COMMON_INCLUDE@           \
    @LIBGUAC_INCLUDE@

guacsvc_ldflags =                  \
    -module -avoid-version -shared \
    @PTHREAD_LIBS@                 \
    @RDP_LIBS@

guacsvc_libadd =    \
    @COMMON_LTLIB@  \
    @LIBGUAC_LTLIB@

#
# Autogenerate keymaps
#

CLEANFILES = _generated_keymaps.c
BUILT_SOURCES = _generated_keymaps.c

rdp_keymaps =                   \
    $(srcdir)/keymaps/base.keymap         \
    $(srcdir)/keymaps/failsafe.keymap     \
    $(srcdir)/keymaps/de_de_qwertz.keymap \
    $(srcdir)/keymaps/en_gb_qwerty.keymap \
    $(srcdir)/keymaps/en_us_qwerty.keymap \
    $(srcdir)/keymaps/es_es_qwerty.keymap \
    $(srcdir)/keymaps/fr_fr_azerty.keymap \
    $(srcdir)/keymaps/fr_ch_qwertz.keymap \
    $(srcdir)/keymaps/it_it_qwerty.keymap \
    $(srcdir)/keymaps/ja_jp_qwerty.keymap \
    $(srcdir)/keymaps/pt_br_qwerty.keymap \
    $(srcdir)/keymaps/sv_se_qwerty.keymap \
    $(srcdir)/keymaps/tr_tr_qwerty.keymap

_generated_keymaps.c: $(rdp_keymaps)
	$(srcdir)/keymaps/generate.pl $(rdp_keymaps)

EXTRA_DIST =            \
    $(rdp_keymaps)      \
    keymaps/generate.pl

if LEGACY_FREERDP_EXTENSIONS

# FreeRDP 1.0-style extensions
freerdp_LTLIBRARIES = \
    guacdr.la         \
    guacsvc.la

guacdr_la_SOURCES = ${guacdr_sources}
guacdr_la_CFLAGS  = ${guacdr_cflags}
guacdr_la_LDFLAGS = ${guacdr_ldflags}
guacdr_la_LIBADD  = ${guacdr_libadd}

guacsvc_la_SOURCES = ${guacsvc_sources}
guacsvc_la_CFLAGS  = ${guacsvc_cflags}
guacsvc_la_LDFLAGS = ${guacsvc_ldflags}
guacsvc_la_LIBADD  = ${guacsvc_libadd}

else

# FreeRDP 1.1 (and hopefully onward) extensions
freerdp_LTLIBRARIES = \
    guacdr-client.la  \
    guacsvc-client.la

guacdr_client_la_SOURCES = ${guacdr_sources}
guacdr_client_la_CFLAGS  = ${guacdr_cflags}
guacdr_client_la_LDFLAGS = ${guacdr_ldflags}
guacdr_client_la_LIBADD  = ${guacdr_libadd}

guacsvc_client_la_SOURCES = ${guacsvc_sources}
guacsvc_client_la_CFLAGS  = ${guacsvc_cflags}
guacsvc_client_la_LDFLAGS = ${guacsvc_ldflags}
guacsvc_client_la_LIBADD  = ${guacsvc_libadd}

endif

freerdpdir = ${libdir}/freerdp

