# Actions the server cannot be reached for wait in a queue

Status: accepted, in use since migration v21.

## Context

A user changes mail offline, or while the server is down: stars it,
marks it read or unread, labels it, moves it. Until now the window could
only fail, and the change was lost. The alternative — applying only what
the network reaches, or blocking the action on a connection — makes the
user's intent depend on the network at the exact moment they act.
Whatever is kept must also be safe to replay: applying an action twice,
or applying one whose folder changed underneath, is worse than a
failure.

## Decision

`Service.SetFlags`, `Service.SetLabel` and `Service.MoveMessages` write
the action to `pending_operations` before the server is touched, show it
in the cache and return (`mail/queue.go`, `store/pending.go`). An
account's one queue worker, which runs beside its watcher, makes the
actions oldest first and takes each with one state change
(`Store.ClaimOperation`), so two workers never make the same action at
once. Actions one command can do together are claimed together
(`Store.ClaimOperationsByKey`).
A server that cannot be reached is retried with a growing wait; one that
refuses an action leaves it `failed` with its reason for the window, and
a refused move puts the message's row back. A folder whose UIDVALIDITY
changed since the action was queued is not touched, and a move whose
message the folder no longer has is done.

A block queues the move that files a blocked sender's mail
(`planBlocked`), though the block, not the arrival, is the user's
action: the mail must leave the check's path, so a server out of reach
delays neither the check nor what it announces. Its move is a queued
move like any other, made by the same worker and shown in the same
line.

Rules and their undo keep their own log of what has not happened, and a
reminder waits for its next run, so neither is queued: the queue is for
mail the user asked the server to change, one message at a time, made
now or by the worker.

## Consequences

- Writing mail works offline and survives a restart; the cache shows the
  change before the server hears of it.
- One claim decides an action, so no two workers make it in one run. A
  claim a crash left is taken back after a grace, and the kinds are ones
  a repeat cannot make worse: a flag set again, a label added again, a
  move whose message the folder no longer has is done. A later sync
  settles what the cache and the server disagree on.
- The queue has one worker per account and no parallelism inside one; a
  slow server delays later actions instead of overtaking them.
- Undo cancels the queued actions it can (`Store.CancelOperations`,
  only those no worker has begun) instead of making them first; the rest
  are taken back by the inverse change.
- The window learns of the queue as a `WatchEvent` with `Queue` set and
  shows it under the watcher's line, so a failure is visible rather than
  silent.
