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

#ifndef GUAC_VNC_VNC_H
#define GUAC_VNC_VNC_H

#include "config.h"

#include "common/clipboard.h"
#include "common/display.h"
#include "common/iconv.h"
#include "common/surface.h"

#include <guacamole/client.h>
#include <guacamole/layer.h>
#include <rfb/rfbclient.h>

#include <pthread.h>
#include <stdbool.h>

/**
 * VNC-specific client data.
 */
typedef struct guac_vnc_settings {

    /**
     * The hostname of the VNC server (or repeater) to connect to.
     */
    char* hostname;

    /**
     * The port of the VNC server (or repeater) to connect to.
     */
    int port;

    /**
     * The password given in the arguments.
     */
    char* password;

    /**
     * Space-separated list of encodings to use within the VNC session.
     */
    char* encodings;

    /**
     * Whether the red and blue components of each color should be swapped.
     * This is mainly used for VNC servers that do not properly handle
     * colors.
     */
    bool swap_red_blue;

    /**
     * The color depth to request, in bits.
     */
    int color_depth;

    /**
     * Whether this connection is read-only, and user input should be dropped.
     */
    bool read_only;

#ifdef ENABLE_VNC_REPEATER
    /**
     * The VNC host to connect to, if using a repeater.
     */
    char* dest_host;

    /**
     * The VNC port to connect to, if using a repeater.
     */
    int dest_port;
#endif

#ifdef ENABLE_VNC_LISTEN
    /**
     * Whether not actually connecting to a VNC server, but rather listening
     * for a connection from the VNC server (reverse connection).
     */
    bool reverse_connect;

    /**
     * The maximum amount of time to wait when listening for connections, in
     * milliseconds.
     */
    int listen_timeout;
#endif

    /**
     * Whether the cursor should be rendered on the server (remote) or on the
     * client (local).
     */
    bool remote_cursor;

    /**
     * The number of connection attempts to make before giving up.
     */
    int retries;

    /**
     * The encoding to use for clipboard data sent to the VNC server, or NULL
     * to use the encoding required by the VNC standard.
     */
    char* clipboard_encoding;

} guac_vnc_settings;

/**
 * Parses all given args, storing them in a newly-allocated settings object. If
 * the args fail to parse, NULL is returned.
 *
 * @param user
 *     The user who submitted the given arguments while joining the
 *     connection.
 *
 * @param argc
 *     The number of arguments within the argv array.
 *
 * @param argv
 *     The values of all arguments provided by the user.
 *
 * @return
 *     A newly-allocated settings object which must be freed with
 *     guac_vnc_settings_free() when no longer needed. If the arguments fail
 *     to parse, NULL is returned.
 */
guac_vnc_settings* guac_vnc_parse_args(guac_user* user,
        int argc, const char** argv);

/**
 * Frees the given guac_vnc_settings object, having been previously allocated
 * via guac_vnc_parse_args().
 *
 * @param settings
 *     The settings object to free.
 */
void guac_vnc_settings_free(guac_vnc_settings* settings);

/**
 * NULL-terminated array of accepted client args.
 */
extern const char* GUAC_VNC_CLIENT_ARGS[];

/**
 * The maximum duration of a frame in milliseconds.
 */
#define GUAC_VNC_FRAME_DURATION 40

/**
 * The amount of time to allow per message read within a frame, in
 * milliseconds. If the server is silent for at least this amount of time, the
 * frame will be considered finished.
 */
#define GUAC_VNC_FRAME_TIMEOUT 0

/**
 * The amount of time to wait for a new message from the VNC server when
 * beginning a new frame. This value must be kept reasonably small such that
 * a slow VNC server will not prevent external events from being handled (such
 * as the stop signal from guac_client_stop()), but large enough that the
 * message handling loop does not eat up CPU spinning.
 */
#define GUAC_VNC_FRAME_START_TIMEOUT 1000000

/**
 * The number of milliseconds to wait between connection attempts.
 */
#define GUAC_VNC_CONNECT_INTERVAL 1000

/**
 * The maximum number of bytes to allow within the clipboard.
 */
#define GUAC_VNC_CLIPBOARD_MAX_LENGTH 262144

/**
 * VNC-specific client data.
 */
typedef struct guac_vnc_client {

    /**
     * The VNC client thread.
     */
    pthread_t client_thread;

    /**
     * The underlying VNC client.
     */
    rfbClient* rfb_client;

    /**
     * The original framebuffer malloc procedure provided by the initialized
     * rfbClient.
     */
    MallocFrameBufferProc rfb_MallocFrameBuffer;

    /**
     * Whether copyrect  was used to produce the latest update received
     * by the VNC server.
     */
    int copy_rect_used;

    /**
     * Client settings, parsed from args.
     */
    guac_vnc_settings* settings;

    /**
     * The current display state.
     */
    guac_common_display* display;

    /**
     * Internal clipboard.
     */
    guac_common_clipboard* clipboard;

    /**
     * Clipboard encoding-specific reader.
     */
    guac_iconv_read* clipboard_reader;

    /**
     * Clipboard encoding-specific writer.
     */
    guac_iconv_write* clipboard_writer;

} guac_vnc_client;

/**
 * Allocates a new rfbClient instance given the parameters stored within the
 * client, returning NULL on failure.
 *
 * @param client
 *     The guac_client associated with the settings of the desired VNC
 *     connection.
 *
 * @return
 *     A new rfbClient instance allocated and connected according to the
 *     parameters stored within the given client, or NULL if connecting to the
 *     VNC server fails.
 */
rfbClient* guac_vnc_get_client(guac_client* client);

/**
 * VNC client thread. This thread initiates the VNC connection and ultimately
 * runs throughout the duration of the client, existing as a single instance,
 * shared by all users.
 *
 * @param data
 *     The guac_client instance associated with the requested VNC connection.
 *
 * @return
 *     Always NULL.
 */
void* guac_vnc_client_thread(void* data);

/**
 * Key which can be used with the rfbClientGetClientData function to return
 * the associated guac_client.
 */
extern char* GUAC_VNC_CLIENT_KEY;

#endif

