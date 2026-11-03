// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import SwiftTerm
import SwiftUI

/// TerminalDisplay shows a terminal session in SwiftTerm: the server's
/// screen diffs are escape sequences, its grid sets the emulator's, and
/// what the user types goes back as terminal input, as the web client's
/// xterm.js does. The owner's window sets the grid: once it stays one
/// size, the remote is asked for the cells that fit.
@MainActor
public final class TerminalDisplay: NSObject, SessionDisplay, TerminalViewDelegate {
    public let view: SwiftTerm.TerminalView
    public private(set) weak var model: SessionModel?

    // Set while the emulator's grid follows the remote's, whose change is
    // no request.
    private var following = false
    private var resize: Task<Void, Never>?

    /// How long the window stays one size before the owner asks for its
    /// grid.
    static let resizeDelay = Duration.milliseconds(300)

    public init(model: SessionModel) {
        view = SwiftTerm.TerminalView(frame: CGRect(x: 0, y: 0, width: 640, height: 400),
                                      font: NSFont.monospacedSystemFont(ofSize: 13, weight: .regular))
        self.model = model
        super.init()
        view.terminalDelegate = self
        view.setAccessibilityLabel("Remote terminal")
        model.display = self
    }

    /// grid is the emulator's size in cells.
    public var grid: (cols: Int, rows: Int) {
        let d = view.terminalDimensions
        return (d.cols, d.rows)
    }

    public func show(_ event: Event, of session: Session) {
        switch event {
        case let .termSnapshot(d):
            view.resetToInitialState()
            view.feed(byteArray: ArraySlice(d))
        case let .termData(d):
            view.feed(byteArray: ArraySlice(d))
        case let .resize(cols, rows):
            if grid != (cols, rows) {
                following = true
                view.resize(cols: cols, rows: rows)
                following = false
            }
        default:
            break
        }
    }

    // MARK: - TerminalViewDelegate

    public func sizeChanged(source: SwiftTerm.TerminalView, newCols: Int, newRows: Int) {
        guard !following, let model else {
            return
        }
        resize?.cancel()
        guard model.role.resize else {
            // A viewer's grid is the owner's.
            let size = model.size
            if size.width > 0 && size.height > 0 && (newCols, newRows) != (Int(size.width), Int(size.height)) {
                following = true
                view.resize(cols: Int(size.width), rows: Int(size.height))
                following = false
            }
            return
        }
        resize = Task { [weak self] in
            try? await Task.sleep(for: Self.resizeDelay)
            if !Task.isCancelled {
                self?.model?.requestResize(width: newCols, height: newRows)
            }
        }
    }

    public func send(source: SwiftTerm.TerminalView, data: ArraySlice<UInt8>) {
        guard let model, model.role.input, model.ended == nil else {
            return
        }
        model.session.send(.termInput(Data(data)))
    }

    public func setTerminalTitle(source: SwiftTerm.TerminalView, title: String) {
        model?.terminalTitle(title)
    }

    public func bell(source: SwiftTerm.TerminalView) {
        NSSound.beep()
    }

    public func requestOpenLink(source: SwiftTerm.TerminalView, link: String, params: [String: String]) {
        // The remote names the link; only the web's are opened.
        if let url = URL(string: link), url.scheme == "https" || url.scheme == "http" {
            NSWorkspace.shared.open(url)
        }
    }

    public func clipboardCopy(source: SwiftTerm.TerminalView, content: Data) {
        // OSC 52 from the remote, which the clipboard toggle governs.
        guard let model, model.clipboardEnabled, let text = String(data: content, encoding: .utf8) else {
            return
        }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    public func clipboardRead(source: SwiftTerm.TerminalView) -> Data? {
        nil // the remote does not read the local clipboard
    }

    public func hostCurrentDirectoryUpdate(source: SwiftTerm.TerminalView, directory: String?) {}
    public func scrolled(source: SwiftTerm.TerminalView, position: Double) {}
    public func iTermContent(source: SwiftTerm.TerminalView, content: ArraySlice<UInt8>) {}
    public func rangeChanged(source: SwiftTerm.TerminalView, startY: Int, endY: Int) {}
}

/// TerminalView shows a terminal session in SwiftUI.
public struct TerminalView: NSViewRepresentable {
    public let model: SessionModel

    public init(model: SessionModel) {
        self.model = model
    }

    public func makeCoordinator() -> TerminalDisplay {
        TerminalDisplay(model: model)
    }

    public func makeNSView(context: Context) -> SwiftTerm.TerminalView {
        context.coordinator.view
    }

    public func updateNSView(_ v: SwiftTerm.TerminalView, context: Context) {}
}
