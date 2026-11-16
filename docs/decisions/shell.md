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

## Decision

The shell (`app/shell.go`, `app/palette.go`) draws one column:

- **A bar**: the window controls, the account's picture, **one field**,
  the state of receiving mail, sync, compose, and the overflow menu.
- **The stream**: the list, in a column of a readable width, in the
  middle of the window, with its title, its counts and its band of what
  the rows ask of the user.
- **The reading pane** beside it, as before: a message, opened by
  choosing its row, with its own toolbar.
- **A line at the bottom**: how the watcher and the queue are going, the
  account's counts, and whose account it is.

The field is the window's only way to change what it shows. It opens a
command palette (`app/palette.go`) listing the ways into the mail, the
account's labels, the pinned searches, and the screens and commands a
user reaches in a session; what is typed filters the rows, and a word
that names nothing is offered as a search of the list on screen. There
is no folder tree anywhere in it, and no sidebar to hide or show.

## Consequences

- Nothing is a place the user maintains: not a tree, not a rail. What
  the window shows is one list, chosen by name or by words.
- The sidebar's footer moves to the line under the stream; the watch
  state also becomes a dot in the bar, which is where a status belongs
  when it is not being acted on.
- Keyboard reach grows: ⌘K opens the field from anywhere, and the keys
  of the list (J, K, E, R, F, U, S, Delete, L) live in the stream's
  scope.
- The sidebar's own tests — geometry, the hide and show motion, the
  footer's line, every check that a folder is listed — describe a window
  that no longer exists. Migrating them is the work that makes the shell
  the default: they are mechanical, and fake-service driven, but there
  are more than a hundred call sites.
- The palette is not yet a search of the mail: it lists structure. A
  sentence typed into it is a search of the list on screen until the
  question-in-words step lands (#130).

## Open

- Whether the reading pane stays beside the stream or opens over it as a
  sheet once the list has its own ranking: the sheet is better for
  reading, the pane is better for triage.
- Whether the account's picture earns its place in the bar, or the
  account belongs in the field.
- Migrating the tests, then deleting `mailView` and `app/sidebar.go`.
