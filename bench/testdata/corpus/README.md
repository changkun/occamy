# Codec corpus

Frames of the bench desktop, recorded over VNC, on which `bench/codecs`
measures tile codecs for
[the graphics pipeline](../../../specs/008-native-architecture/graphics-pipeline.md).
The results are in [docs/benchmarks/codecs.md](../../../docs/benchmarks/codecs.md),
and `make codecs` measures again.

Every frame is the full 1024×768 screen as x11vnc sent it, stored as PNG
(with a palette where the frame has 256 colours or fewer, which changes no
pixel). The 13 files take 2.5 MiB (2,586,822 bytes).

| File | What is on the screen |
|------|------------------------|
| `desktop.png` | The idle scene (F1): the gradient wallpaper and the grey probe swatch |
| `terminal.png` | xterm with its default bitmap font, 120×40, showing `ls -l --color=always /usr/bin`, over the idle scene |
| `terminal-aa.png` | xterm with DejaVu Sans Mono 10 through Xft (antialiased), dark theme, showing `cat -n` of the desktop's `start.sh`, over the idle scene |
| `browser.png` | dillo 3.0.5 showing [page.html](../../vnccapture/page.html), a documentation page in DejaVu Sans through Xft, with the browser's toolbar |
| `photo.png` | The photograph (F6), generated from fractal noise |
| `drag-0.png` … `drag-2.png` | Three consecutive updates of the dragged window (F4) |
| `scroll-0.png` … `scroll-2.png` | Three consecutive updates of the scrolling text (F3) |
| `motion-0.png`, `motion-1.png` | Two consecutive updates of the full-screen plasma (F5) |

## How it was recorded

On 2026-10-30, with `make corpus` on the host described in
[docs/benchmarks.md](../../../docs/benchmarks.md) (Apple M5 Pro, podman
6.0.2). That target:

1. builds the bench desktop image (`docker/bench/desktop/Dockerfile`)
   with `EXTRA_PACKAGES="dillo procps xterm"`, tagged
   `occamy-bench-corpus`;
2. runs `go run ./bench/vnccapture`, which starts that image as the
   container `occamy-corpus` with its VNC server on `127.0.0.1:5959`,
   shows each scene, saves the frames here, and removes the container.

For the terminal and the browser, `vnccapture` starts xterm or dillo on
the display with `podman exec` over the idle scene; with no window
manager, their windows have no decorations. For the bench scenes it
presses their function keys. A still is a complete update; a sequence
is the screen after each update that x11vnc sent next, held in memory
until the sequence ends, so it is what a VNC backend would see. The
three scroll updates came within 42 ms, the three drag updates within
48 ms and the two motion updates within 20 ms.

The capture asks x11vnc for pixels in red, green, blue order and for the
cursor's shape as a pseudo-encoding, so the pointer is not painted into
the frames. For the photograph it also asks a second connection for
x11vnc's own blue-first order and checks that the two carry the same
image.

To re-record, run `make corpus` from the repository root. The terminal
listing and the browser's fonts come from Debian packages, so a later
recording can differ in detail; the bench scenes are generated from
fixed seeds and do not, apart from where a sequence's updates fall.
