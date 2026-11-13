# Implementing Shu's core email features

## Context

Shu currently has only a UI skeleton: `main.go` (431 lines, 3 Go files) draws the
three-pane interface, all data comes from `demoMessages()`, every button is inert,
and there is no account, network, persistence or message-parsing code. This round
turns it into a client that can actually send and receive mail: multiple accounts,
IMAP fetching and reading, the common actions (star/archive/junk/delete), compose
and send, reply/forward, and plain-text/HTML dual-mode display of HTML bodies.

Persistence uses **MyGo's built-in SQLite plugin**
(`github.com/egoist/mygo/plugins/sqlite`, shipped with v0.9.0 at the pinned
version, a Zig native module, precompiled, no cgo) as the single-file primary
store. Passwords still live only in the OS keychain. The UI can therefore read the
cache on startup and incrementally sync in the background, and the plugin's
bundled FTS5 (with the trigram tokenizer) gives us substring search over CJK mail
without a second storage layer.

The repository's latest `AGENTS.md` additionally requires: the mail engine must not
depend on `mygo/ui`; user-visible strings must go through the i18n catalog; tests
may only use local fake IMAP/SMTP servers; dev-build data directories are isolated
from real installs (`mygo dev` already renames the app to `shu Dev` and appends
`.dev` to the identifier, so `PathUserData` is isolated for free).

## Confirmed scope

In this round:

- Multiple accounts (IMAP/SMTP, manual server configuration + common provider presets;
  signing in with Google and Microsoft came later, see below and `docs/oauth.md`)
- Folder list (special-use first, name heuristics as fallback), unread counts
- Message list (cached headers + incremental sync of new mail), local FTS search,
  manual sync
- Reading bodies (MIME parsing, plain text; HTML rendered on demand in an untrusted webview)
- Mark read on open; toggle star; archive/junk/delete (IMAP move)
- Compose, send (SMTP), APPEND to Sent after sending, with a crash-recoverable outbox
- Reply, reply-all, forward (plain-text quoting)
- Attachment list and "Save as"
- en / zh-Hans, following the system locale with a manual override

Explicitly out of scope: IDLE/background polling, conversation threading (the
schema leaves room for a `threads` migration, but v1 ships none and the UI has no
threading), draft autosave, rich-text compose, PGP/S-MIME, inline image rendering,
storing raw `.eml` blobs.

OAuth was out of scope in this round too, and has been added since: a Gmail or
Outlook account can be added by signing in at Google or Microsoft in the system
browser, with no host, port or app password to type. `docs/oauth.md` describes
the flow, the tokens and what the maintainers do to turn it on. The manual
configuration below, IMAP and SMTP servers with a password, is unchanged and is
the fallback: for every other provider, for a build without the providers'
client IDs, and for an account whose organization does not allow Shu.

## Architecture and package boundaries

Following the new `AGENTS.md`: protocol/storage/sync do not import
`github.com/egoist/mygo/ui`; the UI calls the engine through plain service values,
and the engine returns data only.

```
shu/
  main.go              app struct, window assembly, root view
  sidebar.go           account switcher + folders
  messagelist.go       list header, search, virtual list and rows
  reading.go           reading pane, header, attachments, actions
  accounts.go          account settings/first-run views
  compose.go           compose window (mygo.NewWindow)
  htmlview.go          webview lifecycle + HTML sanitizing/CSP
  icons.go             icons (add the Lucide v1.72.0 version comment)
  locales/             en.json, zh-Hans.json, catalog_gen.go (generated)
  mail/                pure protocol/models/service, no ui or sqlite dependency
    account.go         Account/Server/TLSMode, provider presets, validation
    message.go         Summary/Message/Folder/Outgoing and other models
    store.go           Store interface (the cache, outbox and account boundary)
    service.go         Service: account CRUD, cache-first reads, sync, send
    imap.go            LIST/STATUS, SELECT, FETCH, STORE, MOVE/COPY, APPEND
    smtp.go            go-mail send + the raw message back
    mime.go            go-message parsing, bodies/attachments, HTML→text
    specialuse.go      special-use mapping and name fallback
    *_test.go          fake IMAP/SMTP, MIME, service tests
  store/               SQLite implementation, imports mail's models, no ui dependency
    store.go           Open, pragmas, migration array, transaction helpers
    accounts.go        accounts and kv
    folders.go         folders rows and sync cursors
    messages.go        message headers/bodies, attachments, tombstones
    search.go          FTS5 query builders and the LIKE fallback
    outbox.go          send queue states and crash recovery
    *_test.go          temp-db/in-memory-db tests
  testdata/            fake message fixtures shared by mail and store tests
```

package main keeps the single `app` struct, splits files by concern, and adds no
extra abstraction layers. Update the Architecture section of `AGENTS.md` in the
same change to record the new `mail/`, `store/` and `locales/` layout.

## Persistence: SQLite

### Decision

MyGo's `plugins/sqlite` (no third-party driver, no new entry in `go.mod`), called
directly as `sqlite.Open(ctx, path, sqlite.OpenOptions{})` — no `mygo.Use` needed,
no `accounts.json`. It is a framework native module, so the no-cgo build stays
as-is; foreign keys are on by default and it exposes a busy timeout; and the
bundled SQLite includes FTS5 with the trigram tokenizer, which is what makes CJK
search work.

- Database: `mygo.App.Path(mygo.PathUserData)/shu.db` (`shu Dev/shu.db` in dev).
- One connection, opened at startup and `Close`d on shutdown. The handle is safe
  from any goroutine and serializes internally, but by discipline **only the
  engine writes** (see write discipline below); the UI reads through the service.
- `PRAGMA journal_mode=WAL` (ignore failure for in-memory DBs),
  `PRAGMA busy_timeout=5000`, foreign keys on, sequential migrations keyed on
  `PRAGMA user_version` and run inside a transaction.
- Passwords never enter the database: `mygo.Secrets` with key
  `account:<id>:password`; `mail.Service` takes a `SecretStore` interface
  (`mygo.Secrets` in production, an in-memory fake in tests). The same holds
  for the refresh token of an account that signs in with OAuth
  (`account:<id>:oauth`); its access token stays in memory (`docs/oauth.md`).
- The server is the source of truth; the local database is a cache and an offline
  copy. Server semantics such as `UIDVALIDITY` are stored and compared, never
  interpreted or used for reasoning about message identity.

### What lives where

| Data | Where | Why |
| --- | --- | --- |
| Accounts, servers, settings (`kv`) | DB | Small, transactional, needed before any network call |
| Folders + sync cursor (`uidvalidity`, `last_uid`) | DB | Cursor must move in the same transaction as the data it describes |
| Message headers, flags, preview, bodies (text + HTML) | DB | Transactional consistency and trigger-maintained FTS index |
| Attachment metadata | DB | Listing and rendering need it while offline |
| Attachment bytes | Disk cache, path in `attachments.cache_path` | Keeps the database small; deduped by content hash, LRU-cleaned |
| Outbox entries and their state | DB | Sending must survive a crash and never double-send |
| Passwords, OAuth refresh tokens, API keys | Keychain only | Established rule, never in the DB or the repo; OAuth access tokens live in memory only |

Bodies are stored inline in the `messages` row: consistency
in one transaction, and the FTS triggers keep the index in step. The cost is a
larger database, kept in check by three guardrails: the list phase fetches headers
only, bodies are fetched on demand with a size cap and a `body_truncated` flag, and
each account has a sync cap (`defaultSyncCap = 2000` headers per account for this
round; the visible window is 50 and paging comes later). Raw `.eml` is not stored;
an export/blob area can be added later without touching v1.

### Schema v1

