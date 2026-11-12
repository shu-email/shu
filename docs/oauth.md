# Signing in with Google and Microsoft

Shu can add a Gmail or Outlook account by signing in at Google or Microsoft
in the system browser, with nothing to type but the password at the
provider: no host, port or app password. This page says how that works and
what the maintainers do to turn it on. The only step left is registering
Shu with each provider and building with the client IDs they give.

None of this has been tried against Google or Microsoft yet: the clients are
not registered. Everything is tested against a fake provider and fake IMAP
and SMTP servers on 127.0.0.1 (`mail/oauth*_test.go`, `mail/signin_test.go`).

## How it works

- **No Shu server.** Shu runs the authorization code flow for native apps
  (RFC 8252) with PKCE (RFC 7636): it opens the provider's page in the
  system browser, listens on `http://127.0.0.1:<port>` (a port the system
  picks for that sign-in, never on another address), and the browser
  brings the code straight back to it. The listener takes one answer, the
  one carrying the sign-in's random `state`, shows a page saying to return
  to Shu, and closes. It also closes after five minutes, or when the user
  cancels in the window.
- **Tokens stay on the computer.** The refresh token is kept in the OS
  keychain (`mygo.Secrets`, one entry per account). The access token is
  kept in memory only: the keychain takes no secret larger than 2560 bytes
  (the Windows Credential Manager's limit, which MyGo applies everywhere),
  and Microsoft's two tokens together are often larger. Starting the app
  therefore costs one token refresh per account. No token goes into the
  database, a log or an error message; errors of the token endpoint carry
  its OAuth error code and description, with any secret of the request
  removed.
- **IMAP and SMTP with XOAUTH2.** Gmail and Outlook both take the access
  token by the XOAUTH2 mechanism; Outlook takes nothing else. The servers
  are the provider's presets (`imap.gmail.com`, `outlook.office365.com`,
  …), whatever the address's domain, so Google Workspace and Microsoft 365
  addresses work too.
- **Renewal.** An access token is renewed shortly before it expires, before
  a connection signs in with it. Renewals of one account happen one at a
  time, so concurrent syncs, the watcher and a send renew once, and never
  spend a refresh token twice (Microsoft replaces it at every renewal; the
  new one is stored). A server that refuses a token that had not expired
  gets a renewed one, once.
- **Revoked or expired sign-in.** When the provider refuses to renew
  (`invalid_grant`, or Microsoft's `interaction_required` and
  `consent_required`), the refresh token is deleted and the account needs
  a sign-in: its operations fail with `mail.ErrSignInRequired` without
  touching the network, its watcher stops checking, and its cache, settings
  and cached mail stay as they are. Signing in again uses the account's
  address as the login hint, and is refused if the user signs in as another
  address, so that another mailbox's tokens never serve this account's
  cache.
