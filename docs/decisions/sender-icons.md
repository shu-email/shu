# Sender icons: the person, then the domain, asked once and kept here

Status: accepted, in use since schema v27; keyed since v28.

## Context

A row of mail shows who sent it. Shu drew the initials of the name on a
color picked from it, which says nothing about a sender the user has
never written to, and mail from a product — GitHub, Vercel, a bank —
reads better with the mark the user already knows. Real photographs of
people would need an address book of them, which Shu has not, or a
service keyed on the address, which would tell a third party who writes
to the user.

Some addresses name a person on the host their mail comes from: GitHub
writes `<id>+<account>@users.noreply.github.com`, and GitLab
`<account>@users.noreply.gitlab.com`. That host already knows the
account; asking it for the picture tells no one anything new.

## Decision

The icon of a sender is looked for by key, in order (`mail/avatars.go`),
and what answers is drawn in a list's rows, the reading pane, a
conversation's message headers, the address book and the account switcher
(`app/avatars.go`). A row keeps the height it had: the picture sits inside
the lines of text a row already shows.

1. The person, where the address names one: `profile:<host>/<account>`,
   which is GitHub's `<host from github.com>/<account>.png`, or GitLab's
   API for the account, whose answer is taken only from GitLab's own
   uploads — a Gravatar is a service of its own, and this decision asks it
   nothing.
2. The mark of that host, `domain:<host>`, so a bot or an account without
   a picture still shows GitHub's mark rather than a stranger's.
3. Otherwise the mark of the sender's own domain, `domain:<domain>`: its
   `apple-touch-icon.png` and `favicon.ico` first, and only for a domain
   with neither a public icon service, which learns the domain and nothing
   else.

Only the domain and the account name an address itself carries leave the
device, once, and only while the user leaves `Settings.SenderIcons` on,
which is the default and is the settings' switch in the General tab.

What a lookup found — a PNG, an ICO decoded into one — is kept in the
cache directory by content hash, and what it found in the store
(`sender_icons`, migration v27, keyed since v28). A key is asked once; one
that found nothing is asked again after a month, a source that could not
be reached or said it was busy (a rate limit, a server's 5xx) records
nothing, and a file the sweep took is asked for again. The window decodes
a kept icon once and remembers a bounded number of them.

Two rules keep the icon honest:

- The domains of the mail services themselves — gmail.com, outlook.com,
  qq.com, 163.com and their kind — are never asked: their icon is the
  provider's, not the sender's, and drawing it says nothing about who
  wrote. Those senders keep their initials.
- An address that names no domain, or an IP host, has no icon; a bare
  number names no account to ask for, since a number is an id elsewhere.

## Consequences

- The domain of every sender the user looks at leaves this device once,
  and to the icon service as well for a domain with no icon of its own;
  an account name the address carries leaves it to that account's own
  host. The address, the subject and the message never do. Turning the
  setting off stops it and shows initials again; the files stay until the
  sweep of the cache takes them.
- The icons are a cache, not data: the sweep removes the files no row
  names, a file a sweep took is asked for again, and no icon is kept in
  the database beyond the key that names it and the hash of its file.
- What an icon service returns is not a brand list Shu maintains: there
  is no bundled mark, no trademark in the repository, and a new sender is
  covered without a release.
- A host that changes an API is one rule of `iconProfiles`: the key stays
  in the cache, and the fallback to the host's mark is what shows while
  the rule is wrong. GitLab's rate limit is per address, so a busy minute
  costs a retry later, not a month of initials.
- The cached headers still say nothing of a sender beyond what the server
  sent: no lookup reads them, and an icon is no verdict on a message.
- The lookup is bounded in time, size and redirects, decodes only image
  formats the window can draw, and never runs on the UI thread: the
  initials show while an icon is on its way, and an icon that arrives
  redraws the frame.
- Rejected for now: Gravatar and its kind, which send a stable hash of the
  address itself; BIMI, which needs a DNS record and a certificate and
  returns a brand's logo, not a person's picture; and a photo the user
  sets per contact, which is the local first move for recognizing people
  and a change of its own.