```sql
CREATE TABLE accounts (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, username TEXT NOT NULL,
  imap_host TEXT NOT NULL, imap_port INTEGER NOT NULL, imap_tls TEXT NOT NULL,
  smtp_host TEXT NOT NULL, smtp_port INTEGER NOT NULL, smtp_tls TEXT NOT NULL);

-- Misc app settings; active_account lives here.
CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);

-- The IMAP folder row carries the sync cursor, so a sync either advances both
-- the cursor and the data or neither.
CREATE TABLE folders (
  id INTEGER PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name TEXT NOT NULL, delimiter TEXT, special_use TEXT,
  unread INTEGER NOT NULL DEFAULT 0, total INTEGER NOT NULL DEFAULT 0,
  uidvalidity INTEGER NOT NULL DEFAULT 0, last_uid INTEGER NOT NULL DEFAULT 0,
  synced_at INTEGER,
  UNIQUE (account_id, name));

CREATE TABLE messages (
  id INTEGER PRIMARY KEY,
  folder_id INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
  server_uid INTEGER NOT NULL,
  message_id TEXT, in_reply_to TEXT, references_json TEXT,
  from_name TEXT, from_email TEXT,
  to_json TEXT, cc_json TEXT, reply_to_json TEXT,
  subject TEXT, preview TEXT, date INTEGER, size INTEGER,
  seen INTEGER NOT NULL DEFAULT 0, flagged INTEGER NOT NULL DEFAULT 0,
  has_attachments INTEGER NOT NULL DEFAULT 0,
  body_text TEXT, body_html TEXT, body_truncated INTEGER NOT NULL DEFAULT 0,
  raw_headers TEXT, cached_at INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0, -- tombstone until the server confirms
  UNIQUE (folder_id, server_uid));

CREATE INDEX messages_list ON messages (folder_id, date DESC);
CREATE INDEX messages_message_id ON messages (message_id) WHERE message_id IS NOT NULL;

CREATE TABLE attachments (
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  part TEXT NOT NULL,
  filename TEXT, content_type TEXT, size INTEGER,
  content_hash TEXT, cache_path TEXT,
  PRIMARY KEY (message_id, part));

-- At-most-once sending: a row is marked 'sending' before SMTP is dialed and
-- 'sent' afterwards; a 'sending' row found at startup is reported as unknown
-- rather than resent.
CREATE TABLE outbox (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  raw TEXT NOT NULL, envelope_json TEXT NOT NULL,
  state TEXT NOT NULL,             -- pending | sending | sent | failed
  attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT,
  created_at INTEGER NOT NULL, sent_at INTEGER);
```

`threads`/`thread_refs` are deliberately absent: v1 has no threading, and the
migration array is where they will arrive (with `messages.message_id` already
indexed to make that cheap). AI artifacts (`model`, body hash, summary) get their
own table later for the same reason.

### Search: FTS5 with trigram

```sql
CREATE VIRTUAL TABLE messages_fts USING fts5(
  subject, from_name, from_email, body_text,
  content='messages', content_rowid='id', tokenize='trigram');

CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, subject, from_name, from_email, body_text)
  VALUES (new.id, new.subject, new.from_name, new.from_email, new.body_text);
END;
-- messages_ad (delete) and messages_au (update: delete + insert) mirror it.
```

- The tokenizer must be `trigram`: the default `unicode61` cannot segment Chinese,
  while trigram indexes sliding three-character windows (and makes substring
  search fast for Latin text too). That is the whole reason to prefer it here.
- A trigram query needs at least three characters. Queries shorter than that fall
  back to `LIKE` on `subject`/`from_name`, which is cheap at this size.
- Search is scoped to the current account (and folder when one is selected), capped
  at 100 results, and served entirely from the cache — no network.
- List paging never goes through FTS: it uses `messages_list`
  (`folder_id, date DESC`).
- A store test asserts a two-character and a three-character Chinese query both
  find a known message, so a downgraded SQLite build fails loudly instead of
  silently returning nothing.

### Write discipline

- **The engine is the only writer.** One connection, WAL, all writes from the
  engine's background jobs; views read through the service and never open a
  connection of their own.
- **Changeset transactions.** A sync pulls a batch of server changes, writes the
  whole batch in one transaction, and only in that same transaction advances the
  folder cursor (`last_uid`, `uidvalidity`, `unread`, `synced_at`). A crash can
  therefore leave the old state or the new state, never a cursor that claims data
  that was not written.
- **Tombstones for deletes.** Local actions that remove mail first set
  `deleted = 1` and hide the row, then confirm with the server (MOVE/STORE), and
  only then delete the row. A failed confirmation restores the row and surfaces
  the error.
- **Optimistic UI, authoritative server.** Star/move update the database and the
  UI first, then call IMAP; on failure the change is rolled back from the server
  response and one sync reconciles.
- **Outbox for sends.** `Send` inserts the row and the composed raw message in one
  transaction, flips it to `sending`, dials SMTP, then records `sent` and appends
  the copy. Startup reports any leftover `sending` row as "delivery unknown" with
  a manual retry, so the client never sends twice automatically.
- **Migrations are an explicit array.** `var migrations = [][]string{...}` stepped
  by `user_version`, forward only, one entry per version, each run in a
  transaction; no ORM and no auto-migrate. A migration test opens an empty
  database, asserts the target `user_version` and the presence of every table and
  index, then reopens it to prove idempotence.

### Attachment files on disk

- Bytes live under `mygo.App.Path(mygo.PathCache)/attachments/<content-hash>`
  (dev: `shu Dev`), written once per hash, so identical attachments dedupe.
- `attachments` rows carry `content_hash` and `cache_path`; a row with a null
  `cache_path` means "metadata only, fetch the bytes when needed".
- A size budget (default 200 MB) with LRU cleanup by file mtime runs after a sync;
  removing a file nulls `cache_path`, the database stays consistent.
- File names on disk never come from the message; "Save as" is the only place a
  human-chosen name is used, and it is sanitized.

### Cache first + incremental sync

- Startup/folder switch: read `folders` and the newest 50 rows of the current
  folder from SQLite, show them immediately; then `SyncAccount` + `SyncFolder` in
  the background.
- `SyncAccount`: `LIST` (`ReturnSpecialUse`, `ReturnStatus`) refreshes folders,
  unread counts and the delimiter; one transaction upserts `folders`.
- `SyncFolder`: after `SELECT`, compare `UIDVALIDITY`:
  - changed → drop that folder's cached rows (the server reset the folder) and
    refetch up to the sync cap;
  - unchanged or absent → `UID FETCH (last_uid+1):*` for new mail, plus
    `FETCH ... (UID FLAGS)` on cached UIDs to refresh seen/flagged. The batch and
    the new `last_uid` land in one transaction.
- Opening a message: read the cached body first; otherwise `FetchMessage` the whole
  message, parse it, write it, and mark `\Seen` on IMAP (a failure does not block
  display; it is returned as `FetchResult.SeenErr`).
- Offline: cached display + an error banner (Retry); browsing cached mail is never
  blocked.

### Why not the alternatives

- **A KV store (bbolt/Badger)**: threading, filtered listing and full-text search
  would all be hand-built; mail is a query-heavy workload.
- **Raw `.eml` files as primary storage**: at 100k messages, index rebuilds and
  consistency are a nightmare; it is a good export format, not a primary store.
- **An ORM (GORM and friends)**: a handful of tables, so hand-written SQL plus an
  explicit migration array is smaller, faster and easier to review.
- **FTS `unicode61` or plain `LIKE` everywhere**: the first cannot search Chinese,
  the second cannot scale past the cached window; trigram FTS plus a short-query
  `LIKE` fallback covers both.

### Tests

The SQLite native module is prepared into the MyGo module's `.mygo/native` by
`go tool mygo generate` (which calls `generateNative` internally); if
`sqlite.Load()` reports "not built", `store` tests `t.Skip` with a hint to run
that command, matching MyGo's own plugin tests. `mail` and UI tests do not depend
on SQLite.

## Dependencies (approved)

- `github.com/emersion/go-imap/v2 v2.0.0-beta.8` (client + `imapserver` for tests)
- `github.com/emersion/go-message v0.18.2` (MIME read/write, charset)
- `github.com/wneessen/go-mail v0.8.2` (SMTP, UTF-8, attachments, context)
- `github.com/egoist/mygo/plugins/sqlite` (ships with MyGo v0.9.0, no new external dependency)
- Test-only: `github.com/emersion/go-smtp` (local fake SMTP server)
- `golang.org/x/net/html` (HTML→plain text; already an indirect dependency, now direct)

MyGo stays pinned at v0.9.0: no version upgrade in this round, and no incidental
changes to existing MyGo API usage (per `AGENTS.md`, upgrades happen in their own
commit).

