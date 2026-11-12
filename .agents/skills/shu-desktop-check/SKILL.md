---
name: shu-desktop-check
description: Run the Shu dev build and drive its UI from the shell to check a change by hand, walk a flow, take screenshots or audit accessible labels, in the background without taking the user's focus. Uses MyGo's automation plugin (go tool mygo-automation) first and the agent-desktop CLI for what lies outside the window.
---

# Checking Shu by hand

Two tools reach the running app. Use the first unless the check needs the
second.

- **`go tool mygo-automation`**, MyGo's automation plugin. It works with
  the window in the background and covered: actions, the element tree and
  screenshots show what the app holds now, and the user keeps the focus.
  Elements carry Shu's own keys (`key=INBOX`, `key=Archive`), which do
  not change with the language.
- **`agent-desktop`**, which reads the system accessibility tree. Use it
  for what lies outside the window (the menu bar, notifications, the Dock
  badge) and to see what a screen reader really gets. A covered MyGo
  window holds its frames, so through agent-desktop a result that arrives
  later (a search, a sync) does not show until the window does.

Neither replaces `ui.NewTester` tests, which must still cover every
behavior change and run without a display.

## Safety

- Drive only the dev build, `shu Dev`. It keeps its data apart from a real
  install (`~/Library/Application Support/shu Dev` on macOS). Never attach
  agent-desktop to a release build: its buttons act on real mail.
- Never add a real account to the dev build. If it has none, stop and ask
  the user.
- Do not press Send, Delete, Move to Junk, Empty Trash or any AI button
  (summary, triage, draft reply) unless the user asked for that step. AI
  buttons send mail text to the user's model provider.
- Leave the app as you found it: clear the search, go back to the folder
  you started in, close windows you opened.
- Screenshots hold mail. Keep them in a temporary directory; never commit
  them.

## Launch in the background

`automation_e2e.go` links the plugin only under the `e2e` tag, and it
listens only when `MYGO_AUTOMATION` names a socket. Put the socket in a
directory only the user can write to (`$TMPDIR` on macOS,
`$XDG_RUNTIME_DIR` on Linux), not in `/tmp`.

```sh
SOCK="${XDG_RUNTIME_DIR:-${TMPDIR%/}}/shu-automation.sock"
GOFLAGS=-tags=e2e MYGO_AUTOMATION="$SOCK" go tool mygo dev -background -lang en
```

Run that as a background job. `-background` starts the app, and restarts
it after each rebuild, without bringing it to the front. `-lang en` fixes
the language for this run only and leaves the user's setting alone; use
`-lang zh-Hans` to check the Chinese strings.

Wait until the socket exists, then attach:

```sh
go tool mygo-automation open --socket "$SOCK"
```

When done, `go tool mygo-automation close` and stop the `mygo dev` job;
`close` alone leaves the app running. A rebuild restarts the app, which
ends the session's refs: `open` again.

## Observe, act, verify

`go tool mygo-automation skills` prints the full guide for the pinned
version. Read it once per session. The loop is snapshot, act with
`--settle`, read the diff, act again:

```sh
go tool mygo-automation snapshot -i
go tool mygo-automation press key=Archive --settle      # open a folder
go tool mygo-automation fill @e2 "invoice" --settle     # @e2: the search field
go tool mygo-automation wait --text "Search results"
```

- Prefer selectors by key, which last across runs, then refs from the
  latest snapshot or diff. Prefer `press` and `fill`; they need neither
  coordinates nor focus.
- `--settle` waits for the frames the window asked for, not for work in
  the background. For results Shu loads off the UI thread (search,
  opening a message, sync, AI), wait for what you expect with
  `wait <sel>` or `wait --text <s>`; never sleep.
- `press` really presses. Check the state before you repeat one, or a
  second press archives a second message.

## Shu's tree

- Title bar: Hide sidebar, the search field (`textField`, with a Clear
  button once it has text), Sync, Settings, Compose.
- Sidebar: the Accounts `menuButton`, then the Mailboxes `tree`, with a
  `treeItem` per folder keyed by its name (`key=INBOX`, `key=Sent`,
  `key=drafts`, `key=starred`); the open one is `checked`.
- Message list: a `list` of `listItem`s named "sender subject", keyed
  `thread/<account>/<id>` when grouped by conversation; the open one is
  `checked`. The list ends with a button that loads older mail from the
  server. A search with no match shows a no-results text and Clear Search.
- Reading pane: archive, delete, junk, star, unread, reply, reply all,
  forward and summarize, worded for a conversation when the list is
  grouped by conversation, then one group of buttons per message.

## Screenshots

The tree tells what is there, its name and its state, which is enough for
behavior and labels. Take a screenshot only to judge looks: layout at the
minimum window size (1000×640), light and dark appearance, clipping and
overlap.

```sh
go tool mygo-automation screenshot "${TMPDIR%/}/shu.png"
```

The plugin renders the window's content as it is now, without the title
bar the system draws, even while the window is covered.

## agent-desktop

Install it when a check needs it:

```sh
brew install agent-desktop          # macOS and Linux
npm install -g agent-desktop        # any OS, Node 18+
agent-desktop status                # permissions and supported surfaces
agent-desktop skills get desktop    # the guide for the installed version
agent-desktop skills get platform   # what differs on this OS
```

- macOS needs Accessibility for the terminal (`agent-desktop permissions
  --guide`), and Screen Recording for screenshots. Ask the user to grant
  them; do not try to work around a denied permission.
- Linux needs the graphical session's AT-SPI bus; an empty
  `supported_surfaces` means it did not answer.
- Address the app as `--app "shu Dev"`. Names follow the app's language.
- To see a later result through it, the window must show: run
  `agent-desktop focus-window --app "shu Dev"` only after an element has
  appeared (`snapshot --wait-for "textfield:"`), and tell the user, since
  it takes their focus.

## Auditing labels

List the interactive elements of `snapshot -i` that have no label. A
button without one is an icon-only button missing its `Label`, which
AGENTS.md requires; fix it in the view and cover it in a `ui.NewTester`
test. An unnamed container (`list`, `tree`) is worth a label too, so a
screen reader can say what the list is, but report it rather than fixing
it on the side of another change.
