# Thread aggregates: counts kept by triggers, not counted per read

Status: accepted, in use since migration v16.

## Context

The window lists conversations, not messages. A row needs a
conversation's newest date, counts (messages, unread, starred, with
attachments), a preview and its participants, and a list narrowed to a
folder needs those counts in that folder alone. Computed per read, that
is an aggregate over every message row of every conversation on the
page, and a conversation can hold hundreds of messages in several
folders. The cache is also written by syncs, moves, flag changes and the
queue, so any cached aggregate would drift unless every writer
remembered it.

## Decision

`threads` keeps a conversation's totals for its account;
`thread_folders` keeps the same totals per folder. SQLite triggers write
both in the transaction of each insert, delete or update of a message,
including a move's tombstone and its removal (`store/thread_aggregates.go`,
`store/store.go` migration v16). `Store.RebuildThreadAggregates`
recomputes them from `messages`, and `Service.CheckIntegrity` verifies
them without changing anything.

The trigger body and the rebuild share one SQL fragment, so the two
cannot mean different things.

## Consequences

- A list reads one row per conversation, or per conversation and
  folder, instead of aggregating messages: paging stays a simple index
  scan.
- Every write of a message pays the aggregate cost, but inside the
  transaction that already holds the row; no writer can forget.
- The aggregates are derived state. They may be rebuilt at any time,
  and nothing else may treat them as truth: a message hidden while it
  moves (`deleted = 1`) counts in neither, and `thread_refs` remains the
  only record of who a conversation is.
- Adding a column to a row means changing both the trigger and the
  rebuild, in one change.