## Mail engine `mail/`

Connection strategy: one short connection (TLS + LOGIN) per network operation,
closed when done; `SyncFolder` does LIST/SELECT/FETCH within one connection,
`FetchMessage` does FETCH + STORE within one connection. Pre-alpha favors simple
and reliable; connection reuse/IDLE come later. Every method takes a
`context.Context`; the UI passes a 30s timeout; cancellation closes the connection.

`Service` exposes (UI tests substitute a same-named interface `mailService`):

```go
// Accounts (writes SQLite, writes the password to the keychain)
Accounts(ctx) ([]Account, activeID string, error)
AddAccount(ctx, a Account, password string) error
UpdateAccount(ctx, a Account, password string) error   // password "" keeps the existing one
RemoveAccount(ctx, id string) error
SetActive(ctx, id string) error
TestConnection(ctx, a Account, password string) error  // IMAP login + SMTP dial/auth, sends nothing

// Cache reads (fast, work offline)
CachedFolders(ctx, accountID string) ([]Folder, error)
CachedMessages(ctx, accountID, folder, query string, limit int) ([]Summary, error)
CachedMessage(ctx, accountID, folder string, uid UID) (FetchResult, bool, error)

// Network sync (writes the cache)
SyncAccount(ctx, accountID string) ([]Folder, error)
SyncFolder(ctx, accountID, folder string, limit int) error
FetchMessage(ctx, accountID, folder string, uid UID, markSeen bool) (FetchResult, error)

// Actions and sending
SetFlag(ctx, accountID, folder string, uid UID, flag Flag, on bool) error
Move(ctx, accountID, folder string, uid UID, dest string) error
Send(ctx, accountID string, out Outgoing) (SendResult, error) // outbox → SMTP → APPEND to Sent
PendingSends(ctx, accountID string) ([]PendingSend, error)     // crash recovery surface

// Sending with a delay, added since (see Compose and send)
Schedule(ctx, accountID string, out Outgoing, delay time.Duration) (PendingSend, error) // outbox, sends nothing
SendScheduled(ctx, id string) (SendResult, error) // waits until due, then as Send; ErrUnsent if taken back
Unsend(ctx, id string) (Draft, error)             // takes it back and returns its draft; ErrAlreadyLeft too late
DeliverScheduled(ctx) ([]ScheduledResult, error)  // at startup: what an earlier run left waiting

// How messages are written, added since: settings, signatures, identities
ComposeSettings(ctx) (ComposeSettings, error); SaveComposeSettings(ctx, ComposeSettings) error
Signature(ctx, accountID) (Signature, error); SaveSignature(ctx, accountID, Signature) error
Identities(ctx, accountID) ([]Identity, error) // the account's own address first, then aliases
AddIdentity / UpdateIdentity / DeleteIdentity  // ErrIdentityAddress, ErrIdentityExists
AttachFromMessage(ctx, d Draft, accountID, folder string, uid UID) (Draft, []AttachFailure, error) // a forward's files

// Signing in with Google and Microsoft, added since (docs/oauth.md)
OAuthConfigured(provider string) bool                          // false: the build has no client, no button
AddOAuthAccount(ctx, provider, name string, prompt OAuthPrompt) (Account, error)
SignInAgain(ctx, accountID string, prompt OAuthPrompt) error    // as the account's own address only
SignOut(ctx, accountID string) error                            // removes the tokens, keeps the account
NeedsSignIn(ctx, accountID string) (bool, error)                // reads the keychain, not the network

// The address book, added since: on this device alone (see Address book below)
Suggest(ctx, typed string, exclude []string, limit int) ([]Contact, error) // best first, without exclude; limit <= 0 is 8
Contacts(ctx, search string, offset, limit int) (ContactPage, error)      // a page of the book, best first, and More
AddContact(ctx, name, email string) (Contact, error)        // ErrContactAddress, ErrContactExists, ErrContactOwn
RenameContact(ctx, email, name string) (Contact, error)     // "" hands the naming back to the mail
FavoriteContact(ctx, email string, on bool) (Contact, error)
DeleteContact(ctx, email string) error                      // writing to it again brings it back
CheckRecipients(ctx, field string) (RecipientCheck, error)  // what of a field is no address, and whom a name may mean

// Labels, added since: IMAP keywords where a folder keeps them, else on this device (see AGENTS.md)
Labels(ctx, accountID string) ([]Label, error)                            // in order, with Unread and Local
CreateLabel(ctx, accountID, name string, color LabelColor) (Label, error) // ErrLabelName, ErrLabelColor, ErrLabelExists
UpdateLabel(ctx, accountID string, id LabelID, name string, color LabelColor) (Label, error) // the keyword stays
SetLabel(ctx, accountID string, msgs []MessageRef, id LabelID, on bool) error // cache first, one STORE per folder
DeleteLabel(ctx, accountID string, id LabelID) error // off every message on the server, then gone; ErrLabelPartlyRemoved

// Rules, added since: they sort the mail that arrives (see AGENTS.md)
Rules(ctx) ([]Rule, error)                                            // in order, the first tried first
CheckRule(ctx, r Rule) error                                          // *RuleError: ErrRuleName, ErrRuleEmpty, ErrRuleCondition, ErrRuleAction, ErrRuleAccount, ErrRuleFolder, ErrRuleLabel
SaveRule(ctx, r Rule) (Rule, error)                                   // ID 0 adds it after the others; touches no mail
PauseRule(ctx, id RuleID, paused bool) (Rule, error)                  // disable; drops what it had pending and had not begun
DeleteRule(ctx, id RuleID) error                                      // its log stays, to be undone
MoveRule(ctx, id RuleID, index int) error
PreviewRule(ctx, r Rule, offset, limit int) (RulePreview, error)      // the cached matches now, newest first; changes nothing
RunRule(ctx, id RuleID, targets []RuleTarget) ([]RuleActivity, error) // once, over a preview's targets; ErrRulePaused, ErrRuleRunTooLarge
RuleActivity(ctx, q ActivityQuery) (ActivityPage, error)              // the log, newest first
UndoActivity(ctx, id ActivityID) error                                // ErrRuleMessageGone when the message is not where the rule left it
UndoRule(ctx, id RuleID) (int, error)                                 // every entry of a rule's
OnRuleActivity(report func(accountID string))                         // actions applied in the background
```

IMAP details:

- `imapclient.Options{WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}, TLSConfig: s.tlsConfig}`;
  `DialTLS` / `DialStartTLS`, `WaitGreeting`, `Login`. Only implicit TLS and
  STARTTLS are supported; "no TLS" is not exposed to users, and tests use a
  self-signed certificate with trust injected via `Service.TLSConfig`.
- special-use mapping `\Inbox \Sent \Drafts \Trash \Junk \Archive \Flagged`, with
  fallback matching Inbox/Sent/Sent Items/Drafts/Trash/Deleted Items/Junk/Spam/Archive/All Mail;
  skip `\Noselect`.
- Listing: `Fetch` requests `Envelope`, `Flags`, `InternalDate`, `RFC822Size`,
  `BodyStructure`, plus a partial `BODY.PEEK[TEXT]<0.256>` as the preview (empty
  preview when unsupported); results are written to the cache newest first.
- Starred: a virtual folder, `UIDSearch(FLAGGED)` then fetch by UID.
- Body: `FetchItemBodySection{Peek: true}` for the whole message, iterated with
  go-message's `mail.CreateReader`; read `Message-ID`, `References`, `Reply-To`,
  `To`, `Cc`, `Date`; bodies are stored up to 1 MB per part with
  `body_truncated = 1` past that; attachments collect filename, type, size and
  bytes (cap 25 MB per message).
- Move: prefer `MOVE`; otherwise `COPY` + `STORE \Deleted` + `UIDExpunge` (with
  UIDPLUS) or `EXPUNGE`.
- Sent: `APPEND` (`\Seen`) to the server's Sent folder; with no Sent folder return
  `SendResult{Copied: false}` and the UI says "Sent, but the copy could not be
  saved".

