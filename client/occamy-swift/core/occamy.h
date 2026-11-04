// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// occamy.h is the C interface of Occamy's Go client core
// (changkun.de/x/occamy/client), which liboccamy.a exports for the
// macOS app (specs/010-native-swift-client.md).
//
// Rules:
//   - Every function may be called from any thread. occamy_next blocks,
//     so a caller pulls events on a thread of its own.
//   - Strings are UTF-8 and NUL-terminated. Every pointer the core
//     returns, in an event or otherwise, is the caller's, allocated with
//     malloc: free an event with occamy_event_free, a dial error with
//     occamy_dial_error_free, and anything else with occamy_free.
//   - The core keeps no pointer the caller passes beyond the call.
//   - Input functions return the input's sequence number in *seq, if
//     seq is not NULL, which a frame's last_seq reports once the server
//     applied it.
#ifndef OCCAMY_H
#define OCCAMY_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

// A session: a handle the core gives out, valid until occamy_release.
typedef uintptr_t occamy_session;

// Results.
#define OCCAMY_OK 0
#define OCCAMY_TIMEOUT 1          // occamy_next: no event before the timeout
#define OCCAMY_ERR -1             // a bad argument or a failure
#define OCCAMY_ERR_CLOSED -2      // the session ended, or the handle is unknown
#define OCCAMY_ERR_READ_ONLY -3   // input from a read-only viewer
#define OCCAMY_ERR_NOT_OWNER -4   // a prompt reply from a viewer other than the owner

// Roles and session classes, as protocol v1 numbers them.
#define OCCAMY_ROLE_OWNER 1
#define OCCAMY_ROLE_VIEWER 2
#define OCCAMY_ROLE_READ_ONLY 3
#define OCCAMY_CLASS_TERMINAL 1
#define OCCAMY_CLASS_GRAPHICAL 2

// Pointer buttons, a bit each, and wheel modes.
#define OCCAMY_BUTTON_LEFT 1
#define OCCAMY_BUTTON_MIDDLE 2
#define OCCAMY_BUTTON_RIGHT 4
#define OCCAMY_BUTTON_BACK 8
#define OCCAMY_BUTTON_FORWARD 16
#define OCCAMY_WHEEL_PIXELS 0
#define OCCAMY_WHEEL_LINES 1

// What occamy_dial asks for.
typedef struct {
  const char *name;       // the client and its version; NULL for the core's default
  uint16_t width, height; // the screen to ask for, in the remote's pixels; 0 leaves it to the remote
  uint16_t cols, rows;    // the terminal to ask for, in cells; 0 for the server's default
  uint16_t pixel_ratio;   // device pixels per point, in hundredths; 0 for 100
  bool hidden;            // start hidden: no frames until occamy_view shows it
} occamy_options;

// Why occamy_dial failed.
typedef struct {
  int32_t close_code; // the server's close code if it closed the connection, such as 4000 unauthorized; else 0
  char *close_reason; // the server's reason if it closed the connection, such as "failed"; else NULL
  char *message;      // for people and logs
  char *session_id;   // the session the server made and the client refused, which the caller may end; or NULL
} occamy_dial_error;

typedef struct {
  int32_t x, y, width, height;
} occamy_rect;

typedef struct {
  uint32_t viewer_id;
  int32_t role;          // OCCAMY_ROLE_*
  int32_t session_class; // OCCAMY_CLASS_*
  char *session_id;
} occamy_session_info;

typedef struct {
  char *label;
  bool secret; // a password: not echoed
} occamy_prompt_field;

// Event kinds.
#define OCCAMY_EVENT_FRAME 1
#define OCCAMY_EVENT_RESIZE 2
#define OCCAMY_EVENT_TERM_SNAPSHOT 3
#define OCCAMY_EVENT_TERM_DATA 4
#define OCCAMY_EVENT_CURSOR 5
#define OCCAMY_EVENT_CURSOR_MOVE 6
#define OCCAMY_EVENT_CLIPBOARD 7
#define OCCAMY_EVENT_CLIPBOARD_STATUS 8
#define OCCAMY_EVENT_BELL 9
#define OCCAMY_EVENT_TITLE 10
#define OCCAMY_EVENT_PROMPT 11
#define OCCAMY_EVENT_VIEWER_JOINED 12
#define OCCAMY_EVENT_VIEWER_LEFT 13
#define OCCAMY_EVENT_END 14

