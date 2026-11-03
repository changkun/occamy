// swift-tools-version: 6.0

// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

import PackageDescription

// The Go core, liboccamy.a, which `make swift-core` builds into
// build/core as a universal archive (specs/010-native-swift-client.md).
let core = Context.packageDirectory + "/build/core"

let package = Package(
    name: "Occamy",
    platforms: [.macOS("26.0")],
    products: [
        .library(name: "OccamyKit", targets: ["OccamyKit"]),
        .executable(name: "Occamy", targets: ["OccamyApp"]),
    ],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm", from: "1.20.0"),
    ],
    targets: [
        .systemLibrary(name: "COccamy"),
        .target(
            name: "OccamyKit",
            dependencies: ["COccamy", .product(name: "SwiftTerm", package: "SwiftTerm")],
            linkerSettings: [
                .unsafeFlags(["-L", core]),
                // What the Go runtime and net package link against.
                .linkedFramework("CoreFoundation"),
                .linkedFramework("Security"),
                .linkedLibrary("resolv"),
            ]
        ),
        // The app; `make macos-app` bundles it as Occamy.app.
        .executableTarget(name: "OccamyApp", dependencies: ["OccamyKit"]),
        .testTarget(name: "OccamyKitTests", dependencies: ["OccamyKit"]),
    ]
)
