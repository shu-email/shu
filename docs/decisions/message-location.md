# Messages, or messages plus message locations

Status: decided — **no-go** for now. This note changes no code and no
production schema. It compares the cache's current shape with a
`messages` + `message_locations` one, so that a later change has the
groundwork and this decision need not be argued again from nothing.

## Recommendation first

**No-go.** Shu keeps one row per message copy in a folder. A message
identity table with a location per mailbox is the right model for a
client whose unit is the message (Gmail labels as identity); Shu's unit
is the mailbox, as IMAP's is, and the features that need message
identity already deduplicate by it — conversations, contacts, local
labels, attachment bytes. Normalizing would rewrite the write path every
feature depends on, plus a table rebuild and a full re-index, and would
buy no capability on its own. Revisit it if Shu adopts multi-folder
labels as a first-class thing. The rest of this note is the comparison
the issue asks for, and the follow-ups at its end.

## Context

Shu's cache keeps **one row per message copy in a folder**:
`messages(folder_id, server_uid)` is unique. A move tombstones the
source row (`deleted = 1`), the server's copy is made, and the source row
is deleted once the server confirms; the destination row is written by
the next sync of that folder, so a move reads as the message leaving the
folder until that sync brings it there (`mail/changes.go`,
`mail/queue.go`, `store/messages.go`). Geary (`MessageTable` +
`MessageLocationTable`), meli (`folder_and_envelope`) and Penguin Mail
(`message_mailboxes`) instead keep one row per message identity and a
row per mailbox it appears in.

What the current model already shares across copies:

- Conversations are the cache's own: every message row gets a
  `thread_id` from `mail.ThreadKeys` (Message-ID, In-Reply-To,
  References, Thread-Index), and `thread_refs` remembers keys per
  account. Copies of a message in two folders therefore join the same
  conversation.
- `messages.msg_key` (Message-ID, or a digest of sender, date and
  subject) is the same on both copies, and local labels and move
  expectations travel by it (`mail.LabelKey`).
- A contact counts a message once by its contact key — the Message-ID,
  or the same digest — not once per copy (`contact_messages`).
