<h1 align="center">
  <a href="https://github.com/shu-email/shu"><img src="resources/icon.png" alt="Shu app icon" width="128" height="128"></a><br>
  Shu
</h1>

<p align="center">
  A native, AI-first, open-source email client for macOS, Windows and Linux.<br>
  IMAP/SMTP direct. Bring your own model. No servers in between.
</p>

<p align="center">
  <a href="#features"><strong>Features</strong></a> ·
  <a href="#status"><strong>Status</strong></a> ·
  <a href="#build-from-source"><strong>Build</strong></a> ·
  <a href="#contributing"><strong>Contributing</strong></a> ·
  <a href="docs/glossary.md"><strong>Glossary</strong></a> ·
  <a href="docs/decisions/"><strong>Decisions</strong></a>
</p>

<p align="center">
  <a href="https://github.com/shu-email/shu/actions/workflows/ci.yml"><picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/github/ci/shu-email/shu.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;workflow=ci.yml&amp;label=CI&amp;logo=lu:Hammer&amp;valueColor=4ADE80" /><img alt="CI status" src="https://shieldcn.dev/github/ci/shu-email/shu.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;workflow=ci.yml&amp;label=CI&amp;logo=lu:Hammer&amp;valueColor=16A34A" /></picture></a>
  <a href="LICENSE"><picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/LICENSE-APACHE--2.0.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=lu:Scale" /><img alt="License: Apache-2.0" src="https://shieldcn.dev/badge/LICENSE-APACHE--2.0.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=lu:Scale" /></picture></a>
  <picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/GO-1.27.4.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=go&amp;valueColor=22D3EE" /><img alt="Go 1.27.4" src="https://shieldcn.dev/badge/GO-1.27.4.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=go&amp;valueColor=00ADD8" /></picture>
  <picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/MACOS%20%7C%20WINDOWS%20%7C%20LINUX.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=lu:Monitor" /><img alt="macOS, Windows and Linux" src="https://shieldcn.dev/badge/MACOS%20%7C%20WINDOWS%20%7C%20LINUX.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=lu:Monitor" /></picture>
  <br>
  <picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/IMAP%2FSMTP-DIRECT.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=lu:Mail&amp;valueColor=60A5FA" /><img alt="IMAP and SMTP direct" src="https://shieldcn.dev/badge/IMAP%2FSMTP-DIRECT.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=lu:Mail&amp;valueColor=2563EB" /></picture>
  <picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/BYOK-YOUR%20KEY.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=lu:KeyRound&amp;valueColor=A78BFA" /><img alt="Bring your own key" src="https://shieldcn.dev/badge/BYOK-YOUR%20KEY.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=lu:KeyRound&amp;valueColor=7C3AED" /></picture>
  <picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/badge/MYGO-0.9.0.svg?variant=ghost&amp;font=geist-mono&amp;mode=dark&amp;logo=lu:Blocks&amp;valueColor=FBBF24" /><img alt="Built with MyGo 0.9.0" src="https://shieldcn.dev/badge/MYGO-0.9.0.svg?variant=ghost&amp;font=geist-mono&amp;mode=light&amp;logo=lu:Blocks&amp;valueColor=F59E0B" /></picture>
</p>

<p align="center">
  <em>书 (shū) means "letter" — as in 家书, a letter home. In Chinese, 书 and 信 have meant the
  same thing for a thousand years: a message carried to someone who matters.</em>
</p>

