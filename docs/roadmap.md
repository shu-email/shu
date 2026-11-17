# Where Shu's window is going, and how far it has got

A living page for the intent-first work: what was decided, what shipped,
what is a preview and what is still ahead. The decisions themselves are
one file each in [decisions/](decisions/); the direction and its steps
are issue [#130](https://github.com/shu-email/shu/issues/130), and the
first change is [#129](https://github.com/shu-email/shu/pull/129).

The question it answers came from a reader of the launch post:

> looks great but i wonder if you can actually RETHINK emails altogether.
> do we even need to see folders/labels? just show me what (you think) i
> want to see or let me search for it in natural language.

## The bet

The machine proposes structure, the cache is the source of truth, the
user confirms and can undo. Nothing in this direction lets AI move,
delete, send or forget mail on its own.

Three rules it keeps:

- **A folder is where mail sits on a server, not where the user keeps
  it.** Sync, rules, blocks and moves still name folders; the window
  does not walk a tree of them.
- **What a message asks of the user is read on this device**, by rules
  before models, from the preview the cache already keeps. A code is
  exact, short-lived, and must not leave the device to be read.
- **One accent, one stream, one field.** The look and the shell are
  calm; the window is mail, not a form.

## Shipped

| What | Where | Commit |
| --- | --- | --- |
| The sidebar lists ways into the mail, not the account's folders; folder management and drag-to-folder are gone | `app/nav.go`, `app/folders.go` | `fbfa527` (#129) |
| A message's code, amount and due date are read on the device by rules, never a model | `mail/extract.go` | `fbfa527` |
| A list fetch carries a longer preview so those are within it (2048 bytes, 400 runes) | `mail/imap_fetch.go`, `mail/mime.go` | `fbfa527` |
| A row shows them as outlined chips that copy on click, and says "Copied" where it happened | `app/stream.go`, `app/messagelist.go` | `fbfa527` |
| The stream: every account's inbox, with a line above it counting what the rows ask of the user | `app/stream.go` | `fbfa527` |
| The seeded dev mailbox has a sign-in code and an invoice, to see the above by hand | `internal/fakemail/seed.go` | `06d4164` |
| Paper and ink: the app sets its own theme once per frame, one teal accent, tinted (not filled) chosen rows, outlined chips | `app/theme.go` | `5823258` |
| Tints are mixed with the pane's color, not left translucent over the window's material | `app/banner.go`, `app/messagelist.go` | `42f08a0` |

What the shipped window keeps from before: the three panes, the reading
pane's toolbar, every action, labels, pins, rules, blocks, the outbox,
drafts, sync and the queue. What it no longer has: a folder tree, making
or renaming or deleting a folder, dragging mail onto one.

## Preview

**The shell** — a window of one stream and one field, no sidebar
(`app/shell.go`, `app/palette.go`, [decisions/shell.md](decisions/shell.md)).
A run shows it with:

```sh
GOFLAGS=-tags=e2e SHU_SEED=1 SHU_WINDOW=shell go tool mygo dev -lang en
```

- A bar: the account's picture, **one field**, the state of receiving
  mail as a dot, sync, compose, the overflow menu.
- The stream in a readable width in the middle of the window.
- Reading opens over the stream: what a chosen row reads takes the same
  column, at the same width, with a way back first among its actions —
  and going back, or archiving what is open, gives the column back to
  the stream. The window is one thing at a time.
- A line at the bottom: the watcher, the queue, the counts, the account.

The field opens a command palette: the ways into the mail (stream,
starred, follow up, reminders, drafts, sent, archive, junk, trash), the
account's labels, the pinned searches, and the commands a session
reaches for (compose, settings, rules, contacts, sync, group by
conversation). Typing filters them; a word that names nothing is offered
as a search of the list on screen.

**Why it is not the default yet.** The test suite is the contract, and
more than a hundred of its call sites describe the sidebar window: its
geometry (`x >= 223 && x <= 607`), its tree rows, the hide-and-show
motion, the footer's watch line. Making the shell the default without
migrating them leaves about two hundred failures. Until then the sidebar
window stays the default and the shell is one environment variable away.

## Next

0. **Cards, grouped by topic — done.** The list draws a message as a
   card (`messageCard`), under a caption its group shares
   (`app/groups.go`). The caption is the day's until the user asks for
   the model's: ⌘K → *Group by topic* (`app/topics.go`) sends the
   **subjects alone** of up to `ai.TopicLimit` rows, behind the same
   review gate every capability has (`aiReviewTopicsSize` says how many
   and how many characters), and the model answers one topic per
   numbered subject. The cards are ordered by the topics as the model
   first names them, the selection is kept by key, and the store's
   cursor is untouched, so paging still reads the same pages. Asking
   again drops the grouping. A subject the model says nothing about
   draws last under *Ungrouped*.
1. **The three-pane window is gone.** `mailView`, `app/sidebar.go`,
   `messageList`, `listHead` and `listHeader` were deleted with the shell
   made the window. Its tests stay red until they are rewritten against
   the shell: they are the four buckets below, and the migration is a
   change of its own. Nothing runs the old window any more.
2. **Land on the stream, and rank it.** The list's cursor is a date and a
   row, so ordering by intent needs a store-side read — a rank column or
   a two-pass read — not a sort in the window, which would break paging
   when a page arrives.
3. **Ask the mail in words.** `mail.ParseSearch` first; when what is
   typed is not a query, the model fills in a query (`mail.Search` +
   `mail.Scope` + a time window) which the cache answers locally, and
   only then a summary over the results. The answer pins into a smart
   mailbox — that mechanism already exists.
4. **A rule proposed from a correction.** The user says "this is not
   mine"; the model drafts a `mail.Rule`, the user confirms,
   `Service.CheckRule` still refuses anything that sends, forwards or
   deletes, and `RuleActivity` carries the undo.
5. **Open loops**, behind a switch: a conversation plus a state and a due
   time, over `reminders` and `followup`. Only this one changes the
   schema.

## Open

- Whether what is read keeps the stream's measure or takes a narrower
  one of its own: the same width keeps the column still as reading
  opens, and a measure of its own reads easier.
- Whether the account's picture earns its place in the bar, or the
  account belongs in the field.
- Whether the list keeps its hairline once rows are spaced further.
- A type scale of its own: a larger base and a display face for a
  conversation's subject, and the shell's window already holds together
  at 720×520.
- The dev build's data directory still holds an old `Demo` account whose
  keychain entry is gone, so every run opens with a red sync note.