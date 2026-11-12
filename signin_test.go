package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// googleAccount is an account that signs in with Google, at Gmail's
// servers, as the engine adds one.
func googleAccount(id, name, email string) mail.Account {
	gmail, _ := mail.ProviderByID("gmail")
	return mail.Account{
		ID: id, Name: name, Email: email, Username: email,
		IMAP: gmail.IMAP, SMTP: gmail.SMTP, OAuth: mail.OAuthGoogle,
	}
}

// browserLog is what the window asked the system browser to open, and
// what opening answers.
type browserLog struct {
	urls []string
	err  error
}

// withBrowser has the window open its pages into a log.
func withBrowser(a *app) *browserLog {
	b := &browserLog{}
	a.openBrowser = func(url string) error {
		b.urls = append(b.urls, url)
		return b.err
	}
	return b
}

// firstRunApp starts the window on a service without accounts that can
// sign in with the providers given.
func firstRunApp(t *testing.T, providers ...string) (*app, *fakeService, *browserLog) {
	t.Helper()
	fs := newFakeService()
	for _, p := range providers {
		fs.oauth[p] = true
	}
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	b := withBrowser(a)
	a.start()
	return a, fs, b
}

// googleApp starts the window on Work, which signs in with Google and
// has the sample mail, and Home, which signs in with a password.
func googleApp(t *testing.T, fs *fakeService) (*app, *browserLog) {
	t.Helper()
	fs.oauth[mail.OAuthGoogle] = true
	fs.addAccount(googleAccount("a1", "Work", "ann@gmail.com"), "", testFolders(), testMessages("a1"))
	fs.addAccount(testAccount("a2", "Home", "home@example.com"), "secret", testFolders(), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Welcome home", Seen: true,
		From: mail.Address{Name: "Sam", Email: "sam@example.com"}, Date: time.Now().Add(-48 * time.Hour),
	}})
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	b := withBrowser(a)
	a.start()
	return a, b
}

// runQueued runs the jobs queued so far and those they queue, as the
// goroutines they would be, while the job that queued them waits on.
func (q *jobQueue) runQueued() {
	for len(q.jobs) > 0 {
		job := q.jobs[0]
		q.jobs = q.jobs[1:]
		job()
	}
}

// rawSignInTexts are what the engine's errors and the fake's say, which
// the window must put in its own words.
var rawSignInTexts = []string{"has to sign in again", "invalid_grant", "expired or revoked", "oauth:", "mail:"}

// noRawTexts fails the test for any of the engine's own words on screen.
func noRawTexts(t *testing.T, tt *ui.Tester, what string) {
	t.Helper()
	for _, raw := range rawSignInTexts {
		if tt.HasText(raw) {
			t.Errorf("%s: the window shows %q: %q", what, raw, tt.Texts())
		}
	}
}

// The add view offers a button for each provider the build can sign in
// with, above the manual form, which is there as it always was. A build
// with none, as the repository's, shows the manual form alone. So does
// the editor of a password account.
func TestSignInButtonsOnlyForConfiguredProviders(t *testing.T) {
	google, microsoft := locales.SignInWithGoogle(), locales.SignInWithMicrosoft()
	manual := []string{locales.Provider(), locales.AccountName(), locales.Email(), locales.Password(),
		locales.PasswordAppNote(), locales.ImapSection(), locales.SmtpSection(), locales.TestAndSave()}
	for _, tc := range []struct {
		providers []string
		want, not []string
	}{
		{nil, nil, []string{google, microsoft, locales.ManualSetup(), locales.OauthTitle(), locales.OauthName()}},
		{[]string{mail.OAuthGoogle}, []string{google, locales.ManualSetup()}, []string{microsoft}},
		{[]string{mail.OAuthMicrosoft}, []string{microsoft, locales.ManualSetup()}, []string{google}},
		{[]string{mail.OAuthGoogle, mail.OAuthMicrosoft}, []string{google, microsoft, locales.ManualSetup()}, nil},
	} {
		name := fmt.Sprint(tc.providers)
		look := func(where string, tt *ui.Tester) {
			t.Helper()
			for _, text := range append(append([]string(nil), tc.want...), manual...) {
				if !tt.HasText(text) {
					t.Errorf("%s, %s: lacks %q: %q", name, where, text, tt.Texts())
				}
			}
			for _, text := range tc.not {
				if tt.HasText(text) {
					t.Errorf("%s, %s: shows %q", name, where, text)
				}
			}
			// The providers come first, and the manual form is the way
			// after them.
			if len(tc.providers) > 0 {
				button, _ := tt.Find(signInWith(tc.providers[0]))
				heading, _ := tt.Find(locales.ManualSetup())
				form, _ := tt.Find(locales.Password())
				if !(button.Y < heading.Y && heading.Y < form.Y) {
					t.Errorf("%s, %s: button at %v, heading at %v, form at %v", name, where, button, heading, form)
				}
			}
		}

		a, _, _ := firstRunApp(t, tc.providers...)
		look("first run", ui.NewTester(a.view, 1220, 1400))

		fs := newFakeService()
		for _, p := range tc.providers {
			fs.oauth[p] = true
		}
		a, _ = startSample(t, fs)
		a.openAccountForm(nil)
		tt := ui.NewTester(a.view, 1220, 900)
		// The form scrolls in the panel: the manual part is below.
		look("over the mail window", tt)

		a.openAccountForm(a.activeAccount())
		tt.Frame()
		for _, text := range []string{google, microsoft, locales.SignOut(), locales.ManualSetup(), locales.SignInSection()} {
			if tt.HasText(text) {
				t.Errorf("%s: the editor of a password account shows %q", name, text)
			}
		}
		for _, text := range []string{locales.Password(), locales.ImapSection(), locales.TestAndSave()} {
			if !tt.HasText(text) {
				t.Errorf("%s: the editor of a password account lacks %q: %q", name, text, tt.Texts())
			}
		}
	}
}

