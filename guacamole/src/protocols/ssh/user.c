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

#include "clipboard.h"
#include "common/display.h"
#include "input.h"
#include "key.h"
#include "user.h"
#include "ssh.h"
#include "settings.h"

#include <guacamole/client.h>
#include <guacamole/socket.h>
#include <guacamole/user.h>

#include <pthread.h>
#include <string.h>

int guac_ssh_user_join_handler(guac_user* user, int argc, char** argv) {

    guac_client* client = user->client;
    guac_ssh_client* ssh_client = (guac_ssh_client*) client->data;

    /* Parse provided arguments */
    guac_ssh_settings* settings = guac_ssh_parse_args(user,
            argc, (const char**) argv);

    /* Fail if settings cannot be parsed */
    if (settings == NULL) {
        guac_user_log(user, GUAC_LOG_INFO,
                "Badly formatted client arguments.");
        return 1;
    }

    /* Store settings at user level */
    user->data = settings;

    /* Connect via SSH if owner */
    if (user->owner) {

        /* Store owner's settings at client level */
        ssh_client->settings = settings;

        /* Start client thread */
        if (pthread_create(&(ssh_client->client_thread), NULL,
                    ssh_client_thread, (void*) client)) {
            guac_client_abort(client, GUAC_PROTOCOL_STATUS_SERVER_ERROR,
                    "Unable to start SSH client thread");
            return 1;
        }

    }

    /* If not owner, synchronize with current display */
    else {
        // FIXME: occamy: this is a temporal solution.
        // If two users race on same connection, the client->display is
        // a NULL pointer, which can cause segment fault.
        // see bug report: https://issues.apache.org/jira/browse/GUACAMOLE-898
        if (ssh_client->term != NULL) {
            guac_terminal_dup(ssh_client->term, user, user->socket);
        }
        guac_socket_flush(user->socket);
    }

    /* Only handle events if not read-only */
    if (!settings->read_only) {

        /* General mouse/keyboard/clipboard events */
        user->key_handler       = guac_ssh_user_key_handler;
        user->mouse_handler     = guac_ssh_user_mouse_handler;
        user->clipboard_handler = guac_ssh_clipboard_handler;

        /* Display size change events */
        user->size_handler = guac_ssh_user_size_handler;

    }

    return 0;

}

int guac_ssh_user_leave_handler(guac_user* user) {

    guac_ssh_client* ssh_client = (guac_ssh_client*) user->client->data;

    /* Update shared cursor state */
    guac_common_cursor_remove_user(ssh_client->term->cursor, user);

    /* Free settings if not owner (owner settings will be freed with client) */
    if (!user->owner) {
        guac_ssh_settings* settings = (guac_ssh_settings*) user->data;
        guac_ssh_settings_free(settings);
    }

    return 0;
}

#include <stdlib.h>
#include <string.h>

guac_common_ssh_user* guac_common_ssh_create_user(const char* username) {

    guac_common_ssh_user* user = malloc(sizeof(guac_common_ssh_user));

    /* Init user */
    user->username = strdup(username);
    user->password = NULL;
    user->private_key = NULL;

    return user;

}

void guac_common_ssh_destroy_user(guac_common_ssh_user* user) {

    /* Free private key, if present */
    if (user->private_key != NULL)
        guac_common_ssh_key_free(user->private_key);

    /* Free all other data */
    free(user->password);
    free(user->username);
    free(user);

}

void guac_common_ssh_user_set_password(guac_common_ssh_user* user,
        const char* password) {

    /* Replace current password with given value */
    free(user->password);
    user->password = strdup(password);

}

int guac_common_ssh_user_import_key(guac_common_ssh_user* user,
        char* private_key, char* passphrase) {

    /* Free existing private key, if present */
    if (user->private_key != NULL)
        guac_common_ssh_key_free(user->private_key);

    /* Attempt to read key without passphrase if none given */
    if (passphrase == NULL)
        user->private_key = guac_common_ssh_key_alloc(private_key,
                strlen(private_key), "");

    /* Otherwise, use provided passphrase */
    else
        user->private_key = guac_common_ssh_key_alloc(private_key,
                strlen(private_key), passphrase);

    /* Fail if key could not be read */
    return user->private_key == NULL;

}

