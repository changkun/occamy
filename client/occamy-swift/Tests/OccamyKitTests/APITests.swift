// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Foundation
import Testing

@testable import OccamyKit

/// Mock answers each test's requests by its host, so that the tests run
/// in parallel.
final class Mock: URLProtocol, @unchecked Sendable {
    typealias Handler = @Sendable (URLRequest, Data?) -> (Int, String)

    nonisolated(unsafe) private static var handlers: [String: Handler] = [:]
    private static let lock = NSLock()

    /// api is an API at base whose requests handler answers with a
    /// status and a body.
    static func api(_ base: String, _ handler: @escaping Handler) -> API {
        let url = URL(string: base)!
        lock.withLock { handlers[url.host()!] = handler }
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [Mock.self]
        return API(base: url, session: URLSession(configuration: c))
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let h = Self.lock.withLock({ Self.handlers[request.url?.host() ?? ""] }) else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotFindHost))
            return
        }
        // URLSession hands a body to the protocol as a stream.
        var body: Data?
        if let s = request.httpBodyStream {
            s.open()
            var d = Data()
            var b = [UInt8](repeating: 0, count: 4096)
            while case let n = s.read(&b, maxLength: b.count), n > 0 {
                d.append(b, count: n)
            }
            s.close()
            body = d
        }
        let (status, answer) = h(request, body ?? request.httpBody)
        let r = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1",
                                headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: r, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(answer.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

private func json(_ d: Data?) -> [String: Any] {
    d.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
}

@Suite struct APITests {
    @Test func signIn() async throws {
        let api = Mock.api("https://sign-in.test/occamy/") { r, body in
            guard r.httpMethod == "POST", r.url?.path() == "/occamy/api/v1/auth/token",
                  json(body)["username"] as? String == "demo", json(body)["password"] as? String == "occamy"
            else {
                return (400, #"{"error":{"code":"bad_request","message":"no"}}"#)
            }
            return (200, #"{"access_token":"t","token_type":"Bearer","expires_in":900}"#)
        }
        let t = try await api.signIn(username: "demo", password: "occamy")
        #expect(t.token == "t")
        // A tenth early.
        #expect(abs(t.expires.timeIntervalSinceNow - 810) < 5)
        #expect(!t.expired)
    }

    @Test func errors() async {
        let api = Mock.api("https://errors.test/") { r, _ in
            if r.url?.path() == "/api/v1/connections" {
                return (401, #"{"error":{"code":"invalid_token","message":"the access token is not valid"}}"#)
            }
            return (502, "<html>bad gateway</html>")
        }
        await #expect(throws: APIError(status: 401, code: "invalid_token", message: "the access token is not valid")) {
            try await api.connections(token: "old")
        }
        do {
            _ = try await api.oidc()
            Issue.record("a 502 passed")
        } catch let e as APIError {
            #expect(e.status == 502 && e.code == "http_502" && !e.signIn)
        } catch {
            Issue.record("\(error)")
        }
    }

    /// The errors the app shows say what happened in sentences of its
    /// own where it knows the code, as the web client's do, while message
    /// keeps the server's words for the codes a call does not explain.
    @Test func descriptions() async {
        let api = Mock.api("https://descriptions.test/") { r, _ in
            switch r.url?.path() {
            case "/api/v1/auth/token":
                (401, #"{"error":{"code":"unauthorized","message":"wrong user name or password"}}"#)
            case "/api/v1/connections":
                (401, #"{"error":{"code":"token_expired","message":"the access token has expired"}}"#)
            default:
                (404, #"{"error":{"code":"not_found","message":"no such session"}}"#)
            }
        }
        do {
            _ = try await api.signIn(username: "demo", password: "nope")
            Issue.record("a wrong password passed")
        } catch {
            #expect(error.localizedDescription == "The user name or password is wrong.")
        }
        do {
            _ = try await api.connections(token: "old")
            Issue.record("an expired token passed")
        } catch let e as APIError {
            #expect(e.message == "the access token has expired")
            #expect(e.localizedDescription == "Your sign-in has expired. Sign in again.")
        } catch {
            Issue.record("\(error)")
        }
        do {
            _ = try await api.share(session: "gone", role: .viewer, token: "t")
            Issue.record("a share of no session passed")
        } catch {
            #expect(error.localizedDescription == "The session has ended, or it isn't yours.")
        }
    }

    @Test func connections() async throws {
        let api = Mock.api("https://connections.test") { r, _ in
            guard r.value(forHTTPHeaderField: "Authorization") == "Bearer t" else {
                return (401, #"{"error":{"code":"invalid_token","message":"no"}}"#)
            }
            return (200, #"{"connections":[{"id":"vnc","name":"Desktop","protocol":"vnc"}],"adhoc_targets":true}"#)
        }
        let c = try await api.connections(token: "t")
        #expect(c == Connections(connections: [Connection(id: "vnc", name: "Desktop", protocol: "vnc")], adhocTargets: true))
    }

    @Test func openAndShare() async throws {
        let api = Mock.api("https://open.test/") { r, body in
            switch (r.httpMethod, r.url?.path()) {
            case ("POST", "/api/v1/sessions"):
                let b = json(body)
                guard b["protocol"] as? String == "ssh", b["target"] as? String == "box:22",
                      b["username"] as? String == "alice", b["insecure_ignore_host_key"] as? Bool == true,
                      b["host_keys"] == nil
                else {
                    return (400, #"{"error":{"code":"bad_request","message":"\#(b)"}}"#)
                }
                return (201, #"{"id":"s 1","role":"owner","stream_token":"st","expires_in":60}"#)
            case ("POST", "/api/v1/sessions/s%201/shares"):
                let b = json(body)
                guard b["role"] as? String == "read-only", b["expires_in"] as? Int == 3600 else {
                    return (400, #"{"error":{"code":"bad_request","message":"\#(b)"}}"#)
                }
                return (201, #"{"token":"sh","role":"read-only","expires_in":3600}"#)
            default:
                return (404, #"{"error":{"code":"not_found","message":"\#(r.url!)"}}"#)
            }
        }
        let t = try #require(AdHocTarget(address: "ssh://alice@box:22"))
        var target = t
        target.insecureIgnoreHostKey = true
        let o = try await api.open(target, token: "t")
        #expect(o == OpenedSession(id: "s 1", streamToken: "st"))
        let s = try await api.share(session: o.id, role: .readOnly, token: "t")
        #expect(s == Share(token: "sh", role: .readOnly, expiresIn: 3600))
    }

    @Test func urls() {
        let api = API(base: URL(string: "https://occamy.example/sub/")!)
        #expect(api.connectionStream("a/b").absoluteString == "wss://occamy.example/sub/api/v1/connections/a%2Fb/stream")
        #expect(API(base: URL(string: "http://localhost:5636")!).sessionStream("s").absoluteString
            == "ws://localhost:5636/api/v1/sessions/s/stream")
        #expect(api.shareLink(session: "s1", share: "x+y").absoluteString
            == "https://occamy.example/sub/static/#join=s1&token=x%2By")
        let login = api.oidcLogin(returnURL: URL(string: "de.changkun.occamy:/oidc")!, challenge: "c")
        let q = URLComponents(url: login, resolvingAgainstBaseURL: false)!
        #expect(q.path == "/sub/api/v1/auth/oidc/login")
        #expect(q.queryItems == [
            URLQueryItem(name: "return_url", value: "de.changkun.occamy:/oidc"),
            URLQueryItem(name: "code_challenge", value: "c"),
            URLQueryItem(name: "code_challenge_method", value: "S256"),
        ])
    }

    @Test func pkce() {
        // RFC 7636, appendix B.
        #expect(PKCE.challenge(of: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk") == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM")
        let p = PKCE()
        #expect(p.verifier.count == 43)
        #expect(p.verifier.allSatisfy { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" })
        #expect(PKCE().verifier != p.verifier)
    }

    @Test(arguments: [
        ("de.changkun.occamy:/oidc#code=abc", OIDCOutcome.code("abc")),
        ("de.changkun.occamy:/oidc#error=access_denied", .error("access_denied")),
        ("de.changkun.occamy:/oidc#code=&error=unavailable", .error("unavailable")),
        ("de.changkun.occamy:/oidc", nil),
        ("de.changkun.occamy:/oidc#state=x", nil),
    ])
    func oidcOutcome(url: String, want: OIDCOutcome?) {
        #expect(OIDCOutcome(returnURL: URL(string: url)!) == want)
    }
}

@Suite struct AddressTests {
    @Test(arguments: [
        ("vnc://desk:5901", "vnc", "desk:5901", nil),
        ("  rdp://win  ", "rdp", "win", nil),
        ("ssh://alice@box", "ssh", "box", "alice"),
        ("SSH://a%40b@box:2222", "ssh", "box:2222", "a@b"),
        ("vnc://[::1]:5900", "vnc", "[::1]:5900", nil),
        ("vnc://desk/", "vnc", "desk", nil),
    ] as [(String, String, String, String?)])
    func parses(address: String, proto: String, target: String, user: String?) throws {
        let t = try #require(AdHocTarget(address: address))
        #expect(t.protocol == proto)
        #expect(t.target == target)
        #expect(t.username == user)
        #expect(AdHocTarget(address: t.address) == t)
    }

    @Test(arguments: ["desk", "desk:5900", "http://desk", "telnet://desk", "vnc://", "vnc://desk/x",
                      "vnc://desk?a=b", "vnc://desk#x", "ssh://a:pw@box", "vnc://desk:0", "vnc://desk:70000"])
    func refuses(address: String) {
        #expect(AdHocTarget(address: address) == nil)
    }

    @Test func encodes() throws {
        let vnc = try JSONSerialization.jsonObject(with: JSONEncoder().encode(AdHocTarget(protocol: "vnc", target: "d", username: "")))
        #expect((vnc as? [String: String]) == ["protocol": "vnc", "target": "d"])
        let ssh = AdHocTarget(protocol: "ssh", target: "b", username: "u", hostKeys: ["SHA256:x"])
        let back = try JSONDecoder().decode(AdHocTarget.self, from: JSONEncoder().encode(ssh))
        #expect(back == ssh)
    }
}
