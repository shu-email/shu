# AGENTS.md

Guidelines for agents working in this repository.

## The project

Shu is a native, AI-first, open-source desktop email client for macOS,
Windows and Linux. It is written in Go with
[MyGo](https://mygo.run) and draws its own UI, with no webview or Electron.

- Shu talks IMAP/SMTP directly to the mail provider. There are no Shu
  servers, no telemetry, and mail never passes through us.
- AI features are BYOK and local-first: keys stay on the user's machine.
- The project is pre-alpha. Keep changes small and reviewable, and do not
  add speculative scaffolding for features that were not asked for.
- Anything that sends mail, deletes mail or sends data to a model happens
  only after an explicit user action, and is always visible and reviewable
  by the user.

## Architecture

- The app starts as a single `package main`, but the mail engine is not
  UI code. Protocol (IMAP/SMTP), sync, storage and AI code must never
  import `github.com/egoist/mygo/ui`; the UI reaches the engine through a
  plain service value it calls, and the engine reports back with data,
  not elements.
- When a subsystem (sync, store, ai) grows past a file or two, move it
  into its own package in the same change, keep the boundary above, and
  update this section to describe the new layout.
- Background work (sync, indexing, model calls) runs off the UI thread
  and publishes results to the window; it never touches view state
  directly.
- The engine is the only writer to the cache. `mail/` holds accounts,
  IMAP/SMTP, MIME and the `Service` the UI calls; `store/` is the SQLite
  implementation of the `mail.Store` interface and owns the schema,
  migrations, FTS5 index and outbox. Views read through `Service` and
  never open a connection or write a row themselves. The migrations are
  the schema; a schema change follows `docs/schema-migrations.md` and
  upgrades every past version in `store/migrations_test.go`.
- A draft lives in the database, and that copy is the truth; the message
  in the server's Drafts folder mirrors it for other mail clients.
  `Service.SaveDraft` writes the row and never touches the network, so
  writing works offline. `Service.SyncDrafts` is the separate step that
  uploads, removes and reconciles with what other clients did; it can
  fail and run again. Views save on every change and ask for a sync; they
  never decide what the server holds.
- The files attached to a draft are the user's data, not cache. The
  engine copies each one at once into `draft-files/` in the user-data
  directory, one file per content hash, and the database keeps only
  their names, types, sizes and hashes. Only `Service.AttachFile` and
  `Service.RemoveAttachment` change a draft's attachments; `SaveDraft`
  leaves them alone. A stored file goes when no draft and no unsent
  outbox entry names it. `Service.SyncDraftEdits`, the sync views ask for
  on a pause in the typing, leaves a draft with large attachments for
  `SyncDrafts`.
- The outbox keeps a message without attachments as its bytes. For one
  with attachments it keeps the envelope and the references to the
  stored files, and builds the message again for each try.
- A message sent with a delay waits it out in the outbox, `scheduled`
  with the time it is due, so that it outlives the app: `Service.Schedule`
  writes it, `SendScheduled` sends it when it is due, `Unsend` takes it
  back until then and returns its draft, and `DeliverScheduled` sends,
  as the app starts, those an earlier run left waiting. One change of the
  entry's state in the store (`Store.ClaimOutbox`, from `scheduled` to
  `sending` or to `cancelled`) decides, so a message is never both sent
  and taken back. A message goes from one of its account's identities,
  its own address or an alias the user entered, in its From and in the
  envelope alike. Its Bcc recipients are in the envelope only: the copy
  saved in Sent alone names them.
- A reminder is a conversation the user asked to come back to
  (`mail/reminders.go`, `store/reminders.go`). It is this device's: one
  per conversation, in the store, with the subject it had when it was
  set. `Service.SetReminder` moves the conversation's inbox messages to
  the account's Archive folder and saves the time; an account with no
  archive cannot remind one in its inbox (`ErrNoArchive`).
  `DeliverReminders` at that time, as the app starts and each minute it
  runs, moves the archive's messages back to the inbox, syncs that
  folder, and drops the reminder; only the archive's messages move, so
  mail the user moved elsewhere meanwhile stays, and a reminder a thread
  merge carried follows the conversation to its new ID. The reminders'
  list is `mail.RemindersScope`, the cache alone, soonest due first, and
  in every folder `Folder.InLabelScope` keeps; its conversations span
  folders, so their actions leave Sent alone. The undo of setting one
  takes it off, when the mail came back, and puts back the one it
  replaced.