SMTP: go-mail `NewMsg` + `FromFormat/To/Cc/Subject/SetBodyString(text/plain; charset=utf-8)`
+ `SetDate/SetMessageID` + `In-Reply-To/References` (From, and the envelope sender,
are the identity's, added since; Bcc goes in the envelope only);
`NewClient(host, WithPort, WithUsername, WithPassword, WithSMTPAuth, WithTLSPolicy(mandatory)/WithSSL, WithTLSConfig, WithTimeout)`,
then `DialAndSendWithContext`. The raw message from `msg.WriteTo(&buf)` is stored
in the outbox and reused for APPEND.

MIME: prefer `text/plain`; when only HTML exists, extract text with
`x/net/html` (skip script/style/head, break on block elements, collapse blank
lines). Keep the original HTML for the webview.

### Settings window (added since)

- The settings open in a window of their own (`settingsWindowOptions`), not by
  replacing the mail view, so mail stays readable behind them. One window serves
  every opening: a second `Settings` call, the Gear, `Cmd+,` or an AI shortcut
  brings the open one forward and selects the tab.
- The window draws a centered title and an icon tab bar (General, Network, AI),
  with the chosen tab filled with the accent, as a system settings window does.
  The body scrolls below the bar; the General tab keeps language, mail,
  compose, import/export and account management. `closeSettings` closes the
  window; closing it by its title bar clears the same state.
- Tests run without a window, where `newWindow` is nil: the settings then show
  in the main view (with a Done button) so existing view tests can read them;
  `settings_test.go` covers the window options, reuse and close paths.

### Keyboard reference and accessibility (added since)

- `shortcuts.go` holds the reference as data (groups and rows), opened with
  Cmd+/ (Ctrl+/ elsewhere) or the account menu, in a modal with the keys drawn
  as the system writes them. It lists the implemented shortcuts only; a test
  checks the rows the window takes and both languages.
- Plain keys are registered in the panes' focus subtree, so typing in a field
  never takes them (`TestShortcutsDoNotStealTyping`); Cmd shortcuts are scoped
  the same way. The modal is opened from the root context, where no text field
  can be focused.
- Icon-only buttons keep a `Label` and a `Tooltip`; an automation audit of the
  main screens finds no unnamed interactive element.
- Motion uses `ui.ElementTransition` and MyGo's reduced-motion behavior; text
  sizes come from the theme (`t.FontSize`), and colors from the theme, so the
  platform's larger text and contrast settings carry through. The layout is
  checked at 1000×640 in light and dark.

### Import, export and the settings backup (added since)

- `mail/transfer.go` streams an mbox with `readMbox` (the `From ` separators
  dropped, `>From ` unescaped) and appends each message with APPEND to the
  folder the user chose, counting refusals and reporting progress;
  `ImportEML` appends one message. Import talks to that one account and no
  other server.
- `Service.Export` resolves chosen refs, a folder or every folder (Trash and
  Junk left out), reads each message raw with `BODY.PEEK[]` on one read-only
  SELECT per folder, and writes mbox (with escaped `From ` lines) or one eml.
  Without attachments it rewrites the message as its headers and plain text.
- `BackupConfig` writes accounts, identities and compose settings as JSON with
  no password, token or key; `RestoreConfig` adds what a machine lacks and
  leaves the accounts it has, and restored accounts need their sign-in once.
- The General settings tab runs each transfer in a job with progress and a
  Cancel action; the file dialogs open in the job, and the result names what
  was imported, exported or skipped. All strings are in en and zh-Hans.

### Proxies and certificate authorities (added since)

- `mail/network.go` reads the network settings from the store before each
  connection, so a change applies to the next dial without a restart. `Mode`
  follows the environment's proxy variables, connects directly, or uses an
  explicit `http`, `https`, `socks5` or `socks5h` proxy with a bypass list whose
  rules are `NO_PROXY`'s. The proxy password is in the keychain
  (`ProxyPasswordKey`); credentials typed into the proxy address are moved there
  and the stored address is kept without them.
- Both IMAP (implicit TLS and STARTTLS) and SMTP (go-mail's
  `WithDialContextFunc`) dial through `Service.dialNetwork`, so account tests,
  syncs, sending and attachment fetches take the same path. An HTTP proxy is
  tunnelled with CONNECT and `Proxy-Authorization`; a SOCKS5 proxy uses
  `golang.org/x/net/proxy`.
- A PEM certificate authority imported in the settings is appended to the
  system roots for every TLS config (`tlsConfigFor`), including go-mail's
  STARTTLS. Certificate checks stay on. `Service.HTTPClient` gives the AI layer a
  client with the same proxy, roots and bypass list, and the window rebuilds it
  after a save.
- The Network tab of the settings edits the mode, address, user, password,
  bypass list and authority, shows whether a password is stored, tests the same
  path real mail takes (`Service.TestNetwork`), and saves to the store and the
  keychain. No setting or password is written to logs.

## UI changes (package main)

### State and async model

`app` gains: `svc mailService`, `win *mygo.Window`, `run func(func())`,
`update func(func())`, `accounts`, `active`, `folders`, `folder`,
`all []mail.Summary`, `bodies map[UID]FetchResult`, `loading/err`, `gen uint64`,
`syncAt time.Time`, `showAccounts`, `compose []*composeState`, `htmlOn`,
`webview *mygo.WebView`, `closed bool`.

- Production: `run = func(f){ go f() }`, `update = win.Update`, `win.OnClosed` sets
  `closed` and cancels pending work.
- Tests: `run`/`update` execute synchronously and fake services return immediately,
  so there are no races and no sleeping.
- Every account/folder/selection change increments `gen`; background results check
  `gen` and `closed` on the main thread and drop stale ones.
- The first frame shows the cache (`CachedFolders`/`CachedMessages`), then syncs in
  the background and refreshes; a failed sync only adds an error banner and never
  clears the cache.
- Background jobs must not capture `*ui.Context` or `ui.Element`; capture
  `c.Services()` up front when URLs or the clipboard are needed.

### i18n (lands with the feature, not after)

- Add `"i18n": {"dir": "locales", "source": "en"}` to `mygo.json`.
- Create `locales/en.json` and `locales/zh-Hans.json` (ARB-style ICU
  MessageFormat, with `@@locale` and `@key` metadata) covering every existing UI
  string and the new strings kept in the UI, using plurals (unreadCount,
  messageCount).
- Run `go tool mygo generate` to produce `locales/catalog_gen.go`; all views call
  the generated functions (for example `locales.Compose()`) instead of hardcoding.
- The settings view offers a language choice: Follow system / English / 简体中文,
  calling `mygo.App.SetLocale("")`/`"en"`/`"zh-Hans"` (MyGo persists the choice).
- Tests set `i18n.SetLocaleFunc(func() string { return "en" })` in `TestMain` so
  machine language cannot affect assertions; add a test that both catalogs have
  the same key set. Unit-test Chinese rendering through
  `locales.In("zh-Hans").X()`.

### Multiple accounts and account settings

- The sidebar account header becomes a clickable anchor; `ui.Menu` lists all
  accounts (current one checked), `Add account…`, `Manage accounts…`; selecting an
  account persists `active`, loads that account's cache first, then syncs.
- `accounts.go` is a full-window view: shown automatically on first run (no
  accounts), reachable from the menu for management. Fields: display name, email,
  password, IMAP host/port/TLS, SMTP host/port/TLS, provider preset
  (Gmail/iCloud/Outlook/Fastmail/Yahoo/Custom, auto-filling servers); "Test and
  save" runs `TestConnection` in the background with progress/inline errors; an
  empty password while editing means unchanged; deletion requires
  `mygo.Dialog.Message` confirmation (an explicitly destructive action) and clears
  the SQLite rows (cascaded) and the keychain entry.
- Password note: on this manual path, Gmail with two-factor auth requires an app
  password, and Microsoft has been retiring passwords for IMAP and SMTP.
- Signing in with Google and Microsoft (added after this round, `docs/oauth.md`):
  in a build that has a provider's client ID, the add view offers "Sign in with
  Google" and "Sign in with Microsoft" above the manual form, which follows them
  unchanged under "Or set up manually with a password"; a build without one
  shows the manual form alone. A provider's button signs in in the system
  browser while the window waits, with a way to open the page again and Cancel
  (or Escape); denial, timeout, a provider error and a network error each say
  so in the user's words, and the button tries again. The account added opens
  as one saved by hand. Its editor shows the provider and the address, changes
  only the name, and offers Sign out, which asks first and removes the tokens
  from the keychain while the account, its cache and settings stay.
- An account that has to sign in again (revoked, expired or signed out) says
  so in a banner above its cached mail, which stays readable, in the account
  menu when it is not on screen, and in the sidebar's footer; its syncs, sends
  and downloads wait instead of failing with an error line. "Sign in again"
  signs in as the account's own address; a send that failed for it stays in its
  window and in Drafts until the user sends it again.

### Folders and message list

- The sidebar is generated from cached `folders`, hiding absent ones; the unread
  badge comes from the cached STATUS. Starred is a virtual entry (FLAGGED). Remove
  the fake Labels section.
- The list title follows the current folder; the search field calls
  `CachedMessages(query)` and hits FTS5 (subject/from/body), with the short-query
  `LIKE` fallback.
- `ui.List` stays virtualized with stable identity via `ListState.Key`
  (key = accountID/folder/UID); after a switch, restore the selection by UID where
  possible.
- States: loading skeleton, empty folder, error banner + Retry; the sync button
  refreshes and preserves the selection.
- Remove the currently inert Filter/More buttons (do not ship dead controls).
- Folder management (`mail/folders.go`, `store/folders.go`, `folders.go` in the
  window): `Service.CreateFolder`, `RenameFolder`, `DeleteFolder` and
  `SetFolderSubscribed` send CREATE/RENAME/DELETE/SUBSCRIBE to the server first
  and change the cache only after it agreed, so a failure leaves the previous
  list. A system folder (Inbox, Sent, Drafts, Trash, Junk, Archive, Flagged)
  cannot be renamed or deleted, and deleting a folder asks first and says that
  its mail goes with it. A rename moves the folder's children and keeps their
  cursors and messages. `Folder.Subscribed` comes from LIST-EXTENDED where the
  server has it (the column defaults to subscribed for servers that cannot
  tell); the sidebar hides a hidden folder unless it is the one on screen, and
  the account menu lists hidden folders to show again. Folders are renamed,
  deleted and made from their context menu and the account menu; a message moves
  by name from the list's, a message's or the toolbar's Move to folder button and
  submenu, and by dragging rows onto a folder (`ListState.DragRows` and
  `ui.Drop`), which highlights the target, with the same undo as the toolbar's
  moves.

### Reading pane

- Selection changes come from `List.OnChange` (with a guarded job): read the cached
  body first, otherwise fetch in the background; show a placeholder while loading
  and an error + Retry on failure.
- The header shows the real sender, address, To, and time (`c.Now()` plus the
  relative-time rules); body paragraphs are `Selectable()`; attachment cards offer
  "Save as" (`mygo.Dialog.Save` + writing the file).
- Opening a message marks `\Seen` and updates SQLite, the row and the Inbox unread
  count in step.
- HTML mode: plain text by default; when an HTML body exists show a
  "Show HTML/Show plain text" toggle. HTML is rendered by an on-demand
  `win.NewWebView({Page:{Untrusted:true}})` with `ui.WebView` in the body slot;
  before loading, inject the CSP
  `default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'`
  (or the same with `img-src data: http: https:` once the user allowed that
  message's remote images),
  strip `<script>` and event attributes; `Page.OnWillNavigate` cancels navigation
  and asks before opening an http(s) link, showing its real destination
  (`security.go`); the user's `Open link` calls `c.Services().OpenURL`. Windowless
  tests only check the toggle state and the sanitizing function, not rendering.
- Remote images and previews (`mail/images.go`, `images.go`): an inline image a
  message names by `Content-ID` is embedded at parse time as a data URI, so it
  shows with no request and is not listed as an attachment. Remote `img` sources
  stay blocked by default; the reading pane says so and offers **Load images**
  for the message or **Always for this sender**, the latter kept by the engine
  (`Service.RemoteImages` / `SetRemoteImages`, a JSON setting keyed by address
  without case) and revocable with **Block images again**. Attachment cards offer
  a preview for images, text/PDF; the image decodes in the app, text shows
  selectable, a PDF loads into a second untrusted web view when there is a
  window, and anything else can still be opened in the default app or saved.
  **Save all** writes every file to a chosen folder, keeping duplicate names
  apart.
- Sender authentication and phishing advice (`mail/trust.go`, `security.go`):
  `Message.Trust` reads the `Authentication-Results` the receiving server added
  (else `ARC-Authentication-Results`, else `Received-SPF`) from `RawHeaders`, and
  reports passed, failed or absent; absent is never drawn as safe. The reading
  pane adds a written, icon-labeled notice for the verdict, an external sender
  (outside the accounts' and identities' domains) and a `Reply-To` at another
  domain, with an expandable explanation and the raw headers. Attachment names
  and types are checked for programs and disguises (`mail.RiskOf`) with a written
  warning. Every check reads cached bytes only, performs no lookup, changes no
  flag and blocks nothing.

### Common actions

- Star: `SetFlag(\Flagged)`, optimistically update the database/UI, roll back and
  report on failure.
- Archive/junk/delete: `Move` to the matching folder; disable the button when the
  target does not exist; on success remove the row, select the next message and
  update the unread badges. The row goes through the tombstone path, so a failure
  leaves the message visible.
- Reply/reply-all/forward: open a compose window with To/Subject/quoted body
  prefilled; reply-all excludes the user's own addresses, every identity of the
  account's, and dedupes; forward leaves the recipients empty. Set
  `In-Reply-To/References`. A reply or forward goes from the identity the message
  reached (`mail.ReplyIdentity`: its To, then Cc, then Delivered-To, else the
  account's own address), and a reply to the user's own message from the identity it
  went from. The Reply button that comes first and the R key do the default reply of
  the settings (reply to the sender, or to all); the button beside it and Shift+R do
  the other.
- Every send, delete and move is triggered only by a user click and is always
  visible in the UI.

### Compose and send

- `mygo.NewWindow` (720×620, min 520×420, no StateKey, multiple windows allowed)
  hosts `compose.view`; To/Cc/Subject use `ui.TextInput`, the body is an
  auto-sizing `ui.TextArea`; a Send button and a `ui.Cmd+Enter` shortcut.
- Recipients are validated with `net/mail.ParseAddressList`, and a field it does not
  read whole with the engine's `CheckRecipients` (see Address book, autocomplete and
  recipient validation, added since); the button is disabled while sending; on
  failure the window stays open with an inline error and the outbox row is left
  `failed` with its error; on success the window closes and Sent/the current folder
  refresh.
- Closing the window cancels the sending context; the outbox row remains so a
  restart can report "delivery unknown" instead of silently losing the message.

Added since: Bcc, identities, signatures, quoting and a send delay with Undo.

- Bcc: the field is folded away behind a Bcc button at the end of the Cc row, which
  the keyboard reaches; asked for, it opens with the keyboard in it and stays. A
  draft that has a Bcc opens with the field shown, and drafts keep it. Bcc recipients
  get the message through the envelope only: the message delivered names none of
  them, and the copy the engine saves in Sent alone has a `Bcc` header.
- Identities: an account sends from its own address and from aliases the user adds
  in the account editor ("Send from": add, edit, remove; the engine's
  `ErrIdentityAddress` and `ErrIdentityExists` are said in the user's words). Neither
  IMAP nor SMTP can list them, so the engine cannot check them: a server that does
  not allow one refuses the message. With more than one, compose shows a From picker,
  the account's own address first; the choice is kept in the draft
  (`Draft.Identity`) and the message goes from it, in its From, its envelope sender
  and its Sent copy.
- Signatures: each account has one (account editor: the text, plain and multi-line,
  and a switch each for new messages, replies and forwards, on by default), saved as
  it is typed. The engine's builders write the bodies: `NewBody` for a new message
  (room to write, then the signature), `ReplyBody` for a reply (above the quote by
  default, or at the end of a reply written below it) and `ForwardBody` (a note, the
  signature, then the message under its headers), the signature after the RFC 3676
  "-- " line. The caret starts where `Body.Caret` says: the window opens with the
  keyboard in the text of a reply, and in To otherwise. Tab into the body puts the
  caret there too rather than selecting everything, which the next key would erase.
  A new message whose body is still only its signature counts as empty and leaves no
  draft.
- Quoting: the settings choose how a reply quotes (`> ` before each line under a
  line saying who wrote it, or the message as it was under an "Original Message"
  block of its headers) and whether a reply is written above or below the quote. The
  words of the quotes come from the catalog, in the user's language, and the dates
  are written as the window writes them. An AI draft goes where the reply is written.
- Forwards carry the original's attachments when the settings say so (on by
  default): `AttachFromMessage` copies them into the forward's draft in a job, the
  window shows them coming and Send waits for them, and each one that could not be
  copied says why while the others stay attached. A reply never carries them.
- Send delay: the settings offer Off, 5, 10 (the default), 20 and 30 seconds. With a
  delay, Send saves the draft with exactly what is scheduled, calls `Schedule`, and
  closes the window; the mail window then shows "Sending in N s" above the list with
  the subject, counting down, and Undo, which the keyboard reaches. A job that lasts
  as long as the app, not the compose window, runs `SendScheduled`; its result is
  said as a send's is (a Sent copy not saved, or why it failed, the message then
  among the unsent ones to retry and in Drafts), in a note that stays until it is
  dismissed. Undo calls `Unsend` and opens the returned draft as it was written:
  text, recipients, Bcc, identity and attachments. An Undo that comes too late
  (`ErrAlreadyLeft`) says the message already left. A delay of 0 sends at once as
  before. At startup `DeliverScheduled` runs once in a job and the window says what
  it sent, and what it could not, which stays in the outbox.
- Settings, General tab, "Writing and sending": the send delay, the default reply,
  the quote style, replies above or below the quote, and forwards with attachments,
  each saved as it is chosen. The format is plain text, the only one, which the
  section says rather than offering a choice; rich text stays out of scope.

### Address book, autocomplete and recipient validation (added since)

The engine keeps the address book in the database and ranks it (`AGENTS.md` says how
it counts and orders contacts); the window shows it and asks it through the
`mailService` calls above. Nothing of it leaves the device: no request to a mail
server or a model carries it, and `ai/` has no way to it.

- Autocomplete: To, Cc and Bcc are combo boxes. For the entry under the caret (the
  text between the commas around it, as `mail.RecipientEntries` splits a field), once
  it holds two characters, or one of a script that writes names without spaces (a
  Chinese family name), a job asks `Suggest`, leaving out the addresses the three
  fields hold already. A list below the field shows what it returns in the engine's
  order, at most eight, each contact with its name and address, the parts the typed
  words begin in bold and a star on a favorite. The first is chosen, unless the entry
  is already a whole address, which then stays as typed unless the user moves to an
  offer. Up and Down move, Enter or Tab writes the chosen contact's address
  (`Contact.Address().String()`) in place of that entry, the entries around it kept,
  with ", " after it and the caret after both, as one step of undo; a click does the
  same, and Escape closes the list (the window again once it is closed). A query that
  a later keystroke overtook does not run, and an answer for text typed over since is
  dropped; until the new answer comes, the list keeps those it showed that the text
  still finds by the engine's own rules (`mail.ContactWords`, `mail.ContactTerms`), so
  it neither blinks at each key nor offers someone the text no longer finds. Typing
  never waits for the book. To assistive technology each field is a combo box whose
  suggestions complete what is typed (`aria-autocomplete=list`, expanded while the list
  shows), the list a list box of options with their place in it, and the field points
  at the chosen one (active descendant), as MyGo's comboboxes do.
- Validation before sending: a field `net/mail` reads whole goes as before, being
  exactly what `CheckRecipients` returns for it. When one does not, Send sends nothing:
  a job runs `CheckRecipients` on To, Cc and Bcc, the keyboard goes to the first field
  with problems, those fields are marked invalid with their messages as their errors,
  and a panel below the fields says, field by field, what each entry is: not an email
  address; a name no contact has; a name several contacts match, each offered as a
  button; a name one contact matches, offered with "Use …". An offer writes the
  contact's address in place of that entry, the spaces around it kept. A field with
  problems is checked again as it changes, so an entry fixed by typing loses its
  message. Drafts keep saving whatever is typed, unvalidated.
- The address book view: a contacts button beside Settings in the mail window's title
  bar opens it in place of the three panes (Done or Escape go back); it is no mail
  folder. It says that the contacts stay on this device, has a search field, which
  takes the keyboard as the view opens and finds contacts as suggestions do, and lists
  them in the engine's order in a virtual list read a page at a time (`Contacts` with
  an offset) as it nears the end of those read; after each change it reads them again
  from the top for as many as it showed, so the change shows at once where it puts the
  contact. Each row has the name and address and buttons, named for the contact, to
  make it a favorite or not, rename it in place (Enter saves, Escape cancels, an empty
  name hands the naming back to the mail) and delete it after a question that says
  writing to it again brings it back. "Add contact" opens a form whose errors are the
  engine's in the user's words: not one address, already in the book, one of the
  user's own addresses. Every action is reachable with Tab and Enter, and after an edit
  the keyboard goes back to the row's button, or after a delete to the next row's.
  Edits show in the next suggestion.

