# Occamy for macOS

Occamy for macOS opens the connections of your `occamyd` servers in
windows of their own, as VNC Viewer does: one window to find a machine,
and one window per session. It speaks protocol v1 to `occamyd`, through
the same Go client core as the tests, and runs on macOS 26 and later,
on Apple silicon and Intel.

## Installing it

Download `Occamy-<version>.dmg`, such as `Occamy-1.0.0-beta.2.dmg`,
from the [releases](https://github.com/changkun/occamy/releases), open
it, and drag Occamy to Applications. The app is signed with a Developer
ID and notarized by Apple, so Gatekeeper opens it; macOS asks once
whether to open an app downloaded from the internet.

## Building it

To build it yourself, you need Xcode 26 or later and Go, as [Try Occamy](try-it.md#1-what-you-need)
says. From the repository's root:

```sh
make swift-core
swift run --package-path client/occamy-swift Occamy
```

`make swift-core` builds the Go core as a C archive, which the app links.

To build the app as a bundle you can copy to Applications:

```sh
make swift-app    # client/occamy-swift/build/app/Occamy.app
make swift-dmg    # and a disk image of it beside it
```

The bundle runs on Apple silicon and Intel, with the hardened runtime.
Without a Developer ID it is signed ad hoc, so it runs on the Mac that
built it, while Gatekeeper refuses a copy downloaded on another Mac. To
sign it for other Macs, name your Developer ID certificate, and to have
Apple notarize the disk image, the profile that
`xcrun notarytool store-credentials` saved:

```sh
make swift-notarize DEVELOPER_ID="Developer ID Application: Your Name (TEAMID)" NOTARY_PROFILE=occamy
```

## Adding a server

Choose **File > Add Server…** (⌘N), or **+** in the toolbar, and enter
the server's address, such
as `occamy.example.com`, which the app reaches over https unless the
address says `http://`, and your user name and password. The demo stack
of [Try Occamy](try-it.md) is `http://localhost:5637`, with the user
`demo` and the password `occamy`.

`occamyd`'s sign-ins last 15 minutes and are not renewed. Turn on
**Remember the password in the Keychain** and the app signs in again by
itself when one runs out; otherwise it asks you again. The app keeps
each server's access token, and the password if you choose, in your
login keychain, under `de.changkun.occamy`. **Sign Out** and **Remove
Server**, in a server's context menu, delete them.

On a server that signs in through OIDC, the sheet offers **Sign In with
Single Sign-On…**, which opens the provider's page in a browser window
and comes back to the app at `de.changkun.occamy:/oidc`. The server's
administrator lists that address in
[`auth.oidc.return_urls`](configuration.md#signing-in-through-oidc).

## Opening a connection

The sidebar lists your servers, and a server's connections show as
cards. Double-click one, or select it and press Return, to open it in a
new window. A card shows a picture of the connection as you last left
it, which the app keeps on this Mac only, in
`~/Library/Application Support/Occamy/Thumbnails`.

The field in the toolbar searches the connections by name, ID and
protocol; Return opens the first match. On a server that allows ad-hoc
targets ([`allow_adhoc_targets`](configuration.md#ad-hoc-targets)), it
also takes an address, which opens a machine that is not among the
connections:

| Address | Opens |
|---------|-------|
| `vnc://host:5901` | a VNC server; 5900 without a port |
| `rdp://host` | an RDP server; 3389 without a port |
| `ssh://user@host:2222` | an SSH server; 22 without a port |

An ssh address asks for the server's host key first, as `ssh-keyscan`
prints it or as a `SHA256:` fingerprint, so that no machine in the
middle can pose as the server. Whatever else the remote needs, such as a
password, it asks for when the session opens.

## In a session

The window opens at the remote screen's size, or smaller to fit your
display. Move the pointer to the window's top edge for its controls:

| Control | Does |
|---------|------|
| Scaling | Scale the screen to the window, show it at its actual size with scroll bars, or, for the session's owner, resize the remote to the window |
| Full screen | Fill the display; the controls stay at the top edge |
| Ctrl-Alt-Del | Send Control-Alt-Delete, which the Mac would not pass on |
| ⌘ | Send ⌘ shortcuts, such as ⌘Q or ⌘W, to the remote instead of the app |
| Clipboard | Share the clipboard with the remote, or stop sharing it |
| Share | Copy a link that lets someone join the session in their browser for an hour, with control, to type, point and use the clipboard, or view only |
| End Session | End the session for everyone in it, and close the window |

Text you copy on the Mac reaches the remote when you switch to the
session's window, and text copied on the remote reaches the Mac's
clipboard. ⌘ alone, or with a key the app does not use, is the remote's
Super key, the Windows key on Windows.

When the remote asks for something, such as a password the connection
does not carry, a sheet asks you; **Save in the Keychain** answers it by
itself the next time. If the session ends, the window says why, and
**Connect Again** opens it again, or, if only the connection to
`occamyd` was lost, joins the session again where you left it.

Closing the window leaves the session without ending it: a session you
own waits a minute for you, as for a lost connection, and then ends.

## See also

- [HTTP API (v1)](api.md), which the app signs in and opens sessions
  through.
- [Occamy Protocol (v1)](protocol.md), which carries its sessions.
- [Spec 010](../specs/010-native-swift-client.md), the app's design.