// A provider's button signs in, and the account it adds opens as a
// password account does after saving, by the name typed or else its
// address. Nothing else is typed.
func TestSignInAddsAndOpensTheAccount(t *testing.T) {
	a, fs, browser := firstRunApp(t, mail.OAuthGoogle, mail.OAuthMicrosoft)
	fs.newInbox = []mail.Summary{{
		UID: 7, Folder: "INBOX", Subject: "Your account is ready", Preview: "Welcome",
		From: mail.Address{Name: "Google", Email: "no-reply@accounts.google.com"}, Date: time.Now().Add(-time.Hour),
	}}
	tt := ui.NewTester(a.view, 1220, 900)
	typeIn(t, tt, locales.OauthName(), "Ann Lee")
	click(t, tt, locales.SignInWithGoogle())

	if len(fs.accounts) != 1 {
		t.Fatalf("accounts = %+v", fs.accounts)
	}
	added := fs.accounts[0]
	if added.OAuth != mail.OAuthGoogle || added.Name != "Ann Lee" || added.Email != "ann@gmail.com" {
		t.Errorf("added = %+v", added)
	}
	if a.showAccounts || a.accountOpen || a.active != added.ID || a.signIn != nil {
		t.Errorf("after the sign-in: accounts view %v, form %v, active %q, sign-in %+v", a.showAccounts, a.accountOpen, a.active, a.signIn)
	}
	for _, text := range []string{"Your account is ready", "Ann Lee", "ann@gmail.com", locales.Inbox()} {
		if !tt.HasText(text) {
			t.Errorf("the new account did not open: lacks %q: %q", text, tt.Texts())
		}
	}
	if want := []string{signInPage(mail.OAuthGoogle)}; !slices.Equal(browser.urls, want) {
		t.Errorf("opened %q, want %q", browser.urls, want)
	}
	if len(fs.prompts) != 1 || fs.prompts[0].Done != locales.SignInDonePage() {
		t.Errorf("prompts = %+v", fs.prompts)
	}
	if fs.active != added.ID {
		t.Errorf("the active account stored is %q", fs.active)
	}

	// Another, over the mail window, without a name: it goes by its
	// address.
	fs.newAddress, fs.newInbox = "ann@outlook.com", nil
	a.openAccountForm(nil)
	tt.Frame()
	if a.signInName != "" {
		t.Errorf("the name typed for the last account stays: %q", a.signInName)
	}
	click(t, tt, locales.SignInWithMicrosoft())
	if len(fs.accounts) != 2 {
		t.Fatalf("accounts = %+v", fs.accounts)
	}
	second := fs.accounts[1]
	if second.OAuth != mail.OAuthMicrosoft || second.Name != "ann@outlook.com" || a.active != second.ID || a.accountOpen {
		t.Errorf("second = %+v, active %q, form open %v", second, a.active, a.accountOpen)
	}
	if want := []string{"add google", "add microsoft"}; !slices.Equal(fs.signIns, want) {
		t.Errorf("sign-ins = %v, want %v", fs.signIns, want)
	}
}