- **Signing out** deletes the account's tokens from the keychain and keeps
  the account, its cache and settings, needing a sign-in. Removing the
  account deletes them as well. Shu does not call the providers' revocation
  endpoints: users withdraw Shu's access at
  [Google](https://myaccount.google.com/permissions) or
  [Microsoft](https://account.live.com/consent/Manage)
  ([work accounts](https://myapps.microsoft.com)).

## The manual path stays

Adding an account with its IMAP and SMTP servers and a password works as
it always did, and existing accounts keep signing in with their passwords.
It is the fallback: for every other provider, for a build without the
client IDs (the window then offers no Google or Microsoft button), and for
an account whose organization does not allow Shu. Gmail takes an
[app password](https://support.google.com/accounts/answer/185833) there
when the account has 2-Step Verification. Microsoft has been retiring
passwords for IMAP and SMTP, so for Outlook signing in with OAuth is the
way that keeps working.

## Scopes

Shu asks for the least that reads and sends mail over IMAP and SMTP and
names the account:

| Provider | Scopes | Why |
| --- | --- | --- |
| Google | `https://mail.google.com/` | The only scope Gmail's IMAP and SMTP take. The narrower Gmail scopes (`gmail.readonly`, `gmail.send`, `gmail.modify`) are for the Gmail API, which Shu does not use. |
| | `openid`, `email` | The ID token with the account's verified address, so that the user types nothing. No `profile`. |
| Microsoft | `https://outlook.office.com/IMAP.AccessAsUser.All` | Reading, flagging and moving mail over IMAP. |
| | `https://outlook.office.com/SMTP.Send` | Sending over SMTP. |
| | `offline_access` | A refresh token, so the user does not sign in every hour. |
| | `openid`, `email` | The address, as above (a work account without an `email` claim is named by `preferred_username`). |

Google returns a refresh token only to `access_type=offline` and only when
it asked the user for consent, so every Google sign-in sends
`prompt=consent` (with `select_account` for a new account). Shu keeps no
grant across signing out or removing an account, and would otherwise get
no refresh token the second time. A Google sign-in whose user unticked the
mail permission is refused as declined.

**`https://mail.google.com/` is a restricted scope.** Until Google has
verified the app, at most 100 users can sign in, and they are warned that
the app is unverified. Verification of a restricted scope includes a
yearly security assessment by a third party. While the app's publishing
status is "Testing", only the test users listed in the console can sign
in, and their refresh tokens expire after seven days.

## Registering the clients

### Google

1. In the [Google Cloud console](https://console.cloud.google.com/), create
   a project for Shu.
2. Enable the Gmail API for the project, so that its scope can be declared
   on the consent screen. Shu itself only speaks IMAP and SMTP.
3. Configure the OAuth consent screen (Google Auth Platform): audience
   External; the app's name, support address and links; data access: the
   scopes `https://mail.google.com/`, `openid` and
   `https://www.googleapis.com/auth/userinfo.email`.
4. Create an OAuth client of type **Desktop app**. A desktop client takes a
   redirect to the loopback address on any port, so no redirect URI is
   registered. Google issues it a client ID and a client secret; Google
   requires the secret with every token request, even with PKCE, and does
   not treat it as secret for a desktop client (RFC 8252, section 8.5).
5. Add test users, or submit the app for verification (see above).

### Microsoft

1. In the [Microsoft Entra admin center](https://entra.microsoft.com/), go to
   App registrations, New registration: name Shu; supported account types
   **Accounts in any organizational directory and personal Microsoft
   accounts**, which the `common` endpoint Shu uses needs.
2. Register the redirect URI `http://127.0.0.1` for the **mobile and
   desktop applications** platform (a public client), without a port or a
   path: Microsoft ignores the port of a loopback redirect and matches its
   path, and Shu sends `http://127.0.0.1:<port>`. Microsoft's documentation
   says the portal's form refuses an `http` loopback address; if it does,
   add the address to the application manifest, under
   `publicClient.redirectUris` (in the older manifest format, in
   `replyUrlsWithType` with the type `InstalledClient`).
3. Create no client secret: Shu is a public client, and Microsoft refuses a
   secret from one.
4. API permissions, delegated: `IMAP.AccessAsUser.All`, `SMTP.Send`,
   `offline_access`, `openid` and `email`. The portal lists the first two
   under Microsoft Graph, although the tokens are for
   `outlook.office.com`.
5. Consider publisher verification: many organizations let their users
   consent only to apps of a verified publisher.

A Microsoft 365 mailbox signs in only where its organization allows IMAP
and authenticated SMTP for it; Outlook.com accounts allow both.

## Building with the client IDs

The IDs are package variables of `shu/mail`, empty in the repository:
`GoogleClientID`, `GoogleClientSecret` and `MicrosoftClientID`. A build sets
them with the linker's `-X`. With `mygo build`, through `MYGO_LDFLAGS`:

```sh
MYGO_LDFLAGS='-X shu/mail.GoogleClientID=…apps.googleusercontent.com -X shu/mail.GoogleClientSecret=… -X shu/mail.MicrosoftClientID=…' \
  go tool mygo build
```

With plain Go, `go build -ldflags '-X shu/mail.GoogleClientID=… …' .`. In
CI, set `MYGO_LDFLAGS` from the repository's variables or secrets. A
provider is offered only when its client is set (`Service.OAuthConfigured`):
Google needs the ID and the secret, Microsoft the ID.

`mygo dev` passes no extra `-ldflags`. To try signing in during
development, set the variables in an `init` function of a file kept out of
the repository, such as `mail/oauth_local.go` listed in
`.git/info/exclude`.
