// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import AuthenticationServices
import Foundation
import Observation
import OccamyKit

/// A server: an occamyd the app signs in to, by its URL.
struct Server: Codable, Identifiable, Hashable {
    var id = UUID()
    var url: URL
    /// The user who signed in with a password, if one did.
    var username: String?

    var name: String {
        guard let host = url.host() else {
            return url.absoluteString
        }
        return url.port.map { "\(host):\($0)" } ?? host
    }

    var api: API { API(base: url) }

    /// Server parses what a user typed as a server's URL: https unless
    /// it says http.
    init?(address: String) {
        var s = address.trimmingCharacters(in: .whitespaces)
        if !s.contains("://") {
            s = "https://" + s
        }
        guard var c = URLComponents(string: s), c.scheme == "https" || c.scheme == "http",
              let host = c.host, !host.isEmpty
        else {
            return nil
        }
        c.query = nil
        c.fragment = nil
        if !c.path.hasSuffix("/") {
            c.path += "/"
        }
        guard let url = c.url else {
            return nil
        }
        self.url = url
    }
}

/// What opens a session window: a connection of a server, or an
/// ad-hoc target. Each request is a window of its own.
struct SessionRequest: Codable, Hashable {
    var id = UUID()
    var server: Server.ID
    var connection: Connection?
    var target: AdHocTarget?

    var name: String { connection?.name ?? target?.address ?? "Session" }
    var `protocol`: String { connection?.protocol ?? target?.protocol ?? "" }
}

/// SignInNeeded is thrown when the app holds no valid access token of a
/// server and cannot sign in again on its own.
struct SignInNeeded: Error, LocalizedError {
    var errorDescription: String? { "Sign in to the server first." }
}

/// Store holds the servers, their sign-ins and connections, for every
/// window of the app. Servers are kept in the preferences; their access
/// tokens, and passwords the user saved, in the Keychain.
@MainActor
@Observable
final class Store {
    private(set) var servers: [Server] = []
    var selection: Server.ID?
    /// Each signed-in server's connections, as last listed.
    private(set) var connections: [Server.ID: Connections] = [:]
    /// Why listing a server's connections failed.
    private(set) var problems: [Server.ID: String] = [:]
    /// The servers the app holds an access token of.
    private(set) var signedIn: Set<Server.ID> = []
    /// The server whose sign-in sheet shows, or a new one.
    var signingIn: SignIn?
    // Counts the thumbnails saved, so that the cards show the new ones.
    private var thumbnailsSaved = 0

    struct SignIn: Identifiable {
        var id = UUID()
        var server: Server?
    }

