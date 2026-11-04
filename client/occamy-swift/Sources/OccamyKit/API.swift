// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import CryptoKit
import Foundation

/// An access token of occamyd, from a sign-in.
public struct AccessToken: Codable, Sendable, Equatable {
    public var token: String
    /// When it expires, a little early, so that a request does not race
    /// its expiry.
    public var expires: Date

    public init(token: String, expires: Date) {
        self.token = token
        self.expires = expires
    }

    public var expired: Bool { expires <= Date() }
}

/// A connection of occamyd's registry.
public struct Connection: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    /// ssh, vnc, rdp or kubernetes.
    public var `protocol`: String

    public init(id: String, name: String, protocol: String) {
        self.id = id
        self.name = name
        self.protocol = `protocol`
    }
}

/// What GET /api/v1/connections answers.
public struct Connections: Codable, Sendable, Equatable {
    public var connections: [Connection]
    /// Whether POST /api/v1/sessions takes ad-hoc targets.
    public var adhocTargets: Bool

    enum CodingKeys: String, CodingKey {
        case connections
        case adhocTargets = "adhoc_targets"
    }
}

/// A session that POST /api/v1/sessions opened.
public struct OpenedSession: Sendable, Equatable {
    public var id: String
    /// A session token that joins the session's stream as its owner.
    public var streamToken: String
}

/// A share of a session: a token that joins its stream.
public struct Share: Sendable, Equatable {
    public enum Role: String, Sendable, CaseIterable {
        case viewer
        case readOnly = "read-only"
    }

    public var token: String
    public var role: Role
    public var expiresIn: Int
}

/// APIError is API v1's error: a stable code, and a message for people.
public struct APIError: Error, Sendable, Equatable, LocalizedError {
    public var status: Int
    public var code: String
    public var message: String

    public init(status: Int, code: String, message: String) {
        self.status = status
        self.code = code
        self.message = message
    }

    /// errorDescription says what went wrong for people, as the web
    /// client's errorMessage does: a sentence of the app's for the codes
    /// it knows, and the server's message otherwise.
    public var errorDescription: String? {
        switch (status, code) {
        case (401, "token_expired"), (401, "invalid_token"):
            "Your sign-in has expired. Sign in again."
        case (503, "limit_exceeded"):
            "The server is already running as many sessions as it allows. Try again later."
        case (429, "limit_exceeded"):
            "You've reached the server's limit on sessions or share links. End a session, or wait for a link to expire, and try again."
        default:
            message
        }
    }

    /// Whether the access token is the trouble: signing in again helps.
    public var signIn: Bool { status == 401 }
}

/// API is a client of occamyd's HTTP API v1 (docs/api.md).
public struct API: Sendable {
    /// The server's URL, such as https://occamy.example/.
    public let base: URL
    private let http: URLSession

    public init(base: URL, session: URLSession = .shared) {
        self.base = base
        http = session
    }

    /// oidc reports whether occamyd signs users in through OIDC
    /// (GET /api/v1/auth/methods); a password always works.
    public func oidc() async throws -> Bool {
        struct Methods: Decodable { var oidc: Bool? }
        return try await call("GET", "api/v1/auth/methods", as: Methods.self).oidc ?? false
    }

    /// signIn signs a static user in with a password (POST /api/v1/auth/token).
    public func signIn(username: String, password: String) async throws -> AccessToken {
        try await explained("unauthorized", "The user name or password is wrong.") {
            try await call("POST", "api/v1/auth/token", body: ["username": username, "password": password],
                           as: TokenResponse.self).accessToken
        }
    }

    /// oidcLogin is where a browser starts an OIDC sign-in that returns
    /// to returnURL, with the S256 challenge of a verifier the caller
    /// keeps (GET /api/v1/auth/oidc/login).
    public func oidcLogin(returnURL: URL, challenge: String) -> URL {
        var c = URLComponents(url: url("api/v1/auth/oidc/login"), resolvingAgainstBaseURL: false)!
        c.queryItems = [
            URLQueryItem(name: "return_url", value: returnURL.absoluteString),
            URLQueryItem(name: "code_challenge", value: challenge),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
        ]
        return c.url!
    }

    /// oidcToken exchanges an OIDC sign-in's one-time code and its
    /// verifier for an access token (POST /api/v1/auth/oidc/token).
    public func oidcToken(code: String, verifier: String) async throws -> AccessToken {
        try await explained("unauthorized", "The sign-in took too long, or it was already used. Sign in again.") {
            try await call("POST", "api/v1/auth/oidc/token", body: ["code": code, "code_verifier": verifier],
                           as: TokenResponse.self).accessToken
        }
    }

    /// connections lists the connections the user may open.
    public func connections(token: String) async throws -> Connections {
        try await call("GET", "api/v1/connections", token: token, as: Connections.self)
    }

    /// open opens a session on an ad-hoc target (POST /api/v1/sessions);
    /// its owner joins it with the stream token within a minute.
    public func open(_ target: AdHocTarget, token: String) async throws -> OpenedSession {
        struct Opened: Decodable {
            var id: String
            var stream_token: String
        }
        let o = try await call("POST", "api/v1/sessions", token: token, body: target, as: Opened.self)
        return OpenedSession(id: o.id, streamToken: o.stream_token)
    }

    /// share mints a share of the owner's session (POST
    /// /api/v1/sessions/{id}/shares).
    public func share(session id: String, role: Share.Role, expiresIn: Int = 3600, token: String) async throws -> Share {
        struct Request: Encodable {
            var role: String
            var expires_in: Int
        }
        struct Minted: Decodable {
            var token: String
            var role: String
            var expires_in: Int
        }
        let m = try await explained("not_found", "The session has ended, or it isn't yours.") {
            try await call("POST", "api/v1/sessions/\(escape(id))/shares", token: token,
                           body: Request(role: role.rawValue, expires_in: expiresIn), as: Minted.self)
        }
        return Share(token: m.token, role: Share.Role(rawValue: m.role) ?? role, expiresIn: m.expires_in)
    }