### Labels (added since)

The engine keeps labels, as IMAP keywords where a folder keeps them and on this device
otherwise (`AGENTS.md` says how); the window shows them and changes them through the
`mailService` calls above, and reads them again, with their unread counts, after each
read of the list (`labels.go`).

- The sidebar: under the folders, a Labels section of the account on screen, in the
  engine's order: each label's color where a folder has its icon, its name, its unread
  count (`Label.Unread`), and on a label kept on this device only (`Label.Local`) a
  mark whose tooltip says why, which screen readers read as the entry's description.
  "New label" ends the section. A label's entry opens its list (`mail.LabelScope`) as
  a folder's opens the folder: pages read as the user nears their end, conversations,
  the toolbar's actions, the label's unread count as the header's badge, each row's
  folder, and an empty state. It reads the cache alone and fetches no older mail, which
  its end says. Its header's line says when the label is kept on this device only, and
  a "⋯" button there, as the entry's context menu, edits the label or deletes it. A
  search from it covers the label's list ("This label") unless widened.
- Making and changing a label: a dialog takes its name, whose errors are the engine's
  in the user's words (empty or too long, another label's in any case) and keep the
  dialog open, and one color of the palette, with a preview of the chip. Renaming a
  label on the server says that other mail apps keep its first name. Each color has a
  shade for the light appearance and one for the dark; a label's name reads on its chip
  at 4.5:1 in both.
