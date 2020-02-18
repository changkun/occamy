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

#include "error.h"
#include "layer.h"
#include "object.h"
#include "palette.h"
#include "protocol.h"
#include "socket.h"
#include "stream.h"
#include "unicode.h"

#include <cairo/cairo.h>

#include <inttypes.h>
#include <setjmp.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>

/* Output formatting functions */

ssize_t __guac_socket_write_length_string(guac_socket* socket, const char* str) {

    return
           guac_socket_write_int(socket, guac_utf8_strlen(str))
        || guac_socket_write_string(socket, ".")
        || guac_socket_write_string(socket, str);

}

ssize_t __guac_socket_write_length_int(guac_socket* socket, int64_t i) {

    char buffer[128];
    snprintf(buffer, sizeof(buffer), "%"PRIi64, i);
    return __guac_socket_write_length_string(socket, buffer);

}

/* Protocol functions */

int guac_protocol_send_ack(guac_socket* socket, guac_stream* stream,
        const char* error, guac_protocol_status status) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "3.ack,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, error)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, status)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_blob(guac_socket* socket, const guac_stream* stream,
        const void* data, int count) {

    int base64_length = (count + 2) / 3 * 4;

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.blob,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || guac_socket_write_int(socket, base64_length)
        || guac_socket_write_string(socket, ".")
        || guac_socket_write_base64(socket, data, count)
        || guac_socket_flush_base64(socket)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_body(guac_socket* socket, const guac_object* object,
        const guac_stream* stream, const char* mimetype, const char* name) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.body,")
        || __guac_socket_write_length_int(socket, object->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, mimetype)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, name)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_cfill(guac_socket* socket,
        guac_composite_mode mode, const guac_layer* layer,
        int r, int g, int b, int a) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "5.cfill,")
        || __guac_socket_write_length_int(socket, mode)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, r)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, g)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, b)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, a)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_clip(guac_socket* socket, const guac_layer* layer) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.clip,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_copy(guac_socket* socket,
        const guac_layer* srcl, int srcx, int srcy, int w, int h,
        guac_composite_mode mode, const guac_layer* dstl, int dstx, int dsty) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.copy,")
        || __guac_socket_write_length_int(socket, srcl->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcx)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcy)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, w)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, h)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, mode)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dstl->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dstx)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dsty)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_cursor(guac_socket* socket, int x, int y,
        const guac_layer* srcl, int srcx, int srcy, int w, int h) {
    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "6.cursor,")
        || __guac_socket_write_length_int(socket, x)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, y)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcl->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcx)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcy)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, w)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, h)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_disconnect(guac_socket* socket) {
    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val = guac_socket_write_string(socket, "10.disconnect;");
    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_dispose(guac_socket* socket, const guac_layer* layer) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "7.dispose,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_end(guac_socket* socket, const guac_stream* stream) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "3.end,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_error(guac_socket* socket, const char* error,
        guac_protocol_status status) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "5.error,")
        || __guac_socket_write_length_string(socket, error)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, status)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_file(guac_socket* socket, const guac_stream* stream,
        const char* mimetype, const char* name) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.file,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, mimetype)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, name)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_mouse(guac_socket* socket, int x, int y,
        int button_mask, guac_timestamp timestamp) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "5.mouse,")
        || __guac_socket_write_length_int(socket, x)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, y)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, button_mask)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, timestamp)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_move(guac_socket* socket, const guac_layer* layer,
        const guac_layer* parent, int x, int y, int z) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.move,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, parent->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, x)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, y)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, z)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_name(guac_socket* socket, const char* name) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.name,")
        || __guac_socket_write_length_string(socket, name)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_pipe(guac_socket* socket, const guac_stream* stream,
        const char* mimetype, const char* name) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.pipe,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, mimetype)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, name)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_img(guac_socket* socket, const guac_stream* stream,
        guac_composite_mode mode, const guac_layer* layer,
        const char* mimetype, int x, int y) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "3.img,")
        || __guac_socket_write_length_int(socket, stream->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, mode)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_string(socket, mimetype)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, x)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, y)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_rect(guac_socket* socket,
        const guac_layer* layer, int x, int y, int width, int height) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.rect,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, x)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, y)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, width)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, height)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_shade(guac_socket* socket, const guac_layer* layer,
        int a) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "5.shade,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, a)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_size(guac_socket* socket, const guac_layer* layer,
        int w, int h) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "4.size,")
        || __guac_socket_write_length_int(socket, layer->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, w)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, h)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_sync(guac_socket* socket, guac_timestamp timestamp) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val = 
           guac_socket_write_string(socket, "4.sync,")
        || __guac_socket_write_length_int(socket, timestamp)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

int guac_protocol_send_transfer(guac_socket* socket,
        const guac_layer* srcl, int srcx, int srcy, int w, int h,
        guac_transfer_function fn, const guac_layer* dstl, int dstx, int dsty) {

    int ret_val;

    guac_socket_instruction_begin(socket);
    ret_val =
           guac_socket_write_string(socket, "8.transfer,")
        || __guac_socket_write_length_int(socket, srcl->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcx)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, srcy)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, w)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, h)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, fn)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dstl->index)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dstx)
        || guac_socket_write_string(socket, ",")
        || __guac_socket_write_length_int(socket, dsty)
        || guac_socket_write_string(socket, ";");

    guac_socket_instruction_end(socket);
    return ret_val;

}

/**
 * Returns the value of a single base64 character.
 */
static int __guac_base64_value(char c) {

    if (c >= 'A' && c <= 'Z')
        return c - 'A';

    if (c >= 'a' && c <= 'z')
        return c - 'a' + 26;

    if (c >= '0' && c <= '9')
        return c - '0' + 52;

    if (c == '+')
        return 62;

    if (c == '/')
        return 63;

    return 0;

}

int guac_protocol_decode_base64(char* base64) {

    char* input = base64;
    char* output = base64;

    int length = 0;
    int bits_read = 0;
    int value = 0;
    char current;

    /* For all characters in string */
    while ((current = *(input++)) != 0) {

        /* If we've reached padding, then we're done */
        if (current == '=')
            break;

        /* Otherwise, shift on the latest 6 bits */
        value = (value << 6) | __guac_base64_value(current);
        bits_read += 6;

        /* If we have at least one byte, write out the latest whole byte */
        if (bits_read >= 8) {
            *(output++) = (value >> (bits_read % 8)) & 0xFF;
            bits_read -= 8;
            length++;
        }

    }

    /* Return number of bytes written */
    return length;

}

