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
            detail = "occamyd is shutting down, which ends its sessions. Try again once it is back."
        case 1002, 1003, 1009, 1011:
            title = "Protocol error"
            detail = "This is a bug in occamyd or in this app."
        case 4000:
            if shared {
                title = "Link not valid"
                detail = "This share link has expired, or its session has ended. Ask the owner for a new one."
            } else {
                title = "Signed out"
                detail = "Your sign-in has expired or is not valid. Sign in again."
                signIn = true
            }
        case 4001:
            title = "Access refused"
            detail = "occamyd refused this stream: the connection or session is not open to you, the session has ended, or you have as many sessions as it allows."
        case 4002:
            detail = "The owner ended the session."
        case 4003:
            detail = "The owner left and did not come back in time."
        case 4004:
            title = "Ended for inactivity"
            detail = "Nobody gave input for the session's idle timeout, so occamyd ended it."
        case 4005:
            detail = "The remote computer ended the session, as when its user logs out."
        case 4006:
            // occamyd's reason is the word "failed" unless it knows the
            // cause.
            if !reason.isEmpty && reason != "failed" {
                title = "Could not connect"
                detail = reason
            } else {
                title = "Remote unreachable"
                detail = "occamyd could not connect to the remote computer, or lost the connection to it."
            }
        case 4007:
            title = "Session full"
            detail = "The session has as many viewers as occamyd allows. Try again later."
        case 4008:
            title = "Disconnected: too slow"
            detail = "occamyd heard nothing from this app for its viewer timeout: the network stalled, or the app could not keep up."
            rejoin = true
        case 0:
            title = "Connection lost"
            detail = "The connection to occamyd failed. The session may still run: join it again."
            rejoin = true
        default:
            break
        }
    }
}