- Putting labels on and taking them off: the toolbar's tag button, and L, open a
  checklist of the account's labels over the rows chosen. A label is checked when every
  row has it and mixed when some do; a click puts it on all of them, or takes it off all
  once they all have it, and the checklist stays open. A conversation stands for its
  messages in the list's scope, as its other actions do, and has a label when any of
  them has it. The list's context menu (a right-click chooses the row first) has a
  Labels submenu that does the same, a label some rows have said to be on some; a
  message of the conversation pane has one in its own menu, for it alone; and each chip
  of the reading pane has a button that takes its label off. "New label…" in the
  checklist and the submenus makes a label and puts it on the rows chosen. Rows of
  several accounts (the unified inbox, a search of every account) take their own
  account's labels; rows of two accounts take none together, which the checklist and the
  menu say. The rows show a change at once; the list is then read again, which shows
  what the engine did and puts back what the server refused, and the line above the
  list says why. A label that would stay on this device for the rows chosen, their
  folder keeping no such keyword (`Folder.PermanentFlags`), is marked in the checklist,
  and once it is on, a note above the list says it is kept on this device only.
- Chips: a row ends its subject's line with its labels, at most two chips and then how
  many more, so that they stay on one line; a conversation's row has the labels of its
  messages in the list. The reading pane shows the message's labels, or the
  conversation's under its subject and each message's beside its sender. A chip is the
  label's dot and name, in its color over a tint of it, with the mark of a label the
  message has on this device only (`MessageLabel.Local`); on the row chosen it takes
  the row's colors. A row's labels are read with it to screen readers.
