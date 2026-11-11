# Shu

**Shu is a native, AI-first, open-source email client for macOS, Windows, and Linux.**
Bring your own key — your mail talks to your model, with nothing in between.

## Why Shu

Most email clients are either web wrappers, closed AI products that route your mail through someone else's servers, or open-source clients designed a decade ago. Shu is a fresh take:

- **Native, not wrapped** — written in Go with [MyGo](https://mygo.run), with a GPU-rendered UI and no Electron or webview. One small binary, instant startup, at home on all three desktop platforms.
- **AI-native** — triage, thread summaries, and reply drafting are the core of the product, not a bolted-on sidebar.
- **BYOK** — bring your own API key (OpenAI, Anthropic, DeepSeek, or a local model via Ollama). Keys are stored on your machine only.
- **Direct by design** — Shu speaks IMAP/SMTP straight to your mail provider. There are no Shu servers, and your mail never passes through us.
- **Open source** — Apache-2.0, developed in the open.

## Planned for v1

- Multiple accounts over IMAP/SMTP (Gmail, Outlook, iCloud, Fastmail, self-hosted)
- AI inbox triage: what needs you, what can wait, what can be archived
- Thread summaries and AI-drafted replies — always reviewed and sent by you
- Local-first storage with instant full-text search
- A modern, keyboard-first interface

## Status

Shu is in **early development (pre-alpha)** and is not yet usable as a daily driver. The roadmap lives in [Issues](https://github.com/shu-email/shu/issues).

## Development

Shu is a single Go module built with the MyGo toolchain:

```sh
git clone https://github.com/shu-email/shu
cd shu
go tool mygo dev
```

## Contributing

Issues and pull requests are welcome. If you plan a large change, please open an issue first so we can agree on the direction.

## License

[Apache-2.0](LICENSE)