- Follow Up is derived from the cache alone (`mail/followup.go`,
  `store/followup.go`): the conversations whose newest message outside
  the drafts, junk and trash is one the user sent
  (`FollowUpMinAge` to `FollowUpMaxAge` old) and no one answered, with no
  message of the conversation in Junk or Trash. `mail.FollowUpScope`
  carries the time that decides, so a list and its count agree; the store
  uses its own clock when it is zero. `Service.DismissFollowUp` keeps a
  conversation off until a message newer than the dismissal is cached
  (`follow_up_dismissals`, one row per conversation, carried by a merge);
  no server hears of it. Its list spans the folders
  `Folder.InLabelScope` keeps, its rows are the sent messages, and the
  window lists it as conversations even where the user lists single
  messages.
- Mail is received while the app runs by one watcher per account inside
  `mail.Service` (`Watch`, `Unwatch`, `Nudge`, `SetOnline`). A watcher
  keeps its own connection to the inbox, opened read-only, idles where
  the server can push and polls otherwise, backs off when it fails, and
  never changes a flag or moves a message; what the rules do to the mail
  a check brought is the service's, on connections of its own (see the
  rules below). It reports data
  (`mail.WatchEvent`: state, folder counts, the messages to announce);
  the window decides what to notify, draws the state and sets the badge.
  A message is handed out for announcing at most once: `Store.TakeNew`
  moves the folder's watermark (`watermarks` table) in the same step
  that returns it. The settings of checking, notifying and listing mail
  are `mail.Settings`, kept in the store. Notifications, focus, wake and
  network reach the window through hook fields on `app` that `attach`
  sets. The target of a notification travels in its ID, which is parsed
  as untrusted input.
