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

#ifndef _GUAC_PROTOCOL_H
#define _GUAC_PROTOCOL_H

/**
 * Provides functions and structures required for communicating using the
 * Guacamole protocol over a guac_socket connection, such as that provided by
 * guac_client objects.
 *
 * @file protocol.h
 */

#include "layer-types.h"
#include "object-types.h"
#include "protocol-types.h"
#include "socket-types.h"
#include "stream-types.h"
#include "timestamp-types.h"

#include <cairo/cairo.h>
#include <stdarg.h>

/* CONTROL INSTRUCTIONS */

/**
 * Sends an ack instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The guac_stream associated with the operation this ack is
 *               acknowledging.
 * @param error The human-readable description associated with the error or
 *              status update.
 * @param status The status code related to the error or status.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_ack(guac_socket* socket, guac_stream* stream,
        const char* error, guac_protocol_status status);

/**
 * Sends a disconnect instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_disconnect(guac_socket* socket);

/**
 * Sends an error instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param error The human-readable description associated with the error.
 * @param status The status code related to the error.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_error(guac_socket* socket, const char* error,
        guac_protocol_status status);

/**
 * Sends a mouse instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket
 *     The guac_socket connection to use.
 *
 * @param x
 *     The X coordinate of the current mouse position.
 *
 * @param y
 *     The Y coordinate of the current mouse position.
 *
 * @param button_mask
 *     An integer value representing the current state of each button, where
 *     the Nth bit within the integer is set to 1 if and only if the Nth mouse
 *     button is currently pressed. The lowest-order bit is the left mouse
 *     button, followed by the middle button, right button, and finally the up
 *     and down buttons of the scroll wheel.
 *
 *     @see GUAC_CLIENT_MOUSE_LEFT
 *     @see GUAC_CLIENT_MOUSE_MIDDLE
 *     @see GUAC_CLIENT_MOUSE_RIGHT
 *     @see GUAC_CLIENT_MOUSE_SCROLL_UP
 *     @see GUAC_CLIENT_MOUSE_SCROLL_DOWN
 *
 * @param timestamp
 *     The server timestamp (in milliseconds) at the point in time this mouse
 *     position was acknowledged.
 *
 * @return
 *     Zero on success, non-zero on error.
 */
int guac_protocol_send_mouse(guac_socket* socket, int x, int y,
        int button_mask, guac_timestamp timestamp);

/**
 * Sends a nop instruction (null-operation) over the given guac_socket
 * connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_nop(guac_socket* socket);

/**
 * Sends a sync instruction over the given guac_socket connection. The
 * current time in milliseconds should be passed in as the timestamp.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param timestamp The current timestamp (in milliseconds).
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_sync(guac_socket* socket, guac_timestamp timestamp);

/* OBJECT INSTRUCTIONS */

/**
 * Sends a body instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket
 *     The guac_socket connection to use.
 *
 * @param object
 *     The object to associated with the stream being used.
 *
 * @param stream
 *     The stream to use.
 *
 * @param mimetype
 *     The mimetype of the data being sent.
 *
 * @param name
 *     The name of the stream whose body is being sent, as requested by a "get"
 *     instruction.
 *
 * @return
 *     Zero on success, non-zero on error.
 */
int guac_protocol_send_body(guac_socket* socket, const guac_object* object,
        const guac_stream* stream, const char* mimetype, const char* name);

/**
 * Sends a filesystem instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket
 *     The guac_socket connection to use.
 *
 * @param object
 *     The object representing the filesystem being exposed.
 *
 * @param name
 *     A name describing the filesystem being exposed.
 *
 * @return
 *     Zero on success, non-zero on error.
 */
int guac_protocol_send_filesystem(guac_socket* socket,
        const guac_object* object, const char* name);

/* MEDIA INSTRUCTIONS */

/**
 * Sends an audio instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket
 *     The guac_socket connection to use when sending the audio instruction.
 *
 * @param stream
 *     The stream to use for future audio data.
 *
 * @param mimetype
 *     The mimetype of the audio data which will be sent over the given stream.
 *
 * @return
 *     Zero on success, non-zero on error.
 */
int guac_protocol_send_audio(guac_socket* socket, const guac_stream* stream,
        const char* mimetype);

/**
 * Sends a file instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The stream to use.
 * @param mimetype The mimetype of the data being sent.
 * @param name A name describing the file being sent.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_file(guac_socket* socket, const guac_stream* stream,
        const char* mimetype, const char* name);

/**
 * Sends a pipe instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The stream to use.
 * @param mimetype The mimetype of the data being sent.
 * @param name An arbitrary name uniquely identifying this pipe.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_pipe(guac_socket* socket, const guac_stream* stream,
        const char* mimetype, const char* name);

/**
 * Writes a block of data to the currently in-progress blob which was already
 * created.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The stream to use.
 * @param data The file data to write.
 * @param count The number of bytes within the given buffer of file data
 *              that must be written.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_blob(guac_socket* socket, const guac_stream* stream,
        const void* data, int count);

/**
 * Sends an end instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The stream to use.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_end(guac_socket* socket, const guac_stream* stream);

/* DRAWING INSTRUCTIONS */

