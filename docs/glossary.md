# Glossary

Shu's words, in the sense this repository means them. The architecture
is in [AGENTS.md](../AGENTS.md) and the behaviour in
[core-spec.md](core-spec.md); this page names the things those describe,
and says where each lives in the code. It repeats no prose from them.

Each entry also names what not to call the thing, so that code, issues
and reviews stay readable together.

## account

One mail provider's mailbox that the user signed in to, with its
servers, sign-in method and identities. Several accounts are several
entries, even on one provider.
`mail.Account` ([mail/account.go](../mail/account.go)), kept in
`accounts` ([store/accounts.go](../store/accounts.go)).
*Not:* "identity" (an address the account sends from) or "provider" (a
domain's known servers).

## folder

A mailbox on the server: INBOX, Sent, a label folder in Gmail, the
Archive the user picked. A folder belongs to one account and is
identified by its name within it; its `\Inbox`-style roles are
`Folder.SpecialUse`.
`mail.Folder` ([mail/message.go](../mail/message.go)), kept in `folders`
([store/folders.go](../store/folders.go)).
*Not:* "mailbox" in code (that is the IMAP word), or "label" (that is
the user's category, below).

## scope

What a list covers: one folder of one account, every folder of some
accounts, or each account's folder of a special use (the unified inbox
is every account's `\Inbox`). A scope is also what an action on a
conversation is limited to.
`mail.Scope` ([mail/list.go](../mail/list.go)), read by
`Service.MessagePage` and `Service.ConversationPage`.
*Not:* "filter" or "view" (the window's state that names a scope).

## conversation (thread)

One exchange of mail: messages of any folder of an account that share
one of the keys in `mail.ThreadKeys` — Message-ID, In-Reply-To,
References, or the conversation part of an Outlook Thread-Index — never
the subject.
`mail.ThreadID`, `mail.ThreadKeys` ([mail/thread.go](../mail/thread.go)),
assigned and merged by `store/` ([store/threads.go](../store/threads.go)).
The window says "conversation"; the schema's column is `thread_id`.
*Not:* "thread" in user-facing text, and never "topic".

## draft

A message the user is writing, kept in the database as the truth. The
copy in the account's Drafts folder mirrors it for other mail clients
and is brought in step by its own sync.
`mail.Draft`, `Service.SaveDraft` and `Service.SyncDrafts`
([mail/draft.go](../mail/draft.go)), `drafts` and `draft_attachments`
([store/drafts.go](../store/drafts.go)).
*Not:* "outbox" (a message the user sent, waiting to leave) or
"template".

## outbox

The queue of messages the user sent that have not left yet, each an
entry that survives a restart. Sending again is safe because one state
change of the entry decides whether it leaves at all.
`mail.PendingSend` ([mail/message.go](../mail/message.go)),
`Service.Send` and `Service.DeliverScheduled`
([mail/smtp.go](../mail/smtp.go), [mail/schedule.go](../mail/schedule.go)),
`outbox` ([store/outbox.go](../store/outbox.go)).
*Not:* "draft" or "scheduled send" when the message is leaving at once.

## scheduled send

An outbox entry with a time it is due, written when the user asks for a
delay so that the waiting outlives the app. Until it is due, `Unsend`
takes it back and returns its draft; `DeliverScheduled` sends those an
earlier run left waiting.
`StateScheduled`, `Service.Schedule`, `Service.SendScheduled`,
`Service.Unsend`, `Service.DeliverScheduled`
([mail/schedule.go](../mail/schedule.go)).
*Not:* "delayed delivery" (the SMTP feature), and it is not a draft
while it waits to leave: the draft stays a draft.

## arrival

Mail that arrived, as opposed to mail already there: what an incremental
sync fetched above a folder's cursor, in a folder synced before under the
same UIDVALIDITY. A first sync, `FetchOlder`, and a sync after the
server changed UIDVALIDITY bring none, and neither does a message this
app moved into the folder itself.
`mail/arrivals.go`, `Store.ExpectArrivals` and `Store.TakeArrivals`
([mail/store.go](../mail/store.go), [store/rules.go](../store/rules.go)).
*Not:* "new mail" (that is what the watcher announces, which leaves out
what a rule moved or kept quiet).

## watermark

A folder's mark of how far mail has been handed to the window to
announce: `Store.TakeNew` moves it in the same step that returns the
messages above it, so a message is announced at most once. It is not the
sync cursor, which says what is cached.
`watermarks` and `Store.TakeNew`
([store/watermarks.go](../store/watermarks.go)), called by the watcher
([mail/watch.go](../mail/watch.go)).
*Not:* "cursor" (that is where a sync resumes fetching, `mail.Cursor`).

## rule

A user's instruction to sort mail that arrives: an order, conditions all
or any of which a message meets, and actions from a closed set that can
never send, forward or delete.
`mail.Rule`, `mail.ActionKinds`, `Service.CheckRule`
([mail/rules.go](../mail/rules.go)), `rules`
([store/rules.go](../store/rules.go)).
*Not:* "filter" (the window may say "rule" everywhere), and not a
smart mailbox (that is a saved search, not an action on new mail).

## rule activity

One match of a rule, in the log: the message, the rule, and the actions
with what applying them changed. It is pending until the service's
goroutine has applied it, and `UndoActivity` puts back what it changed.
`mail.RuleActivity` ([mail/ruleapply.go](../mail/ruleapply.go)), the log
`rule_log` ([store/rules.go](../store/rules.go)).
*Not:* "audit log", or "rule history" (the log is bounded and its
entries go with their rule).

## label

The user's category of messages. On the server a label is an IMAP
keyword, per mailbox, seen by every client; where a folder keeps no
keyword the label stays on this device by `mail.LabelKey` and follows
the message through moves. `Label.Local` says which one a label is.
`mail.Label`, `mail.LabelKeyword`, `mail.LabelKey`
([mail/labels.go](../mail/labels.go)), `labels` and `message_labels`
([store/labels.go](../store/labels.go)).
*Not:* "tag", "category", or "folder" (a label does not hold mail).

## tombstone

A message's cache row while a move is unconfirmed: `deleted = 1` hides
it from lists and counts without losing it, so an undo or a refused move
can bring it back. Once the server confirms, the source row goes.
`Store.MarkDeleted` ([store/messages.go](../store/messages.go)), used by
[move paths in mail/service.go](../mail/service.go).
*Not:* "deleted message" (the user's Trash is a folder; a tombstone is
this device's bookkeeping) or "soft delete" in user-facing text.

## cache

The SQLite database of mail this device has seen: folders, headers,
bodies, conversations, contacts, labels and the derived state a list
reads. The engine is its only writer. It is not the truth for drafts,
the outbox, contacts, labels or rules, which the server cannot give
again.
`store/` ([store/store.go](../store/store.go)), opened by `store.Open`
and prepared at startup by `Service.PrepareCache`
([mail/cache.go](../mail/cache.go)); its form is `mail.CacheVersion`.
*Not:* "database" in user-facing text, and not the attachment store
below.

## attachment store

The files under `draft-files/` in the user-data directory that hold a
draft's attachments, one per content hash, named by the database's rows;
received mail's attachment bytes live in the cache directory instead.
The files are the user's data: a stored file goes only when no draft and
no outbox entry names it.
`Service.AttachFile`, `Service.RemoveAttachment`
([mail/attach.go](../mail/attach.go)), `draft_files`
([store/drafts.go](../store/drafts.go)).
*Not:* "cache" (a draft's files survive the cache's sweep), and not
"uploads" (nothing has been uploaded until the message is sent).