// While the browser has the sign-in, the window says to go on there,
// opens the page again if asked, and stops with Cancel or Escape, which
// end the engine's sign-in and leave nothing to say.
func TestSignInWaitsReopensAndCancels(t *testing.T) {
	a, fs, browser := firstRunApp(t, mail.OAuthGoogle)
	tt := ui.NewTester(a.view, 1220, 900)
	page := signInPage(mail.OAuthGoogle)
	for _, stop := range []string{"Cancel", "Escape"} {
		browser.urls = nil
		q := queueJobs(a)
		click(t, tt, locales.SignInWithGoogle())
		// The engine has not made the page yet: there is nothing to open
		// again.
		for _, text := range []string{locales.SignInWaiting(), locales.SignInWaitingDetail("Google"), locales.OpenSignInPage()} {
			if !tt.HasText(text) {
				t.Errorf("%s: waiting, the window lacks %q: %q", stop, text, tt.Texts())
			}
		}
		if again, _ := tt.AccessByRole(ui.RoleButton, locales.OpenSignInPage()); !again.Disabled {
			t.Errorf("%s: the page can be opened again before there is one", stop)
		}
		// The manual form gives way to the sign-in.
		for _, text := range []string{locales.Password(), locales.TestAndSave(), locales.SignInWithGoogle()} {
			if tt.HasText(text) {
				t.Errorf("%s: waiting, the window shows %q", stop, text)
			}
		}
		var problems []string
		fs.onSignIn = func(ctx context.Context) error {
			// The page opens in a job of its own.
			q.runQueued()
			tt.Frame()
			if !slices.Equal(browser.urls, []string{page}) {
				problems = append(problems, fmt.Sprintf("opened %q", browser.urls))
			}
			if err := tt.Click(locales.OpenSignInPage()); err != nil {
				problems = append(problems, "no way to open the page again: "+err.Error())
			}
			q.runQueued()
			if !slices.Equal(browser.urls, []string{page, page}) {
				problems = append(problems, fmt.Sprintf("after Open the page again, opened %q", browser.urls))
			}
			if stop == "Cancel" {
				if err := tt.Click(locales.CancelSignIn()); err != nil {
					problems = append(problems, "no Cancel: "+err.Error())
				}
			} else {
				tt.Key(0, ui.KeyEscape)
			}
			if ctx.Err() == nil {
				problems = append(problems, stop+" did not end the sign-in")
			}
			return ctx.Err()
		}
		q.drain(a)
		tt.Frame()
		for _, p := range problems {
			t.Errorf("%s: %s", stop, p)
		}
		if a.signIn != nil || a.signInErr != nil || len(fs.accounts) != 0 {
			t.Errorf("%s: sign-in %+v, failure %+v, accounts %+v", stop, a.signIn, a.signInErr, fs.accounts)
		}
		for _, text := range []string{locales.SignInWithGoogle(), locales.Password(), locales.TestAndSave()} {
			if !tt.HasText(text) {
				t.Errorf("%s: after the cancel the window lacks %q: %q", stop, text, tt.Texts())
			}
		}
		for _, text := range []string{locales.SignInWaiting(), locales.SignInFailed()} {
			if tt.HasText(text) {
				t.Errorf("%s: after the cancel the window shows %q", stop, text)
			}
		}
	}

	// Over the mail window, Escape stops the sign-in and leaves the form
	// open for another way.
	fs = newFakeService()
	fs.oauth[mail.OAuthGoogle] = true
	a, _ = startSample(t, fs)
	withBrowser(a)
	a.openAccountForm(nil)
	tt = ui.NewTester(a.view, 1220, 900)
	q := queueJobs(a)
	click(t, tt, locales.SignInWithGoogle())
	cancelled := false
	fs.onSignIn = func(ctx context.Context) error {
		q.runQueued()
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		cancelled = ctx.Err() != nil
		return ctx.Err()
	}
	q.drain(a)
	tt.Frame()
	if !cancelled || !a.accountOpen || a.signIn != nil {
		t.Errorf("Escape over the mail window: cancelled %v, form open %v, sign-in %+v", cancelled, a.accountOpen, a.signIn)
	}
	if !tt.HasText(locales.AddAccount()) || !tt.HasText(locales.SignInWithGoogle()) {
		t.Errorf("the form after Escape: %q", tt.Texts())
	}
	// Closing the form stops a sign-in too.
	q = queueJobs(a)
	click(t, tt, locales.SignInWithGoogle())
	fs.onSignIn = func(ctx context.Context) error {
		a.closeAccountForm()
		cancelled = ctx.Err() != nil
		return ctx.Err()
	}
	cancelled = false
	q.drain(a)
	if !cancelled || a.accountOpen || a.signIn != nil {
		t.Errorf("closing the form: cancelled %v, form open %v", cancelled, a.accountOpen)
	}
}

// A sign-in the engine finished just as the user cancelled it counts: the
// account and its sign-in are there all the same, so the window has them.
func TestSignInDoneAsItIsCancelled(t *testing.T) {
	a, fs, _ := firstRunApp(t, mail.OAuthGoogle)
	tt := ui.NewTester(a.view, 1220, 900)
	q := queueJobs(a)
	click(t, tt, locales.SignInWithGoogle())
	fs.onSignIn = func(ctx context.Context) error {
		q.runQueued()
		tt.Frame()
		click(t, tt, locales.CancelSignIn())
		return nil
	}
	q.drain(a)
	tt.Frame()
	if len(fs.accounts) != 1 || a.account(fs.accounts[0].ID) == nil || a.active != fs.accounts[0].ID {
		t.Fatalf("an account added as the sign-in was cancelled: engine %+v, window %+v", fs.accounts, a.accounts)
	}
	if a.signInErr != nil || a.signIn != nil {
		t.Errorf("failure %+v, sign-in %+v", a.signInErr, a.signIn)
	}

	// So does signing in again.
	fs = newFakeService()
	fs.needSignIn["a1"] = true
	a, _ = googleApp(t, fs)
	tt = ui.NewTester(a.view, 1220, 800)
	q = queueJobs(a)
	click(t, tt, locales.SignInAgainTo("Work"))
	fs.onSignIn = func(ctx context.Context) error {
		q.runQueued()
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		return nil
	}
	q.drain(a)
	tt.Frame()
	if a.needSignIn["a1"] || tt.HasText(locales.SignInNeeded("Work")) {
		t.Errorf("signed in again as it was cancelled: needs %v: %q", a.needSignIn, tt.Texts())
	}
}

