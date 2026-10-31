// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// The server's text, as the shell shows it. The protocol bounds a title,
// a prompt's strings, a viewer's name and a close reason only by the
// agreed message size, 4 MiB by default, so the shell clips each before
// it reaches the page.

/** The longest title, viewer name or close reason the shell shows, in characters. */
export const MAX_LABEL = 200

/** The longest prompt instruction the shell shows, in characters. */
export const MAX_PARAGRAPH = 2000

/** The MIME type of the text the shell puts on the remote's clipboard. */
export const TEXT_MIME = 'text/plain;charset=utf-8'

/**
 * clip returns s cut to at most max characters, counted as code points so
 * that no surrogate pair is split, ending in an ellipsis when it was
 * cut. Control characters other than tab and line breaks become spaces,
 * so a label cannot carry escapes or a bidirectional override to the page.
 */
export function clip(s: string, max: number): string {
  // eslint-disable-next-line no-control-regex
  const clean = s.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f‪-‮⁦-⁩]/g, ' ')
  const chars = Array.from(clean)
  if (chars.length <= max) {
    return clean
  }
  return chars.slice(0, Math.max(max - 1, 0)).join('') + '…'
}

/**
 * clipboardText returns the text of a clipboard the remote sent, as
 * text/plain and the like, or null for content that is not text. The
 * backends send text in UTF-8; bytes that are not UTF-8 become U+FFFD.
 */
export function clipboardText(mime: string, data: Uint8Array): string | null {
  const type = mime.split(';')[0].trim().toLowerCase()
  if (!type.startsWith('text/')) {
    return null
  }
  return new TextDecoder('utf-8', { fatal: false }).decode(data)
}
