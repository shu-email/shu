# The window lists ways into the mail, not the account's folders

Status: accepted; the stream's band and its extracted codes are in use,
the folder tree is gone from the sidebar.

## Context

A folder is where a message sits on a server. It is not what the user
wants to know, and for most mail it is not what they want to decide.
A tree of every folder, with unread counts and drag targets, asks the
user to keep being the index: to file mail so that they can find it, and
to remember where they filed it. A new mail client that keeps that tree
is a better-looking copy of the one it replaces, which was the one thing
this project set out not to build.

The reader this design answers asked it plainly: *do we even need to see
folders and labels? Just show me what you think I want, or let me search
for it in natural language.*

## Decision

The sidebar lists the ways into the mail, and no folder of the account:

- **Stream** (`allInboxes`): every account's inbox, with a band above it
  saying what the rows read ask of the user — how many need an answer,
  how many carry a code, how many only tell (`app/stream.go`).
- **Starred**, **Follow Up**, **Reminders**, **Drafts**, **Sent**,
  **Archive**, **Junk**, **Trash**: the marks a user keeps and the
  places mail goes on its own. They are one entry each, not a tree, and
  the account's own folders are not among them.
- **Labels** stay, because a label is the user's word for their mail
  rather than a place the server keeps it.
- **Pinned**: saved searches (`app/smart.go`), the one structure a user
  builds by asking a question and keeping it.

A folder still exists, on the server and in the cache: rules file into
one, a block sends its sender's mail to one, a move from the row menu
names one, and Archive is where archived mail is. The window simply does
not make the user walk the tree to find it. What the user filed before
is not lost and needs no migration: it is still where it is, still found
by search and by a label, and no list shows it any differently.

What a message asks of the user is read on the device, from the preview
the cache already keeps (`mail/extract.go`), by rules rather than by a
model: a verification code, an amount of money, a due date. Mail is
untrusted, a code is exact and short-lived, and nothing about a code
should leave the device to be read. The rules read the preview's first
few hundred characters, which a list fetch now carries
(`previewRunes`, `previewBytes`); a miss means the user opens the
message, and a wrong hit is worse than a miss.

## Consequences

- The default list is still the account's inbox (`ensureFolder`), so a
  user lands where their mail arrives. The stream is one entry away and
  is the way into every account's inbox at once; with one account it
  reads as that inbox, older mail included (`listQuery.readFolder`).
- Searching from the stream covers the inboxes it lists, as the unified
  inbox always did; the scope control widens it to the account or to
  every account.
- Landing on the stream, and ranking its rows rather than listing them
  newest first, wait on a store-side read that orders intent: the list's
  cursor is a date and a row, so reordering rows in the window would
  break paging. Until then a row shows its intent in the band's counts
  and in its own extras, and nothing is hidden or reordered.
- A code, an amount or a date is shown beside the message as a hint, not
  as a fact: it is copied to the clipboard when the user clicks it, the
  row says so for a moment, and nothing acts on it by itself.
- Making, renaming, deleting and hiding a folder are gone with the tree
  (`app/folders.go`): a folder is a server's business, and a user who
  wants a new place for their mail pins a search instead. The rules
  editor and the row menu still name folders a move may go to.
- Fewer things can be seen at a glance: an account's folder tree no
  longer shows at all, so a user who filed for years sees their filing
  only through a label, a search or a pinned view.

## Open

- Ranking the stream, and landing on it, once `mail.Scope` can read a
  page ordered by intent.
- Pinning a language answer as a smart mailbox (the entry exists; asking
  the mail a question does not yet).
- Whether Archive and Junk stay entries of their own once the stream can
  read every folder of an account.