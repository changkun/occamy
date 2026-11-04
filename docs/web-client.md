# The web client

The web client opens Occamy's remote desktops and terminals in a
browser, with nothing to install. `occamyd` serves it under `/static/`,
such as `https://occamy.example.com/static/`, when its configuration
sets `web_client` ([Configuration](configuration.md)). The quickest way
to try it is the demo stack in [Try Occamy](try-it.md).

## Signing in

Enter your **User name** and **Password**, and click **Sign in**.

Where `occamyd` signs users in through an identity provider, the page
also offers **Sign in with single sign-on**. It takes you to the
provider and back. Single sign-on needs the page served over HTTPS.

The sign-in lasts as long as the page. A new tab, or a reload, asks you
to sign in again. **Sign out**, at the top right of the connections,
signs you out at once.

A sign-in is good for 15 minutes unless the configuration says
otherwise. A session keeps running after that. When you next do
something that needs it, such as sharing, the page asks you to **Sign
in again** over the session. Do so, and the page carries on with what
you asked for. **Cancel** returns to the session.

If single sign-on fails, the page says why:

| Message | What it means |
|---------|---------------|
| The identity provider or occamyd refused this user. | The provider refused you, or `occamyd` does not let you in |
| The identity provider failed, or its answer did not verify. | Something went wrong at the provider |
| The identity provider cannot be reached right now. Try again later. | `occamyd` cannot reach the provider |
| That sign-in was not started in this tab, so it was not used. Sign in here. | The sign-in came back to a tab that did not start it |
| The sign-in took too long or was used already. Sign in again. | The sign-in expired on its way back, or was used once already |

## Connections

After you sign in, **Connections** lists the connections open to you.
Each shows its name, its protocol (SSH, VNC, RDP or Kubernetes), and its
ID when that differs from the name. A screen icon marks a desktop, and
a prompt icon a terminal. **Refresh** reloads the list.

Click a connection to open a session on it. You own the session.
`occamyd` lets each user own 10 sessions at once unless its
configuration says otherwise.

If the list is empty, no connection admits you. The configuration
names the users and groups each connection admits.

## A session

The page says **Connecting to** the connection's name, with **Cancel**,
until the remote's screen arrives. A VNC or RDP connection shows a
desktop, and an SSH or Kubernetes connection a terminal.

The bar along the top holds the session's controls:

| Item | What it does |
|------|--------------|
| The title | The remote's title, such as a terminal's, or else the connection's name. The browser tab shows it too |
| The role | `owner`, `with control` or `view only` (see [Roles](#roles)) |
| **1 other viewer** | Shows when others are in the session. Click it to list them by user name, or as Guest when they joined by a link |
| **Clipboard** | Opens the clipboard panel (see [The clipboard](#the-clipboard)) |
| **Share** | Opens the share panel, for the owner (see [Sharing](#sharing)) |
| **Full screen** | Gives the session the whole screen. Click it again to leave |
| **End session** | Ends the session for everyone, for the owner |
| **Leave** | Leaves the session, for everyone else. It goes on without you |

**End session** ends the session in `occamyd` and returns you to the
connections. Everyone else in it sees "Session ended". If `occamyd`
cannot end it, the page says **Not ended for everyone**, with why, and
offers **Try again**.

When the connection's configuration holds no password or user name,
`occamyd` asks the owner for it. A dialog shows the question, with
**Continue** and **End session**. Only the owner sees it, and the
session waits for the answer.

A bell from the remote flashes the screen. While the tab is hidden,
`occamyd` sends it nothing, and the screen catches up when you return.

An RDP desktop of a Windows host that stays nearly black needs
`disable_gfx: true` on its connection ([Backends](configuration.md#backends)).

## Roles

Whoever opens a session owns it. Others join through a share link, with
control or view only.

| | Owner | With control | View only |
|---|:---:|:---:|:---:|
| Watch the remote | yes | yes | yes |
| Type, point and scroll | yes | yes | no |
| Send to the remote clipboard | yes | yes | no |
| Set the remote's size | yes | no | no |
| Answer the remote's questions | yes | no | no |
| Share and end the session | yes | no | no |

## Keyboard and pointer

Click the remote's screen to give it the keyboard. While it has the
focus, every key goes to the remote, Tab included, and so do the
pointer, right clicks and the wheel. The page shows the remote's
pointer.

Press **Control**, **Alt** and **Shift** together to move the focus
from the screen to the bar. Keys you still hold when the window loses
the focus are released on the remote, so no key stays down there.

Each key carries both the character your keyboard layout gives it and
the physical key. RDP servers, and VNC servers that ask for physical
keys, apply their own layout to it. Other VNC servers take the
character. Text from an input method, such as Japanese, goes to the
remote once you have composed it.

In a terminal, type as in any terminal. To paste, click the terminal and
use your browser's paste: ⌘V, or Ctrl+Shift+V.

## Sharing

The owner can let others watch the session, or work in it, through a
link.

1. Click **Share**. The panel **Share this session** opens.
2. Under **Others join**, choose **View only: they watch** or **With
   control: they also type, point and use the clipboard**.
3. Under **Link expires after**, choose 10 minutes, 1 hour, 4 hours or
   12 hours. The default is 1 hour.
4. Click **Create link**, then **Copy link**, and send the link.

The panel then says "Anyone with this link can join with control until
it expires or the session ends", or view only, as you chose. **Create
another** makes a new link with what the panel now says.

Whoever opens the link joins the session at once, without signing in.
Their bar says "Shared session" until the remote names a title, and
shows their role. Your bar shows **1 other viewer**.

- Anyone who has the link can join, as many times as the session takes
  viewers, until the link expires. A link that expires leaves those who
  joined in the session.
- Every link ends with the session. A single link cannot be withdrawn:
  end the session to end them all.
- The link carries the share after `#`, which the browser sends to no
  server. The page removes it from the address bar as it loads, so a
  viewer who reloads the page needs the link again.

The demo stack is an easy place to try sharing: open a link in a
private window ([Try Occamy](try-it.md#4-open-one-connection-of-each-protocol)).

## The clipboard

On a desktop, text moves between your clipboard and the remote's.

- **From the remote.** When the remote's clipboard changes, the page
  puts its text on your clipboard, if the page has the focus and your
  role may type. A view-only viewer's clipboard is left alone.
- **To the remote.** When the browser window gets the focus, the page
  sends your clipboard to the remote, where the browser lets it read
  the clipboard without asking. Firefox and Safari never do. Use the
  panel there.

**Clipboard** opens the panel. Its box holds the remote clipboard's
text.

| Button | What it does |
|--------|--------------|
| **Send** | Puts the box's text on the remote clipboard |
| **Paste local** | Fills the box from your clipboard. The browser may ask first |
| **Copy** | Puts the box's text on your clipboard |
| **Close** | Closes the panel, as Escape does |

Text you type in the box stays when the remote's clipboard changes. The
panel then says "Edited: the remote clipboard holds other text.", and
**Show it** brings the remote's text back. A view-only viewer can copy
the text, but not send it.

The panel says what became of what you did:

| Message | What it means |
|---------|---------------|
| Sent to the remote clipboard. | The text went |
| Not sent: the text is too large for one message to occamyd. Send less of it. | The text stays in the box. Send part of it |
| The remote hasn't taken the clipboard yet. Your text will go when it does. | The remote has not answered for a while. The text waits, and goes when it does |
| The browser did not let the page read the clipboard. Paste into the box instead. | Paste with the keyboard instead |
| The browser did not let the page write the clipboard. Select the text and copy it instead. | Copy with the keyboard instead |

The waiting message also shows over the desktop, with **Dismiss**. At
an RDP logon screen it is expected: the text goes once you have logged
on. If it stays after that, the remote's clipboard service has probably
stopped, and opening the session again may start it. Text still waiting
ends with its session, so send it again.

A connection's configuration can close the clipboard in either
direction, or both ([Configuration](configuration.md)).

A terminal has no remote clipboard. Its panel says so, and points you
to the browser's paste.

## Resizing

The owner's window sets the remote's size. When you resize the window
or click **Full screen**, the page asks the remote for the new size once
the window stops changing.

- A desktop that can change its size does so, to fill the window.
- A desktop that cannot is scaled to fit the window, keeping its shape.
- A terminal gains or loses rows and columns.
- Everyone else sees the owner's screen scaled to their own window.

The page asks for the window's size in CSS pixels, so programs on the
remote look the same size on a high-density display as on any other.
A browser zoom changes the size in CSS pixels. The owner's zoom
therefore resizes the remote for everyone: at 50%, the remote gets
twice the width and height, and its programs look half as large. Zoom
only scales the view for everyone else.

## Leaving and coming back

A session outlives the owner's page for a minute. If you close the tab
or lose the network, the session runs on, and others in it keep
watching. Come back within 60 seconds and you return to the session as
you left it. Otherwise it ends, and the others see "The owner left and
did not come back in time."

- **Connection lost.** When the page loses `occamyd`, it says
  "Connection lost" and offers **Join again**.
- **Reload.** A reload asks you to sign in again, saying "Sign in again
  to return to" the connection's name. Sign in within the minute and
  the page rejoins the session. **Go to the connections instead** skips
  it. Signing out forgets the session.
- **Opened elsewhere.** If a newer page of yours joins the session, such
  as a duplicated tab, it takes the session over. The older page says
  "Opened elsewhere", and **Join again** takes the session back.

If the session ended while you were away, less than a minute before,
the page says how it ended. After that, it says "Access refused".

A session that nobody gives input to for 30 minutes ends.

## When a session ends

The page says why the session ended, and what you can do.
**Details** shows the technical reason, for a bug report.

| Title | What it means |
|-------|---------------|
| Session ended | The owner ended it, the owner left and did not come back in time, or the remote ended it, as when its user logs out |
| Ended for inactivity | Nobody gave input for 30 minutes |
| Remote unreachable | `occamyd` could not connect to the remote, or lost it |
| Could not connect | `occamyd` could not open the session, and says why |
| Connection lost | The page lost `occamyd`. **Join again** returns to the session if it still runs |
| Disconnected: too slow | `occamyd` heard nothing from the page for 30 seconds, by default: the network stalled, or the page could not keep up. **Join again** |
| Opened elsewhere | Another page of yours took the session over. **Join again** takes it back |
| Signed out | Your sign-in expired. **Sign in** |
| Link not valid | The share link expired, or its session ended. Ask the owner for a new one |
| Access refused | The connection or session is not open to you, the session has ended, or you have as many sessions as `occamyd` allows |
| Session full | The session has as many viewers as `occamyd` allows, 50 unless the configuration says otherwise |
| Server shut down | `occamyd` stopped, which ends every session. Try again once it is back |
| Protocol error | A bug in `occamyd` or in the web client. Report it with the details |

**Back to connections** returns to the list. Someone who joined by a
link gets **Close** instead.