    /// end closes the owner's session for everyone (DELETE
    /// /api/v1/sessions/{id}).
    public func end(session id: String, token: String) async throws {
        _ = try await raw("DELETE", "api/v1/sessions/\(escape(id))", token: token, body: nil)
    }

    /// connectionStream is the stream that opens a session on a
    /// connection and streams it to its owner.
    public func connectionStream(_ id: String) -> URL {
        stream("api/v1/connections/\(escape(id))/stream")
    }

    /// sessionStream is the stream that joins a running session.
    public func sessionStream(_ id: String) -> URL {
        stream("api/v1/sessions/\(escape(id))/stream")
    }

    /// shareLink is the web client's link that joins a session with a
    /// share, which rides in the fragment, as the web client makes it.
    public func shareLink(session id: String, share: String) -> URL {
        var c = URLComponents(url: url("static/"), resolvingAgainstBaseURL: false)!
        var f = URLComponents()
        f.queryItems = [URLQueryItem(name: "join", value: id), URLQueryItem(name: "token", value: share)]
        // The web client reads the fragment with URLSearchParams, to
        // which a + is a space.
        c.percentEncodedFragment = f.percentEncodedQuery?.replacingOccurrences(of: "+", with: "%2B")
        return c.url!
    }

    // MARK: -

    private struct TokenResponse: Decodable {
        var access_token: String
        var expires_in: Double

        var accessToken: AccessToken {
            // A tenth early, so that no request races the expiry.
            AccessToken(token: access_token, expires: Date(timeIntervalSinceNow: expires_in * 0.9))
        }
    }

    private func url(_ path: String) -> URL {
        // The base's own path, if occamyd sits under one, is kept.
        var b = base.absoluteString
        if !b.hasSuffix("/") {
            b += "/"
        }
        return URL(string: b + path)!
    }

    private func stream(_ path: String) -> URL {
        var c = URLComponents(url: url(path), resolvingAgainstBaseURL: false)!
        c.scheme = c.scheme == "https" ? "wss" : "ws"
        return c.url!
    }

    private func escape(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._~"))) ?? s
    }

    private func call<T: Decodable>(_ method: String, _ path: String, token: String? = nil,
                                    body: (any Encodable)? = nil, as: T.Type) async throws -> T {
        let data = try await raw(method, path, token: token, body: try body.map { try JSONEncoder().encode($0) })
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw APIError(status: 200, code: "bad_response", message: "The server sent a response that this app doesn't understand.")
        }
    }

    /// explained runs f, and gives an APIError of code the app's text in
    /// place of the server's message, which a route's caller can put
    /// better, knowing what it asked for.
    private func explained<T>(_ code: String, _ text: String, _ f: () async throws -> T) async throws -> T {
        do {
            return try await f()
        } catch var e as APIError where e.code == code {
            e.message = text
            throw e
        }
    }

    private func raw(_ method: String, _ path: String, token: String?, body: Data?) async throws -> Data {
        var r = URLRequest(url: url(path), cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 30)
        r.httpMethod = method
        if let token {
            r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body {
            r.httpBody = body
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        let (data, response) = try await http.data(for: r)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            struct Body: Decodable {
                struct E: Decodable {
                    var code: String
                    var message: String
                }

                var error: E
            }
            if let e = try? JSONDecoder().decode(Body.self, from: data) {
                throw APIError(status: status, code: e.error.code, message: e.error.message)
            }
            throw APIError(status: status, code: "http_\(status)", message: "The server returned an unexpected response (HTTP \(status)).")
        }
        return data
    }
}

/// PKCE is a verifier for an OIDC sign-in and its S256 challenge
/// (RFC 7636).
public struct PKCE: Sendable {
    public let verifier: String
    public var challenge: String { Self.challenge(of: verifier) }

    /// PKCE makes a verifier of 32 random bytes, 43 characters.
    public init() {
        var b = [UInt8](repeating: 0, count: 32)
        _ = SecRandomCopyBytes(kSecRandomDefault, b.count, &b)
        verifier = Self.base64url(Data(b))
    }

    public init(verifier: String) {
        self.verifier = verifier
    }

    public static func challenge(of verifier: String) -> String {
        base64url(Data(SHA256.hash(data: Data(verifier.utf8))))
    }

    static func base64url(_ d: Data) -> String {
        d.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}

/// OIDCOutcome is what occamyd's callback put in the return URL's
/// fragment: a one-time code, or an error.
public enum OIDCOutcome: Sendable, Equatable {
    case code(String)
    case error(String)

    public init?(returnURL: URL) {
        guard let fragment = URLComponents(url: returnURL, resolvingAgainstBaseURL: false)?.fragment else {
            return nil
        }
        var c = URLComponents()
        c.query = fragment
        let items = c.queryItems ?? []
        if let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty {
            self = .code(code)
        } else if let error = items.first(where: { $0.name == "error" })?.value, !error.isEmpty {
            self = .error(error)
        } else {
            return nil
        }
    }

    /// message describes an error for people, as the web client does.
    public static func message(_ error: String) -> String {
        switch error {
        case "access_denied": "Your sign-in provider or the server didn't allow this account to sign in."
        case "sign_in_failed": "The sign-in provider reported an error, or its response couldn't be verified. Try again."
        case "unavailable": "The sign-in provider can't be reached right now. Try again later."
        default: "The sign-in failed. Try again."
        }
    }
}
