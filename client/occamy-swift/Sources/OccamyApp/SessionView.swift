// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import OccamyKit
import SwiftUI

/// How a session window shows a remote screen.
enum Scaling: String, CaseIterable, Identifiable {
    /// Scaled to the window, whole.
    case fit
    /// One remote pixel to one device pixel, scrolled.
    case actual
    /// The remote resized to the window, for the owner.
    case remote

    var id: Self { self }

    var title: String {
        switch self {
        case .fit: "Scale to Window"
        case .actual: "Actual Size"
        case .remote: "Resize Remote to Window"
        }
    }
}

/// SessionView is a session's window: it opens the session, shows it,
/// and has a bar of controls in Liquid Glass that appears when the
/// pointer reaches the top edge, as VNC Viewer's toolbar does.
struct SessionView: View {
    let request: SessionRequest
    @Environment(Store.self) private var store
    @Environment(\.openWindow) private var openWindow
    @Environment(\.dismissWindow) private var dismissWindow
    @State private var phase = Phase.connecting
    @State private var attempt = 0
    @State private var scaling = Scaling.fit
    @State private var sendsShortcuts = false
    @State private var bar = false
    @State private var overBar = false
    @State private var hide: Task<Void, Never>?
    @State private var window: NSWindow?
    @State private var fullScreen = false
    @State private var notice: String?
    @State private var answered: Set<String> = []
    @State private var fitted = false
    /// The session to join again, as its owner, rather than open anew.
    @State private var rejoin: String?

    enum Phase {
        case connecting
        case open(SessionModel)
        case failed(String, signIn: Bool)
    }

    private var server: Server? { store.server(request.server) }

    var body: some View {
        content
            .frame(minWidth: 400, minHeight: 260)
            .navigationTitle(request.name)
            .background(WindowReader { window = $0 })
            .task(id: attempt) { await connect() }
            .onDisappear { leave() }
            .onReceive(NotificationCenter.default.publisher(for: NSWindow.didChangeOcclusionStateNotification, object: window)) { _ in
                if case let .open(m) = phase, let window {
                    m.setVisible(window.occlusionState.contains(.visible))
                }
            }
            .onReceive(NotificationCenter.default.publisher(for: NSWindow.didBecomeKeyNotification, object: window)) { _ in
                if case let .open(m) = phase {
                    m.syncClipboard()
                }
            }
            .onReceive(NotificationCenter.default.publisher(for: NSWindow.didEnterFullScreenNotification, object: window)) { _ in
                fullScreen = true
            }
            .onReceive(NotificationCenter.default.publisher(for: NSWindow.didExitFullScreenNotification, object: window)) { _ in
                fullScreen = false
            }
    }

