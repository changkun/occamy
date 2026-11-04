# Occamy for macOS

Occamy for macOS opens the remote desktops and shells of your `occamyd`
servers in windows of their own. One window lists your servers and the
connections they offer you. Each session opens in a window of its own.
The app runs on macOS 26 and later, on Apple silicon and Intel. This
page shows how to install it, connect, and work in a session.

## Installing it

1. Download `Occamy-<version>.dmg`, such as `Occamy-1.0.0-beta.2.dmg`,
   from the [releases](https://github.com/changkun/occamy/releases).
2. Open it, and drag Occamy to Applications.
3. Open Occamy from Applications.

The app is signed with a Developer ID and notarized by Apple, so macOS
opens it. The first time, macOS asks whether to open an app downloaded
from the internet. When you add a server on your local network, macOS
also asks whether Occamy may connect to devices there. Allow it, or the
app cannot reach that server.

## Adding a server

1. Choose **File > Add Server…** (⌘N), or **+** in the toolbar.
2. In **Server**, enter the server's address, such as
   `occamy.example.com`. The app connects over https unless the address
   starts with `http://`.
3. Enter your **User** and **Password**, and choose **Sign In**.

The demo stack of [Try Occamy](try-it.md) is at
`http://localhost:5637`, with the user `demo` and the password `occamy`.

The server lists itself in the sidebar, and its connections show on the
right. A server you are signed out of shows a lock.

### Staying signed in

A sign-in lasts 15 minutes unless the server's administrator chose
another length, and `occamyd` does not renew it. Turn on **Remember the
password in the Keychain**, and the app signs in again by itself when
the sign-in runs out. Otherwise it asks you to sign in again.

### Single sign-on

When the server signs users in through an identity provider, the sheet
offers **Sign In with Single Sign-On…**. It opens the provider's page in
a browser sheet, and returns to the app once you have signed in there.
The app cannot sign in again by itself after a single sign-on, so it
asks you again when the sign-in runs out.

The server's administrator has to allow the app's return address,
`de.changkun.occamy:/oidc`, in
[`auth.oidc.return_urls`](configuration.md#signing-in-through-oidc).

## Opening a connection

Each connection shows as a card. To open one in a new window,
double-click it, select it and press Return, or choose **Open** from its
context menu. **Refresh** in the toolbar lists the connections again.

A desktop's card shows a picture of its screen as you last left it. The
pictures stay on this Mac, in
`~/Library/Application Support/Occamy/Thumbnails`.

The field in the toolbar, **Search or enter an address**, finds
connections by name, ID or protocol. Return opens the first match.

### Opening an address

Some servers also open machines that are not among their connections.
Such a server says **Takes addresses: vnc://, rdp://, ssh://** under
its name. Enter an address in the field and press Return:

| Address | Opens | Port without one |
|---------|-------|------------------|
| `vnc://host:5901` | A VNC server | 5900 |
| `rdp://host` | An RDP server | 3389 |
| `ssh://user@host:2222` | An SSH server | 22 |

The server's administrator decides which machines it may reach
([ad-hoc targets](configuration.md#ad-hoc-targets)). If the server
opens no addresses, the app says so.

An ssh address first asks how the server should prove who it is. Enter
its host key in **Host key**: the `ssh-ed25519 AAAA…` part of a line
that `ssh-keyscan host` prints, or its `SHA256:` fingerprint. Then choose
**Connect**. Without the key, a machine in the middle could pose as the
server. **Accept any host key** skips the check, so use it only on a
network you trust.

Whatever else the remote needs, such as a password or, for ssh without
`user@`, a user name, it asks for once the session opens.

## In a session

A desktop's window opens at the size of the remote screen, or smaller to
fit your display. A terminal's window sets the terminal's size. Resize
the window, and the remote terminal follows.

### The bar

Move the pointer to the top edge of the window to show the bar. It
hides again two seconds after the pointer leaves it. Hold the pointer
over a control to see its name.

| Control | What it does |
|---------|--------------|
| **Scaling** | Desktops only. Choose **Scale to Window**, **Actual Size** or **Resize Remote to Window** |
| **Full Screen** | Fill the display. The bar still shows at the top edge. **Exit Full Screen** leaves it |
| **Send Ctrl-Alt-Del** | Desktops only. Send Control-Alt-Delete, which the Mac would not pass on |
| ⌘ | Desktops only. Send ⌘ shortcuts, such as ⌘Q or ⌘W, to the remote instead of the app |
| Clipboard | Share the clipboard with the remote, or stop sharing it. It is shared when the window opens |
| **Share the session** | **Copy Link with Control** or **Copy View-Only Link** |
| **End Session** | End the session for everyone in it, and close the window |

**Scale to Window** shows the whole screen in the window. **Actual
Size** shows one remote pixel per pixel of your display, with scroll
bars. **Resize Remote to Window** asks the remote for a screen the size
of the window. Some VNC servers do not change their size.

### Keyboard

In a desktop session, keys go by their position on the keyboard, so the
remote's keyboard layout decides which character you type.

- Option is Alt.
- ⌘ is the remote's Super key, the Windows key on Windows. Pressing ⌘
  alone presses Super.
- ⌘ with a key the app does not use goes to the remote. The app's own
  shortcuts, such as ⌘W and ⌘Q, stay with the app unless ⌘ in the bar
  is on.
- The Mac's Delete key is Backspace, and Fn-Delete is Delete.
- For Control-Alt-Delete, use **Send Ctrl-Alt-Del** in the bar.

### Clipboard

In a desktop session, text you copy on the Mac reaches the remote when
you switch to the session's window. Text copied on the remote reaches
the Mac's clipboard. Only text goes either way.

In a terminal session, text that a program on the remote copies to the
clipboard reaches the Mac's clipboard.

The clipboard control in the bar stops both. The server's administrator
can also keep the clipboard from going one way or the other for a
connection.

### Sharing

**Share the session** in the bar copies a link to the clipboard.
Anyone who has it can join the session in a browser for an hour, while
the session runs, through the web client that the server serves.

- **Copy Link with Control** lets them type, point and use the
  clipboard.
- **Copy View-Only Link** lets them watch.

Send a link only to the people you mean to join.

### When the remote asks

When the remote asks for something, such as a password the connection
does not carry, a sheet asks you. Answer it and choose **Continue**, or
choose **End Session**.

When a field is secret, **Save in the Keychain** keeps your answer. The
next time the remote asks the same, the app answers for you, once a
session. If the remote asks again, as after a wrong password, the sheet
asks you, and saving replaces the saved answer.

## Ending a session

**End Session** in the bar ends the session for everyone in it.

Closing the window leaves the session without ending it at once. It runs
on for a minute, as after a lost connection, and then ends.

When a session ends in another way, the window says why. **Connect
Again** opens it again. After **Connection lost** or
**Disconnected: too slow**, it joins the same session, which waits a
minute for you. **Close** closes the window.

| The window says | What happened |
|-----------------|---------------|
| **Connection lost** | The connection to `occamyd` failed. The session may still run |
| **Remote unreachable**, **Could not connect** | `occamyd` could not reach the remote, or the remote refused the sign-in |
| **Ended for inactivity** | Nobody in the session typed or used the pointer for 30 minutes |
| **Signed out** | The sign-in ran out. Choose **Sign In…** |
| **Server shut down** | `occamyd` stopped. Try again once it is back |
| **Access refused** | The connection is not open to you, or you have as many sessions as the server allows |

A session that cannot open shows **Could Not Connect**, with **Try
Again**.

## Signing out

Right-click a server in the sidebar:

- **Sign Out** deletes its sign-in and the password you saved.
- **Remove Server** also removes the server and the pictures of its
  connections.

The app keeps every secret in your login keychain, under
`de.changkun.occamy`. Answers you saved for a remote stay there after
you sign out or remove the server. To delete them, open Keychain Access and search for
`de.changkun.occamy`.

## Building it

You need Xcode 26 or later, and Go. From the repository's root:

```sh
make swift-app    # client/occamy-swift/build/app/Occamy.app
```

Without a Developer ID, the app is signed ad hoc. It runs on the Mac
that built it, but Gatekeeper refuses a copy downloaded on another Mac.
[Development](development.md) covers running it from source, its tests,
and signing it for other Macs.
