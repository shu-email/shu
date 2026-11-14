package main

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// oauthProviders are the providers an account can sign in with in the
// browser, in the order the add view offers them.
var oauthProviders = []string{mail.OAuthGoogle, mail.OAuthMicrosoft}

// signInFlow is a sign-in under way: an account being added with a
// provider, or one signing in again. Its job waits for the browser until
// ctx ends.
type signInFlow struct {
	provider string
	// account is the account signing in again, "" for one being added,
	// and email its address.
	account, email string
	// url is the provider's page the browser was sent to, "" until the
	// engine has made it; openFailed says the browser did not open.
	url        string
	openFailed bool
	ctx        context.Context
	cancel     context.CancelFunc
}

// signInFailure is how a sign-in failed, in the user's words, and what
// the provider or the error said, which may follow as detail. account is
// the account it was of, "" for one being added.
type signInFailure struct {
	account      string
	text, detail string
}

// offeredProviders are the providers this build can sign in with.
func (a *app) offeredProviders() []string {
	var out []string
	for _, p := range oauthProviders {
		if a.svc.OAuthConfigured(p) {
			out = append(out, p)
		}
	}
	return out
}

// providerName is a provider's name in the window's sentences.
func providerName(provider string) string {
	if provider == mail.OAuthMicrosoft {
		return locales.ProviderMicrosoft()
	}
	return locales.ProviderGoogle()
}

// signInWith is the label of a provider's button.
func signInWith(provider string) string {
	if provider == mail.OAuthMicrosoft {
		return locales.SignInWithMicrosoft()
	}
	return locales.SignInWithGoogle()
}

// addingWithProvider reports whether an account is being added by
// signing in with a provider.
func (a *app) addingWithProvider() bool { return a.signIn != nil && a.signIn.account == "" }

// oauthEditing reports whether the form edits an account that signs in
// with a provider.
func (a *app) oauthEditing() bool { return a.accountForm.editing && a.accountForm.OAuth != "" }

// startSignIn signs in with a provider in the browser: a new account when
// accountID is "", else that account again. One sign-in runs at a time.
// Its job waits as long as the user takes in the browser, up to the
// engine's limit, and Cancel ends it.
func (a *app) startSignIn(provider, accountID string) {
	if a.signIn != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &signInFlow{provider: provider, account: accountID, ctx: ctx, cancel: cancel}
	if acc := a.account(accountID); acc != nil {
		s.email = acc.Email
	}
	a.signIn, a.signInErr = s, nil
	name := strings.TrimSpace(a.signInName)
	prompt := mail.OAuthPrompt{
		Done: locales.SignInDonePage(),
		// The engine calls it from the job, which must not wait for the
		// browser: the page opens in a job of its own.
		Open: func(url string) { a.post(func() { a.signInOpened(s, url) }) },
	}
	a.run(func() {
		defer cancel()
		var (
			added mail.Account
			err   error
		)
		if accountID == "" {
			added, err = a.svc.AddOAuthAccount(ctx, provider, name, prompt)
		} else {
			err = a.svc.SignInAgain(ctx, accountID, prompt)
		}
		a.post(func() { a.signInEnded(s, added, err) })
	})
}

// signInOpened takes the page the engine sends the browser to, and opens
// it.
func (a *app) signInOpened(s *signInFlow, url string) {
	if a.signIn != s {
		return
	}
	s.url = url
	a.openSignInPage()
	a.redrawComposes()
}

// openSignInPage opens the page of the sign-in under way in the browser:
// as the engine makes it, and again when the user asks, as after closing
// its tab.
func (a *app) openSignInPage() {
	s, open := a.signIn, a.openBrowser
	if s == nil || s.url == "" || open == nil {
		return
	}
	url, ctx := s.url, s.ctx
	a.run(func() {
		// A sign-in cancelled meanwhile has no page to come back to.
		if ctx.Err() != nil {
			return
		}
		err := open(url)
		a.post(func() {
			if a.signIn == s {
				s.openFailed = err != nil
				a.redrawComposes()
			}
		})
	})
}

// cancelSignIn stops the sign-in under way. What its job reports then is
// no failure to show.
func (a *app) cancelSignIn() {
	if s := a.signIn; s != nil {
		s.cancel()
		a.signIn = nil
		a.redrawComposes()
	}
}

