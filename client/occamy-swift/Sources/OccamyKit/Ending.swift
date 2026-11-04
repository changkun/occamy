// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

/// Ending describes how a session ended, for people, as the web
/// client's ending.ts does.
public struct Ending: Sendable, Equatable {
    public var title: String
    public var detail: String
    /// The close code and reason, for a bug report.
    public var technical: String
    /// The session may still run: joining it again may work.
    public var rejoin: Bool
    /// The sign-in expired.
    public var signIn: Bool

    /// Ending describes end; shared says the session was opened by a
    /// share link.
    public init(_ end: End, shared: Bool = false) {
        let reason = String(end.reason.prefix(200))
        title = "Session ended"
        detail = ""
        technical = end.code == 0 ? (end.error ?? reason) : "\(end.code)\(reason.isEmpty ? "" : ": \(reason)")"
        rejoin = false
        signIn = false
        switch end.code {
        case 1000:
            title = "Disconnected"
            detail = "The session was closed."
        case 1001:
            title = "Server shut down"
            detail = "The server is shutting down, which ends every session on it. Try again once it's back."
        case 1002, 1003, 1009:
            title = "Protocol error"
            detail = "The server or this app sent something the other couldn't handle. This is a bug in the server or in this app."
        case 1011:
            // internal-error: the server failed, as the web client's
            // closeKind has it, not the protocol.
            detail = "The server ran into an internal error."
        case 4000:
            if shared {
                title = "Link not valid"
                detail = "This share link has expired, or its session has ended. Ask the owner for a new one."
            } else {
                title = "Signed out"
                detail = "Your sign-in has expired or isn't valid. Sign in again."
                signIn = true
            }
        case 4001:
            title = "Access refused"
            detail = "The server didn't let you join. The connection or session may not be open to you, the session may have ended, or you may already have as many sessions as the server allows."
        case 4002:
            detail = "The owner ended the session."
        case 4003:
            detail = "The owner left and didn't come back in time."
        case 4004:
            title = "Ended for inactivity"
            detail = "Nobody used the session for longer than the server allows, so the server ended it."
        case 4005:
            detail = "The remote computer ended the session, for example because its user logged out."
        case 4006:
            // occamyd's reason is the word "failed" unless it knows the
            // cause.
            if !reason.isEmpty && reason != "failed" {
                title = "Could not connect"
                detail = reason
            } else {
                title = "Remote unreachable"
                detail = "The server couldn't connect to the remote computer, or lost its connection to it."
            }
        case 4007:
            title = "Session full"
            detail = "The session already has as many viewers as the server allows. Try again later."
        case 4008:
            title = "Disconnected: too slow"
            detail = "The server heard nothing from this app for too long. The network may have stalled, or the app couldn't keep up."
            rejoin = true
        case 0:
            title = "Connection lost"
            detail = "The connection to the server failed. The session may still be running, so connect again to return to it."
            rejoin = true
        default:
            detail = "The server ended the session."
        }
    }
}
