// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import OccamyKit
import SwiftUI

/// MainView is the app's main window: the servers in the sidebar, the
/// selected one's connections as cards, and a field in the toolbar that
/// searches them or takes an address, as VNC Viewer's does.
struct MainView: View {
    @Environment(Store.self) private var store
    @Environment(\.openWindow) private var openWindow
    @State private var query = ""
    @State private var hostKey: AdHocTarget?
    @State private var problem: String?

    var body: some View {
        @Bindable var store = store
        NavigationSplitView {
            List(selection: $store.selection) {
                Section("Servers") {
                    ForEach(store.servers) { s in
                        Label(s.name, systemImage: store.signedIn.contains(s.id) ? "server.rack" : "lock")
                            .tag(s.id)
                            .contextMenu {
                                Button("Sign In…") { store.signingIn = .init(server: s) }
                                Button("Sign Out") { store.signOut(s) }
                                    .disabled(!store.signedIn.contains(s.id))
                                Divider()
                                Button("Remove Server", role: .destructive) { store.remove(s) }
                            }
                    }
                }
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 220)
        } detail: {
            if let server = store.server(store.selection) {
                ConnectionsView(server: server, query: query) { open($0, on: server) }
            } else {
                ContentUnavailableView {
                    Label("No Server", systemImage: "server.rack")
                } description: {
                    Text("Add an occamyd server to see the connections it offers you.")
                } actions: {
                    Button("Add Server…") { store.signingIn = .init() }
                }
            }
        }
        // In the window's toolbar, as remote desktop clients have it, not
        // in the sidebar's: beside the window's buttons that one has room
        // for little, and once the sidebar had been hidden and shown it
        // moved the button into an overflow menu.
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button("Add Server", systemImage: "plus") { store.signingIn = .init() }
                    .help("Add an occamyd server")
            }
        }
        .searchable(text: $query, placement: .toolbar, prompt: "Search or enter an address")
        .onSubmit(of: .search) { submit() }
        .sheet(item: $store.signingIn) { s in
            SignInView(server: s.server)
        }
        .sheet(item: $hostKey) { t in
            HostKeyView(target: t) { t in
                if let server = store.server(store.selection) {
                    openWindow(value: SessionRequest(server: server.id, target: t))
                }
            }
        }
        .alert("Cannot Open", isPresented: Binding { problem != nil } set: { if !$0 { problem = nil } }) {
            Button("OK") { problem = nil }
        } message: {
            Text(problem ?? "")
        }
    }

    private func open(_ connection: Connection, on server: Server) {
        openWindow(value: SessionRequest(server: server.id, connection: connection))
    }

    /// submit opens what the field names: an address, as an ad-hoc
    /// target, or else the first connection that matches.
    private func submit() {
        guard let server = store.server(store.selection) else {
            return
        }
        if let target = AdHocTarget(address: query) {
            guard store.connections[server.id]?.adhocTargets == true else {
                problem = "\(server.name) does not open addresses of its own choosing, only its connections."
                return
            }
            if target.protocol == "ssh" {
                hostKey = target
            } else {
                openWindow(value: SessionRequest(server: server.id, target: target))
            }
            return
        }
        if let c = matches(store.connections[server.id]?.connections ?? [], query).first {
            open(c, on: server)
        }
    }
}

/// matches returns the connections whose name, ID or protocol has query
/// in it, in their order.
func matches(_ connections: [Connection], _ query: String) -> [Connection] {
    let q = query.trimmingCharacters(in: .whitespaces)
    guard !q.isEmpty, AdHocTarget(address: q) == nil else {
        return connections
    }
    return connections.filter {
        $0.name.localizedCaseInsensitiveContains(q) || $0.id.localizedCaseInsensitiveContains(q) || $0.protocol == q.lowercased()
    }
}

/// ConnectionsView shows a server's connections as cards.
struct ConnectionsView: View {
    let server: Server
    let query: String
    let open: (Connection) -> Void
    @Environment(Store.self) private var store
    @State private var selected: Connection.ID?