// signInEnded takes a sign-in's outcome. One that worked counts even if
// it was cancelled meanwhile: the account and its sign-in are there all
// the same. A failure is said where the sign-in began.
func (a *app) signInEnded(s *signInFlow, added mail.Account, err error) {
	current := a.signIn == s
	if current {
		a.signIn = nil
	}
	switch {
	case err == nil && s.account == "":
		a.signInName = ""
		// As a password account after saving: the window opens it.
		a.afterAccountSaved(added.ID)
	case err == nil:
		a.signedInAgain(s.account)
	case current && !errors.Is(err, context.Canceled):
		a.signInErr = signInFailureOf(s, err)
	}
	a.redrawComposes()
}

// signedInAgain follows an account that signed in again: it reaches its
// servers once more, and what waited for that goes on.
func (a *app) signedInAgain(id string) {
	delete(a.needSignIn, id)
	if st, ok := a.watch[id]; ok && st.State == mail.WatchSignIn {
		// The engine has the watcher check at once.
		a.watch[id] = mail.WatchStatus{State: mail.WatchConnecting}
	}
	if a.accountForm.editing && a.accountForm.ID == id {
		a.accountErr, a.accountNote = "", locales.SignedInAgain()
	}
	for _, cs := range a.composes {
		if cs.accountID == id && cs.needsSignIn && !cs.closed {
			// Nothing is sent by itself: the message waits for its Send.
			cs.needsSignIn, cs.err, cs.note = false, "", locales.SignedInSendNow()
		}
	}
	if state := a.draftSyncs[id]; state != nil && state.waiting {
		a.syncDrafts(id)
	}
	if a.onScreen(id) {
		a.refresh()
	}
}

// onScreen reports whether the list shows an account's mail.
func (a *app) onScreen(id string) bool { return id == a.active || a.isUnified() }

// signInFailureOf says why a sign-in failed, in the user's words, with
// the provider's own where it gave any.
func signInFailureOf(s *signInFlow, err error) *signInFailure {
	f := &signInFailure{account: s.account}
	name := providerName(s.provider)
	var (
		refusal *mail.OAuthError
		netErr  net.Error
	)
	switch {
	case errors.Is(err, mail.ErrAddressMismatch):
		f.text = locales.SignInWrongAddress(s.email)
	case errors.Is(err, mail.ErrSignInDenied):
		f.text = locales.SignInDenied(name)
		if errors.As(err, &refusal) {
			f.detail = refusal.Description
		}
	case errors.Is(err, mail.ErrSignInTimeout):
		f.text = locales.SignInTimedOut()
	case errors.Is(err, mail.ErrOAuthNotConfigured):
		f.text = locales.SignInNotConfigured(name)
	case errors.As(err, &refusal):
		f.text, f.detail = locales.SignInProviderError(name), refusal.Description
		if f.detail == "" {
			f.detail = refusal.Code
		} else if refusal.Code != "" {
			f.detail += " (" + refusal.Code + ")"
		}
	case errors.As(err, &netErr):
		f.text = locales.SignInNetwork(name)
	default:
		f.text, f.detail = locales.SignInFailed(), err.Error()
	}
	return f
}

// signInRequired reports whether err is that of an account that has to
// sign in again (mail.ErrSignInRequired), and marks the account: the
// window then says so where it shows the account, and offers the
// sign-in, in place of an error.
func (a *app) signInRequired(accountID string, err error) bool {
	if !errors.Is(err, mail.ErrSignInRequired) {
		return false
	}
	a.markSignIn(accountID)
	return true
}

// markSignIn notes that an account has to sign in again.
func (a *app) markSignIn(accountID string) {
	if a.account(accountID) != nil {
		a.needSignIn[accountID] = true
	}
}

// failureText is what the error line says of a change to mail that
// failed: an account that has to sign in again in the user's words, as
// its banner offers the sign-in, and any other failure as it is.
func failureText(err error) string {
	if errors.Is(err, mail.ErrSignInRequired) {
		return locales.ActionNeedsSignIn()
	}
	return err.Error()
}

