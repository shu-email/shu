# The window is one stream and one field

Status: preview. A run shows it with `SHU_WINDOW=shell`; the three-pane
window is still what a run draws by default, until the tests that walk
the sidebar are migrated.

## Context

The window kept a sidebar for as long as it kept a folder tree: an
account switcher, every way into the mail, the labels, the pinned
searches, and a footer of sync state. It is the shape every mail client
has had for thirty years, and it spends a third of the window on a
structure the user rarely reads and mostly maintains.

The first cut of the shell still drew a reading pane beside the stream,
and its open question was whether reading should one day open over the
stream as a sheet. Triage and reading kept negotiating for width: the
list wanted room for its previews and chips, the pane wanted a measure a
person can follow, and at the window's minimum one of them always gave
way. The window is one thing at a time, and its layout said otherwise.

## Decision

The shell (`app/shell.go`, `app/palette.go`) draws one column:

- **A bar**: the window controls, the account's picture, **one field**,
  the state of receiving mail, sync, compose, and the overflow menu.
- **The stream, or what is read**: the column in the middle of the
  window is the stream's — the list, at a readable width, with its
  title, its counts and its band of what the rows ask of the user —
  until a row is chosen to be read, and what is read then takes it
  whole, at the same width, with its own floating toolbar and a way
  back first among its actions. Going back, and moving, deleting or
  archiving what is open, gives the column back to the stream.
- **A line at the bottom**: how the watcher and the queue are going, the
  account's counts, and whose account it is.

The column holds its width and the row centers it (`Justify`, not a
pair of spacers: a grow there shares the window out between growers and
the column ends a third of the window wide).

Choosing is opening: a click, Enter, or J/K on a row puts the window
into reading. The keys of the mail are the window's, not a subtree's —
the reading place holds no list for a focus subtree to lean on — and a
field the focus is in takes its keys first, while the palette and the
dialogs, over the window, take them while they show. The shell keeps
its stream unchosen when a folder opens: a row is read when it is asked
for, and a chosen row a click does not change would not open.

The field is the window's only way to change what it shows. It opens a
command palette (`app/palette.go`) listing the ways into the mail, the
account's labels, the pinned searches, and the screens and commands a
user reaches in a session; what is typed filters the rows, and a word
that names nothing is offered as a search of the list on screen. There
is no folder tree anywhere in it, and no sidebar to hide or show.

## Consequences

- Nothing is a place the user maintains: not a tree, not a rail. What
  the window shows is one thing, chosen by name, by words, or by a row.
- Reading at the stream's measure is wide, so the reading bar keeps its
  action groups folded into menus whatever the window's width, and its
  search field comes by its magnifier rather than sitting beside them.
- Archiving what is read returns the stream at once, with the row that
  took the read one's place checked; Enter or J opens it. A row already
  chosen is not opened by clicking it again (a change of choice is what
  a click reports, as in the three-pane window) — Enter, or another
  row, is the way.
- The sidebar's footer moves to the line under the stream; the watch
  state also becomes a dot in the bar, which is where a status belongs
  when it is not being acted on.
- Keyboard reach grows: ⌘K opens the field from anywhere, and the keys
  of the mail (J, K, E, R, F, U, S, Delete, L, Escape) run wherever the
  focus is, stepping between conversations while one is open.
- The shell reads in one column of its own measure, so its window holds
  together far below the size the three panes needed: its minimum is
  720×520 against the old window's 1000×640.
- The sidebar's own tests — geometry, the hide and show motion, the
  footer's line, every check that a folder is listed — describe a window
  that no longer exists. Migrating them is the work that makes the shell
  the default: they are mechanical, and fake-service driven, but there
  are more than a hundred call sites.
- The palette is not yet a search of the mail: it lists structure. A
  sentence typed into it is a search of the list on screen until the
  question-in-words step lands (#130).

## Open

- Whether what is read keeps the stream's measure or takes a narrower
  one of its own: the same width keeps the column still as reading
  opens, and a measure of its own reads easier.
- Whether the account's picture earns its place in the bar, or the
  account belongs in the field.
- Migrating the tests, then deleting `mailView` and `app/sidebar.go`.