    @ObservationIgnored let keychain = Keychain(service: "de.changkun.occamy")
    @ObservationIgnored let thumbnails = Thumbnails()
    @ObservationIgnored private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: "servers"), let s = try? JSONDecoder().decode([Server].self, from: data) {
            servers = s
        }
        selection = servers.first?.id
        signedIn = Set(servers.filter { keychain.data("token.\($0.id)") != nil }.map(\.id))
    }

    func server(_ id: Server.ID?) -> Server? {
        servers.first { $0.id == id }
    }

    private func save() {
        defaults.set(try? JSONEncoder().encode(servers), forKey: "servers")
    }

    /// add adds a server, or updates the one with its ID.
    func add(_ server: Server) {
        if let i = servers.firstIndex(where: { $0.id == server.id }) {
            servers[i] = server
        } else {
            servers.append(server)
        }
        save()
    }

    func remove(_ server: Server) {
        servers.removeAll { $0.id == server.id }
        connections[server.id] = nil
        problems[server.id] = nil
        setToken(nil, for: server)
        keychain.set(nil, for: "password.\(server.id)")
        thumbnails.remove(server: server.id)
        if selection == server.id {
            selection = servers.first?.id
        }
        save()
    }

    // MARK: - Signing in

    private func setToken(_ t: AccessToken?, for server: Server) {
        keychain.set(value: t, for: "token.\(server.id)")
        if t == nil {
            signedIn.remove(server.id)
        } else {
            signedIn.insert(server.id)
        }
    }

    /// accessToken returns a valid access token of the server, signing
    /// in again with a saved password once the last one expired: occamyd
    /// gives no refresh tokens.
    func accessToken(_ server: Server) async throws -> String {
        if let t = keychain.value(AccessToken.self, "token.\(server.id)"), !t.expired {
            return t.token
        }
        if let user = server.username, let data = keychain.data("password.\(server.id)"),
           let password = String(data: data, encoding: .utf8) {
            let t = try await server.api.signIn(username: user, password: password)
            setToken(t, for: server)
            return t.token
        }
        throw SignInNeeded()
    }

    /// signIn signs in to the server with a password, which it keeps in
    /// the Keychain if remember says so.
    func signIn(_ server: Server, username: String, password: String, remember: Bool) async throws {
        let t = try await server.api.signIn(username: username, password: password)
        var s = server
        s.username = username
        setToken(t, for: s)
        keychain.set(remember ? Data(password.utf8) : nil, for: "password.\(s.id)")
        add(s)
        selection = s.id
        await refresh(s)
    }

    /// The return URL of the app's OIDC sign-in, which occamyd must list
    /// in auth.oidc.return_urls.
    static let oidcReturn = URL(string: "occamy://oidc")!

    /// signInOIDC signs in to the server through its OIDC provider, in a
    /// browser sheet over anchor, with PKCE.
    func signInOIDC(_ server: Server, anchor: ASPresentationAnchor) async throws {
        let pkce = PKCE()
        let login = server.api.oidcLogin(returnURL: Self.oidcReturn, challenge: pkce.challenge)
        let back = try await WebSignIn(anchor: anchor).run(login, scheme: Self.oidcReturn.scheme!)
        switch OIDCOutcome(returnURL: back) {
        case let .code(code):
            let t = try await server.api.oidcToken(code: code, verifier: pkce.verifier)
            var s = server
            s.username = nil
            setToken(t, for: s)
            keychain.set(nil, for: "password.\(s.id)")
            add(s)
            selection = s.id
            await refresh(s)
        case let .error(e):
            throw APIError(status: 0, code: e, message: OIDCOutcome.message(e))
        case nil:
            throw APIError(status: 0, code: "bad_response", message: "The sign-in came back without a code.")
        }
    }

    func signOut(_ server: Server) {
        setToken(nil, for: server)
        keychain.set(nil, for: "password.\(server.id)")
        connections[server.id] = nil
    }

    // MARK: - Connections

    func thumbnail(server: Server.ID, connection: String) -> NSImage? {
        _ = thumbnailsSaved
        return thumbnails.image(server: server, connection: connection)
    }

    func saveThumbnail(_ image: CGImage, server: Server.ID, connection: String) {
        thumbnails.save(image, server: server, connection: connection)
        thumbnailsSaved += 1
    }

    /// refresh lists the server's connections, asking for a sign-in if
    /// the server refuses the token.
    func refresh(_ server: Server) async {
        do {
            let token = try await accessToken(server)
            connections[server.id] = try await server.api.connections(token: token)
            problems[server.id] = nil
        } catch let e as APIError where e.signIn {
            setToken(nil, for: server)
            connections[server.id] = nil
            problems[server.id] = e.message
        } catch is SignInNeeded {
            problems[server.id] = nil
        } catch {
            problems[server.id] = error.localizedDescription
        }
    }
}

/// WebSignIn runs a sign-in in the system's browser sheet until the
/// browser goes to a URL of scheme.
@MainActor
final class WebSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    private let anchor: ASPresentationAnchor
    // The browser sheet lives as long as its sign-in.
    private var session: ASWebAuthenticationSession?

    init(anchor: ASPresentationAnchor) {
        self.anchor = anchor
    }

    func run(_ url: URL, scheme: String) async throws -> URL {
        try await withCheckedThrowingContinuation { c in
            let s = ASWebAuthenticationSession(url: url, callbackURLScheme: scheme) { url, error in
                if let url {
                    c.resume(returning: url)
                } else {
                    c.resume(throwing: error ?? CancellationError())
                }
            }
            s.presentationContextProvider = self
            // The sign-in is the user's at the provider, as in their
            // browser.
            s.prefersEphemeralWebBrowserSession = false
            session = s
            if !s.start() {
                c.resume(throwing: APIError(status: 0, code: "unavailable", message: "The sign-in could not start."))
            }
        }
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated { anchor }
    }
}