// bodyError says why a message's text could not be shown. That of an
// account that has to sign in again waits for the sign-in.
func (a *app) bodyError(accountID string, err error) string {
	if a.signInRequired(accountID, err) {
		return locales.BodyNeedsSignIn()
	}
	return locales.BodyFailed(err.Error())
}

// redrawComposes redraws the compose windows, which show the sign-in of
// their account and are not redrawn with the mail window.
func (a *app) redrawComposes() {
	for _, cs := range a.composes {
		cs.redraw()
	}
}

// providerSignIn is the add view's sign-in with a provider, above the
// manual form: a button for each provider this build can sign in with,
// or the sign-in under way. It reports whether it drew anything: a build
// without a provider shows the manual form alone, as it always did.
func (a *app) providerSignIn(c *ui.Context, t *ui.Theme) bool {
	providers := a.offeredProviders()
	if len(providers) == 0 {
		return false
	}
	if s := a.signIn; s != nil && s.account == "" {
		a.signInWaiting(c, t, s)
		return true
	}
	ui.Column(c).Gap(12).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, locales.OauthTitle()).FontSize(14).Bold()
			ui.Text(c, locales.OauthIntro()).FontSize(12).TextColor(t.TextMuted)
		})
		ui.Column(c).Gap(4).Children(func() {
			caption := ui.Text(c, locales.OauthName()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.TextInput(c, &a.signInName).Placeholder(locales.OauthNamePlaceholder()).LabelledBy(caption).FillWidth()
		})
		ui.Row(c).Gap(10).Wrap().Children(func() {
			for _, p := range providers {
				ui.Button(c, signInWith(p)).Disabled(a.signIn != nil).OnClick(func() { a.startSignIn(p, "") })
			}
		})
		if f := a.signInErr; f != nil && f.account == "" {
			a.failedSignIn(c, t, f)
		}
	})
	return true
}

// signInWaiting is a sign-in waiting for the browser: where to go on,
// the way back to the page, and the way to stop.
func (a *app) signInWaiting(c *ui.Context, t *ui.Theme, s *signInFlow) {
	detail := locales.SignInWaitingDetail(providerName(s.provider))
	if s.email != "" {
		detail = locales.SignInWaitingAs(providerName(s.provider), s.email)
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.SignInWaiting()).FontSize(13).Bold().Role(ui.RoleStatus)
		})
		ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		if s.openFailed {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, locales.SignInBrowserFailed()).FontSize(12).Grow(1).MinWidth(0).MaxLines(3)
			})
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			ui.Button(c, locales.OpenSignInPage()).Disabled(s.url == "").OnClick(a.openSignInPage)
			ui.Button(c, locales.Cancel()).Label(locales.CancelSignIn()).OnClick(a.cancelSignIn)
		})
	})
}

// failedSignIn says how the last sign-in failed.
func (a *app) failedSignIn(c *ui.Context, t *ui.Theme, f *signInFailure) {
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Row(c).Width(14).Shrink(0).Padding(1, 0, 0, 0).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, f.text).FontSize(12).MaxLines(4).Role(ui.RoleStatus)
			if f.detail != "" {
				ui.Text(c, locales.SignInDetail(f.detail)).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			}
		})
	})
}

// signInControls are the way to sign an account in again: its button,
// with how the last try failed, or while it signs in, what that waits for.
func (a *app) signInControls(c *ui.Context, t *ui.Theme, acc mail.Account) {
	if s := a.signIn; s != nil && s.account == acc.ID {
		a.signInWaiting(c, t, s)
		return
	}
	if f := a.signInErr; f != nil && f.account == acc.ID {
		a.failedSignIn(c, t, f)
	}
	ui.Row(c).Children(func() {
		ui.Button(c, locales.SignInAgain()).Label(locales.SignInAgainTo(acc.Label())).
			Disabled(a.signIn != nil).OnClick(func() { a.startSignIn(acc.OAuth, acc.ID) })
	})
}

// accountsOnScreen are the accounts whose mail the list shows: every
// account's in the unified inbox, else the active one's.
func (a *app) accountsOnScreen() []mail.Account {
	if a.isUnified() {
		return a.accounts
	}
	if acc := a.activeAccount(); acc != nil {
		return []mail.Account{*acc}
	}
	return nil
}

