/*
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

#include "config.h"

#include "client.h"
#include "settings.h"

#include <guacamole/user.h>

#include <stdbool.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

/* Client plugin arguments */
const char* GUAC_VNC_CLIENT_ARGS[] = {
    "hostname",
    "port",
    "read-only",
    "encodings",
    "password",
    "swap-red-blue",
    "color-depth",
    "cursor",
    "autoretry",
    "clipboard-encoding",

#ifdef ENABLE_VNC_REPEATER
    "dest-host",
    "dest-port",
#endif

#ifdef ENABLE_VNC_LISTEN
    "reverse-connect",
    "listen-timeout",
#endif

    NULL
};

enum VNC_ARGS_IDX {

    /**
     * The hostname of the VNC server (or repeater) to connect to.
     */
    IDX_HOSTNAME,

    /**
     * The port of the VNC server (or repeater) to connect to.
     */
    IDX_PORT,

    /**
     * "true" if this connection should be read-only (user input should be
     * dropped), "false" or blank otherwise.
     */
    IDX_READ_ONLY,

    /**
     * Space-separated list of encodings to use within the VNC session. If not
     * specified, this will be:
     *
     *     "zrle ultra copyrect hextile zlib corre rre raw".
     */
    IDX_ENCODINGS,

    /**
     * The password to send to the VNC server if authentication is requested.
     */
    IDX_PASSWORD,

    /**
     * "true" if the red and blue components of each color should be swapped,
     * "false" or blank otherwise. This is mainly used for VNC servers that do
     * not properly handle colors.
     */
    IDX_SWAP_RED_BLUE,

    /**
     * The color depth to request, in bits.
     */
    IDX_COLOR_DEPTH,

    /**
     * "remote" if the cursor should be rendered on the server instead of the
     * client. All other values will default to local rendering.
     */
    IDX_CURSOR,

    /**
     * The number of connection attempts to make before giving up. By default,
     * this will be 0.
     */
    IDX_AUTORETRY,

    /**
     * The encoding to use for clipboard data sent to the VNC server if we are
     * going to be deviating from the standard (which mandates ISO 8829-1).
     * Valid values are "ISO8829-1" (the only legal value with respect to the
     * VNC standard), "UTF-8", "UTF-16", and "CP2252".
     */
    IDX_CLIPBOARD_ENCODING,

#ifdef ENABLE_VNC_REPEATER
    /**
     * The VNC host to connect to, if using a repeater.
     */
    IDX_DEST_HOST,

    /**
     * The VNC port to connect to, if using a repeater.
     */
    IDX_DEST_PORT,
#endif

#ifdef ENABLE_VNC_LISTEN
    /**
     * "true" if not actually connecting to a VNC server, but rather listening
     * for a connection from the VNC server (reverse connection), "false" or
     * blank otherwise.
     */
    IDX_REVERSE_CONNECT,

    /**
     * The maximum amount of time to wait when listening for connections, in
     * milliseconds. If unspecified, this will default to 5000.
     */
    IDX_LISTEN_TIMEOUT,
#endif

    VNC_ARGS_COUNT
};

guac_vnc_settings* guac_vnc_parse_args(guac_user* user,
        int argc, const char** argv) {

    /* Validate arg count */
    if (argc != VNC_ARGS_COUNT) {
        guac_user_log(user, GUAC_LOG_WARNING, "Incorrect number of connection "
                "parameters provided: expected %i, got %i.",
                VNC_ARGS_COUNT, argc);
        return NULL;
    }

    guac_vnc_settings* settings = calloc(1, sizeof(guac_vnc_settings));

    settings->hostname =
        guac_user_parse_args_string(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_HOSTNAME, "");

    settings->port =
        guac_user_parse_args_int(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_PORT, 0);

    settings->password =
        guac_user_parse_args_string(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_PASSWORD, ""); /* NOTE: freed by libvncclient */

    /* Remote cursor */
    if (strcmp(argv[IDX_CURSOR], "remote") == 0) {
        guac_user_log(user, GUAC_LOG_INFO, "Cursor rendering: remote");
        settings->remote_cursor = true;
    }

    /* Local cursor */
    else {
        guac_user_log(user, GUAC_LOG_INFO, "Cursor rendering: local");
        settings->remote_cursor = false;
    }

    /* Swap red/blue (for buggy VNC servers) */
    settings->swap_red_blue =
        guac_user_parse_args_boolean(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_SWAP_RED_BLUE, false);

    /* Read-only mode */
    settings->read_only =
        guac_user_parse_args_boolean(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_READ_ONLY, false);

    /* Parse color depth */
    settings->color_depth =
        guac_user_parse_args_int(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_COLOR_DEPTH, 0);

#ifdef ENABLE_VNC_REPEATER
    /* Set repeater parameters if specified */
    settings->dest_host =
        guac_user_parse_args_string(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_DEST_HOST, NULL);

    /* VNC repeater port */
    settings->dest_port =
        guac_user_parse_args_int(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_DEST_PORT, 0);
#endif

    /* Set encodings if specified */
    settings->encodings =
        guac_user_parse_args_string(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_ENCODINGS,
                "zrle ultra copyrect hextile zlib corre rre raw");

    /* Parse autoretry */
    settings->retries =
        guac_user_parse_args_int(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_AUTORETRY, 0);

#ifdef ENABLE_VNC_LISTEN
    /* Set reverse-connection flag */
    settings->reverse_connect =
        guac_user_parse_args_boolean(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_REVERSE_CONNECT, false);

    /* Parse listen timeout */
    settings->listen_timeout =
        guac_user_parse_args_int(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_LISTEN_TIMEOUT, 5000);
#endif

    /* Set clipboard encoding if specified */
    settings->clipboard_encoding =
        guac_user_parse_args_string(user, GUAC_VNC_CLIENT_ARGS, argv,
                IDX_CLIPBOARD_ENCODING, NULL);

    return settings;

}

void guac_vnc_settings_free(guac_vnc_settings* settings) {

    /* Free settings strings */
    free(settings->clipboard_encoding);
    free(settings->encodings);
    free(settings->hostname);

#ifdef ENABLE_VNC_REPEATER
    /* Free VNC repeater settings */
    free(settings->dest_host);
#endif

    /* Free settings structure */
    free(settings);

}