- Deleting a label asks first, in an alert that says its messages keep everything but
  the label, none moved or deleted, and, for a label on the server, that it comes off
  them there too. A deletion that took it off some messages only
  (`ErrLabelPartlyRemoved`) says so in another alert, which offers to finish by
  deleting it again; any other failure keeps the label and says why above the list.
  The list of a label deleted gives way to the inbox.
- Search: `label:` is one of the operators the search field splits its text by; the
  filters have a Label menu (any label, the account's labels, and in a search of
  several accounts the others' of other names) that writes it, quoting a name with a
  space; its chip names the label as the label writes it, and a row it found says it
  matched by its labels (`mail.FieldLabel`).
- Counts: marking messages read or unread changes their labels' unread counts at
  once, as it does their folders', until the labels are read again.

### Rules (added since)

The engine keeps the rules, sorts the mail that arrives with them and logs what they
did (`AGENTS.md` says how); the window writes them, orders them and shows the log
through the `mailService` calls above (`rules.go`, `ruleeditor.go`, `rulelog.go`).
Nothing in the window can make a rule send, forward or delete mail: the editor offers
the engine's actions, `mail.ActionKinds`, and no others.

- Where: a Rules button in the mail window's title bar, beside the address book's,
  opens the rules in place of the three panes (Done or Escape go back). Two tabs: the
  rules, and their activity. "Make a rule from this message…" in the list's menu of a
  row (one message, or one conversation's newest) and in a conversation message's
  own menu opens the editor of a new rule for its mailing list, where the cache keeps
  its List-Id, else for its sender's address, moving its mail to a folder still to
  choose; closing that editor goes back to the mail.
- The list: the rules in their order, the first tried first, each with its name, what
  it looks for and does in a line (`From contains “news@shop.example” → Move to
  Newsletters`), "Paused" on a paused one, a switch that pauses it or has it sort mail
  again (`PauseRule`), buttons that move it up and down, edit it and delete it, each
  named for the rule. The rows also drag to another place; each move is `MoveRule`,
  shown at once, and the keyboard stays on the button pressed, or goes to the other
  once the rule reached an end. Deleting asks first, saying that the rule stops
  sorting and that its log stays, to be undone.
- The editor: the name; whether a message meets all or any of the conditions; the
  condition rows (what each looks at, then the text it looks for, a header's name and
  text, or an account, its folder or its label from pickers, the account's only where
  the app has several, the rule's account by default); the action rows (the engine's
  kinds, then the folder or label, never the Drafts folder); and Stop. A move to the
  Trash shows a warning that the message is not deleted, but goes for good when the
  Trash is emptied. Each change is checked by `CheckRule` in a job, the jobs taking
  turns and one a later change overtook not running, and each `*mail.RuleError` shows
  below the row, the name, or the conditions or actions it names, in the user's words,
  the control at fault marked invalid with it as its error. A fault of something still
  to fill in (an empty name or text, a folder or label not chosen) shows once Save was
  asked for; any other at once. Saving (`SaveRule`) changes no mail; the editor stays
  open on the rule saved and says it sorts new mail from then on.
- The preview, beside the form, reads `PreviewRule` with each check: how many cached
  messages the rule matches now and a page of them (subject, sender, date, folder),
  with pages of 20, and, always, that saving changes none of them and the rule acts
  on new mail only. While rows are still to fill in it previews the complete ones
  (with a stand-in name, and an action that changes nothing while none is complete)
  and says so; a fault of the complete rows holds it. Once the rule is saved and
  unchanged, the preview offers "Apply to these N messages now"; it asks first,
  naming the rule, the count and the actions, and `RunRule` then takes exactly the
  messages counted, which the engine checks again. A paused rule, and more than
  `mail.MaxRuleRun` messages, are not applied, which the offer says. How the run went
  (applied, waiting for the server, failed) stays, with a way to its entries in the
  log. This run is the only way the window has a rule act on mail that was there.
- The activity log: newest first, a page of 50 at a time, each entry with the
  message's subject, its sender, the rule and when it acted, what it did ("(applied by
  you)" for a run), and how that stands: done, waiting for the server with the
  engine's reason, failed with its reason (a message gone in the user's words), or
  undone. Undo (`UndoActivity`) puts the message back; one no longer where the rule
  left it is left alone and the log says so. Each entry's menu pauses its rule or has
  it sort mail again, pauses it and undoes everything it did, and narrows the log to
  it; "Pause and undo everything it did…" asks first, then calls `PauseRule` and
  `UndoRule` and says how many changes went back. A rule deleted since only has what it
  did undone. Filters narrow the log to a rule, where that rule's Pause or Resume and
  "Pause and undo everything it did…" show too, and to a state.
- The window registers `OnRuleActivity` as it starts. When the engine reports actions
  applied in the background, the list of mail is read again where it shows that
  account's mail, the account's folders are read with their counts, and the log and
  the preview of the rule being written are read again if they are on screen.

### Calendar invitations (added since)

- `mail/calendar.go` parses the first VEVENT of a `text/calendar` part (or an
  `.ics` attachment): METHOD, UID, SEQUENCE, SUMMARY, DTSTART/DTEND with
  `VALUE=DATE` and `TZID` (the time keeps its zone), LOCATION, ORGANIZER,
  ATTENDEE PARTSTAT and STATUS:CANCELLED. An unreadable part is kept as
  `CalendarRaw` so the pane can say so instead of showing nothing. The
  invitation is stored with the message (`messages.calendar`) so the card shows
  from the cache after a restart.
- The reading pane shows a card with the summary, the time in the user's zone,
  location, organizer, attendee count, an update or cancellation marker, and the
  user's PARTSTAT when known. Accept / Maybe / Decline send an iMIP reply
  (`CalendarReply`) through the normal `Send` path with a
  `text/calendar; method=REPLY` part; nothing is sent without a click.
  **Add to calendar** writes the event to a temporary `.ics` and hands it to the
  system calendar; it never creates duplicates in the cache.

### Local AI command-line tools (added since)

- `ai/cli.go` adds Claude Code and Codex as providers beside the HTTP ones. A
  request to one runs the user's already signed-in command; Shu never reads or
  stores its credentials. The prompt goes in on standard input, never argv, and
  the working directory is a fresh `0700` folder under the system temporary
  directory, removed when the run ends.
- Claude runs `-p --output-format text --no-session-persistence
  --disable-slash-commands --tools "" --no-chrome --disallowedTools "*"
  --max-turns 1 --strict-mcp-config --mcp-config {"mcpServers":{}}` with a
  safety system prompt and `CLAUDE_CODE_SKIP_PROMPT_HISTORY=1`,
  `ENABLE_CLAUDEAI_MCP_SERVERS=false`; `--bare` is deliberately not used, as it
  would refuse the OAuth sign-in this route reuses.
- Codex runs `codex exec --sandbox read-only --ephemeral --ignore-user-config
  --skip-git-repo-check --color never`, with the feature flags that turn off
  apps, plugins, hooks, shell, browser, computer use, multi-agent and memories.
- The child gets only what it needs (`PATH`, home, user, locale, `NO_COLOR`), not
  the app's API keys or proxy secrets. Output is capped, ANSI is stripped, and a
  cancel kills the whole process group (`Setpgid` on Unix, `taskkill /T` on
  Windows). Errors are classified and never quote the prompt or tool output.
- `Service.CLIStatus` checks a tool with `--version` and its own sign-in command
  (`claude auth status --json`, `codex login status`): no prompt and no quota. A
  set command path that does not run is an error, never a fallback. The AI
  settings tab shows the status, version, path, model and the isolation note.
- `Service.CLIModels` asks Claude, through its `initialize` control request, which
  models it offers; the AI settings use them as autocomplete suggestions for the
  model field, while a typed value still wins. A failed run checks the sign-in
  state (content-free) and reports `Sign in required` rather than a generic
  failure. Codex still takes a free-text model.
- Advanced per-tool environment values live in the Settings → AI local-tool
  section: the configuration keeps only the names (`Config.CLIEnv`), values are
  in the keychain (`ai:cli:<provider>:env:<NAME>`), and reserved names such as
  `PATH`, `HOME` or a tool's managed variables are refused. They are merged into
  the child's environment after the whitelist, with reserved names filtered
  again.
