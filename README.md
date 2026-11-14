# Shu

**Shu is a native, AI-first, open-source email client for macOS, Windows, and Linux.**
Bring your own key — your mail talks to your model, with nothing in between.

> 书 (shū) means "letter" — as in 家书, a letter home. In Chinese, 书 and 信 have meant the same thing for a thousand years: a message carried to someone who matters.

## Why Shu

Most email clients are either web wrappers, closed AI products that route your mail through someone else's servers, or open-source clients designed a decade ago. Shu is a fresh take:

- **Native, not wrapped** — written in Go with [MyGo](https://mygo.run), with a GPU-rendered UI drawn by MyGo itself — not a web app in a shell. One small binary, instant startup, at home on all three desktop platforms.
- **AI-native** — triage, thread summaries, and reply drafting are the core of the product, not a bolted-on sidebar.
- **BYOK** — bring your own API key (OpenAI, Anthropic, DeepSeek, or a local model via Ollama). Keys are stored in your OS keychain, on your machine only.
- **Direct by design** — Shu speaks IMAP/SMTP straight to your mail provider. There are no Shu servers, and your mail never passes through us.
- **Open source** — Apache-2.0, developed in the open.

## Roadmap

Shu is in **early development (pre-alpha)** and is not yet usable as a daily driver. The plan, in the order it is being built:

- **Now** — the walking skeleton: one IMAP/SMTP account, a message list, reading mail, local-first storage.
- **Next** — multiple accounts (Gmail, Outlook, iCloud, Fastmail, self-hosted), instant full-text search, and a keyboard-first interface.
- **Later, for v1** — AI inbox triage (what needs you, what can wait, what can be archived), thread summaries, and AI-drafted replies — always reviewed and sent by you.

## Development

You only need Go 1.27.4 or newer. The MyGo toolchain itself is pinned by the `tool` directive in `go.mod`, so there is nothing else to install:

```sh
git clone https://github.com/shu-email/shu
cd shu
go tool mygo dev            # run the app
go tool mygo build          # build a release app
CGO_ENABLED=0 go test ./... # run the tests
```

## Contributing

Issues and pull requests are welcome. If you plan a large change, please open an issue first so we can agree on the direction. The house rules live in [AGENTS.md](AGENTS.md) — humans and agents alike follow them. The [glossary](docs/glossary.md) names the vocabulary, and [docs/decisions](docs/decisions/) records why the design is what it is.

## License

[Apache-2.0](LICENSE)