// A sign-in that fails says why in the user's words, with the provider's
// own words after them where it gave any, and the button tries again.
func TestSignInFailuresSayWhy(t *testing.T) {
	network := fmt.Errorf("oauth: reach the token endpoint: %w", &url.Error{
		Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: errors.New("dial tcp: lookup oauth2.googleapis.com: no such host"),
	})
	for _, tc := range []struct {
		name         string
		err          error
		text, detail string
		hidden       []string
	}{
		{"denied", &mail.OAuthError{Code: "access_denied", Description: "The user declined."},
			locales.SignInDenied("Google"), "The user declined.", []string{"access_denied"}},
		{"mail not granted", fmt.Errorf("%w: the mail was not granted", mail.ErrSignInDenied),
			locales.SignInDenied("Google"), "", []string{"not granted"}},
		{"timeout", mail.ErrSignInTimeout, locales.SignInTimedOut(), "", []string{"not finished in time"}},
		{"provider", &mail.OAuthError{Code: "invalid_client", Description: "The OAuth client was not found."},
			locales.SignInProviderError("Google"), "The OAuth client was not found. (invalid_client)", nil},
		{"provider without words", &mail.OAuthError{Code: "temporarily_unavailable"},
			locales.SignInProviderError("Google"), "temporarily_unavailable", nil},
		{"network", network, locales.SignInNetwork("Google"), "", []string{"no such host", "oauth2.googleapis.com"}},
		{"other", errors.New("oauth: the provider gave no refresh token"),
			locales.SignInFailed(), "oauth: the provider gave no refresh token", nil},
	} {
		a, fs, _ := firstRunApp(t, mail.OAuthGoogle)
		tt := ui.NewTester(a.view, 1220, 900)
		fs.onSignIn = func(context.Context) error { return tc.err }
		click(t, tt, locales.SignInWithGoogle())
		if !tt.HasText(tc.text) {
			t.Errorf("%s: the window does not say %q: %q", tc.name, tc.text, tt.Texts())
		}
		if detail := locales.SignInDetail(tc.detail); tc.detail != "" && !tt.HasText(detail) {
			t.Errorf("%s: the window does not add %q: %q", tc.name, detail, tt.Texts())
		}
		if tc.detail == "" && tt.HasText(locales.SignInDetail("")) {
			t.Errorf("%s: details without any: %q", tc.name, tt.Texts())
		}
		for _, raw := range tc.hidden {
			if tt.HasText(raw) {
				t.Errorf("%s: the window shows %q: %q", tc.name, raw, tt.Texts())
			}
		}
		if len(fs.accounts) != 0 || a.signIn != nil {
			t.Errorf("%s: accounts %+v, sign-in %+v", tc.name, fs.accounts, a.signIn)
		}
		// The way to try again is the button, which now works.
		fs.onSignIn = nil
		click(t, tt, locales.SignInWithGoogle())
		if len(fs.accounts) != 1 || tt.HasText(tc.text) {
			t.Errorf("%s: trying again: accounts %+v: %q", tc.name, fs.accounts, tt.Texts())
		}
	}
}

// The browser not opening is said, and the page can be opened again.
func TestSignInSaysWhenTheBrowserDoesNotOpen(t *testing.T) {
	a, fs, browser := firstRunApp(t, mail.OAuthGoogle)
	browser.err = errors.New("no default browser")
	tt := ui.NewTester(a.view, 1220, 900)
	q := queueJobs(a)
	click(t, tt, locales.SignInWithGoogle())
	var problems []string
	fs.onSignIn = func(ctx context.Context) error {
		q.runQueued()
		tt.Frame()
		if !tt.HasText(locales.SignInBrowserFailed()) || tt.HasText("no default browser") {
			problems = append(problems, fmt.Sprintf("a browser that did not open: %q", tt.Texts()))
		}
		browser.err = nil
		click(t, tt, locales.OpenSignInPage())
		q.runQueued()
		tt.Frame()
		if tt.HasText(locales.SignInBrowserFailed()) || len(browser.urls) != 2 {
			problems = append(problems, fmt.Sprintf("opened again: %q, %q", browser.urls, tt.Texts()))
		}
		return nil
	}
	q.drain(a)
	for _, p := range problems {
		t.Error(p)
	}
	if len(fs.accounts) != 1 {
		t.Errorf("accounts = %+v", fs.accounts)
	}
}

// The editor of an account that signs in with a provider shows the
// provider and the address, changes the name alone, and signs out after
// a question that says what goes and what stays. Signed out, it offers
// to sign in again, which brings the account back.
func TestOAuthAccountEditor(t *testing.T) {
	fs := newFakeService()
	a, browser := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.ManageAccounts()); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{locales.EditAccount(), locales.OauthSignsInWith("Google"), "ann@gmail.com",
		locales.OauthAccountDetail("Google"), locales.AccountName(), locales.SignedInAs("ann@gmail.com"),
		locales.SignOut(), locales.Save(), locales.DeleteAccount()} {
		if !tt.HasText(text) {
			t.Errorf("the editor lacks %q: %q", text, tt.Texts())
		}
	}
	for _, text := range []string{locales.Password(), locales.PasswordKeep(), locales.Host(), locales.Port(),
		locales.Security(), locales.ImapSection(), locales.SmtpSection(), locales.Provider(), locales.TestAndSave()} {
		if tt.HasText(text) {
			t.Errorf("the editor of an OAuth account shows %q", text)
		}
	}

	// The name, and nothing else: the fake refuses any other change.
	typeIn(t, tt, locales.AccountName(), " at Acme")
	click(t, tt, locales.Save())
	if want := googleAccount("a1", "Work at Acme", "ann@gmail.com"); fs.accounts[0] != want {
		t.Errorf("saved = %+v, want %+v", fs.accounts[0], want)
	}
	if a.accountErr != "" || a.accountOpen {
		t.Errorf("after saving: error %q, still open %v", a.accountErr, a.accountOpen)
	}
	if !tt.HasText("Work at Acme") {
		t.Errorf("the new name does not show: %q", tt.Texts())
	}

	// Signing out asks first, and says what goes and what stays.
	a.openAccountForm(a.account("a1"))
	tt.Frame()
	click(t, tt, locales.SignOut())
	for _, text := range []string{locales.SignOutTitle("Work at Acme"), locales.SignOutConfirm(), locales.SignOutButton()} {
		if !tt.HasText(text) {
			t.Errorf("the question lacks %q: %q", text, tt.Texts())
		}
	}
	for _, words := range []string{"keychain", "mail on this device", "stay", "sign in again"} {
		if !strings.Contains(locales.SignOutConfirm(), words) {
			t.Errorf("the question does not say %q: %q", words, locales.SignOutConfirm())
		}
	}
	tt.Key(0, ui.KeyEscape)
	if len(fs.signOuts) != 0 || a.signOutOpen || !a.accountOpen {
		t.Fatalf("Escape: signed out %v, asking %v, editor open %v", fs.signOuts, a.signOutOpen, a.accountOpen)
	}
	click(t, tt, locales.SignOut())
	click(t, tt, locales.SignOutButton())
	if !slices.Equal(fs.signOuts, []string{"a1"}) || !a.needSignIn["a1"] {
		t.Fatalf("signed out %v, needs sign-in %v", fs.signOuts, a.needSignIn)
	}
	for _, text := range []string{locales.SignedOut(), locales.AccountNeedsSignIn(), locales.SignInAgain()} {
		if !tt.HasText(text) {
			t.Errorf("signed out, the editor lacks %q: %q", text, tt.Texts())
		}
	}
	if tt.HasText(locales.SignOut()) || tt.HasText(locales.SignedInAs("ann@gmail.com")) {
		t.Errorf("signed out, the editor still offers to sign out: %q", tt.Texts())
	}
	// The account and its cached mail stay.
	if len(fs.accounts) != 2 || len(fs.messages[listKey("a1", "INBOX")]) != 3 {
		t.Errorf("after signing out: accounts %+v", fs.accounts)
	}

	// Signing in again from the editor.
	click(t, tt, locales.SignInAgainTo("Work at Acme"))
	if a.needSignIn["a1"] || !slices.Equal(fs.signIns, []string{"again a1"}) {
		t.Fatalf("signed in again: needs %v, sign-ins %v", a.needSignIn, fs.signIns)
	}
	if !tt.HasText(locales.SignedInAgain()) || !tt.HasText(locales.SignedInAs("ann@gmail.com")) || !tt.HasText(locales.SignOut()) {
		t.Errorf("signed in again, the editor says: %q", tt.Texts())
	}
	if len(browser.urls) != 1 {
		t.Errorf("opened %q", browser.urls)
	}

	// A sign-out the keychain refuses says so, and changes nothing.
	fs.signOutErr = errors.New("keychain locked")
	click(t, tt, locales.SignOut())
	click(t, tt, locales.SignOutButton())
	if !tt.HasText(locales.SignOutFailed("keychain locked")) || a.needSignIn["a1"] {
		t.Errorf("a failed sign-out: %q, needs %v", tt.Texts(), a.needSignIn)
	}
}

