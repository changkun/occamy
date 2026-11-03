// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import AppKit
import OccamyKit
import SwiftUI

/// Occamy for macOS, as simple as VNC Viewer: one window to find a
/// machine on an occamyd server, and one window per session
/// (specs/010-native-swift-client.md, App design).
@main
struct OccamyApp: App {
    @NSApplicationDelegateAdaptor private var delegate: AppDelegate
    @State private var store = Store()

    var body: some Scene {
        Window("Occamy", id: "main") {
            MainView()
                .environment(store)
        }
        .defaultSize(width: 960, height: 640)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Add Server…") { store.signingIn = .init() }
                    .keyboardShortcut("n")
            }
        }

        WindowGroup("Session", for: SessionRequest.self) { $request in
            if let request {
                SessionView(request: request)
                    .environment(store)
            }
        }
        .defaultSize(width: 1280, height: 800)
        // A session is not opened again by itself at the next launch.
        .restorationBehavior(.disabled)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // Run from a plain binary, as swift run does, the app has no
        // bundle to say it is one with a Dock icon and menus.
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
    }
}

/// The client's name in hello, with the app's version.
let clientName: String = {
    let v = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"
    return "Occamy for macOS \(v)"
}()

/// symbol is the SF Symbol of a connection's protocol.
func symbol(_ protocol: String) -> String {
    switch `protocol` {
    case "vnc": "display"
    case "rdp": "desktopcomputer"
    case "ssh": "apple.terminal"
    case "kubernetes": "shippingbox"
    default: "network"
    }
}
