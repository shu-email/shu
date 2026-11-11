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
```

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
  a deliberate upgrade of all of them.

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

## Privacy and security

- Never log or transmit credentials, API keys, tokens, message bodies or
  attachments. Nothing leaves the device unless the user asked for it.
- Store secrets in the OS keychain through MyGo (`mygo.Secrets`), not in
  config files or the repository. Never commit keys, tokens or real mail.
- Do not add analytics, crash reporting or update checks that send data
  without the user's consent.

## Commits

- Use [Conventional Commits](https://www.conventionalcommits.org/):
  `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`. Keep the
  subject lowercase, imperative and under ~72 characters; explain the why
  in the body.
- Keep one logical change per commit, and leave the tree building and the
  tests passing. Do not commit generated binaries or `.mygo/`.