// An account that has to sign in again says so from the start, before
// any sync, above its cached mail, which stays readable; the account
// menu says it of an account not on screen.
func TestNeedsSignInAtStartup(t *testing.T) {
	fs := newFakeService()
	fs.needSignIn["a1"] = true
	fs.oauth[mail.OAuthGoogle] = true
	fs.addAccount(googleAccount("a1", "Work", "ann@gmail.com"), "", testFolders(), testMessages("a1"))
	fs.addAccount(testAccount("a2", "Home", "home@example.com"), "secret", testFolders(), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Welcome home", Seen: true, Date: time.Now().Add(-48 * time.Hour),
	}})
	m := testMessages("a1")[1]
	m.Account = "a1"
	fs.bodies[bodyKey("a1", "INBOX", 2)] = mail.Message{Summary: m, BodyText: "The cached text of the pull request.", BodyCached: true}
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	q := queueJobs(a)
	a.start()
	// The sweep and the accounts, and none of the syncs they ask for.
	first := q.jobs[:2]
	q.jobs = q.jobs[2:]
	for _, job := range first {
		job()
	}
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.SignInNeeded("Work")) || len(fs.syncCalls) != 0 {
		t.Fatalf("before any sync: syncs %v: %q", fs.syncCalls, tt.Texts())
	}
	q.drain(a)
	tt.Frame()
	for _, text := range []string{locales.SignInNeeded("Work"), locales.SignInNeededDetail(), locales.SignInAgain(),
		"Design review — Thursday at 10", "Re: Contract for Q4 campaign"} {
		if !tt.HasText(text) {
			t.Errorf("the window lacks %q: %q", text, tt.Texts())
		}
	}
	if a.err != "" {
		t.Errorf("an error line: %q", a.err)
	}
	noRawTexts(t, tt, "at the start")

	// What is cached reads; what is not says it waits for the sign-in.
	click(t, tt, "[shu-email/shu] Pull request #128")
	if !tt.HasText("The cached text of the pull request.") {
		t.Errorf("a cached message cannot be read: %q", tt.Texts())
	}
	click(t, tt, "Re: Contract for Q4 campaign")
	if !tt.HasText(locales.BodyNeedsSignIn()) {
		t.Errorf("a message not cached: %q", tt.Texts())
	}
	noRawTexts(t, tt, "a message not cached")

	// On another account, the banner goes, and the menu says it of Work.
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem("Home · home@example.com"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText(locales.SignInNeeded("Work")) || !tt.HasText("Welcome home") {
		t.Errorf("on Home: %q", tt.Texts())
	}
	click(t, tt, locales.AccountsMenu())
	if !menuSays(tt, "Work · ann@gmail.com", locales.MenuNeedsSignIn()) {
		t.Errorf("the menu does not say Work needs a sign-in: %+v", tt.Menu())
	}
	if menuSays(tt, "Home · home@example.com", locales.MenuNeedsSignIn()) {
		t.Errorf("the menu says Home needs a sign-in")
	}
	tt.CloseMenu()
	// The keychain was read for the account that signs in with Google,
	// once, and not for the one with a password.
	if !slices.Equal(fs.signInChecks, []string{"a1"}) {
		t.Errorf("asked whether accounts need a sign-in: %v", fs.signInChecks)
	}
}