// An event of the session (client.Event). Fields a kind does not name
// are zero.
typedef struct {
  int32_t kind;
  // FRAME: the frame's ID; PROMPT: the prompt's; VIEWER_JOINED and
  // VIEWER_LEFT: the viewer's.
  uint32_t id;
  uint32_t last_seq;      // FRAME: the last input the server applied
  int64_t server_time_us; // FRAME: when the server read it, since the session started
  // CURSOR: the hot spot; CURSOR_MOVE: the pointer's position.
  int32_t x, y;
  // RESIZE: the new size, in pixels or cells; CURSOR: the image's size,
  // 0×0 to hide the pointer.
  int32_t width, height;
  // END: the close code; VIEWER_JOINED: the role; CLIPBOARD_STATUS: 1
  // if the remote's clipboard answers, 0 if not.
  int32_t code;
  occamy_rect *rects; // FRAME: the rectangles a graphical frame changed
  size_t rect_count;
  // TERM_SNAPSHOT and TERM_DATA: the bytes for the terminal emulator;
  // CURSOR: the image, RGBA with straight alpha, width×height×4 bytes;
  // CLIPBOARD: the content.
  uint8_t *data;
  size_t data_len;
  // TITLE: the title; CLIPBOARD: its MIME type; PROMPT: its name;
  // VIEWER_JOINED: the viewer's name; END: the reason.
  char *text;
  // PROMPT: the instruction; END: the client's error, if any.
  char *detail;
  occamy_prompt_field *fields; // PROMPT
  size_t field_count;
} occamy_event;

#ifndef OCCAMY_CGO // cgo declares the functions itself

// occamy_dial opens a session on url, a stream route of occamyd such as
// wss://host/api/v1/connections/lab/stream, with token, an access,
// session or share token, and returns once the server welcomed the
// client. It waits at most timeout_ms milliseconds, or forever if
// negative. On failure it fills *err, if err is not NULL.
int occamy_dial(const char *url, const char *token, const occamy_options *opts,
                int32_t timeout_ms, occamy_session *out, occamy_dial_error *err);
void occamy_dial_error_free(occamy_dial_error *err);

// occamy_next waits up to timeout_ms milliseconds, or forever if
// negative, for the session's next event, and returns OCCAMY_OK with it
// in *ev, OCCAMY_TIMEOUT, or OCCAMY_ERR_CLOSED once END was delivered.
// A FRAME is applied to the framebuffer as occamy_next returns it, and
// calling occamy_next again acknowledges it as shown.
int occamy_next(occamy_session s, int32_t timeout_ms, occamy_event *ev);
void occamy_event_free(occamy_event *ev);

// occamy_info describes the session; free info->session_id.
int occamy_info(occamy_session s, occamy_session_info *info);

// occamy_framebuffer_read copies the rectangle r of a graphical
// session's screen, as of the last FRAME occamy_next returned, into
// dst, a buffer of the whole screen in RGBA with stride bytes a row, at
// the same position. It fails if r is not within the screen, or if
// width and height are not the screen's.
int occamy_framebuffer_read(occamy_session s, uint8_t *dst, size_t stride,
                            int32_t width, int32_t height, occamy_rect r);

int occamy_key(occamy_session s, bool down, uint32_t keysym, uint16_t hid_usage, uint32_t *seq);
int occamy_text(occamy_session s, const char *text, uint32_t *seq);
int occamy_term_input(occamy_session s, const uint8_t *p, size_t n, uint32_t *seq);
int occamy_pointer(occamy_session s, uint16_t x, uint16_t y, uint8_t buttons, uint32_t *seq);
int occamy_wheel(occamy_session s, int32_t dx, int32_t dy, int32_t mode, uint32_t *seq);
int occamy_resize(occamy_session s, uint16_t width, uint16_t height, uint32_t *seq);
int occamy_clipboard(occamy_session s, const char *mime, const uint8_t *p, size_t n, uint32_t *seq);

// occamy_view says whether the session can be seen, and which part of
// the screen it shows; an empty viewport is the whole screen.
int occamy_view(occamy_session s, bool visible, occamy_rect viewport);

// occamy_prompt_reply answers the prompt id with n answers, one a field.
int occamy_prompt_reply(occamy_session s, uint32_t id, const char *const *answers, size_t n);

// occamy_close ends the session: occamy_next then returns END, and
// after it OCCAMY_ERR_CLOSED. The handle stays valid.
void occamy_close(occamy_session s);

// occamy_release ends the session if it has not ended and releases the
// handle. No call on s may be running or follow, occamy_next included.
void occamy_release(occamy_session s);

// occamy_error returns why the session's last call that returned
// OCCAMY_ERR failed, or NULL; free it.
char *occamy_error(occamy_session s);

void occamy_free(void *p);

#endif // OCCAMY_CGO
#endif // OCCAMY_H