Shu is a desktop mail client written in Go with [MyGo](https://mygo.run). It talks IMAP and SMTP
straight to your mail provider, draws its own interface with no webview or Electron, and keeps mail
on your machine. AI features run on a key or a local tool you choose, and every message a model may
read is shown to you before it leaves.

## Why Shu

Most email clients are web wrappers, closed AI products that route your mail through someone else's
servers, or open-source clients designed a decade ago. Shu is a fresh take:

- **Native, not wrapped** — one Go application whose window, views and lists MyGo draws itself. No
  webview, no JavaScript bridge, no telemetry, and no Shu account to create.
- **Direct by design** — Shu speaks IMAP/SMTP to your provider. There are no Shu servers, and your
  mail never passes through us.
- **AI on your terms** — summaries, triage and reply drafts are part of the client, not a bolted-on
  sidebar. Bring your own API key, or use an installed Claude Code, Codex or pi. AI is off until you
  turn it on.
- **Local-first** — the SQLite cache, search index, conversations, contacts, labels, rules and
  drafts all live on this device. Mail already read stays readable offline, and a server that cannot
  be reached never loses an action.

## Features

- **Multiple accounts** — IMAP/SMTP with a password, or Google and Microsoft with OAuth. Shu finds a
  domain's servers from the address and steps out of the way when you would rather type them.
- **Mail that arrives** — an IDLE watcher per account, desktop notifications, a unified inbox, and
  sync progress that says what is happening and why a sync failed.
- **Fast local search** — FTS5 with the trigram tokenizer, so Chinese and partial words work too.
  Search by sender, recipient, subject, body and label, and save the searches you use as smart
  mailboxes.
- **Conversations** — messages are threaded by Message-ID, In-Reply-To, References and Outlook's
  Thread-Index, never by subject. Read a conversation across folders, or switch to single messages.
- **Labels, rules and filing** — server-side labels where the folder keeps them, labels on this
  device where it does not. Rules file, label, star or silence arriving mail. Blocked senders and
  one-click unsubscribe are kept locally until you choose them.
- **Reminders and follow-ups** — set a conversation aside until a time and it leaves the inbox;
  follow up on conversations you sent that nobody answered.
- **Writing without losing work** — drafts are saved to the cache on every change and uploaded when
  the network allows. The outbox keeps what is waiting, sends with a delay, and lets you take a
  message back before it leaves.
- **Safety first** — HTML mail renders in an isolated web view with a strict policy. Remote images
  stay blocked until you allow that sender. Links are checked locally and confirmed before they open.
  Sender authentication and list-unsubscribe options are read once and kept with the message.
- **Your address book, your calendar** — contacts are built from the mail you already have and never
  leave the device. Meeting invitations are read and answered from the reading pane.
- **Bring your setup** — import mbox and eml files, export mail, and back up the configuration.

## Status

> [!NOTE]
> Shu is **pre-alpha**. It is developed in public, but it is not yet a daily driver: release builds,
> signing and the polish a first release needs are still ahead.

The engine and the window are in place. [AGENTS.md](AGENTS.md) describes the architecture and the
rules the code follows; [docs/core-spec.md](docs/core-spec.md) holds the longer specification, and
[docs/decisions](docs/decisions/) records why the design is what it is.

## Build from source

You need Go 1.27.4 or newer. The MyGo toolchain is pinned by the `tool` directive in `go.mod`, so
there is nothing else to install.

```sh
git clone https://github.com/shu-email/shu
cd shu
go tool mygo dev                  # run the app
go tool mygo build                # build a release app
CGO_ENABLED=0 go test ./...       # run the tests
```

The test suite runs without a display, network access or a real clock. Mail tests use local fake
IMAP/SMTP servers only, never a real account.

To check the window by hand, run the dev build with its seeded local account:

```sh
GOFLAGS=-tags=e2e SHU_SEED=1 go tool mygo dev
```

A dev build keeps its data apart from a real install. See
[.agents/skills/shu-desktop-check](.agents/skills/shu-desktop-check/SKILL.md) for driving it from a
shell.

## Contributing

Issues and pull requests are welcome. For a large change, open an issue first so we can agree on the
direction. Keep one logical change per commit, run `gofmt`, `go tool mygo vet .` and the tests before
sending it, and follow the house rules in [AGENTS.md](AGENTS.md) — humans and agents alike.

The [glossary](docs/glossary.md) names each thing the code has, in the sense Shu means it.
[docs/decisions](docs/decisions/) records why a design is what it is, one file per decision.

## Privacy

- Passwords and OAuth refresh tokens live in the OS keychain; access tokens live in memory only.
- The database keeps accounts, folders, message headers and bodies, attachment metadata, drafts and
  the outbox. Attachment bytes go to files keyed by content hash, never into SQLite.
- Nothing is sent to a model until you ask for a feature that needs one. The text that may leave is
  assembled in one place in the AI package.
- No analytics, crash reporting or update checks.

## License

[Apache-2.0](LICENSE)