// menuSays reports whether the open menu's item says a thing besides its
// label, as a subtitle that assistive technology reads as its
// description.
func menuSays(tt *ui.Tester, label, text string) bool {
	for _, info := range tt.AccessAll() {
		if info.Name == label && strings.Contains(info.Description, text) {
			return true
		}
	}
	return false
}

// A watcher that waits for a sign-in says so in the footer, not that it
// is checking, and the window offers the sign-in; the menu says it of an
// account not on screen.
func TestNeedsSignInFromAWatchEvent(t *testing.T) {
	fs := newFakeService()
	fs.oauth[mail.OAuthGoogle] = true
	fs.addAccount(googleAccount("a1", "Work", "ann@gmail.com"), "", watchFolders(2, 3), testMessages("a1"))
	fs.addAccount(googleAccount("a2", "Home", "home@gmail.com"), "", watchFolders(0, 1), []mail.Summary{{
		UID: 1, Folder: "INBOX", Subject: "Welcome home", Seen: true, Date: time.Now().Add(-48 * time.Hour),
	}})
	w := watchApp(t, fs)
	withBrowser(w.a)
	w.a.start()
	tt := ui.NewTester(w.a.view, 1220, 800)
	w.say(t, "a1", mail.WatchStatus{State: mail.WatchSignIn, Failure: mail.WatchFailSignIn})
	tt.Frame()
	for _, text := range []string{locales.WatchSignIn(), locales.SignInNeeded("Work"), locales.SignInAgain(), "Design review — Thursday at 10"} {
		if !tt.HasText(text) {
			t.Errorf("the window lacks %q: %q", text, tt.Texts())
		}
	}
	if tt.HasText(locales.WatchConnecting()) {
		t.Errorf("the footer says it is checking: %q", tt.Texts())
	}
	click(t, tt, "Design review — Thursday at 10")
	if !tt.HasText("Body of Design review — Thursday at 10") {
		t.Errorf("cached mail cannot be read: %q", tt.Texts())
	}

	w.say(t, "a2", mail.WatchStatus{State: mail.WatchSignIn, Failure: mail.WatchFailSignIn})
	tt.Frame()
	click(t, tt, locales.AccountsMenu())
	if !menuSays(tt, "Home · home@gmail.com", locales.MenuNeedsSignIn()) {
		t.Errorf("the menu does not say Home needs a sign-in: %+v", tt.AccessAll())
	}
	tt.CloseMenu()

	// Signed in again, the footer says the watcher checks, as it does at
	// once.
	click(t, tt, locales.SignInAgainTo("Work"))
	tt.Frame()
	if w.a.needSignIn["a1"] || !tt.HasText(locales.WatchConnecting()) || tt.HasText(locales.SignInNeeded("Work")) {
		t.Errorf("signed in again: needs %v: %q", w.a.needSignIn, tt.Texts())
	}
}

// A sync that finds the account has to sign in again shows the banner,
// not an error line, and the cache stays: listed, and readable where the
// body is cached.
func TestNeedsSignInFromAFailedSync(t *testing.T) {
	fs := inMessageView(newFakeService())
	a, _ := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Design review — Thursday at 10")
	fs.needSignIn["a1"] = true
	click(t, tt, locales.Sync())
	for _, text := range []string{locales.SignInNeeded("Work"), "Design review — Thursday at 10", "Re: Contract for Q4 campaign"} {
		if !tt.HasText(text) {
			t.Errorf("after the sync the window lacks %q: %q", text, tt.Texts())
		}
	}
	if a.err != "" || tt.HasText(locales.SyncFailed("")) {
		t.Errorf("an error line: %q", a.err)
	}
	noRawTexts(t, tt, "after the sync")
	click(t, tt, "[shu-email/shu] Pull request #128")
	click(t, tt, "Design review — Thursday at 10")
	if !tt.HasText("Body of Design review — Thursday at 10") {
		t.Errorf("a body read before cannot be read: %q", tt.Texts())
	}
	// A change to mail on the server says why it was not done.
	click(t, tt, locales.StarMessage())
	if a.err != locales.ActionNeedsSignIn() {
		t.Errorf("starring: %q", a.err)
	}
	noRawTexts(t, tt, "starring")

	// So does the unified inbox, of the account that has to.
	a.err = ""
	click(t, tt, locales.AllInboxes())
	if !tt.HasText(locales.SignInNeeded("Work")) || tt.HasText(locales.SignInNeeded("Home")) || a.err != "" {
		t.Errorf("the unified inbox: %q, %q", a.err, tt.Texts())
	}
}

// Older mail and attachments wait for the sign-in, in the user's words.
func TestFetchOlderAndDownloadsWaitForTheSignIn(t *testing.T) {
	fs := inMessageView(newFakeService())
	fs.older[listKey("a1", "INBOX")] = []mail.Summary{{UID: 0x7f, Folder: "INBOX", Subject: "From last year", Date: time.Now().AddDate(-1, 0, 0)}}
	a, _ := googleApp(t, fs)
	m := a.all[0]
	fs.bodies[bodyKey("a1", m.Folder, m.UID)] = mail.Message{
		Summary: m, BodyText: "Body", BodyCached: true,
		Attachments: []mail.Attachment{{Part: "2", Filename: "report.txt", ContentType: "text/plain", Size: 4}},
	}
	fs.attachment = mail.Attachment{Part: "2", Filename: "report.txt"}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	saved := 0
	a.saveFile = func(*mygo.Window, string) (string, error) { saved++; return "", nil }
	fs.needSignIn["a1"] = true
	tt := ui.NewTester(a.view, 1220, 800)

	click(t, tt, "Design review — Thursday at 10")
	click(t, tt, locales.SaveAs())
	if saved != 0 || a.err != "" || !a.needSignIn["a1"] || !tt.HasText(locales.SignInNeeded("Work")) {
		t.Errorf("a download: asked where %d times, error %q: %q", saved, a.err, tt.Texts())
	}

	a.list.ScrollTo(len(a.all), ui.End)
	tt.Frame()
	click(t, tt, locales.LoadOlder())
	if !tt.HasText(locales.OlderNeedsSignIn()) || tt.HasText(locales.RetryFetchOlder()) {
		t.Errorf("older mail: %q", tt.Texts())
	}
	noRawTexts(t, tt, "older mail")
}

