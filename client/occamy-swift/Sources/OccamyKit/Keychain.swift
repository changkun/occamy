// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import Foundation
import Security

/// Keychain keeps the app's secrets, access tokens and the passwords a
/// user chose to save, as generic passwords of one service in the login
/// keychain, never in preferences.
public struct Keychain: Sendable {
    public let service: String

    public init(service: String) {
        self.service = service
    }

    private func query(_ account: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: account]
    }

    /// data returns the secret of account, or nil.
    public func data(_ account: String) -> Data? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess else {
            return nil
        }
        return out as? Data
    }

    /// set keeps data as the secret of account, or removes it for nil.
    @discardableResult
    public func set(_ data: Data?, for account: String) -> Bool {
        guard let data else {
            let s = SecItemDelete(query(account) as CFDictionary)
            return s == errSecSuccess || s == errSecItemNotFound
        }
        let update = [kSecValueData as String: data]
        let s = SecItemUpdate(query(account) as CFDictionary, update as CFDictionary)
        if s == errSecItemNotFound {
            var add = query(account)
            add[kSecValueData as String] = data
            add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
            return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
        }
        return s == errSecSuccess
    }

    public func value<T: Decodable>(_ type: T.Type, _ account: String) -> T? {
        data(account).flatMap { try? JSONDecoder().decode(T.self, from: $0) }
    }

    @discardableResult
    public func set<T: Encodable>(value: T?, for account: String) -> Bool {
        set(value.flatMap { try? JSONEncoder().encode($0) }, for: account)
    }
}
