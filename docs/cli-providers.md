# Local command-line AI providers

Shu can use a Claude Code or Codex command that is already installed and
signed in on this computer as its AI provider, instead of an API key. The
tool runs on the machine, as the user, and the mail text it is given goes
to its vendor under the vendor's terms, not as a request from Shu.

This page says what Shu does with such a tool, what it never does, and
what the user should expect from the vendor's side. The code is
`ai/cli.go`, `ai/cli_unix.go`, `ai/cli_windows.go` and the panel in
`providers.go`.

## Not an official integration

Shu is not affiliated with, endorsed by or supported by Anthropic or
OpenAI. Running Claude Code or Codex from Shu is the user's own use of a
tool they installed and signed into; it is not an integration those
vendors provide, announce or test against.

A subscription bought for the tool's own app is personal, and its terms may
restrict or forbid use through another application. Shu has not been
granted any such permission and cannot promise that a subscription works
here, keeps working, or is allowed to. A user who is unsure should read
their vendor's terms, or use an API key provider, which Shu supports
directly.

The panel says this next to the tool's settings, so the expectation is set
before the first request rather than after.

## How the tool is run

- **The command the user already has.** Shu finds `claude` or `codex` the
  way a terminal would, through `PATH` as the app sees it, or at the path
  the user set in the panel. A path that is set but does not run is an
  error; Shu never quietly falls back to another command, so a run is
  always the tool the panel names.
- **One prompt, on standard input.** The prompt is never an argument, so it
  does not appear in the process list of other users, and it is not
  written to disk by Shu.
- **A private empty folder.** Each run gets a new temporary directory with
  0700 permissions, as its working directory, removed when the run ends.
  There is no repository, no `.claude` or `.codex` project file and no
  mail in it. A run killed before it could clean up leaves its directory
  behind, so the directory is named after the process that owns it and Shu
  removes the ones whose process is gone as it starts: nothing a request
  wrote outlives a crash.
- **Tools, plugins and hooks off.** The launch line turns off what would
  let the model act rather than answer: `claude -p` with `--tools ""` and
  `--disallowedTools "*"`, no slash commands, no session persistence, one
  turn, and a strict MCP config with no servers; `codex exec --sandbox
  read-only --ephemeral --ignore-user-config` with its apps, plugins,
  shell, browser, multi-agent, hooks and memory features set to false. A
  tool that offers no such switch is not given more trust: it still runs in
  the empty folder with the prompt on standard input only.
- **An environment allowlist.** The tool gets the variables a process
  needs to run (`PATH`, `HOME`, `USER`, `TMPDIR`, the locale and terminal
  ones, and on Windows the profile and shell ones) plus the values the
  user added for it, and nothing else: a variable holding another
  service's key is not passed on. Shu's own variables for the run are
  reserved, so a user value cannot replace them. Values the user added
  live in the keychain, under `ai:<provider>:env:<name>`, and are never
  written to the settings file.
- **Bounded and cancellable.** Output is capped (8 MB) and the run has a
  120-second limit. Cancelling the request, or the limit, kills the tool's
  whole process group, so nothing keeps running in the background.

## Credentials

Shu never reads the tool's credentials, its config files or its session.
It does not copy a token out of them and does not store one: the tool
authenticates itself, from its own keychain entry, exactly as it does in a
terminal.

The one thing Shu asks the tool is its own status: `claude --version`,
`claude auth status --json` (and the Codex equivalents), which report the
installed version and whether the tool is signed in. The account address is
read from that answer when the tool reports one, and is masked in the panel
until the user asks to see it. A prompt or a message never goes into a
status command.

Signing in is the user's own step. The panel offers to copy the command
(`claude auth login`, `codex login`); Shu does not run it, because it needs
a browser and would make the tool's credentials something Shu started.

## Version drift

The launch line uses flags that older builds do not know. Shu therefore
compares the reported version against a minimum it is tested with
(`cliMinimum` in `ai/cli.go`) and, when the tool is older, says so in the
panel with a page to update it, instead of failing with a generic error. A
launch that fails while the installed version is below the minimum returns
`KindCLIOutdated`, with the message "update the tool", so the reason is
never left to a guess. A version that cannot be read is not treated as
outdated: refusing a tool that works would be worse than a plain failure.

## What reaches the vendor

The prompt is the excerpt Shu would send to any provider: the message's
subject, sender, recipients, date and the part of the body the excerpt
allows, assembled at the single egress point in `ai/`. The tool then sends
it to its vendor under that vendor's terms, with the user's own
credentials. Nothing else of the mailbox goes with it, and no key, address
book entry or attachment is included.

## See also

- `docs/core-spec.md`: the product's boundaries, including BYOK and
  local-first AI.
- `docs/oauth.md`: how mail accounts sign in, which is the same keychain
  and no-server rule.