// A message that cannot be sent because the account has to sign in
// again stays in its window and in Drafts, says why and offers the
// sign-in. Signed in, it is not sent by itself: its Send sends it, once.
func TestSendWaitsForTheSignIn(t *testing.T) {
	fs := newFakeService()
	a, _ := googleApp(t, fs)
	fs.needSignIn["a1"] = true
	cs := a.openCompose(nil)
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.To(), "bob@example.com")
	typeIn(t, ct, locales.Subject(), "Quarterly plan")
	typeIn(t, ct, locales.Body(), "The numbers are in.")
	click(t, ct, locales.Send())
	if cs.closed || len(fs.sent) != 0 || cs.err != locales.SendNeedsSignIn("Work") || !cs.needsSignIn {
		t.Fatalf("after the send: closed %v, sent %d, error %q", cs.closed, len(fs.sent), cs.err)
	}
	for _, text := range []string{locales.SendNeedsSignIn("Work"), locales.SignInAgain()} {
		if !ct.HasText(text) {
			t.Errorf("the compose window lacks %q: %q", text, ct.Texts())
		}
	}
	if values := fieldValues(ct); !slices.Contains(values, "Quarterly plan") || !slices.Contains(values, "The numbers are in.") {
		t.Errorf("the message left the window: %q", values)
	}
	noRawTexts(t, ct, "the send")
	if d := theDraft(t, fs); d.Subject != "Quarterly plan" || d.Body != "The numbers are in." {
		t.Errorf("draft = %+v", d)
	}
	if !a.needSignIn["a1"] {
		t.Error("the account was not marked")
	}

	click(t, ct, locales.SignInAgainTo("Work"))
	if a.needSignIn["a1"] || cs.err != "" || cs.note != locales.SignedInSendNow() || len(fs.sent) != 0 {
		t.Fatalf("signed in: needs %v, error %q, note %q, sent %d", a.needSignIn, cs.err, cs.note, len(fs.sent))
	}
	if !ct.HasText(locales.SignedInSendNow()) || ct.HasText(locales.SignInAgain()) {
		t.Errorf("signed in, the compose window says: %q", ct.Texts())
	}
	click(t, ct, locales.Send())
	if len(fs.sent) != 1 || !cs.closed {
		t.Errorf("sent %d, closed %v", len(fs.sent), cs.closed)
	}
	// Two sends were asked for, the one refused and the user's second.
	sends := 0
	for _, act := range fs.actsOf("a1") {
		if act == "Send Quarterly plan" {
			sends++
		}
	}
	if sends != 2 {
		t.Errorf("sends asked of the engine: %v", fs.actsOf("a1"))
	}
}

// A draft of an account that has to sign in again waits for the sign-in,
// and says so; signed in, it syncs.
func TestDraftWaitsForTheSignIn(t *testing.T) {
	fs := newFakeService()
	a, _ := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 800)
	fs.needSignIn["a1"] = true
	cs := a.openCompose(nil)
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.Subject(), "Notes")
	a.syncDrafts("a1")
	ct.Frame()
	if !ct.HasText(locales.DraftWaitsSignIn()) || ct.HasText(locales.DraftOffline()) {
		t.Errorf("the draft's status: %q", ct.Texts())
	}
	tt.Frame()
	syncs := fs.draftSyncs
	click(t, tt, locales.SignInAgainTo("Work"))
	ct.Frame()
	if fs.draftSyncs == syncs || ct.HasText(locales.DraftWaitsSignIn()) {
		t.Errorf("signed in: draft syncs %d, was %d: %q", fs.draftSyncs, syncs, ct.Texts())
	}
}

// Signing in again as another address is refused: the window says which
// address the account is, and the account still needs its own sign-in.
func TestSignInAgainAsAnotherAddress(t *testing.T) {
	fs := newFakeService()
	fs.needSignIn["a1"] = true
	a, _ := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 800)
	fs.signInAs = "eve@gmail.com"
	click(t, tt, locales.SignInAgainTo("Work"))
	if !tt.HasText(locales.SignInWrongAddress("ann@gmail.com")) || !a.needSignIn["a1"] || !tt.HasText(locales.SignInNeeded("Work")) {
		t.Fatalf("another address: needs %v: %q", a.needSignIn, tt.Texts())
	}
	noRawTexts(t, tt, "another address")
	fs.signInAs = "Ann@Gmail.com"
	click(t, tt, locales.SignInAgainTo("Work"))
	if a.needSignIn["a1"] || tt.HasText(locales.SignInWrongAddress("ann@gmail.com")) || tt.HasText(locales.SignInNeeded("Work")) {
		t.Errorf("the account's own address: needs %v: %q", a.needSignIn, tt.Texts())
	}
}