- The user's own labels are kept by message identity: `local_labels`
  names `msg_key`, so a label follows the message through moves and
  copies. A server label is per copy in the cache (`message_labels`
  names the copy's row), as a keyword is per mailbox on the server;
  normalizing would have to re-key those rows.
- Attachment bytes are content-addressed on disk and deduplicated by
  content hash; only the small metadata rows repeat.
- The reading pane shows a message once, deduplicating its copies by
  Message-ID in the view (`shownMessages` in `conversations.go`), not in
  the store: `Store.ConversationMessages` returns every cached copy in
  the scope.

Only a list whose scope spans folders counts a copy twice
(`mail.LabelScope`, a smart mailbox with the account scope). That is
where the alternative is genuinely better: Gmail's IMAP presents one UID
per label folder for the same message, so an account-wide or label-wide
list shows it twice. Nothing else in the tree needs the message split
from its mailboxes.

## The alternative, sketched

```sql
-- One row per message identity in an account. Every header, the body,
-- the security state and the conversation live here: today's messages
-- table less its mailbox columns (thread_index, recipients, calendar and
-- the rest stay as they are).
CREATE TABLE messages (
  id INTEGER PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  msg_key TEXT NOT NULL,              -- already there; now unique per account
  message_id TEXT, in_reply_to TEXT, references_json TEXT,
  from_name TEXT, from_email TEXT, to_json TEXT, cc_json TEXT, reply_to_json TEXT,
  subject TEXT, preview TEXT, date INTEGER, size INTEGER,
  has_attachments INTEGER NOT NULL DEFAULT 0,
  body_text TEXT, body_html TEXT, body_truncated INTEGER NOT NULL DEFAULT 0,
  raw_headers TEXT, security TEXT, cached_at INTEGER,
  thread_id INTEGER REFERENCES threads(id) ON DELETE SET NULL,
  UNIQUE (account_id, msg_key));

-- One row per mailbox copy: what the server keeps per mailbox (UID,
-- flags), and where the copy stands. UIDVALIDITY stays on folders.
CREATE TABLE message_locations (
  id INTEGER PRIMARY KEY,
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  server_uid INTEGER NOT NULL,
  seen INTEGER NOT NULL DEFAULT 0,
  flagged INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,   -- tombstone of the copy
  internal_date INTEGER,
  UNIQUE (folder_id, server_uid));
CREATE INDEX message_locations_message ON message_locations (message_id);
```

Headers, body, security and the conversation stay on the message; the
UID, `\Seen`, `\Flagged`, INTERNALDATE and the tombstone move to the
location. `thread_folders` becomes an aggregate over
`(thread_id, folder_id)` joined through locations. The FTS index stays
external-content over `messages` with one row per identity. The label
tables carry over as they are: `message_labels` keeps naming a message,
`local_labels` a `msg_key`.

## Read paths

A folder page, newest first after a cursor, is today's query with a
join:

```sql
SELECT m.id, m.date, m.subject, m.from_name, m.from_email,
       l.server_uid, l.seen, l.flagged
FROM message_locations l
JOIN messages m ON m.id = l.message_id
WHERE l.folder_id = ?
  AND (m.date, m.id) < (?, ?)        -- the mail.Cursor
ORDER BY m.date DESC, m.id DESC
LIMIT ?;
```

A list that spans folders groups by identity, so a copy in two folders
is one row:

```sql
SELECT m.id, m.date, m.subject, ...
FROM messages m
JOIN message_locations l ON l.message_id = m.id
WHERE l.folder_id IN (?, ?, ...)      -- the scope's folders
  AND NOT l.deleted
GROUP BY m.id
ORDER BY m.date DESC, m.id DESC
LIMIT ?;
```

A label's scope adds `JOIN message_labels ml ON ml.message_id = m.id`
and the same `GROUP BY`, which is the duplicate suppression the issue
names. A conversation read joins locations in the scope to messages by
`thread_id`, deduped by `message_id` in the store; the window would no
longer need `shownMessages`' Message-ID pass. Search joins FTS to
`messages`, then to locations in the scope, one hit per message.

Paging keeps `mail.Cursor` (the date and the row's cache number), which
is already independent of the mailbox: the row stays a message id (or a
thread id for conversations), so the cursor's form does not change; only
what the row means does. Watermarks (`TakeNew`) stay per folder and read
locations. Attachments key on `message_id` and stop repeating per copy.
Rules and their log name `(account, folder, UIDVALIDITY, UID)` today;
they would name a location ID, and the per-folder trigger conditions
still read the location.

## Write paths

A sync upserts a message by `(account_id, msg_key)` for each new copy,
writes the location for the folder it read, then threads it as today
(reads before writes, under the store's threading mutex). A folder
already synced works the same for a new copy: one message, one location.
It is the first sync of a folder that creates locations only where the
message is already cached from another folder.

A server-side move is one message with two location rows:

```sql
BEGIN;
INSERT INTO message_locations
  (message_id, folder_id, server_uid, seen, flagged, internal_date)
SELECT message_id, ?, ?, seen, flagged, internal_date
FROM message_locations WHERE id = ?;
UPDATE message_locations SET deleted = 1 WHERE id = ?;   -- tombstone
COMMIT;
-- after the server confirms the copy: DELETE the source location
```

The message row does not move; its conversation does not change. An
undo is the inverse: create the source location again (or revive its
tombstone) and tombstone or delete the destination. A copy expunged
elsewhere deletes its location; the message row goes with its last
location. A body change on one copy would touch the message shared by
all copies — IMAP bodies do not change in practice, but the rule would
be first writer wins, by `cached_at`.

Flags write the location. `SetFlags` addresses `MessageRef{folder, uid}`
today and would keep doing so — a location is exactly that pair. Labels
are unchanged after the backfill: `message_labels` still names the
message row, and the move no longer touches it. A sync's keywords are
still per mailbox, though, so two copies of one message with different
keywords would merge into one set on the shared message; that is a
decision the normalization has to make, and one more reason it is not a
pure win.

## Migration

1. Create `message_locations`; add `account_id` to `messages` (`msg_key`
   and `thread_id` are already there, the latter enforced by triggers).
2. Backfill in chunks outside the transaction, as
   `docs/schema-migrations.md` asks:
   - group the existing rows by `(account_id, msg_key)`;
   - pick a keeper (the one with a body, else the newest `cached_at`)
     and write one location per row, copying its folder, UID, flags and
     tombstone;
   - re-key `attachments.message_id` and `message_labels.message_id`
     through a temporary map from old message IDs to the keeper;
   - `contact_messages` and `local_labels` need nothing: they are keyed
     by the contact key and `msg_key` already.
3. Rewrite the triggers that keep `threads`, `thread_folders` and
   `messages_fts` in step, then rebuild the FTS index.
4. Rebuild `messages` with SQLite's table-rebuild dance to drop
   `folder_id`, `server_uid`, `seen`, `flagged`, `deleted` and the unique
   constraint; recreate its indexes.
5. `store/migrations_test.go` gains a seed at the new version with one
   message in two folders; `checkUpgradedData` proves it became one
   message and two locations. `mail.CacheVersion` is bumped, so parsed
   bodies and attachment rows are dropped and fetched again.

**Size.** The backfill reads the message rows twice (group, then
write) and writes about as many location rows as there are messages.
The `messages` rebuild rewrites the whole table and its FTS index, so
the migration's write volume is roughly three times the cache's
current size: a cache of 20,000 headers and a few hundred cached bodies
is a one-off write in the tens of megabytes. It is one transaction, as
the migration rules require, and a long one.

The upgrade matrix grows by the new version and by the two-folder case,
since `docs/schema-migrations.md` requires every past version to reach
the current schema.

## Rollback

Migrations are forward-only: an older build refuses a database whose
`user_version` is newer (`Open` compares it to `SchemaVersion` before it
reads a row). Dual-writing `folder_id`/`server_uid` for one release
would not help, because the older build still refuses the version. So
the honest rollback is a restored copy:

- Copy the database file aside before the migration
  (`shu.db.bak-v<n>`), and remove the copy once the new version has run
  cleanly in a release. A rollback restores it, losing the mail cached
  after the upgrade.
- Or accept no rollback, as the pre-alpha stance already does for every
  schema change.

## Cost against this tree

- `store/` has 51 Go files, 27 of them tests. 37 files mention
  `messages`, and 26 touch `folder_id` or `server_uid`; every query that
  joins, pages, searches, aggregates threads, contacts, labels, rules,
  watermarks and the queue is re-read.
- Six engine files in `mail/` (`block.go`, `queue.go`, `reminders.go`,
  `changes.go`, `store.go`, `undo.go`) and nine test files use
  `MoveMessages`, `MoveBackMessages`, `MarkDeleted`, `SetFlags` or the
  queue. Their public shape (`MessageRef`, `MoveResult`) survives, but a
  result's meaning (a moved copy) becomes a location.
- The UI is unaffected: views reach the engine through `Service` with
  `mail.MessageRef`, `mail.Scope` and `mail.Summary`. `Summary` would
  stay one per message identity, with a folder list added, and
  `shownMessages` could keep only its special-folder choice.
- Tests: every store test that inspects `messages` directly, and the
  IMAP/scenario tests that assert tombstones and moves, need rework.

## Follow-ups

Smaller steps that do not need the schema change, in the order they
would be worth taking:

- Suppress duplicate rows by `msg_key` in scopes that span folders
  (`LabelScope` and smart mailboxes with the account scope; the unified
  inbox is safe). This is the one visible benefit of normalization and
  it is a read-layer change.
- Keep `msg_key` the single definition of message identity; it is what
  a future normalization would group by.
- Deduplicate attachment metadata per identity: the rows still repeat
  per copy while their bytes do not.

Revisit normalization only if Shu adopts Gmail-style labels as
first-class multi-folder labels, where a message is intentionally in
several mailboxes and each copy is not a separate thing.