// signInBanners say above the list which of the accounts on screen have
// to sign in again, and offer it. Their cached mail stays below them,
// to read.
func (a *app) signInBanners(c *ui.Context, t *ui.Theme) {
	for _, acc := range a.accountsOnScreen() {
		if !a.needSignIn[acc.ID] {
			continue
		}
		bannerColumn(c, "sign-in/"+acc.ID, t.Warning.Alpha(0.10)).Padding(10, 14).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, locales.SignInNeeded(acc.Label())).FontSize(12).Bold().Grow(1).MinWidth(0).MaxLines(2)
			})
			ui.Text(c, locales.SignInNeededDetail()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			a.signInControls(c, t, acc)
		})
	}
}

// oauthAccountFields is the editor of an account that signs in with a
// provider: the provider and the address, which are the provider's, the
// name, which alone changes, and the sign-in, to end or to make again.
func (a *app) oauthAccountFields(c *ui.Context, t *ui.Theme) {
	acc := a.account(a.accountForm.ID)
	if acc == nil {
		return
	}
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, locales.OauthSignsInWith(providerName(acc.OAuth))).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.Text(c, acc.Email).FontSize(15).Bold()
		ui.Text(c, locales.OauthAccountDetail(providerName(acc.OAuth))).FontSize(12).TextColor(t.TextMuted)
	})
	ui.Column(c).Gap(4).Children(func() {
		caption := ui.Text(c, locales.AccountName()).FontSize(11).Bold().TextColor(t.TextMuted)
		ui.TextInput(c, &a.accountForm.Name).Placeholder(acc.Email).LabelledBy(caption).FillWidth()
	})
	a.section(c, locales.SignInSection(), func() {
		if a.needSignIn[acc.ID] {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, locales.AccountNeedsSignIn()).FontSize(12).Grow(1).MinWidth(0).MaxLines(3)
			})
			a.signInControls(c, t, *acc)
			return
		}
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.SignedInAs(acc.Email)).FontSize(12).Grow(1).MinWidth(0)
			ui.Button(c, locales.SignOut()).Disabled(a.signIn != nil).OnClick(func() { a.signOutOpen = true })
		})
	})
}

// signOutAlert asks before an account's sign-in is removed, and says what
// goes and what stays.
func (a *app) signOutAlert(c *ui.Context) {
	acc := a.account(a.accountForm.ID)
	if acc == nil {
		return
	}
	if ui.AlertDialog(c, &a.signOutOpen, locales.SignOutTitle(acc.Label()), locales.SignOutConfirm(),
		locales.Cancel(), locales.SignOutButton()) == 1 {
		a.signOut(acc.ID)
	}
}

// signOut removes an account's sign-in from the keychain. The account,
// its cache and its settings stay, and it needs a sign-in from then on.
func (a *app) signOut(id string) {
	a.accountErr, a.accountNote = "", ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SignOut(ctx, id)
		a.post(func() {
			if err != nil {
				a.accountErr = locales.SignOutFailed(err.Error())
				return
			}
			a.markSignIn(id)
			a.accountNote = locales.SignedOut()
		})
	})
}

// saveOAuthAccount saves the one thing of an OAuth account that changes,
// its name, for which the address stands when it is left empty, as when
// the account was added. Nothing about reaching the account changed, so
// nothing is tested.
func (a *app) saveOAuthAccount() {
	acc := a.account(a.accountForm.ID)
	if acc == nil || a.accountBusy {
		return
	}
	renamed := *acc
	if renamed.Name = strings.TrimSpace(a.accountForm.Name); renamed.Name == "" {
		renamed.Name = renamed.Email
	}
	ctx, cancel := a.ctx()
	a.accountBusy, a.accountErr, a.accountNote = true, "", locales.Saving()
	a.run(func() {
		defer cancel()
		err := a.svc.UpdateAccount(ctx, renamed, "")
		a.post(func() {
			a.accountBusy = false
			if err != nil {
				a.accountNote = ""
				a.accountErr = locales.SaveFailed(err.Error())
				return
			}
			a.accountNote = locales.AccountSaved()
			a.afterAccountSaved(renamed.ID)
		})
	})
}
