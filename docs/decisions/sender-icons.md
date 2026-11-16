# Sender icons: a sender's domain, asked once, kept on this device

Status: accepted, in use since schema v27.

## Context

A row of mail shows who sent it. Shu drew the initials of the name on a
color picked from it, which says nothing about a sender the user has
never written to, and mail from a product — GitHub, Vercel, a bank —
reads better with the mark the user already knows. Real photographs of
people would need an address book of them, which Shu has not, or a
service keyed on the address, which would tell a third party who writes
to the user.

## Decision

The icon of a sender is the icon of its domain (`mail/avatars.go`). The
reading pane, a conversation's message headers, the address book and the
account switcher draw it in place of the initials (`app/avatars.go`).
Only the domain is looked up, never the address, and only when the user
leaves `Settings.SenderIcons` on, which is the default and is the
settings' switch in the General tab.

A lookup tries the domain's own `apple-touch-icon.png` and `favicon.ico`
first; only a domain with neither is asked at a public icon service,
which learns the domain and nothing else. What a lookup found — a PNG, an
ICO decoded into one — is kept in the cache directory by content hash, and
what it found in the store (`sender_icons`, migration v27). A domain is
asked once; one that had no icon is asked again after a month, and one
that could not be reached records nothing, so going offline costs no
month of initials. The window decodes a kept icon once and remembers a
bounded number of them.

Two rules keep the icon honest:

- The domains of the mail services themselves — gmail.com, outlook.com,
  qq.com, 163.com and their kind — are never asked: their icon is the
  provider's, not the sender's, and drawing it says nothing about who
  wrote. Those senders keep their initials.
- An address that names no domain, or an IP host, has no icon.

## Consequences

- The domain of every sender the user looks at leaves this device once,
  and to the icon service as well for a domain with no icon of its own.
  The address, the subject and the message never do. Turning the setting
  off stops it and shows initials again; the files stay until the sweep
  of the cache takes them.
- The icons are a cache, not data: the sweep removes the files no row
  names, a file a sweep took is asked for again, and no icon is kept in
  the database beyond the hash that names its file.
- What an icon service returns is not a brand list Shu maintains: there
  is no bundled mark, no trademark in the repository, and a new sender is
  covered without a release.
- The cached headers still say nothing of a sender beyond what the server
  sent: no lookup reads them, and an icon is no verdict on a message.
- The lookup is bounded in time, size and redirects, decodes only image
  formats the window can draw, and never runs on the UI thread: the
  initials show while an icon is on its way, and an icon that arrives
  redraws the frame.