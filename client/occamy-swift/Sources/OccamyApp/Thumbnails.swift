// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import ImageIO
import UniformTypeIdentifiers

/// Thumbnails keeps a picture of the last session the app showed of each
/// connection, in the app's Application Support folder, for the
/// connection's card. They stay on this Mac.
final class Thumbnails: Sendable {
    let folder: URL

    init(folder: URL? = nil) {
        self.folder = folder ?? URL.applicationSupportDirectory.appending(path: "Occamy/Thumbnails", directoryHint: .isDirectory)
    }

    private func file(server: UUID, connection: String) -> URL {
        // A connection's ID is occamyd's, so it is made safe as a file name.
        let name = connection.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "connection"
        return folder.appending(path: server.uuidString, directoryHint: .isDirectory).appending(path: name + ".png")
    }

    func image(server: UUID, connection: String) -> NSImage? {
        NSImage(contentsOf: file(server: server, connection: connection))
    }

    /// save keeps image, scaled to at most 640 pixels wide.
    func save(_ image: CGImage, server: UUID, connection: String) {
        let w = min(image.width, 640)
        let h = max(image.height * w / max(image.width, 1), 1)
        guard let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0,
                                  space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                  bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)
        else {
            return
        }
        ctx.interpolationQuality = .high
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
        guard let small = ctx.makeImage() else {
            return
        }
        let url = file(server: server, connection: connection)
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        guard let dest = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil) else {
            return
        }
        CGImageDestinationAddImage(dest, small, nil)
        CGImageDestinationFinalize(dest)
    }

    func remove(server: UUID) {
        try? FileManager.default.removeItem(at: folder.appending(path: server.uuidString, directoryHint: .isDirectory))
    }
}
