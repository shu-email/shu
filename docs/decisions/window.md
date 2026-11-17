# The window is a brief, a stream, and a page

Status: decided. The shell draws the stream and a reading pane today; this
is the structure it becomes.

## Context

The last thirty years of mail clients share one skeleton: a rail of
folders, a list, and a reading pane. Shu has dropped the rail
([navigation.md](navigation.md)) and drawn the list as cards under a
caption ([shell.md](shell.md)), but the skeleton is still a client's: the
list is the first thing seen, and the model is a button on it. A window
whose first screen is a list cannot be AI-native, whatever it does after
the list.

The question this answers: **which of the window's three surfaces does
the model answer?** The first one.

## Decision

The window is four layers, and nothing else:

1. **L0 — the brief.** What a run shows. Four to six things that ask
   something of the user today — a reply owed, a code, a date, an amount
   — each one line, opening straight into its message; then one line for
   the rest (*61 just news*), which opens the stream.
2. **L1 — the stream.** The list, a page at a time, as cards under the
   captions of what they ask (needs you / just news), or under the topics
   the model gave them ([Look](../decisions/look.md), `app/topics.go`).
3. **L2 — the page.** Reading is a page, not a pane: choosing a row
   replaces the window, with one way back. A page is a measure for
   reading — a large subject, a narrow column, no floating chrome over
   the text.
4. **Navigation.** One field, opened with ⌘K. It lists the ways in, the
   labels, the pinned searches and the app's screens, and what is typed
   is run: a search of the mail, a query in words, or a command. There is
   no rail, no chips row, and nothing persistent beside the mail.

### The brief without a model

The brief is the model's work, and it is also the rules'. Its lines are
built from what the cache already knows — a code extracted from a
preview, an amount or a due date, a conversation that waits for an
answer (`followup`), a reminder due — and the model's only share is the
sentence that ties them together. With AI off, the same layout shows the
same lines with plainer words, and one line says it is this device's
sorting, not a model's.

That is what makes the structure honest: **nothing in it exists only
because a model is available.** Turning AI on makes the brief read
better, never makes it appear.

## Consequences

- The reading pane goes, and with it the pane's floating toolbar: a page
  carries its actions in its own head, where a reader expects them.
- The list stops being the landing surface. A user who wants it says so —
  *Show all mail*, ⌘0 — and a user who wants a folder of it types its
  name.
- The brief is a cache, like a summary: one row of items and one
  sentence, computed when the account's mail changes, never on a frame.
  An empty or slow brief must not hold the window: it draws what the
  rules already know, and fills in.
- The window's least size drops again: a page and a field hold at
  700×520.
- Every test that walks the list as the landing surface describes a
  window that will not exist; they are migrated with the rest
  ([roadmap.md](../roadmap.md)).

## Open

- How much of the brief is a sentence and how much is a list: a
  paragraph reads better, a list is scannable. Start at three lines of
  items and one sentence of summary.
- Whether the brief keeps "what you did yesterday" (sent, filed, closed
  loops) or only what is open.
- Whether a page scrolls its conversation or steps message by message.