- How each sync is going is reported, not polled: `mail.SyncEvent`
  (`mail/sync_events.go`) names the account, the folder (empty for
  drafts), what kind of sync it is (`mail.SyncKind`), the step it is in
  (`mail.SyncPhase`), how many of how many it has done when the server
  said, and, when it ends, whether the app cancelled it or why it failed
  (`mail.SyncFailure`, the classes `failureOf` sorts a watcher's failures
  into, so the window says its own words rather than the server's).
  `Service.OnSync` sets the one receiver, which runs on the syncing
  goroutine; the window posts each event to the UI thread and keeps the
  last word of each of an account's syncs (`syncstatus.go`), drawing the
  account on show above the list. A sync that finishes or is cancelled
  takes its own line away; a failed one stays until that sync runs again,
  and a sync still running keeps its line when another of the account
  ends. A watcher's check and an older-mail fetch have lines of their own
  (the state above the list and the list's footer), so only a failure of
  theirs takes this one. A sync never waits for the window.
- An action on mail the server cannot be reached for is neither lost nor
  a failure: `Service.SetFlags`, `Service.SetLabel` and
  `Service.MoveMessages` (and so `Move`) write it to
  `pending_operations` (migration v21, `mail/queue.go`,
  `store/pending.go`) before the server is touched, show it in the cache
  and return. The account's one queue worker, which runs beside its
  watcher, makes the actions oldest first, taking each with one state
  change (`Store.ClaimOperation`) so that no action is made twice, and
  it groups those one command makes (`Store.ClaimOperationsByKey`). A
  server it cannot reach is tried again with a growing wait, and one
  that refuses an action leaves it `failed` with its reason for the
  window to show; a refused move puts back the message's row. A folder
  whose UIDVALIDITY changed since the action was queued is not touched,
  and a move whose message the folder no longer has is done. The
  actions are the ones in `mail.OperationKind`; rules and their undo
  keep their own log of what has not happened, and a reminder waits for
  its next run, so neither is queued. The window hears of the queue as a
  `WatchEvent` with `Queue` set and says it under the watcher's line.
- The list's one undo banner takes back the last change, whether the
  server made it or it waits in the queue. An undo cancels the queued
  actions it can (`Store.CancelOperations` drops only those no worker
  has begun) rather than making them first, puts the cache back, and
  leaves the rest for the inverse change (`mail/undo.go`); a move's undo
  cancels its action and says so when the worker had begun it already
  (`Service.MoveBackMessages`).
- An account signs in with a password or, for Google and Microsoft, with
  OAuth (`Account.OAuth`, `mail/oauth.go`, `mail/signin.go`,
  `docs/oauth.md`). The engine runs the whole flow, PKCE and a one-answer
  listener on 127.0.0.1, but never opens the browser: the window passes
  an `OAuthPrompt` that opens the URL it is given. The refresh token is
  in the keychain and the access token in memory only; each connection
  takes a token from `accessToken`, which renews an account's token one
  call at a time. A sign-in the provider refuses to renew is deleted, and
  the account then fails fast with `ErrSignInRequired` (its watcher
  reports `WatchSignIn` and waits) until `SignInAgain`, which takes only
  the account's own address; its cache and settings are never touched.
  Client IDs are set at build time (`-X shu/mail.GoogleClientID=…`).
- The account form finds a domain's servers before the user types them
  (`mail/discover.go`, `store/discover.go`, `accounts.go`): the entry
  the user made for the domain, a provider's preset
  (`mail.ProviderFor`), what an earlier lookup kept, then Thunderbird's
  autoconfig at `autoconfig.<domain>` and its well-known path,
  Microsoft's autodiscover, and DNS SRV (`_imaps`/`_imap`,
  `_submissions`/`_submission`). Only the address leaves, never a
  password or token; each request is bounded in time, size and
  redirects, the whole lookup is cancelled when the form closes or the
  address changes, and nothing is sent elsewhere. A complete finding is
  kept in `discoveries` (migration v20); the servers the user enters win
  over a preset and every later lookup. A lookup that finds only one
  server, or none, returns what it found with a `*mail.DiscoveryError`
  naming the sources that failed, and the manual form stays either way.
- Conversations are the cache's own. `store/` puts every message in one
  (`thread_id`) as it writes it, by the keys of `mail.ThreadKeys`: its
  Message-ID, In-Reply-To and References, and the conversation part of an
  Outlook Thread-Index, never the subject. Messages of any folder of an
  account that share a key are one conversation; a message that links two
  merges them into the lower ID, and `threads` hands out IDs that are never
  reused. `thread_refs` keeps every key an account has seen, so a reply
  that arrives before its original is found by it. Assigning reads before
  it writes, so `Store` holds a mutex over the reads and the transaction
  that writes the rows with their conversations. A sync reads References,
  In-Reply-To and Thread-Index with `BODY.PEEK[HEADER.FIELDS (...)]`, also
  for the cached window it refreshes. Views list conversations with
  `Service.ConversationPage` and read one with
  `Service.CachedConversation`, from the cache alone; a conversation-wide
  action acts on exactly what `Service.ScopedConversation` returns for the
  list's scope, `MoveMessages` / `MoveBackMessages` move such a set and undo it,
  and `SetFlags` stars it or marks it read with one connection and one
  STORE per folder, changing only `\Seen` and `\Flagged` and never moving
  or expunging. The window lists conversations unless the user switched
  to single messages (`mail.Settings.Conversations`); the Drafts entry
  always lists drafts one by one. A row is a conversation's messages in
  the list's scope, and the toolbar acts on those. A conversation's
  counts, snippet and participants are aggregates on `threads`, and those
  of one folder on `thread_folders`, so that a list reads a row per
  conversation rather than its messages; triggers write them in the
  transaction of every write of a message, and
  `Store.RebuildThreadAggregates` recomputes them from the messages. The
  reading pane shows
  the conversation across folders, each message once and without the
  copies in Drafts, Junk and Trash the list does not show, and each
  expanded message has actions of its own.
- Lists read the cache a page at a time. A `mail.Scope` says what a list
  covers: a folder of an account, every folder of one or more accounts,
  or each account's folder of a special use (the unified inbox is every
  account's `\Inbox`). `Service.MessagePage` and
  `Service.ConversationPage` return the rows newest first after a
  `mail.Cursor`, the date and row of the last one, so mail that arrives
  meanwhile neither repeats nor skips a row of the pages after it. Every
  row carries its account, and every action still takes one account:
  nothing in the engine acts on two in one call. A `mail.Search`, which
  `mail.ParseSearch` reads from typed text, combines its parts with AND;
  each term is one phrase, through the trigram index from three
  characters and LIKE below, never an operator of the index, and each
  row says in which parts (`mail.Field`) its terms were found. The index
  holds the subject, sender, recipients, body and preview; it is an FTS5
  external-content table whose triggers write it in the transaction of
  every message, and `Store.CheckSearchIndex` proves it is in step. The
  older reads that take an account, a folder and a query are this read,
  with the query as one term.
- A folder's first sync caches its newest `mail.Settings.SyncWindow`
  messages, the window's and the watcher's alike. `Service.FetchOlder`
  fetches on demand, read-only and as a sync fetches, the newest
  messages the cache lacks below the sync cursor; it moves neither the
  cursor nor the watermark of announced mail, so older mail is never
  announced. `Folder.Complete` records that the cache lacks none of a
  folder's messages, and `Service.FolderCache` counts what is on this
  device against what the server has. The window reads the next page as
  the user nears the end of the rows read, reads a list again from the
  top for as many rows as it shows, and calls `FetchOlder` only when the
  user asks. Its keys of rows, bodies and summaries include the account,
  and an action takes the account from the row it acts on.
- A smart mailbox is a named search this device keeps (`mail/smart.go`,
  `store/smart.go`, `smart.go`): a name, the list it was saved from (an
  account's folder, a label, Starred, or the folder of a special use),
  the search text and the scope it covered, from `mail.SmartScope*`.
  `Service.SaveSmartMailbox` trims and checks them and a name is one per
  account whatever its case; a change keeps the saved list, and
  `Service.DeleteSmartMailbox` drops only the entry. Opening one puts its
  folder, query and scope on the window and the ordinary list read does
  the rest, so its pages, cursor and rows are a search's, with no search
  engine of its own, from the cache alone. `Service.CountUnread` counts
  the unread mail the same scope and search read, for the entry's badge;
  the window keeps those counts in `smartState` and refreshes them as
  mail and syncs move the cache. A saved folder that is gone lists
  nothing and says so, rather than widen the search to every folder.
- The address book is the cache's own too, and never leaves the device.
  `store/` keeps a contact per address, whatever its case, across the
  accounts (`contacts`), by the rules of `mail.ContactState`. It counts,
  in the transaction of the write and each message once (by Message-ID,
  `contact_messages`), the sender of every message it caches outside Junk
  and Drafts or, for a message from one of the user's own addresses (an
  account's or an identity's), its recipients; and the recipients, Bcc
  too, of each message the outbox records as sent. The user's own
  addresses are no contacts, and a sender that takes no replies makes
  none. A contact ranks by the messages that made it one, each weighing
  half as much a month older: favorites first, then those the user wrote
  to or added, then those that only wrote to the user. The user's edits
  (`Service.AddContact`, `RenameContact`, `FavoriteContact`,
  `DeleteContact`) are rows as well: a name the user gave is never
  changed by mail, and a deleted contact keeps its address alone, so that
  the mail   it sends does not bring it back; a message the user writes to
  it does. `Service.Suggest` reads the book by the prefix of a word typed
  (`contact_terms`), and `Service.CheckRecipients` reads a recipient field
  as a send takes it and tells a broken address from a bare name, with
  the contacts that name may mean. `ai/` has no way to the book.
- What a message's headers say of its sender and its list is read once
  and kept with the cached message (`mail/security.go`, migration v22's
  `messages.security`): the authentication verdict with its checks and
  the evidence they came from (`Message.Trust`), and the
  `List-Unsubscribe` and `List-Unsubscribe-Post` headers that say how to
  leave the list (`Message.Unsubscribe`). A sync reads those headers for
  every message it fetches (`threadSection`) and keeps them
  (`Message.SetRawHeaders`), and a read of the cache has the verdict
  without them and after a restart; a message whose headers said nothing
  keeps none and stays unverified, never safe. Nothing looks anything up
  on the network, and nothing unsubscribes without the user's action.
- Labels are the user's categories of messages, an account's each
  (`mail/labels.go`, `store/labels.go`). On the server a label is an IMAP
  keyword: `mail.LabelKeyword` writes a name as one, as it is when it is
  an atom and in MIME's Q encoding otherwise ("To Do" is `To_Do`), which
  reads back whatever case a server keeps it in, and `mail.LabelName`
  names the label a keyword found on the server becomes: Thunderbird's
  `$label1`-`$label5` by its names, the keywords of the system, of junk
  and of forwarding never. A folder keeps keywords when its PERMANENTFLAGS
  have `\*` or name the keyword; `Folder.PermanentFlags` is what a
  read-write SELECT said (a sync's, or a change of labels'), never a
  read-only one's. A sync writes the labels of the keywords it fetches,
  and its refresh of flags brings what other clients changed.
  `Service.SetLabel` changes a label on messages as `SetFlags` changes a
  flag, but the cache first: one connection, one silent UID STORE of the
  keyword per folder, and the cache put back when the server refuses. In
  a folder that keeps no such keyword the label stays on this device, by
  `mail.LabelKey` (the Message-ID, or a digest of sender, date and subject
  for a message without), so that it follows the message through moves,
  resyncs and restarts, and no server hears of it. `Label.Local` says a
  label is kept on this device only (no folder known keeps its keyword),
  `MessageLabel.Local` that a message has one so, and `Summary.LabelsLocal`
  that a message's folder keeps no new keyword. A label's name and color
  are this device's: a rename keeps its keyword. `Service.DeleteLabel`,
  which the window confirms first, searches every folder of the server for
  the keyword and takes it off with STORE -FLAGS, then drops the label; it
  never moves or deletes a message, a failure leaves the label
  (`ErrLabelPartlyRemoved` once some messages lost it) and deleting it
  again finishes. `mail.LabelScope` lists a label's messages in every
  folder but Junk, Trash and Drafts, with its unread count in
  `Label.Unread`; a conversation's row carries the labels of its messages
  in the list's scope, and `label:` searches by name.
- Rules sort the mail that arrives (`mail/rules.go`, `mail/arrivals.go`,
  `mail/ruleapply.go`, `mail/ruleundo.go`, `store/rules.go`). A rule is
  in an order, enabled or paused, with conditions all or any of which a
  message meets (sender, recipients, subject and a named header contain a
  text in any case; attachments; account, folder, label) and actions of a
  closed set, `mail.ActionKinds`: move to a folder of the message's
  account, add or remove a label, star, mark read or unread, skip the
  notification. No rule can send, forward or delete; `Service.CheckRule`
  refuses any other kind, and what it refuses comes as a `*RuleError` that
  names the condition or action. A rule names one account at most and goes
  with it. Mail that arrives is what an incremental sync fetched above a
  folder's cursor, in a folder synced before under the same UIDVALIDITY:
  never a first sync, `FetchOlder` or a sync after a reset, and never a
  message this app moved there itself, which `Store.ExpectArrivals` notes
  by its key before the move, so that an undo is not undone. The rules
  watch each account's inbox and the folders their conditions name, never
  Drafts. A sync writes its batch and sorts what arrived in one step under
  `Service.arrivalMu`, from the cache alone, having fetched only the
  headers enabled rules read (kept in `raw_headers`); the watcher takes
  the mail to announce under the same lock and leaves out what a rule
  moves, marks read or keeps quiet. Every match is an entry of the log
  (`mail.RuleActivity`), pending until a goroutine of the service applies
  its actions with `SetLabel`, `SetFlags` and `MoveMessages` and records
  what they changed; a server out of reach leaves it pending for a later
  sync, so that nothing delays the mail. `UndoActivity` and `UndoRule` put
  back what entries changed, moving back only a message whose envelope
  makes the entry's key; `PauseRule` disables a rule and drops what it
  had pending that had not begun. `PreviewRule` lists the cached matches
  of a rule, saved or not, and changes nothing; `RunRule` runs a saved
  rule once over exactly those, logged as any match. The log keeps the
  newest `mail.MaxRuleActivity` entries and every pending one.
- Leaving a mailing list and blocking a sender are the user's own choices,
  kept on this device (`mail/unsubscribe.go`, `mail/block.go`,
  `store/block.go`, and the window's `block.go`). What a message's
  `List-Unsubscribe` headers offer is read from the cached headers alone
  (`mail.ParseUnsubscribeOffer`); the window offers an unsubscribe only
  when one exists, and the confirmation names the exact URL or address
  before anything leaves. `Service.Unsubscribe` makes the method chosen:
  one RFC 8058 POST through `Service.HTTPClient`, an email from the
  account through `Service.Send`, or nothing at all for a page, which the
  window alone opens. Nothing unsubscribes by itself, and the choice is
  kept per sender and account (`unsubscribes`, migration v23).
  A block names an address or a whole domain, per account, and the folder
  its new mail goes to. `fileBlocked` runs in `sortArrivals` before the
  rules, takes the sender's mail out of the batch and files it with
  `MoveMessages`, so the move waits in the queue when the server is out
  of reach, and the watcher announces none of it (`heldBack`). No cached
  mail moves, and no server hears of the block. The banner's undo drops
  the block and puts back the one it replaced, and the settings' Privacy
  tab lists the blocks to unblock (`Service.BlockedSenders`,
  `UnblockSender`, `blocked_senders`).
- `ai/` is the AI layer: provider settings, the excerpt that decides what
  part of a message may leave the device, the features (summary of a
  message or a conversation, triage, draft reply) and `Service.send`, the
  single egress point every model
  request passes through. It imports neither the UI nor `mail/`; views
  reach it through the `aiService` value. Its settings and the summary
  cache live in `store/`; its keys live only in `mygo.Secrets`. Local
  Claude Code and Codex providers run the user's already signed-in
  command in a private temporary folder with tools off where the tool
  allows it, the prompt on standard input and a bounded, cancellable
  process (`ai/cli.go`); their credentials are never read or stored. It
  returns text and suggestions, and has no way to change the mailbox.
- `locales/` holds the catalogs and their generated accessors. Every
  user-visible string comes from there, in en and zh-Hans together.
- Passwords and OAuth refresh tokens live only in `mygo.Secrets`, and
  access tokens only in memory; the database keeps accounts (with how
  they sign in), folders, message headers and bodies, attachment
  metadata, the outbox and the drafts. Attachment bytes never go into SQLite: those of
  received mail go to the cache directory, those of drafts to the
  user-data directory, both keyed by content hash.
- The parsed cache is versioned: `mail.CacheVersion` names the form of
  the bodies and attachment rows, and `Service.PrepareCache`, which the
  app runs as it starts, drops them when a build changed it, then sweeps
  the attachment files and temporary files no row names. It never touches
  drafts, the outbox, contacts, labels, rules or settings, which the
  server cannot give again.
- The cache's derived state can be checked without changing it:
  `Service.CheckIntegrity` (`mail/integrity.go`, `store/integrity.go`)
  reports the schema version, the references the foreign keys promise,
  the full-text index, the conversations' aggregates, and the
  attachment and draft files, with counts and a few IDs and never a
  message's content. The settings' General tab runs it
  (`integrity.go`); it repairs nothing.

## Before changing MyGo UI

- Check the pinned MyGo version and read its source before using an API:
  `go list -m -f '{{.Dir}}' github.com/egoist/mygo`.
- Read the repository skills that cover the change:
  - `.agents/skills/mygo-maintenance/SKILL.md` for views, lifetime,
    identity, focus, actions, threading and testing.
  - `.agents/skills/mygo-native-modules/SKILL.md` only when a change
    touches a native module (Swift, Rust or Zig).
- The app pins MyGo `v0.9.0` for now. Do not upgrade MyGo or its APIs as a
  side effect of another change; do it in a change of its own.

## Commands

```sh
go tool mygo dev          # run the app with the pinned toolchain
go tool mygo vet .        # check stored elements and goroutine captures
CGO_ENABLED=0 go test ./... # run the tests
go tool mygo build        # build a release app
GOFLAGS=-tags=e2e MYGO_AUTOMATION="$SOCK" go tool mygo dev -background
                          # run it for go tool mygo-automation to drive
GOFLAGS=-tags=e2e SHU_SEED=1 go tool mygo dev
                          # run it with a seeded account on local servers
```

To check a UI change in the running app, follow
`.agents/skills/shu-desktop-check/SKILL.md`.

Run `gofmt` on files you change, `go tool mygo vet .` and the tests before
committing. The test suite must pass without a display, network access or a
real clock.

## Code

- Write comments and identifiers in English, as the rest of the code is.
  Comments explain why, not what the next line does.
- Keep the package small: prefer plain functions, data and a single `app`
  struct over abstraction layers. Split files by concern (`icons.go`,
  `main.go`) rather than by layer.
- Do not add a dependency for something the standard library or MyGo
  already does. Ask before adding one that affects the build, the app's
  size or its network access.
- Handle errors rather than ignoring them. Do not panic for failures a
  user can cause or fix; `mustIcon` and other startup-only helpers may
  panic on programmer errors.
- User-visible strings go through the i18n catalog, never hardcoded in a
  view, so en and zh-Hans stay in step from the start.

## Native UI

- A view is a function of the app's state and runs on the main thread. It
  may build several times per event, so trigger side effects from actions
  or explicitly guarded jobs, never from rendering alone.
- Never keep `ui.Element` values or custom parts in app state or workers;
  they expire before the next build pass. Keep app data, widget state and
  `ui.Handle` values instead.
- Give stateful widgets and lists stable identity with `c.Key`,
  `.Bind(&handle)` or `ListState.Key` before they are built. List rows
  must keep their state when the list changes around them.
- Keep slow I/O and expensive work off the UI thread. Publish results
  with `Window.Update` and discard stale results.
- Do not capture `*ui.Context` in goroutines or long-lived callbacks.
  Capture `c.Services()` when a background job or persistent callback needs
  clipboard or URL access.
- Use `c.Now()` instead of `time.Now()` inside views so tests control the
  clock. Use theme values (`c.Theme()`, `t.Space`, `t.Rem`) instead of
  hardcoded colors and sizes, and use `light-dark(...)` for window colors
  that must follow the system appearance.
- Use `ui.Cmd` for the primary shortcut: it is Command on macOS and Ctrl
  elsewhere. Keep keyboard focus order sensible.
- Icon-only buttons need a `Label` and a `Tooltip`, and icon names must be
  unique enough for tests to find them.
- Use the existing widgets (`ui.Sidebar`, `ui.List`, `ui.Toolbar`,
  `ui.Split`, `ui.SearchField`, …) rather than rebuilding their input,
  focus and accessibility behavior. For long collections, build rows with
  a virtual `ui.List` instead of a column of hand-built rows.
- The layout must hold together at the window's minimum size (1000×640),
  in light and dark appearance, and on all three desktop platforms.

## Icons

- Do not draw SVG icons by hand or inline paths in Go code.
- Use [Lucide](https://lucide.dev) icons only: download the SVG file from
  Lucide (for example `https://unpkg.com/lucide-static@<version>/icons/<name>.svg`)
  and put it in `icons/`.
- Embed the directory and parse each icon once at startup, then reuse the
  package-level `*ui.SVG` values, as `icons.go` does with
  `//go:embed icons/*.svg` and `mustIcon`.
- Keep the files as Lucide ships them: keep the license header and
  `currentColor`, and do not edit the artwork. Name the Go variable after
  the Lucide icon, adding `Icon` (`chevron-down.svg` → `chevronDownIcon`).
- Add icons at the Lucide version already in `icons/` unless the change is
  a deliberate upgrade of all of them. The version in use is recorded in
  the header comment of `icons.go`; update it in the same change whenever
  icons are added from a newer Lucide release.

## Testing

- Add or update a `ui.NewTester` test for UI behavior: views run without a
  window, and tests click and type as a user would.
- Cover the state the change touches, including extra frames after
  elements disappear and return, and focus, typing and shortcut behavior
  where relevant.
- Do not depend on the current time, network or a real display in tests.
  `ui.NewTester` has its own clock; advance it with `tt.Advance`.
- For rendering changes, check the light and dark appearances, not just
  the default one.
- The HTML sanitizer's regression corpus is `testdata/html/`
  (`TestHTMLCorpus`). Every `*.html` fixture is rendered both blocked and
  allowed; its `<!-- keep: … -->` and `<!-- drop: … -->` comment lines say
  what must survive and must not, and the test also holds every document
  to the policy. A new dangerous case is a new fixture.
- Mail tests run against fake IMAP/SMTP servers only (`internal/fakemail`)
  — never a real account, not even a test mailbox on a real provider.
  The dev build's seeded account (`seed_e2e.go`) runs on the same
  servers; only test files and e2e-tagged files may import that package,
  and CI checks that release builds leave it out. Development builds
  use a separate data directory from any real install, so a dev run can
  never move, mark or delete a user's real mail.

## Privacy and security

- Never log or transmit credentials, API keys, tokens, message bodies or
  attachments. Nothing leaves the device unless the user asked for it.
- Store secrets in the OS keychain through MyGo (`mygo.Secrets`), not in
  config files or the repository. Never commit keys, tokens or real mail.
- Do not add analytics, crash reporting or update checks that send data
  without the user's consent.
- All text sent to a model provider passes through a single egress point
  in the AI package, so masking, logging and user approval can be
  enforced in one place. AI output is a draft: it is never sent, filed or
  deleted without the user's explicit action.

## Commits

- Use [Conventional Commits](https://www.conventionalcommits.org/):
  `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`. Keep the
  subject lowercase, imperative and under ~72 characters; explain the why
  in the body.
- Keep one logical change per commit, and leave the tree building and the
  tests passing. Do not commit generated binaries or `.mygo/`.
