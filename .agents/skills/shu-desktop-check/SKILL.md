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
- Leave the app as you found it: clear the search (`fill <field> ""`),
  go back to the folder you started in, move the keyboard focus back to
  the list (`focus <row>`), close windows you opened.
- Screenshots hold mail. Keep them in a temporary directory; never commit
  them.

## Launch in the background

`automation_e2e.go` links the plugin only under the `e2e` tag, and it
listens only when `MYGO_AUTOMATION` names a socket. Put the socket in a
directory only the user can write to (`$TMPDIR` on macOS,
`$XDG_RUNTIME_DIR` on Linux), not in `/tmp`.

Shell calls of an agent keep no variables, so set `SOCK` in each one, and
call the client through a function: zsh does not split `$A` in
`A="go tool mygo-automation"; $A open`.

```sh
SOCK="${XDG_RUNTIME_DIR:-${TMPDIR%/}}/shu-automation.sock"
M() { go tool mygo-automation "$@"; }
front() { osascript -e 'tell application "System Events" to get name of first application process whose frontmost is true'; }
```

Note `front` (macOS), then start the app as a background job:

```sh
rm -f "$SOCK"   # a stopped app leaves its socket behind
GOFLAGS=-tags=e2e MYGO_AUTOMATION="$SOCK" go tool mygo dev -background -lang en
```

`-background` starts the app, and restarts it after each rebuild, without
bringing it to the front. `-lang en` fixes the language for this run only
and leaves the user's setting alone. A cached build takes a few seconds.

Attach once the app answers, then check that `front` still names the
user's app:

```sh
for i in $(seq 60); do M open --socket "$SOCK" 2>/dev/null && break; sleep 1; done
```

A rebuild restarts the app, which ends the session's refs: `open` again.
To check the other language, relaunch: `close`, stop, start again with
`-lang zh-Hans`, `open`. Keys stay the same; labels change.

Stop with `M close`, which leaves the app running, then
`pkill -INT -f 'mygo dev -background'`, which quits it.

## Observe, act, verify

`go tool mygo-automation skills` prints the full guide for the pinned
version. Read it once per session. The loop is snapshot, act with
`--settle`, read the diff, act again:

```sh
M snapshot -i
M press key=Sent --settle
M wait --text "Loading…" --gone
```

- Prefer selectors by key, which last across runs, then refs from the
  latest snapshot or diff. Prefer `press` and `fill`; they need neither
  coordinates nor focus.
- The open conversation's row shares its key with the reading pane, so
  `press` or `focus` by that key is refused as ambiguous; use the row's
  ref.
- `press` really presses. Check the state before you repeat one, or a
  second press archives a second message.

`--settle` waits for the frames the window asked for, not for work Shu
does off the UI thread. Wait for what that work shows; never sleep. Text
that changes at once proves nothing: the "Search results" heading and the
field's own value appear before any result does.

| After | Wait for |
| --- | --- |
| opening a folder | `wait --text "Loading…" --gone`; it then syncs and opens its first conversation |
| a search (`fill <field> "invoice"`) | `wait key=end` (the "No more results" row), `wait --text "Syncing…" --gone`, then read the count ("1 conversation") |
| clearing it (`fill <field> ""`) | `wait --text "Syncing…" --gone`, then the folder's count is back |
| opening a message | an element of the reading pane, not the subject, which the row already shows |

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
behavior and labels. Take a screenshot only to judge looks: clipping,
overlap, layout at the minimum window size.

```sh
M screenshot "${TMPDIR%/}/shu.png"
```

- The plugin renders the window's content as it is now, without the title
  bar the system draws, even while the window is covered. A window in the
  background is drawn inactive: selections are gray and accents muted.
- For the minimum size, `agent-desktop resize-window --app "shu Dev"
  --width 1000 --height 640` resizes without taking focus. It may answer
  `TIMEOUT` although the window did resize; check with `agent-desktop
  list-windows --app "shu Dev"`. The window stops at its own minimum
  height, which is a little more than 640 because of the title bar.
- The app follows the system appearance and has no override. Do not
  change System Settings; check the current appearance, say which one it
  was, and leave the other to the user.

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
- Reading the menu bar is headless:
  `agent-desktop snapshot --app "shu Dev" --surface menubar -i --compact`.
  Its Apple menu lists the user's recent items, which are their files:
  report only the app's own menus. Items macOS adds itself follow the
  system language, not `-lang`.
- To see a later result through it, the window must show: run
  `agent-desktop focus-window --app "shu Dev"` only after an element has
  appeared (`snapshot --wait-for "textfield:"`), and tell the user, since
  it takes their focus.

## Auditing labels

List the interactive elements that have no label, on each screen you
touched and in both languages:

```sh
M snapshot -i | grep -v '"'
```

The message list (`[list] selectable multiselectable`) is a known one. A
button without one is an icon-only button missing its `Label`, which
AGENTS.md requires; fix it in the view and cover it in a `ui.NewTester`
test. An unnamed container (`list`, `tree`) is worth a label too, so a
screen reader can say what the list is, but report it rather than fixing
it on the side of another change.
