package mail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// tokensKey is the keychain key of an OAuth account's refresh token. Its
// access token stays in memory: the keychain takes no secret larger than
// 2560 bytes (the Windows Credential Manager's limit, which MyGo keeps
// everywhere), and Microsoft's two tokens together are often more. A
// start of the app costs a refresh per account.
func tokensKey(accountID string) string { return "account:" + accountID + ":oauth" }

// oauthToken is an OAuth account's access token, with the lock that
// makes the account's renewals one at a time.
type oauthToken struct {
	lock    chan struct{}
	access  string
	renewAt time.Time
}

// acquire takes the lock, unless ctx ends first.
func (t *oauthToken) acquire(ctx context.Context) error {
	select {
	case t.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *oauthToken) release() { <-t.lock }

// tokenOf returns an account's access token and lock.
func (s *Service) tokenOf(accountID string) *oauthToken {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	t := s.tokens[accountID]
	if t == nil {
		t = &oauthToken{lock: make(chan struct{}, 1)}
		s.tokens[accountID] = t
	}
	return t
}

// AddOAuthAccount adds the account a user signs in with at a provider, in
// the system browser (see OAuthPrompt). Its address is the one the
// provider names and its servers are the provider's, so that nothing is
// typed; name is the name the user goes by, "" for the address. The
// refresh token goes to the keychain, and nothing of the sign-in to the
// database. It takes as long as the user does, up to five minutes, and
// ends when ctx does. Its errors include ErrSignInDenied,
// ErrSignInTimeout, ErrOAuthNotConfigured and the provider's OAuthError.
func (s *Service) AddOAuthAccount(ctx context.Context, provider, name string, prompt OAuthPrompt) (Account, error) {
	g, address, err := s.authorize(ctx, provider, "", prompt)
	if err != nil {
		return Account{}, err
	}
	preset, ok := ProviderByID(s.oauthProviders[provider].preset)
	if !ok {
		return Account{}, fmt.Errorf("mail: no servers for signing in with %q", provider)
	}
	a := Account{
		ID: NewAccountID(), Name: strings.TrimSpace(name), Email: address, Username: address,
		IMAP: preset.IMAP, SMTP: preset.SMTP, OAuth: provider,
	}
	if a.Name == "" {
		a.Name = address
	}
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	if err := s.store.SaveAccount(ctx, a); err != nil {
		return Account{}, err
	}
	if err := s.keepSignIn(ctx, a.ID, g); err != nil {
		_ = s.store.DeleteAccount(context.WithoutCancel(ctx), a.ID)
		return Account{}, err
	}
	active, err := s.store.ActiveAccount(ctx)
	if err == nil && active == "" {
		_ = s.store.SetActiveAccount(ctx, a.ID)
	}
	return a, nil
}

// SignInAgain signs an OAuth account in again, as AddOAuthAccount signs
// one in, with the account's address for the provider to propose. A
// sign-in as another address is refused (ErrAddressMismatch): that
// mailbox's tokens must never serve this account's cache. Once it is
// done the account has new tokens and no longer needs a sign-in, and its
// watcher checks at once. Its cache and settings are not touched.
func (s *Service) SignInAgain(ctx context.Context, accountID string, prompt OAuthPrompt) error {
	a, err := s.oauthAccount(ctx, accountID)
	if err != nil {
		return err
	}
	g, address, err := s.authorize(ctx, a.OAuth, a.Email, prompt)
	if err != nil {
		return err
	}
	if !strings.EqualFold(address, a.Email) {
		return fmt.Errorf("%w: signed in as %s, for the account %s", ErrAddressMismatch, address, a.Email)
	}
	if err := s.keepSignIn(ctx, a.ID, g); err != nil {
		return err
	}
	s.signedIn(a.ID)
	return nil
}

// keepSignIn puts the tokens of a sign-in in place of an account's, once
// any renewal under way has ended, and unless the account was removed
// while the user was in the browser.
func (s *Service) keepSignIn(ctx context.Context, accountID string, g oauthGrant) error {
	t := s.tokenOf(accountID)
	if err := t.acquire(ctx); err != nil {
		return err
	}
	defer t.release()
	if _, ok, err := s.store.Account(ctx, accountID); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	if err := s.secrets.Set(tokensKey(accountID), []byte(g.refresh)); err != nil {
		return fmt.Errorf("mail: store the sign-in in the keychain: %w", err)
	}
	t.access, t.renewAt = g.access, g.renewAt
	return nil
}

// SignOut removes an OAuth account's tokens from the keychain. The account
// stays, with its cache and settings, and needs a sign-in
// (SignInAgain) before it reaches its servers again; its watcher hangs
// up and waits for that. The provider is not told: the user withdraws
// Shu's access there.
func (s *Service) SignOut(ctx context.Context, accountID string) error {
	a, err := s.oauthAccount(ctx, accountID)
	if err != nil {
		return err
	}
	t := s.tokenOf(a.ID)
	if err := t.acquire(ctx); err != nil {
		return err
	}
	err = s.secrets.Delete(tokensKey(a.ID))
	if err == nil {
		t.access, t.renewAt = "", time.Time{}
	}
	t.release()
	if err != nil {
		return fmt.Errorf("mail: remove the sign-in from the keychain: %w", err)
	}
	s.signedOut(a.ID)
	return nil
}

// NeedsSignIn reports whether an account has to sign in again before it
// reaches its servers, which its syncs and sends then fail with
// (ErrSignInRequired): an OAuth account whose sign-in the provider
// revoked or let expire, or that was signed out. It reads the keychain,
// not the network. An account that signs in with a password never does.
func (s *Service) NeedsSignIn(ctx context.Context, accountID string) (bool, error) {
	a, ok, err := s.store.Account(ctx, accountID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	if a.OAuth == "" {
		return false, nil
	}
	err = s.signInState(a.ID)
	if errors.Is(err, ErrSignInRequired) {
		return true, nil
	}
	return false, err
}

// oauthAccount loads an account that signs in with OAuth.
func (s *Service) oauthAccount(ctx context.Context, id string) (Account, error) {
	a, ok, err := s.store.Account(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if !ok {
		return Account{}, ErrNotFound
	}
	if a.OAuth == "" {
		return Account{}, errors.New("mail: the account signs in with a password")
	}
	return a, nil
}

// signInState is nil for an OAuth account that has its sign-in in the
// keychain, and ErrSignInRequired for one that has to sign in again.
func (s *Service) signInState(accountID string) error {
	_, err := s.secrets.Get(tokensKey(accountID))
	switch {
	case errors.Is(err, ErrNotFound):
		return ErrSignInRequired
	case err != nil:
		return fmt.Errorf("mail: read the sign-in from the keychain: %w", err)
	}
	return nil
}

// accessToken returns an OAuth account's access token for a connection to
// sign in with, renewed first when it is about to expire. refused is a
// token the server refused although it had not expired, as one revoked
// early is: it is renewed all the same, unless another connection did
// that meanwhile. An account's renewals are one at a time, so that the
// syncs, the watcher and a send that find its token expiring together
// renew it once, and never spend one refresh token twice: Microsoft
// replaces it with every renewal. A refresh the provider refuses for good
// leaves the account needing a sign-in: its refresh token goes, and
// nothing asks the provider again until the user signs in.
func (s *Service) accessToken(ctx context.Context, a Account, refused string) (string, error) {
	t := s.tokenOf(a.ID)
	if err := t.acquire(ctx); err != nil {
		return "", err
	}
	defer t.release()
	if t.access != "" && t.access != refused && s.now().Before(t.renewAt) {
		return t.access, nil
	}
	p, ok := s.oauthProviders[a.OAuth]
	if !ok {
		return "", fmt.Errorf("mail: signing in with %q is not supported", a.OAuth)
	}
	refresh, err := s.secrets.Get(tokensKey(a.ID))
	if errors.Is(err, ErrNotFound) {
		t.access = ""
		return "", ErrSignInRequired
	}
	if err != nil {
		return "", fmt.Errorf("mail: read the sign-in from the keychain: %w", err)
	}
	g, err := s.redeem(ctx, p, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {string(refresh)}})
	var refusal *OAuthError
	if errors.As(err, &refusal) && refusal.final() {
		t.access = ""
		if derr := s.secrets.Delete(tokensKey(a.ID)); derr != nil {
			return "", fmt.Errorf("%w (%w), and the keychain kept it: %w", ErrSignInRequired, err, derr)
		}
		s.signedOut(a.ID)
		return "", fmt.Errorf("%w (%w)", ErrSignInRequired, err)
	}
	if err != nil {
		return "", err
	}
	if g.refresh != "" && g.refresh != string(refresh) {
		if err := s.secrets.Set(tokensKey(a.ID), []byte(g.refresh)); err != nil {
			return "", fmt.Errorf("mail: store the sign-in in the keychain: %w", err)
		}
	}
	t.access, t.renewAt = g.access, g.renewAt
	return t.access, nil
}