- Not yet in this slice: the separate Providers management panel, Codex
  app-server, and live verification on Windows and Linux.

## Testing discipline

Tests are part of each change, not a later phase:

- Every commit in the steps below ships the tests for what it adds. A feature with
  no tests is not done, and a bug found during the work gets a regression test in
  the same commit.
- Test each layer at its own level: `store` against a real (temp/in-memory)
  database, `mail` against fake IMAP/SMTP servers, UI through `ui.NewTester`, and
  pure functions (HTML→text, sanitizing, address parsing, cursor math, query
  builders) as table-driven unit tests.
- Failure paths are first-class: network and auth errors, cancelled contexts,
  `UIDVALIDITY` resets, `sqlite.Load()` not built, downgraded SQLite without FTS5,
  outbox crash recovery, and rollback after a failed move.
- Tests stay deterministic: no wall clock (`tt.Advance`, injected clock), no
  network beyond `127.0.0.1` fakes, no display, no real account or keychain. Run
  without cgo: `CGO_ENABLED=0 go test ./...`.
- Keep fixtures in `testdata/` as real message bytes so MIME and store tests share
  them; prefer small hand-written fixtures over generated blobs.
- Use `go test -cover ./...` as a signal for the changed packages and mention
  uncovered branches in the commit body; it is not a gate.

## Key file list

Modified:

- `main.go`: new `app` fields, SQLite service wiring, sync/async helpers, root view
  dispatch by state (account settings/three panes), remove `demoMessages`
- `icons.go`: add 5 icons (settings/plus/x/check/circle-alert) and record the Lucide
  version in the header comment
- `mygo.json`: i18n configuration
- `main_test.go`: switch to the fake service; update `TestMailboxChoice` (the list
  must change after switching)
- `AGENTS.md`: Architecture section records the `mail/`, `store/` and `locales/`
  layout, the SQLite choice and the engine-only write rule
- `go.mod` / `go.sum`

New: `mail/` (models/Store interface/Service/IMAP/SMTP/MIME/special-use + tests),
`store/` (SQLite open and migrations, accounts/kv, folders, messages,
attachments, FTS search, outbox + tests), `testdata/` fixtures, and under package
main `sidebar.go`, `messagelist.go`, `reading.go`, `accounts.go`, `compose.go`,
`htmlview.go`, `mailservice.go` (the interface and fake may live in tests),
`locales/en.json`, `locales/zh-Hans.json`, the generated `locales/catalog_gen.go`,
and 5 `icons/*.svg`.

Reused as-is: the three-pane layout and `ui.List`/`ui.Sidebar`/`ui.Scroll`/`ui.Toolbar`,
the icon pipeline, the Tester pattern, `mygo.Window.Update`, `mygo.Secrets`,
`mygo.App.Path`, `ui.PopoverMenuButton`/`ui.Menu`, `mygo.Dialog`,
`mygo/plugins/sqlite`.

## Implementation steps (split by commit)

Each step ships with its tests (see Testing discipline).

1. `feat: add i18n catalogs and localize the shell`: config, catalogs, generated
   code, migrate existing strings, catalog-parity test, stabilize test assertions.
2. `feat: add the SQLite store`: open/pragmas/migration array, accounts and kv,
   folders with sync cursors, message headers/bodies, attachments, tombstones,
   outbox, FTS5 trigram + triggers with the LIKE fallback, store tests including a
   CJK search test.
3. `feat: add the mail engine`: dependencies, models, Store interface, keychain,
   IMAP/SMTP/MIME, fake-server tests.
4. `feat: add account setup and multi-account switching`: account view, menu,
   persistence, language choice.
5. `feat: load cached mail and sync folders`: dynamic sidebar, cached list,
   incremental sync with changeset transactions, FTS search, unread, empty/error
   states.
6. `feat: add reading and common message actions`: body loading, seen, attachment
   save-as and disk cache, star/archive/junk/delete with tombstones.
7. `feat: add compose, send, reply and forward`: compose window, outbox, SMTP, Sent
   copy, reply prefill, crash recovery surface.
8. `feat: render HTML mail in an isolated web view`: sanitizing, CSP, navigation
   blocking, dual-mode toggle.

Every commit must build, pass tests, and leave `gofmt` clean.

## Testing and verification

Automated:

- `store/store_test.go`: migrations from empty (`user_version`, tables, indexes,
  idempotent reopen), account/kv round-trips, folder cursor advance in the same
  transaction as the batch, UIDVALIDITY reset, message upsert, tombstone
  confirm/rollback, foreign-key cascades, attachment metadata and hash dedup, outbox
  state transitions and crash recovery, FTS5 CJK search (2- and 3-character
  queries) plus its LIKE fallback, LRU cleanup; skip with a hint to run
  `go tool mygo generate` when `sqlite.Load()` is not ready.
- `mail/mime_test.go`: fixtures from `testdata/` for plain text, HTML-only,
  multipart/alternative, attachments, encoded subjects/charsets, truncated bodies.
- `mail/imap_test.go`: an `imapserver` + in-memory `Session` fake on `127.0.0.1:0`
  (self-signed certificate + STARTTLS) covering LIST/special-use, incremental sync,
  UIDVALIDITY change, FETCH+mark seen, STORE, MOVE (with and without capability),
  APPEND, and cancelled contexts; `mail.Store` is an in-memory fake and never
  touches SQLite.
- `mail/smtp_test.go`: a `go-smtp` fake server recording messages, covering
  success, auth failure, and raw headers (To/Cc/Subject/In-Reply-To).
- `mail/config_test.go`: in-memory Store + in-memory SecretStore, verifying account
  CRUD, secret cleanup on delete, and error paths.
- UI: `ui.NewTester` + a fake `mailService`, synchronous `run/update`, fixed
  locale; covering first-run onboarding, multi-account switching, cache-first
  first screen, folder switching, search, selecting a message to load the body
  and mark it read, retry after an error, star/move with rollback, compose send
  success/failure/unknown, reply prefill, HTML toggle; both light and dark
  appearance; the 1000×640 minimum size.

Manual:

- `gofmt -w` the changed files; `go tool mygo generate` (prepares both the i18n
  and the SQLite native module); `go tool mygo i18n check` (if available).
- `go vet ./...`, `go tool mygo vet .`, `CGO_ENABLED=0 go test ./...` and
  `go test -cover ./...` for the changed packages.
- `go tool mygo dev` with a throwaway test account to walk through login,
  reading, starring, archiving, replying, sending and the HTML toggle: on the
  manual path with an app password, or by signing in with Google or Microsoft
  with development clients set as `docs/oauth.md` describes; the dev data
  directory is `shu Dev` and never touches a real install; commit no
  credentials, no client secrets and no real mail.

## Risks and caveats

- SQLite native module: `go test`/`go run` rely on the precompiled `.mygo/native`
  libraries prepared by `go tool mygo generate` (or `mygo dev`); when unprepared,
  `store` tests skip and the app must not run before that step. The build still
  needs no cgo/Zig.
- FTS5/trigram depends on the bundled SQLite build. A startup smoke query plus a
  CJK store test fail loudly if a future plugin version drops it; keep the `LIKE`
  fallback so search degrades instead of breaking.
- Bodies inline make the database grow; the sync cap, on-demand bodies and the
  `body_truncated` flag are the guardrails. Revisit with a size metric on a large
  account before raising the cap.
- go-imap v2 is still beta: pin `v2.0.0-beta.8` and verify field names such as
  `FetchItemBodySection`/`StatusOptions` against the source after `go get`; degrade
  to an empty preview when partial fetches fail.
- UIDVALIDITY is compared, never interpreted: the only allowed conclusion from a
  change is "this folder's cache is stale, drop and refetch".
- `mygo.Tester`'s locale differs from the global `i18n.Locale()`: tests override the
  global in `TestMain` and unit-test Chinese via `locales.In("zh-Hans")`.
- The webview only ever receives sanitized HTML: Untrusted + CSP + navigation
  blocking are three layers of defense; remote images and scripts are forbidden.
- One short connection per network operation adds latency; the cache-first first
  screen hides most of it, and session reuse/IDLE come later.
- The keychain is unavailable in some Linux environments: report a clear error,
  never fall back to plaintext.
