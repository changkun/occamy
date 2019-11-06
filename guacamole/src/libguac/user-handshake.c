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
#include "error.h"
#include "parser.h"
#include "protocol.h"
#include "socket.h"
#include "user.h"

#include <pthread.h>
#include <stdlib.h>
#include <string.h>

/**
 * Prints an error message using the logging facilities of the given user,
 * automatically including any information present in guac_error.
 *
 * @param user
 *     The guac_user associated with the error that occurred.
 *
 * @param level
 *     The level at which to log this message.
 *
 * @param message
 *     The message to log.
 */
static void guac_user_log_guac_error(guac_user* user,
        guac_client_log_level level, const char* message) {

    if (guac_error != GUAC_STATUS_SUCCESS) {

        /* If error message provided, include in log */
        if (guac_error_message != NULL)
            guac_user_log(user, level, "%s: %s", message,
                    guac_error_message);

        /* Otherwise just log with standard status string */
        else
            guac_user_log(user, level, "%s: %s", message,
                    guac_status_string(guac_error));

    }

    /* Just log message if no status code */
    else
        guac_user_log(user, level, "%s", message);

}

/**
 * The thread which handles all user input, calling event handlers for received
 * instructions.
 *
 * @param data
 *     A pointer to a guac_user_input_thread_params structure describing the
 *     user whose input is being handled and the guac_parser with which to
 *     handle it.
 *
 * @return
 *     Always NULL.
 */
void* guac_user_input_thread(guac_parser* parser, guac_user* user,
        int usec_timeout) {

    guac_client* client = user->client;
    guac_socket* socket = user->socket;

    /* Guacamole user input loop */
    while (client->state == GUAC_CLIENT_RUNNING && user->active) {

        /* Read instruction, stop on error */
        if (guac_parser_read(parser, socket, usec_timeout)) {

            if (guac_error == GUAC_STATUS_TIMEOUT)
                guac_user_abort(user, GUAC_PROTOCOL_STATUS_CLIENT_TIMEOUT, "User is not responding.");

            else {
                if (guac_error != GUAC_STATUS_CLOSED)
                    guac_user_log_guac_error(user, GUAC_LOG_WARNING,
                            "Guacamole connection failure");
                guac_user_stop(user);
            }

            return NULL;
        }

        /* Reset guac_error and guac_error_message (user/client handlers are not
         * guaranteed to set these) */
        guac_error = GUAC_STATUS_SUCCESS;
        guac_error_message = NULL;

        /* Call handler, stop on error */
        if (guac_user_handle_instruction(user, parser->opcode, parser->argc, parser->argv) < 0) {

            /* Log error */
            guac_user_log_guac_error(user, GUAC_LOG_WARNING,
                    "User connection aborted");

            /* Log handler details */
            guac_user_log(user, GUAC_LOG_DEBUG, "Failing instruction handler in user was \"%s\"", parser->opcode);

            guac_user_stop(user);
            return NULL;
        }

    }

    return NULL;

}
