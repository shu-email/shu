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
the issue asks for, and the two smaller follow-ups at its end.

## Context

Shu's cache keeps **one row per message copy in a folder**:
`messages(folder_id, server_uid)` is unique, and a move writes the
destination row, tombstones the source row until the server confirms,
and lets a sync drop the tombstone (`store/messages.go`,
`store/oldermail.go`, `mail/queue.go`). Geary (`MessageTable` +
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
- Contacts count a message once by Message-ID (`contact_messages`), not
  once per copy.
- Attachment bytes are content-addressed on disk and deduplicated by
  hash; only the small metadata rows repeat.
- A conversation-level read deduplicates its messages by thread; only a
  list whose scope spans folders counts a copy twice (`mail.LabelScope`,
  a smart mailbox with the account scope).

That last point is where the alternative is genuinely better: Gmail's
IMAP presents one UID per label folder for the same message, so an
account-wide or label-wide list shows it twice. Nothing else in the
tree needs the message split from its mailboxes.

## The alternative, sketched

```sql
-- One row per message identity in an account. Every header, the body,
-- the security state and the conversation live here.
CREATE TABLE messages (
  id INTEGER PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  msg_key TEXT NOT NULL,              -- Message-ID, or the digest
  message_id TEXT, in_reply_to TEXT, references_json TEXT,
  from_name TEXT, from_email TEXT, to_json TEXT, cc_json TEXT, reply_to_json TEXT,
  subject TEXT, preview TEXT, date INTEGER, size INTEGER,
  body_text TEXT, body_html TEXT, body_truncated INTEGER NOT NULL DEFAULT 0,
  raw_headers TEXT, security TEXT, cached_at INTEGER,
  thread_id INTEGER REFERENCES threads(id) ON DELETE SET NULL,
  UNIQUE (account_id, msg_key));

-- One row per mailbox copy: what the server keeps per mailbox (UID,
-- flags, keywords), and where the copy stands.
CREATE TABLE message_locations (
  id INTEGER PRIMARY KEY,
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  server_uid INTEGER NOT NULL,
  uidvalidity INTEGER NOT NULL,
  seen INTEGER NOT NULL DEFAULT 0,
  flagged INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,   -- tombstone of the copy
  keywords_json TEXT,                   -- IMAP keywords, per mailbox
  internal_date INTEGER,
  UNIQUE (folder_id, server_uid));
CREATE INDEX message_locations_message ON message_locations (message_id);
```

Per-mailbox facts move to the location: `\Seen`, `\Flagged`, keywords,
UID, UIDVALIDITY, INTERNALDATE, the tombstone, the cached body copy.
Headers, body, security and the conversation stay on the message.
Local labels get their own table keyed by `(message_id, label_id)`,
since they are the user's and not a mailbox's; server labels remain
keywords on the location.

`thread_folders` becomes an aggregate over `(thread_id, folder_id)`
joined through locations. The FTS index stays external-content over
`messages` with one row per identity.

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
SELECT m.id, MAX(m.date), m.subject, ...
FROM messages m
JOIN message_locations l ON l.message_id = m.id
WHERE l.folder_id IN (?, ?, ...)      -- the scope's folders
  AND NOT l.deleted
GROUP BY m.id
ORDER BY m.date DESC, m.id DESC
LIMIT ?;
```

A label's scope adds `AND l.keywords_json LIKE ...` and the same
`GROUP BY`, which is the duplicate suppression the issue names. A
conversation read joins locations in scope to messages by `thread_id`,
deduped by `message_id`; the reading pane would show a message once and
name the folders it is in (today it lists the copies the scope keeps
out). Search joins FTS to `messages`, then to locations in the scope,
one hit per message.

Paging keeps `mail.Cursor` (date, row); the row becomes a message or a
conversation, and the tie-breaker a stable `(m.id)` rather than
`(folder_id, server_uid)`. Watermarks (`TakeNew`) stay per folder and
read locations. Attachments key on `message_id` and stop repeating per
copy. Rules and their log name `(account, folder, UIDVALIDITY, UID)`
today; they would name a location ID, and the per-folder trigger
conditions still read the location.

## Write paths

A sync writes locations for the folder it read and, for each new copy,
upserts the message by `(account_id, msg_key)`, then threads it as
today (reads before writes, under the store's threading mutex). A first
sync of a folder creates locations only.

A server-side move is one message with two location rows:

```sql
BEGIN;
INSERT INTO message_locations
  (message_id, folder_id, server_uid, uidvalidity, seen, flagged,
   keywords_json, internal_date)
SELECT message_id, ?, ?, ?, seen, flagged, keywords_json, internal_date
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

Keywords and flags write the location; local labels write the local
label table. `SetFlags` addresses `MessageRef{folder, uid}` today and
would keep doing so — a location is exactly that pair.

## Migration

1. Create `message_locations` and the local label table; add
   `account_id`, `msg_key` and a nullable `thread_id` to `messages`.
2. Backfill in chunks outside the migration transaction:
   - group the existing rows by `(account_id, msg_key)`;
   - pick a keeper (the one with a body, else the newest `cached_at`)
     and write one location per row, copying UID, UIDVALIDITY, flags,
     keywords and the tombstone;
   - re-key `attachments.message_id` and `contact_messages` through a
     temporary map from old message IDs to the keeper.
3. Rewrite the triggers that keep `threads`, `thread_folders` and
   `messages_fts` in step, then rebuild the FTS index.
4. Rebuild `messages` with SQLite's table-rebuild dance to drop
   `folder_id`, `server_uid`, `seen`, `flagged`, `keywords` and the
   unique constraint; recreate its indexes.
5. `store/migrations_test.go` gains a seed at the new version with one
   message in two folders; `checkUpgradedData` proves it became one
   message and two locations. `mail.CacheVersion` is bumped, so parsed
   bodies and attachment rows are dropped and fetched again.

**Size.** The backfill reads the message rows twice (group, then
write) and writes about as many location rows as there are messages.
The `messages` rebuild rewrites the whole table and its FTS index, so
the migration's write volume is roughly three times the cache's
current size: a cache of 20,000 headers and a few hundred cached bodies
is a one-off write in the tens of megabytes. That is acceptable for a
desktop app, but it is not a small migration, and it is not one that
runs in a single short transaction.

`docs/schema-migrations.md` requires every past version to upgrade, so
the seed matrix grows by the new version and by the two-folder case.

## Rollback

Migrations are forward-only: an older build refuses a database whose
`user_version` is newer. Two options for a release that may be rolled
back:

- Dual-write for one release, keeping `messages.folder_id` and
  `server_uid` filled so an older build still reads; drop the columns in
  the next release. This roughly doubles the write paths meanwhile.
- Or accept no rollback: copy the database file aside before the
  migration (`shu.db.bak-v<n>`) and remove the copy once the new version
  has run cleanly in a release.

The second is simpler and fits the pre-alpha stance.

## Cost against this tree

- `store/` has 51 Go files, 27 of them tests. 37 files mention
  `messages`, and 26 touch `folder_id` or `server_uid`; every query that
  joins, pages, searches, aggregates threads, contacts, labels, rules,
  watermarks and the queue is re-read.
- `mail/` has six engine files (`block.go`, `queue.go`, `reminders.go`,
  `service.go`, `store.go`, `undo.go`) and ten test files that use
  `MoveMessages`, `MoveBack`, `MarkDeleted`, `SetFlags` or the queue.
  Their public shape (`MessageRef`, `MoveResult`) survives, but a
  result's meaning (a moved copy) becomes a location.
- The UI is unaffected: views reach the engine through `Service` with
  `mail.MessageRef`, `mail.Scope` and `mail.Summary`. `Summary` would
  stay one per message identity, with a folder list added.
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
