# Full-text search: an external-content FTS5 index kept by triggers

Status: accepted, in use since migration v7, widened in v8.

## Context

Search is local and instant, and must find a message by its subject,
sender, recipients, body or preview. The body arrives later than the
headers, so the index has to change when a message's body is filled in
and when a sync refreshes a message it already holds. The engine writes
through several paths — a sync's batch, `FetchOlder`, flag refreshes,
moves, the queue — under a store that owns the transaction.

## Decision

`messages_fts` is an FTS5 table with `content='messages'` and
`content_rowid='id'`, tokenized by trigram, holding subject, sender,
recipients, body and preview (`store/store.go`, migration v8).
After-insert, after-delete and after-update triggers keep it in step
inside the transaction that writes the row, using FTS5's `delete`
command for the old values of an update. There is no separate indexing
job, no queue of documents, and no eventual consistency window.
`Store.CheckSearchIndex` proves the index is in step, and a schema
change that adds a column drops the triggers, recreates the table and
rebuilds it in the same migration.

## Consequences

- A message is searchable the moment its transaction commits, including
  one cached with only headers and a preview.
- Writers pay the index cost inside their own transaction. A large
  migration that rebuilds the table writes the whole index once.
- Every column added to the index is a change to the table, the three
  triggers and the rebuild together; forgetting one shows up in
  `Service.CheckIntegrity`.
- The trigram tokenizer answers terms of three characters and more;
  shorter terms fall back to a `LIKE` scan in the read path, which is a
  read concern, not an index one.
- Trigram tokens are never operators: `mail.ParseSearch` treats each
  term as a phrase, so typed text cannot inject FTS syntax.
