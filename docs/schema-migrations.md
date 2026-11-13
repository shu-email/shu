# Schema migrations

The cache in `store/` is a long-lived SQLite database. It holds mail,
but also drafts, the outbox, contacts, labels and rules that the user made:
an upgrade that loses them loses work, so every change to the schema follows
this checklist.

The migrations are the schema. `migrations` in `store/store.go` lists every
version from the first install to today, and `Open` applies the missing ones
in order, each in its own transaction. `SchemaVersion` is the length of the
list, and an older build refuses a database newer than it rather than
writing into a schema it does not know. There is no separate schema file to
keep in step.

## Checklist for a schema change

1. **Bump the version.** Append the statements of the new version to the
   end of `migrations`; never edit an entry that shipped, because databases
   in the wild already ran it. One migration per change, so a failure names
   the change. `SchemaVersion` follows the length by itself.
2. **Keep old rows meaningful.** A new column needs a default, or a value
   the data can live without until the next sync fills it. A new table is
   empty, and its feature must work on an empty one.
3. **Backfill when existing rows need data.** Add a function to
   `backfills`, keyed by the version it migrates to. It reads the rows
   outside the transaction (in chunks) and returns statements that run
   inside it, so a big cache does not hold a write lock while it reads.
4. **Rebuild what the schema derives.** The full-text index is rebuilt in
   the migration that changes its columns (`INSERT INTO messages_fts
   (messages_fts) VALUES ('rebuild')`), never left for a later sync.
5. **Test the upgrade, not only the feature.** `store/migrations_test.go`
   builds a database at every past version and upgrades it. If the change
   adds data that must be computed, add the matching rows to
   `seedAtVersion` and the invariant to `checkUpgradedData`.
6. **No network, display or clock.** A migration runs offline, on a machine
   whose clock may be wrong, and may be interrupted: it must not wait for a
   server, and its result must not depend on today's date. `s.now` is the
   store's clock; use it where a time is needed.
7. **Fail clean.** Every version runs in one transaction: an error rolls
   back the schema and the backfill together, and `Open` returns it without
   opening the store. Do not write files, keychain entries or anything else
   outside the database in a migration.
8. **Run the suite.** `gofmt`, `go tool mygo vet .` and
   `CGO_ENABLED=0 go test ./...`; the migration tests skip without the
   SQLite module, so run them once with it available (`SHU_REQUIRE_SQLITE=1`
   in CI makes the skip a failure).

## Reviewing a migration

- The version list only grows, and the new entry is at the end.
- A database created at any earlier version reaches the current schema
  (the baseline test covers every version, including the empty one).
- Opening a current database twice is a no-op.
- Drafts, the outbox, identities, contacts, labels and rules survive the
  upgrade; nothing of them is derived from a server.
- Nothing secret (a password, a token, a key) enters the database.
- The migration adds no dependency, no network access and no native module.