/**
 * Sends a cfill instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param mode The composite mode to use.
 * @param layer The destination layer.
 * @param r The red component of the color of the rectangle.
 * @param g The green component of the color of the rectangle.
 * @param b The blue component of the color of the rectangle.
 * @param a The alpha (transparency) component of the color of the rectangle.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_cfill(guac_socket* socket,
        guac_composite_mode mode, const guac_layer* layer,
        int r, int g, int b, int a);

/**
 * Sends a clip instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The layer to set the clipping region of.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_clip(guac_socket* socket, const guac_layer* layer);

/**
 * Sends a copy instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param srcl The source layer.
 * @param srcx The X coordinate of the source rectangle.
 * @param srcy The Y coordinate of the source rectangle.
 * @param w The width of the source rectangle.
 * @param h The height of the source rectangle.
 * @param mode The composite mode to use.
 * @param dstl The destination layer.
 * @param dstx The X coordinate of the destination, where the source rectangle
 *             should be copied.
 * @param dsty The Y coordinate of the destination, where the source rectangle
 *             should be copied.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_copy(guac_socket* socket, 
        const guac_layer* srcl, int srcx, int srcy, int w, int h,
        guac_composite_mode mode, const guac_layer* dstl, int dstx, int dsty);

/**
 * Sends a cursor instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param x The X coordinate of the cursor hotspot.
 * @param y The Y coordinate of the cursor hotspot.
 * @param srcl The source layer.
 * @param srcx The X coordinate of the source rectangle.
 * @param srcy The Y coordinate of the source rectangle.
 * @param w The width of the source rectangle.
 * @param h The height of the source rectangle.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_cursor(guac_socket* socket, int x, int y,
        const guac_layer* srcl, int srcx, int srcy, int w, int h);

/**
 * Sends an img instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket
 *     The guac_socket connection to use when sending the img instruction.
 *
 * @param stream
 *     The stream over which the image data will be sent.
 *
 * @param mode
 *     The composite mode to use when drawing the image over the destination
 *     layer.
 *
 * @param layer
 *     The destination layer.
 *
 * @param mimetype
 *     The mimetype of the image data being sent.
 *
 * @param x
 *     The X coordinate of the upper-left corner of the destination rectangle
 *     within the destination layer, in pixels.
 *
 * @param y
 *     The Y coordinate of the upper-left corner of the destination rectangle
 *     within the destination layer, in pixels.
 *
 * @return
 *     Zero if the instruction was successfully sent, non-zero on error.
 */
int guac_protocol_send_img(guac_socket* socket, const guac_stream* stream,
        guac_composite_mode mode, const guac_layer* layer,
        const char* mimetype, int x, int y);

/**
 * Sends a rect instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The destination layer.
 * @param x The X coordinate of the rectangle.
 * @param y The Y coordinate of the rectangle.
 * @param width The width of the rectangle.
 * @param height The height of the rectangle.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_rect(guac_socket* socket, const guac_layer* layer,
        int x, int y, int width, int height);

/**
 * Sends a transfer instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param srcl The source layer.
 * @param srcx The X coordinate of the source rectangle.
 * @param srcy The Y coordinate of the source rectangle.
 * @param w The width of the source rectangle.
 * @param h The height of the source rectangle.
 * @param fn The transfer function to use.
 * @param dstl The destination layer.
 * @param dstx The X coordinate of the destination, where the source rectangle
 *             should be copied.
 * @param dsty The Y coordinate of the destination, where the source rectangle
 *             should be copied.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_transfer(guac_socket* socket, 
        const guac_layer* srcl, int srcx, int srcy, int w, int h,
        guac_transfer_function fn, const guac_layer* dstl, int dstx, int dsty);

/* LAYER INSTRUCTIONS */

/**
 * Sends a dispose instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The layer to dispose.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_dispose(guac_socket* socket, const guac_layer* layer);

/**
 * Sends a move instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The layer to move.
 * @param parent The parent layer the specified layer will be positioned
 *               relative to.
 * @param x The X coordinate of the layer.
 * @param y The Y coordinate of the layer.
 * @param z The Z index of the layer, relative to other layers in its parent.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_move(guac_socket* socket, const guac_layer* layer,
        const guac_layer* parent, int x, int y, int z);

/**
 * Sends a shade instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The layer to shade.
 * @param a The alpha value of the layer.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_shade(guac_socket* socket, const guac_layer* layer,
        int a);

/**
 * Sends a size instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param layer The layer to resize.
 * @param w The new width of the layer.
 * @param h The new height of the layer.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_size(guac_socket* socket, const guac_layer* layer,
        int w, int h);

/* TEXT INSTRUCTIONS */

/**
 * Sends a clipboard instruction over the given guac_socket connection.
 *
 * If an error occurs sending the instruction, a non-zero value is
 * returned, and guac_error is set appropriately.
 *
 * @param socket The guac_socket connection to use.
 * @param stream The stream to use.
 * @param mimetype The mimetype of the clipboard data being sent.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_clipboard(guac_socket* socket, const guac_stream* stream,
        const char* mimetype);

/**
 * Sends a name instruction over the given guac_socket connection.
 *
 * @param socket The guac_socket connection to use.
 * @param name The name to send within the name instruction.
 * @return Zero on success, non-zero on error.
 */
int guac_protocol_send_name(guac_socket* socket, const char* name);

/**
 * Decodes the given base64-encoded string in-place. The base64 string must
 * be NULL-terminated.
 *
 * @param base64 The base64-encoded string to decode.
 * @return The number of bytes resulting from the decode operation.
 */
int guac_protocol_decode_base64(char* base64);

#endif