    @ViewBuilder private var content: some View {
        switch phase {
        case .connecting:
            ProgressView("Connecting to \(request.name)…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case let .failed(message, signIn):
            ContentUnavailableView {
                Label("Could Not Connect", systemImage: "exclamationmark.triangle")
            } description: {
                Text(message)
            } actions: {
                if signIn {
                    Button("Sign In…") { signInAgain() }
                } else {
                    Button("Try Again") { attempt += 1 }
                }
            }
        case let .open(model):
            open(model)
        }
    }

    private func open(_ model: SessionModel) -> some View {
        ZStack(alignment: .top) {
            display(model)
            // The top edge shows the bar.
            Color.clear
                .frame(height: 6)
                .frame(maxWidth: .infinity)
                .contentShape(Rectangle())
                .onHover { if $0 { show() } }
            if bar || model.ended != nil {
                controls(model)
                    .padding(.top, 10)
                    .onHover { inside in
                        overBar = inside
                        if !inside {
                            show()
                        }
                    }
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
            if let notice {
                Text(notice)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 8)
                    .glassEffect(.regular, in: .capsule)
                    .padding(.top, 64)
                    .transition(.opacity)
            }
            if let end = model.ended {
                ended(end, model)
            }
        }
        .animation(.snappy, value: bar)
        .animation(.snappy, value: notice)
        .navigationSubtitle(model.title ?? "")
        .sheet(item: Binding { model.prompts.first } set: { _ in }) { p in
            PromptSheet(prompt: p, saved: saved(p) != nil) { answers, save in
                if save {
                    store.keychain.set(value: answers, for: promptKey(p))
                }
                model.answer(p, with: answers)
            } end: {
                model.close()
            }
            .interactiveDismissDisabled()
        }
        .onChange(of: model.prompts.first?.id, initial: true) {
            // A saved answer goes once a session; a prompt that comes
            // again, as for a wrong password, is the user's.
            guard let p = model.prompts.first, let answers = saved(p), !answered.contains(promptKey(p)) else {
                return
            }
            answered.insert(promptKey(p))
            model.answer(p, with: answers)
        }
        .onChange(of: model.size) { old, new in
            if !fitted, old == .zero, new != .zero, model.sessionClass == .graphical {
                fitted = true
                fitWindow(to: new)
            }
        }
    }

    @ViewBuilder private func display(_ model: SessionModel) -> some View {
        if model.sessionClass == .terminal {
            TerminalView(model: model)
        } else {
            switch scaling {
            case .fit:
                FramebufferView(model: model, sendsShortcuts: sendsShortcuts)
            case .actual:
                let scale = window?.backingScaleFactor ?? 2
                ScrollView([.horizontal, .vertical]) {
                    FramebufferView(model: model, sendsShortcuts: sendsShortcuts)
                        .frame(width: model.size.width / scale, height: model.size.height / scale)
                }
                .background(.black)
            case .remote:
                GeometryReader { g in
                    FramebufferView(model: model, sendsShortcuts: sendsShortcuts)
                        .task(id: g.size) {
                            // Once the window stays one size: a remote
                            // pixel is a point, as for the web client.
                            try? await Task.sleep(for: .milliseconds(300))
                            if !Task.isCancelled {
                                model.requestResize(width: Int(g.size.width), height: Int(g.size.height))
                            }
                        }
                }
            }
        }
    }

    private func controls(_ model: SessionModel) -> some View {
        GlassEffectContainer {
            HStack(spacing: 6) {
                if model.sessionClass == .graphical {
                    Menu {
                        Picker("Scaling", selection: $scaling) {
                            ForEach(Scaling.allCases.filter { $0 != .remote || model.role.resize }) { s in
                                Text(s.title).tag(s)
                            }
                        }
                        .pickerStyle(.inline)
                    } label: {
                        Image(systemName: "aspectratio")
                    }
                    .menuIndicator(.hidden)
                    .help("Scaling")
                }
                button(fullScreen ? "Exit Full Screen" : "Full Screen",
                       fullScreen ? "arrow.down.right.and.arrow.up.left" : "arrow.up.left.and.arrow.down.right") {
                    window?.toggleFullScreen(nil)
                }
                if model.sessionClass == .graphical && model.role.input {
                    button("Send Ctrl-Alt-Del", "lock.desktopcomputer") { ctrlAltDel(model) }
                    Toggle(isOn: $sendsShortcuts) {
                        Image(systemName: "command")
                    }
                    .toggleStyle(.button)
                    .help(sendsShortcuts ? "⌘ shortcuts go to the remote" : "⌘ shortcuts stay with the app")
                }
                Toggle(isOn: Binding { model.clipboardEnabled } set: { model.clipboardEnabled = $0 }) {
                    Image(systemName: model.clipboardEnabled ? "list.clipboard" : "clipboard")
                }
                .toggleStyle(.button)
                .help(model.clipboardEnabled ? "The clipboard is shared" : "The clipboard is not shared")
                if model.role.manage {
                    Menu {
                        Button("Copy Link with Control") { share(.viewer, model) }
                        Button("Copy View-Only Link") { share(.readOnly, model) }
                    } label: {
                        Image(systemName: "link")
                    }
                    .menuIndicator(.hidden)
                    .help("Share the session")
                }
                button(model.role.manage ? "End Session" : "Disconnect", "xmark") {
                    disconnect(model)
                }
            }
            .buttonStyle(.borderless)
            .controlSize(.large)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .glassEffect(.regular.interactive(), in: .capsule)
        }
    }

    private func button(_ title: String, _ image: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: image)
                .frame(width: 24, height: 24)
        }
        .help(title)
        .accessibilityLabel(title)
    }

    private func ended(_ end: End, _ model: SessionModel) -> some View {
        let e = Ending(end)
        return ContentUnavailableView {
            Label(e.title, systemImage: "bolt.horizontal.circle")
        } description: {
            Text(e.detail)
            if !e.technical.isEmpty {
                Text(e.technical)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } actions: {
            if e.signIn {
                Button("Sign In…") { signInAgain() }
            } else {
                Button("Connect Again") {
                    // The owner's session may run on for its grace.
                    rejoin = e.rejoin && model.role == .owner ? model.session.sessionID : nil
                    attempt += 1
                }
            }
            Button("Close") { dismissWindow() }
        }
        .padding(24)
        .glassEffect(.regular, in: .rect(cornerRadius: 24))
        .frame(maxWidth: 480, maxHeight: .infinity)
    }

    // MARK: - Actions

    private func show() {
        bar = true
        hide?.cancel()
        hide = Task {
            try? await Task.sleep(for: .seconds(2))
            if !Task.isCancelled && !overBar {
                bar = false
            }
        }
    }

    private func flash(_ text: String) {
        notice = text
        Task {
            try? await Task.sleep(for: .seconds(2.5))
            if notice == text {
                notice = nil
            }
        }
    }

    private func connect() async {
        phase = .connecting
        fitted = false
        guard let server else {
            phase = .failed("The server was removed.", signIn: false)
            return
        }
        do {
            let token = try await store.accessToken(server)
            let size = window?.contentLayoutRect.size ?? CGSize(width: 1280, height: 800)
            let options = Options(name: clientName, width: Int(size.width), height: Int(size.height),
                                  pixelRatio: Double(window?.backingScaleFactor ?? 2))
            let session: Session
            if let id = rejoin {
                rejoin = nil
                session = try await Session.dial(url: server.api.sessionStream(id), token: token, options: options)
            } else if let c = request.connection {
                session = try await Session.dial(url: server.api.connectionStream(c.id), token: token, options: options)
            } else if let t = request.target {
                let opened = try await server.api.open(t, token: token)
                session = try await Session.dial(url: server.api.sessionStream(opened.id), token: opened.streamToken, options: options)
            } else {
                return
            }
            let model = SessionModel(session: session)
            phase = .open(model)
            await model.run()
        } catch is SignInNeeded {
            phase = .failed("Sign in to \(server.name) again.", signIn: true)
        } catch let e as DialError {
            let ending = Ending(End(code: e.closeCode, reason: e.closeCode == 0 ? "" : e.message, error: e.message))
            phase = .failed(e.closeCode == 0 ? e.message : "\(ending.title): \(ending.detail)", signIn: ending.signIn)
        } catch let e as APIError {
            phase = .failed(e.message, signIn: e.signIn)
        } catch is CancellationError {
        } catch {
            phase = .failed(error.localizedDescription, signIn: false)
        }
    }

    /// leave leaves the session as its window closes, keeping a picture
    /// of it for its card. The owner's session runs on for occamyd's
    /// owner grace.
    private func leave() {
        guard case let .open(model) = phase else {
            return
        }
        if model.sessionClass == .graphical, let c = request.connection, let image = model.session.screen.image() {
            store.saveThumbnail(image, server: request.server, connection: c.id)
        }
        model.close()
    }

    /// disconnect leaves the session and closes the window; the owner
    /// ends the session for everyone, as the web client's End does.
    private func disconnect(_ model: SessionModel) {
        model.close()
        if model.role.manage, let server {
            let id = model.session.sessionID
            Task {
                // The session ends anyway once the owner's grace runs
                // out, so a failure here is left at that.
                if let token = try? await store.accessToken(server) {
                    try? await server.api.end(session: id, token: token)
                }
            }
        }
        dismissWindow()
    }

    private func signInAgain() {
        store.signingIn = .init(server: server)
        openWindow(id: "main")
    }

    private func ctrlAltDel(_ model: SessionModel) {
        let keys = [Key(keysym: 0xffe3, usage: 0xe0), Key(keysym: 0xffe9, usage: 0xe2), Key(keysym: 0xffff, usage: 0x4c)]
        for k in keys {
            model.session.send(.key(down: true, k))
        }
        for k in keys.reversed() {
            model.session.send(.key(down: false, k))
        }
    }

    private func share(_ role: Share.Role, _ model: SessionModel) {
        guard let server else {
            return
        }
        Task {
            do {
                let token = try await store.accessToken(server)
                let id = model.session.sessionID
                let s = try await server.api.share(session: id, role: role, token: token)
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(server.api.shareLink(session: id, share: s.token).absoluteString, forType: .string)
                flash(role == .viewer ? "Link with control copied, for an hour" : "View-only link copied, for an hour")
            } catch {
                flash(error.localizedDescription)
            }
        }
    }

    /// fitWindow sizes the window to show a screen of size at a point a
    /// pixel, within the display, as VNC Viewer opens at the remote's
    /// size.
    private func fitWindow(to size: CGSize) {
        guard let window, !fullScreen, let screen = window.screen ?? NSScreen.main else {
            return
        }
        let frame = window.frame
        let content = window.contentLayoutRect.size
        let chrome = CGSize(width: frame.width - content.width, height: frame.height - content.height)
        let room = CGSize(width: screen.visibleFrame.width - chrome.width, height: screen.visibleFrame.height - chrome.height)
        let scale = min(1, room.width / size.width, room.height / size.height)
        let want = CGSize(width: (size.width * scale).rounded(), height: (size.height * scale).rounded())
        var f = window.frameRect(forContentRect: CGRect(origin: .zero, size: want))
        f.origin = CGPoint(x: frame.minX, y: frame.maxY - f.height)
        f = f.offsetBy(dx: min(0, screen.visibleFrame.maxX - f.maxX), dy: max(0, screen.visibleFrame.minY - f.minY))
        window.setFrame(f, display: true, animate: true)
    }

    // MARK: - Saved answers

    private func promptKey(_ p: Prompt) -> String {
        "prompt.\(request.server).\(request.connection?.id ?? request.target?.address ?? "").\(p.name)"
    }

    private func saved(_ p: Prompt) -> [String]? {
        guard let a = store.keychain.value([String].self, promptKey(p)), a.count == p.fields.count else {
            return nil
        }
        return a
    }
}

