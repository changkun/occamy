// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import COccamy
import CoreGraphics
import Foundation

/// Screen is a graphical session's picture: the app's copy of the
/// core's framebuffer, which the session's thread brings up to date by
/// each frame's dirty rectangles before it delivers the frame. Any
/// thread may read it.
public final class Screen: @unchecked Sendable {
    private let lock = NSLock()
    private var pixels: UnsafeMutableRawPointer?
    private var _width = 0
    private var _height = 0
    private var stride = 0

    /// The screen's size in pixels; 0×0 before the first frame.
    public var size: (width: Int, height: Int) {
        lock.withLock { (_width, _height) }
    }

    deinit {
        pixels?.deallocate()
    }

    /// resize makes the screen width×height pixels, black.
    func resize(width: Int, height: Int) {
        lock.withLock {
            pixels?.deallocate()
            pixels = nil
            _width = max(width, 0)
            _height = max(height, 0)
            // Rows on 64 bytes, as Core Graphics likes them.
            stride = (_width * 4 + 63) & ~63
            if _width > 0 && _height > 0 {
                let p = UnsafeMutableRawPointer.allocate(byteCount: stride * _height, alignment: 64)
                p.initializeMemory(as: UInt8.self, repeating: 0, count: stride * _height)
                pixels = p
            }
        }
    }

    /// read copies the rectangles of the core's framebuffer into the
    /// screen; it returns the core's error for one it could not.
    func read(from session: occamy_session, rects: [CGRect]) -> Int32 {
        lock.withLock {
            guard let pixels else {
                return OCCAMY_OK
            }
            for r in rects {
                let res = occamy_framebuffer_read(
                    session, pixels.assumingMemoryBound(to: UInt8.self), stride,
                    Int32(_width), Int32(_height),
                    occamy_rect(x: Int32(r.minX), y: Int32(r.minY), width: Int32(r.width), height: Int32(r.height)))
                if res != OCCAMY_OK {
                    return res
                }
            }
            return OCCAMY_OK
        }
    }

    /// image returns a copy of the screen, or nil before it has a size.
    /// The remote's pixels are opaque sRGB.
    public func image() -> CGImage? {
        let copy: (data: Data, width: Int, height: Int, stride: Int)? = lock.withLock {
            guard let pixels else {
                return nil
            }
            return (Data(bytes: pixels, count: stride * _height), _width, _height, stride)
        }
        guard let copy, let provider = CGDataProvider(data: copy.data as CFData),
              let space = CGColorSpace(name: CGColorSpace.sRGB)
        else {
            return nil
        }
        return CGImage(
            width: copy.width, height: copy.height, bitsPerComponent: 8, bitsPerPixel: 32,
            bytesPerRow: copy.stride, space: space,
            bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.noneSkipLast.rawValue),
            provider: provider, decode: nil, shouldInterpolate: true, intent: .defaultIntent)
    }

    /// rgba returns the screen's pixels, RGBA with width×4 bytes a row.
    public func rgba() -> (width: Int, height: Int, pixels: [UInt8]) {
        lock.withLock {
            guard let pixels else {
                return (0, 0, [])
            }
            var out = [UInt8](repeating: 0, count: _width * 4 * _height)
            out.withUnsafeMutableBytes { dst in
                for y in 0..<_height {
                    (dst.baseAddress! + y * _width * 4).copyMemory(from: pixels + y * stride, byteCount: _width * 4)
                }
            }
            return (_width, _height, out)
        }
    }
}