// The banner's sign-in waits as the add view's does, and Escape stops it.
func TestBannerSignInWaitsAndCancels(t *testing.T) {
	fs := newFakeService()
	fs.needSignIn["a1"] = true
	a, browser := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 800)
	q := queueJobs(a)
	click(t, tt, locales.SignInAgainTo("Work"))
	var problems []string
	fs.onSignIn = func(ctx context.Context) error {
		q.runQueued()
		tt.Frame()
		for _, text := range []string{locales.SignInWaiting(), locales.SignInWaitingAs("Google", "ann@gmail.com"), locales.OpenSignInPage()} {
			if !tt.HasText(text) {
				problems = append(problems, fmt.Sprintf("waiting, the banner lacks %q: %q", text, tt.Texts()))
			}
		}
		if len(browser.urls) != 1 {
			problems = append(problems, fmt.Sprintf("opened %q", browser.urls))
		}
		// On another account the banner is not on screen, and Escape
		// leaves the sign-in alone.
		a.switchAccount("a2")
		q.runQueued()
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		if ctx.Err() != nil {
			problems = append(problems, "Escape on another account ended the sign-in")
		}
		a.switchAccount("a1")
		q.runQueued()
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		if ctx.Err() == nil {
			problems = append(problems, "Escape did not end the sign-in")
		}
		return ctx.Err()
	}
	q.drain(a)
	tt.Frame()
	for _, p := range problems {
		t.Error(p)
	}
	if a.signIn != nil || a.signInErr != nil || !a.needSignIn["a1"] || !tt.HasText(locales.SignInAgain()) || tt.HasText(locales.SignInWaiting()) {
		t.Errorf("after Escape: sign-in %+v, failure %+v: %q", a.signIn, a.signInErr, tt.Texts())
	}
}

// Accounts that sign in with a password are as they were: their editor
// tests and saves with the password, they never need a sign-in, and the
// manual form adds one beside the providers' buttons.
func TestPasswordAccountsUnchanged(t *testing.T) {
	fs := newFakeService()
	a, _ := googleApp(t, fs)
	tt := ui.NewTester(a.view, 1220, 900)
	a.switchAccount("a2")
	tt.Frame()
	a.openAccountForm(a.account("a2"))
	a.accountForm.Password = "another secret"
	tt.Frame()
	click(t, tt, locales.TestAndSave())
	if fs.password["a2"] != "another secret" || a.accountErr != "" {
		t.Errorf("saved password %q, error %q", fs.password["a2"], a.accountErr)
	}
	a.openAccountForm(nil)
	a.accountForm.Name = "Third"
	a.accountForm.Email = "third@example.com"
	a.accountForm.Password = "secret"
	a.accountForm.Host = "imap.example.com"
	a.accountForm.Port = "993"
	a.accountForm.SMTPHost = "smtp.example.com"
	a.accountForm.SMTPPort = "465"
	tt.Frame()
	click(t, tt, locales.TestAndSave())
	if len(fs.accounts) != 3 || fs.accounts[2].OAuth != "" || fs.password[fs.accounts[2].ID] != "secret" || len(fs.signIns) != 0 {
		t.Errorf("added by hand: %+v, sign-ins %v", fs.accounts, fs.signIns)
	}
}

// The ways to sign in hold at the smallest window, in the dark and in
// Simplified Chinese: the providers' buttons, the banner and its button,
// the editor and its sign-out.
func TestSignInLayoutAtMinimumSize(t *testing.T) {
	defer i18n.SetLocaleFunc(func() string { return "en" })
	inside := func(t *testing.T, name string, tt *ui.Tester, text string, right float32) {
		t.Helper()
		box, ok := tt.Find(text)
		if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.X+box.W > right || box.Y < 0 || box.Y+box.H > 640 {
			t.Errorf("%s: %q is at %+v (found %v): %q", name, text, box, ok, tt.Texts())
		}
	}
	for _, locale := range []string{"en", "zh-Hans"} {
		for _, dark := range []bool{false, true} {
			name := fmt.Sprintf("%s dark=%v", locale, dark)
			i18n.SetLocaleFunc(func() string { return locale })

			a, _, _ := firstRunApp(t, mail.OAuthGoogle, mail.OAuthMicrosoft)
			tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
			tt.SetLocale(locale)
			for _, text := range []string{locales.SignInWithGoogle(), locales.SignInWithMicrosoft(), locales.ManualSetup()} {
				inside(t, name+" first run", tt, text, 1000)
			}

			fs := newFakeService()
			fs.needSignIn["a1"] = true
			a, _ = googleApp(t, fs)
			fs.oauth[mail.OAuthMicrosoft] = true
			tt = ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
			tt.SetLocale(locale)
			// The banner is the list's, beside the sidebar.
			inside(t, name+" banner", tt, locales.SignInNeeded("Work"), 222+384)
			inside(t, name+" banner", tt, locales.SignInAgainTo("Work"), 222+384)
			if !tt.HasText("Design review — Thursday at 10") {
				t.Errorf("%s: the banner pushed the mail out: %q", name, tt.Texts())
			}
			a.openAccountForm(nil)
			tt.Frame()
			for _, text := range []string{locales.SignInWithGoogle(), locales.SignInWithMicrosoft()} {
				inside(t, name+" add", tt, text, 1000)
			}
			a.openAccountForm(a.account("a1"))
			tt.Frame()
			for _, text := range []string{locales.SignInAgainTo("Work"), locales.Save(), locales.DeleteAccount()} {
				inside(t, name+" editor", tt, text, 1000)
			}
			click(t, tt, locales.SignInAgainTo("Work"))
			inside(t, name+" editor", tt, locales.SignOut(), 1000)
		}
	}
}
