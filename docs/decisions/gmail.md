# Gmail's IMAP extension is spoken by a client of its own

Status: accepted; the command layer is implemented. Its consumers are separate changes.

## Context

Gmail is a mail provider Shu must be good at, and its IMAP is not a plain
mailbox server. A message lives in one place and carries labels; the
folders Shu sees are the labels, so a message is in Inbox, in All Mail
and in each label it has at once. Archiving is removing the `\Inbox`
label, deleting is adding `\Trash`, and the categories of the inbox
(Primary, Social, Promotions) are not visible over IMAP at all. All Mail
is a view of everything, not a folder to sync. Gmail also saves its own
copy of anything SMTP delivers and takes fifteen connections at once.

What makes a client feel like Gmail is in `X-GM-EXT-1`, the extension
Gmail advertises beside IMAP4rev1 (Google, *IMAP Extensions*):
`X-GM-LABELS` to read and change labels without moving a message,
`X-GM-MSGID` for an identity that one message keeps in every folder,
`X-GM-THRID` for Gmail's own thread, and `X-GM-RAW` for Gmail's search
syntax, `category:` among it.

Shu pins `github.com/emersion/go-imap/v2 v2.0.0-beta.8`, which has no
`X-GM` string anywhere, and no way around it:

- An untagged FETCH that carries a data item the client does not know
  fails the connection: `imapclient/fetch.go:805` returns
  `unsupported msg-att name`. So a client cannot even read
  `X-GM-LABELS` beside its own typed items, on the same connection.
- STORE is `FLAGS` and nothing else: `imapclient/store.go:32` writes the
  data item name itself. `+X-GM-LABELS` cannot be expressed.
- SEARCH keys are a closed struct (`search.go:38`), so `X-GM-RAW`,
  `X-GM-MSGID` and `X-GM-THRID` cannot be expressed either.
- The codec that would parse and write these responses is
  `internal/imapwire`, which a module outside go-imap cannot import.

Aerc met the same need on go-imap v1, whose client exposed
`Execute(command, response)`: it wrote its own command types for
`X-GM-LABELS`, `X-GM-THRID` and `X-GM-RAW` and ran them through it
(`imap: support the Gmail extension (X-GM-EXT-1)`, and the later
`imap: integrate X-GM-EXT-1 Gmail extension directly`). v2 dropped that
escape hatch: every command is typed, and the client offers no way to
send another.

## Decision

The engine speaks the four commands it needs itself, over a connection
of its own, through `mail/gmailext_client.go` and `mail/gmailext_wire.go`.
It reads and writes the IMAP framing it uses (tagged commands, untagged FETCH and SEARCH, quoted
strings and literals), and nothing else. It is used where Gmail's own
semantics are the point — reading and writing labels, finding a
message's Gmail identity and thread, and a search in Gmail's syntax —
and not inside the sync loop, which keeps reading headers, flags and
bodies through go-imap as it does today.

The extension is offered only to an account whose server advertises
`X-GM-EXT-1`; every other account keeps the plain path, and a Gmail
account without it (an older Workspace, a proxy that strips the
capability) loses the features rather than the mail.

The connection is short-lived, one operation at a time, dialled with the
same account and secret the sync uses. Gmail's fifteen connections are
the reason it is not kept open beside the watcher: a label change or a
search is not worth a permanent slot.

The label values are modified UTF-7 (RFC 3501 §5.1.3) and are owned
here, not by go-imap: `gmailLabelEncode` and `gmailLabelDecode`, with
the RFC's own example and Google's transcript values as tests.

### Consequences

- One more implementation of IMAP framing in the repository, bounded to
  what those four commands need: the cost of not owning the client.
- The codec is ours to keep right: literals in FETCH responses, modified
  UTF-7 in label values, 64-bit decimal IDs. Transcripts from Google's
  documentation are tests, so a change in the shape we expect fails the
  build rather than a mailbox.
- The fake server's Gmail profile has an extension test mode that answers
  `X-GM-*` over local TLS. Its normal mode uses go-imap's server for general
  IMAP commands.
- A Gmail account spends one connection per operation, not one per
  account. If that proves slow on a large mailbox, the next review can
  keep one beside the watcher, at the cost of a slot.
- Nothing in this decision touches sync, the cache or the store: the
  Gmail identity and thread arrive as *additional* reads, and their
  consumers decide what to key on.
- If go-imap v2 gains a raw command or the extension upstream, the codec
  can be dropped without touching its callers: the interface it sits
  behind is the seam.

### Rejected

- **Patching or forking go-imap v2 now.** The module is a beta; a fork
  pinned by commit is a dependency the project carries for as long as
  the patch is unmerged, and AGENTS.md asks that a dependency change be
  its own decision. Revisit if upstream adds the extension or an escape
  hatch, which aerc's v1 experience shows people want.
- **Going back to go-imap v1 for its `Execute`.** It would cost the typed
  API the sync relies on, for one provider.
- **The Gmail API.** It has the labels, threads, categories, filters and
  history-based sync that IMAP lacks, and Mimestream shows how good that
  is. But it is a second mail model beside the engine's, its history
  window is about a week, its push needs a server Shu does not have, and
  `https://mail.google.com/` and `gmail.modify` are restricted scopes
  that need Google's verification for a public app. That trade is not
  worth it while IMAP can carry the same features.
- **Importing go-imap's `internal` packages.** Not possible outside the
  module, and not a decision one can rely on.