    var body: some View {
        Group {
            if let list = store.connections[server.id] {
                let shown = matches(list.connections, query)
                if shown.isEmpty {
                    if query.isEmpty {
                        ContentUnavailableView("No Connections", systemImage: "rectangle.on.rectangle.slash",
                                               description: Text("\(server.name) offers you no connections."))
                    } else if AdHocTarget(address: query) != nil {
                        ContentUnavailableView("Open \(query)", systemImage: "arrow.right.circle",
                                               description: Text("Press Return to open the address."))
                    } else {
                        ContentUnavailableView.search(text: query)
                    }
                } else {
                    grid(shown)
                }
            } else if !store.signedIn.contains(server.id) {
                ContentUnavailableView {
                    Label("Signed Out", systemImage: "lock")
                } description: {
                    Text(store.problems[server.id] ?? "Sign in to \(server.name) to see its connections.")
                } actions: {
                    Button("Sign In…") { store.signingIn = .init(server: server) }
                }
            } else if let p = store.problems[server.id] {
                ContentUnavailableView {
                    Label("Cannot List Connections", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(p)
                } actions: {
                    Button("Try Again") { Task { await store.refresh(server) } }
                }
            } else {
                ProgressView()
            }
        }
        .navigationTitle(server.name)
        .navigationSubtitle(store.connections[server.id]?.adhocTargets == true ? "Takes addresses: vnc://, rdp://, ssh://" : "")
        .task(id: server.id) { await store.refresh(server) }
        .toolbar {
            ToolbarItem {
                Button("Refresh", systemImage: "arrow.clockwise") { Task { await store.refresh(server) } }
                    .help("List the connections again")
            }
        }
    }

    private func grid(_ shown: [Connection]) -> some View {
        ScrollView {
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 220, maximum: 320), spacing: 20)], spacing: 20) {
                ForEach(shown) { c in
                    ConnectionCard(connection: c, image: store.thumbnail(server: server.id, connection: c.id),
                                   selected: selected == c.id)
                        .onTapGesture(count: 2) { open(c) }
                        .simultaneousGesture(TapGesture().onEnded { selected = c.id })
                        .contextMenu {
                            Button("Open") { open(c) }
                        }
                }
            }
            .padding(20)
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.return) {
            guard let c = shown.first(where: { $0.id == selected }) else {
                return .ignored
            }
            open(c)
            return .handled
        }
    }
}

/// ConnectionCard is a connection: a picture of its last session, kept on
/// this Mac, its name and its protocol's symbol.
struct ConnectionCard: View {
    let connection: Connection
    let image: NSImage?
    let selected: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // The frame sets the card's size and the picture only fills
            // it, whole, so that every card in a row is as tall whatever
            // the shape of its remote's screen.
            Rectangle()
                .fill(.quaternary)
                .aspectRatio(16 / 10, contentMode: .fit)
                .overlay {
                    if let image {
                        Image(nsImage: image)
                            .resizable()
                            .aspectRatio(contentMode: .fit)
                    } else {
                        Image(systemName: symbol(connection.protocol))
                            .font(.system(size: 40, weight: .light))
                            .foregroundStyle(.secondary)
                    }
                }
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .strokeBorder(selected ? Color.accentColor : Color.secondary.opacity(0.2), lineWidth: selected ? 3 : 1)
            }
            Label(connection.name, systemImage: symbol(connection.protocol))
                .lineLimit(1)
                .truncationMode(.tail)
            Text(connection.protocol.uppercased())
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
        .accessibilityHint("Double-click to open")
    }
}

/// HostKeyView asks how to check an ssh address's host key, which an
/// ad-hoc ssh target needs.
struct HostKeyView: View {
    let target: AdHocTarget
    let open: (AdHocTarget) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var key = ""
    @State private var any = false

    var body: some View {
        Form {
            Section {
                TextField("Host key", text: $key, prompt: Text("SHA256:… or ssh-ed25519 AAAA…"))
                    .disabled(any)
                Toggle("Accept any host key", isOn: $any)
            } header: {
                Text("How should \(target.target) prove who it is?")
            } footer: {
                Text("Without a host key, a machine in the middle could pose as the server. `ssh-keyscan` or the server's administrator gives its key.")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .frame(width: 440)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Connect") {
                    var t = target
                    if any {
                        t.insecureIgnoreHostKey = true
                    } else {
                        t.hostKeys = [key.trimmingCharacters(in: .whitespacesAndNewlines)]
                    }
                    dismiss()
                    open(t)
                }
                .disabled(!any && key.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
    }
}

extension AdHocTarget: Identifiable {
    public var id: String { address }
}
