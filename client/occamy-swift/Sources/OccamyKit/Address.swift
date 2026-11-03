// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Foundation

/// AdHocTarget is a remote that is not in occamyd's registry, which a
/// server with allow_adhoc_targets opens (docs/api.md, Ad-hoc targets).
/// Its credentials are asked of the owner on the session's stream.
public struct AdHocTarget: Codable, Sendable, Hashable {
    /// ssh, vnc or rdp.
    public var `protocol`: String
    /// host:port, or a host alone for the protocol's default port.
    public var target: String
    /// The user to log in as; without one, the remote asks if it needs
    /// one.
    public var username: String?
    /// ssh only: the host keys the target may present, public keys or
    /// SHA256: fingerprints.
    public var hostKeys: [String]?
    /// ssh only: accept any host key.
    public var insecureIgnoreHostKey = false

    public init(protocol: String, target: String, username: String? = nil, hostKeys: [String]? = nil,
                insecureIgnoreHostKey: Bool = false) {
        self.protocol = `protocol`
        self.target = target
        self.username = username
        self.hostKeys = hostKeys
        self.insecureIgnoreHostKey = insecureIgnoreHostKey
    }

    /// The protocols an address names.
    public static let protocols = ["vnc", "rdp", "ssh"]

    /// AdHocTarget parses an address as VNC Viewer's address field takes
    /// one, with the protocol as its scheme: vnc://host:5900, rdp://host
    /// or ssh://user@host. It is nil for anything else, which the app
    /// searches its connections for.
    public init?(address: String) {
        let s = address.trimmingCharacters(in: .whitespaces)
        guard let c = URLComponents(string: s), let scheme = c.scheme?.lowercased(),
              Self.protocols.contains(scheme), let host = c.host, !host.isEmpty,
              c.path.isEmpty || c.path == "/", c.query == nil, c.fragment == nil, c.password == nil
        else {
            return nil
        }
        // URLComponents leaves an IPv6 address in its brackets.
        var target = host
        if let port = c.port {
            guard (1...65535).contains(port) else {
                return nil
            }
            target += ":\(port)"
        }
        self.init(protocol: scheme, target: target, username: c.user.flatMap { $0.removingPercentEncoding })
    }

    /// address is the target as an address, the way init(address:)
    /// takes it.
    public var address: String {
        "\(self.protocol)://\(username.map { "\($0)@" } ?? "")\(target)"
    }

    enum CodingKeys: String, CodingKey {
        case `protocol`, target, username
        case hostKeys = "host_keys"
        case insecureIgnoreHostKey = "insecure_ignore_host_key"
    }

    // occamyd refuses host key fields on another protocol than ssh, so
    // empty ones are left out.
    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(self.protocol, forKey: .protocol)
        try c.encode(target, forKey: .target)
        try c.encodeIfPresent(username.flatMap { $0.isEmpty ? nil : $0 }, forKey: .username)
        if let hostKeys, !hostKeys.isEmpty {
            try c.encode(hostKeys, forKey: .hostKeys)
        }
        if insecureIgnoreHostKey {
            try c.encode(true, forKey: .insecureIgnoreHostKey)
        }
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.protocol = try c.decode(String.self, forKey: .protocol)
        target = try c.decode(String.self, forKey: .target)
        username = try c.decodeIfPresent(String.self, forKey: .username)
        hostKeys = try c.decodeIfPresent([String].self, forKey: .hostKeys)
        insecureIgnoreHostKey = try c.decodeIfPresent(Bool.self, forKey: .insecureIgnoreHostKey) ?? false
    }
}