/// PromptSheet asks the owner for what the remote asks, such as a
/// password the connection lacks. Nothing is kept unless the user saves
/// it, in the Keychain.
struct PromptSheet: View {
    let prompt: Prompt
    let saved: Bool
    let answer: ([String], Bool) -> Void
    let end: () -> Void
    @State private var answers: [String] = []
    @State private var save = false

    var body: some View {
        Form {
            Section {
                ForEach(prompt.fields.indices, id: \.self) { i in
                    let f = prompt.fields[i]
                    let label = f.label.isEmpty ? "Answer \(i + 1)" : f.label
                    if f.secret {
                        SecureField(label, text: binding(i))
                    } else {
                        TextField(label, text: binding(i))
                    }
                }
                if prompt.fields.contains(where: \.secret) {
                    Toggle("Save in the Keychain", isOn: $save)
                }
            } header: {
                Text(prompt.name.isEmpty ? "The remote asks" : String(prompt.name.prefix(200)))
            } footer: {
                if !prompt.instruction.isEmpty {
                    Text(String(prompt.instruction.prefix(1000)))
                        .foregroundStyle(.secondary)
                }
                if saved {
                    Text("The saved answer was not taken; it is replaced if you save again.")
                        .foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 420)
        .onAppear { answers = Array(repeating: "", count: prompt.fields.count) }
        .toolbar {
            // The remote waits for the answer, so the sheet's other way
            // out ends the session.
            ToolbarItem(placement: .cancellationAction) {
                Button("End Session", role: .destructive) { end() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Continue") { answer(answers, save) }
            }
        }
    }

    private func binding(_ i: Int) -> Binding<String> {
        Binding { i < answers.count ? answers[i] : "" } set: { if i < answers.count { answers[i] = $0 } }
    }
}

/// WindowReader tells the view which window it is in.
struct WindowReader: NSViewRepresentable {
    let found: (NSWindow) -> Void

    func makeNSView(context: Context) -> Reader {
        let v = Reader()
        v.found = found
        return v
    }

    func updateNSView(_ v: Reader, context: Context) {}

    final class Reader: NSView {
        var found: ((NSWindow) -> Void)?

        // Only a window, never its leaving one: SwiftUI puts a new reader
        // in the window as it opens and as the session connects again, and
        // the old one leaves after the new one has found it, so that its
        // nil, coming last, lost the window.
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard let w = window else {
                return
            }
            DispatchQueue.main.async { [found] in found?(w) }
        }
    }
}
