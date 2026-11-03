// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import OccamyKit
import SwiftUI

/// SignInView adds a server by its URL, or signs in to one again: with a
/// password, or through the server's OIDC provider where it has one.
struct SignInView: View {
    /// The server to sign in to; nil to add one.
    let server: Server?
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var address = ""
    @State private var username = ""
    @State private var password = ""
    @State private var remember = false
    @State private var oidc = false
    @State private var busy = false
    @State private var problem: String?

    private var target: Server? {
        if let server {
            return server
        }
        return Server(address: address)
    }

    var body: some View {
        Form {
            Section {
                if let server {
                    LabeledContent("Server", value: server.name)
                } else {
                    TextField("Server", text: $address, prompt: Text("occamy.example.com"))
                        .textContentType(.URL)
                }
            }
            Section {
                TextField("User", text: $username)
                    .textContentType(.username)
                SecureField("Password", text: $password)
                    .textContentType(.password)
                Toggle("Remember the password in the Keychain", isOn: $remember)
            } footer: {
                Text("occamyd's sign-ins last minutes; with the password remembered, the app signs in again by itself.")
                    .foregroundStyle(.secondary)
            }
            if oidc {
                Section {
                    Button("Sign In with Single Sign-On…") { Task { await signInOIDC() } }
                        .disabled(busy || target == nil)
                }
            }
            if let problem {
                Text(problem)
                    .foregroundStyle(.red)
            }
        }
        .formStyle(.grouped)
        .frame(width: 440)
        .disabled(busy)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Sign In") { Task { await signIn() } }
                    .disabled(busy || target == nil || username.isEmpty || password.isEmpty)
            }
        }
        .onAppear {
            username = server?.username ?? ""
        }
        .task(id: target?.url) {
            // Whether the server offers OIDC, once its address has stayed
            // put for a moment.
            oidc = false
            guard let t = target else {
                return
            }
            try? await Task.sleep(for: .milliseconds(server == nil ? 500 : 0))
            oidc = (try? await t.api.oidc()) ?? false
        }
    }

    private func signIn() async {
        guard let t = target else {
            return
        }
        busy = true
        defer { busy = false }
        do {
            try await store.signIn(t, username: username, password: password, remember: remember)
            dismiss()
        } catch {
            problem = error.localizedDescription
        }
    }

    private func signInOIDC() async {
        guard let t = target, let anchor = NSApp.keyWindow ?? NSApp.windows.first else {
            return
        }
        busy = true
        defer { busy = false }
        do {
            try await store.signInOIDC(t, anchor: anchor)
            dismiss()
        } catch is CancellationError {
        } catch let e as NSError where e.domain == "com.apple.AuthenticationServices.WebAuthenticationSession" && e.code == 1 {
            // The user closed the browser sheet.
        } catch {
            problem = error.localizedDescription
        }
    }
}
