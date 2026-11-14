package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// accountForm is the account view's editable state.
type accountForm struct {
	ID       string
	Name     string
	Email    string
	Password string
	Provider string
	Host     string
	Port     string
	TLS      string
	SMTPHost string
	SMTPPort string
	SMTPTLS  string
	editing  bool
	// OAuth is the provider the account edited signs in with, "" for
	// one with a password.
	OAuth string
	// disc is the lookup of the address's servers (discoverSettings).
	disc discoveryForm
}

// discoveryPause is how long the form waits after the address changes
// before looking its servers up: the typing is for another domain while
// it goes on.
const discoveryPause = 500 * time.Millisecond

// discoveryForm is what the account form has of the lookup of the
// address's servers.
type discoveryForm struct {
	// seq tells the lookups apart, of which the one begun last is kept;
	// cancel ends that one, as the form closes or the address changes.
	seq    uint64
	cancel context.CancelFunc
	timer  *time.Timer
	busy   bool
	domain string
	// source is where the servers on show came from, and found what the
	// lookup answered, for keeping the user's own entry.
	source mail.DiscoverySource
	found  mail.Discovery
	// note says what was found, and err why nothing complete was; both
	// are shown under the address.
	note string
	err  string
	// manual is set once the user changed the servers, which a lookup
	// then leaves alone.
	manual bool
}

// providerNames are the Select labels: the presets then Custom.
func providerNames() []string {
	out := make([]string, 0, len(mail.Providers)+1)
	for _, p := range mail.Providers {
		out = append(out, p.Name)
	}
	return append(out, locales.ProviderCustom())
}

// tlsNames are the security choices.
func tlsNames() []string { return []string{"TLS", "STARTTLS"} }

// formFromAccount fills the form for editing.
func formFromAccount(a mail.Account) accountForm {
	f := accountForm{
		ID: a.ID, Name: a.Name, Email: a.Email, Provider: locales.ProviderCustom(),
		Host: a.IMAP.Host, Port: strconv.Itoa(a.IMAP.Port), TLS: tlsLabel(a.IMAP.TLS),
		SMTPHost: a.SMTP.Host, SMTPPort: strconv.Itoa(a.SMTP.Port), SMTPTLS: tlsLabel(a.SMTP.TLS),
		editing: true, OAuth: a.OAuth,
	}
	if p, ok := mail.ProviderFor(a.Email); ok && p.IMAP == a.IMAP && p.SMTP == a.SMTP {
		f.Provider = p.Name
	}
	return f
}

func tlsLabel(m mail.TLSMode) string {
	if m == mail.TLSStartTLS {
		return "STARTTLS"
	}
	return "TLS"
}

func tlsMode(label string) mail.TLSMode {
	if strings.EqualFold(label, "starttls") {
		return mail.TLSStartTLS
	}
	return mail.TLSImplicit
}

// account builds the engine model from the form.
func (f accountForm) account() (mail.Account, error) {
	imapPort, err := strconv.Atoi(strings.TrimSpace(f.Port))
	if err != nil {
		return mail.Account{}, fmt.Errorf("%s: %v", locales.Port(), err)
	}
	smtpPort, err := strconv.Atoi(strings.TrimSpace(f.SMTPPort))
	if err != nil {
		return mail.Account{}, fmt.Errorf("%s: %v", locales.Port(), err)
	}
	id := f.ID
	if id == "" {
		id = mail.NewAccountID()
	}
	return mail.Account{
		ID: id, Name: strings.TrimSpace(f.Name), Email: strings.TrimSpace(f.Email),
		Username: strings.TrimSpace(f.Email),
		IMAP:     mail.Server{Host: strings.TrimSpace(f.Host), Port: imapPort, TLS: tlsMode(f.TLS)},
		SMTP:     mail.Server{Host: strings.TrimSpace(f.SMTPHost), Port: smtpPort, TLS: tlsMode(f.SMTPTLS)},
	}, nil
}

// openAccountForm shows the account form in a floating panel over the
// mail window, or the full-window onboarding view when no account exists.
func (a *app) openAccountForm(acc *mail.Account) {
	a.closeSettings()
	a.stopDiscovery()
	a.accountErr = ""
	a.accountNote = ""
	a.signOutOpen = false
	if !a.addingWithProvider() {
		a.signInName = ""
	}
	if f := a.signInErr; f != nil && f.account == "" {
		a.signInErr = nil
	}
	if len(a.accounts) == 0 {
		a.showAccounts = true
		a.accountOpen = false
	} else {
		a.showAccounts = false
		a.accountOpen = true
	}
	if acc == nil {
		a.accountForm = accountForm{Provider: providerNames()[0], TLS: "TLS", SMTPTLS: "STARTTLS"}
		if a.accountForm.Provider != locales.ProviderCustom() {
			if p, ok := mail.ProviderByID(strings.ToLower(a.accountForm.Provider)); ok {
				a.applyProvider(p)
				// The servers are the provider's, not this address's:
				// typing another domain's address replaces them.
				a.accountForm.disc.source = mail.DiscoveryPreset
			}
		}
		a.openSending("")
		return
	}
	a.accountForm = formFromAccount(*acc)
	a.openSending(acc.ID)
}

// closeAccountForm dismisses the floating form, and with it an account
// being added by signing in.
func (a *app) closeAccountForm() {
	a.cancelAdding()
	a.stopDiscovery()
	a.accountOpen = false
	a.signOutOpen = false
	a.accountErr = ""
	a.accountNote = ""
}

// cancelAdding stops the sign-in of an account being added, whose view
// goes.
func (a *app) cancelAdding() {
	if a.addingWithProvider() {
		a.cancelSignIn()
	}
}

// accountModal floats the account form over the mail window.
func (a *app) accountModal(c *ui.Context) {
	if !a.accountOpen {
		return
	}
	t := c.Theme()
	modal := ui.Modal(c, &a.accountOpen, func() {
		ui.Column(c).Width(560).Gap(12).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(10).Children(func() {
				title := locales.AddAccount()
				if a.accountForm.editing {
					title = locales.EditAccount()
				}
				ui.Text(c, title).FontSize(18).Bold()
				ui.Spacer(c)
				a.iconButton(c, closeIcon, locales.Cancel(), locales.Cancel()).OnClick(a.closeAccountForm)
			})
			switch {
			case a.addingWithProvider():
				// The browser has the sign-in: the form waits beside it.
				ui.Column(c).Padding(2, 2, 6, 2).Children(func() { a.providerSignIn(c, t) })
				return
			case a.oauthEditing():
				ui.Scroll(c).Height(430).KeepScrollbars(true).Children(func() {
					ui.Column(c).Padding(2, 2).PaddingEnd(10).Gap(14).Children(func() {
						a.oauthAccountFields(c, t)
						a.accountStatus(c, t)
						a.sendingSections(c, t)
					})
				})
			default:
				ui.Scroll(c).Height(430).KeepScrollbars(true).Children(func() {
					ui.Column(c).Padding(2, 2).PaddingEnd(10).Gap(14).Children(func() {
						if !a.accountForm.editing && a.providerSignIn(c, t) {
							a.manualHeading(c)
						}
						a.accountFields(c, t)
						a.accountStatus(c, t)
						a.sendingSections(c, t)
					})
				})
			}
			ui.Divider(c)
			a.accountActions(c, true)
		})
		a.signOutAlert(c)
	})
	if modal.Dismissed() && a.signIn != nil && (a.addingWithProvider() || a.signIn.account == a.accountForm.ID) {
		// Escape stops the sign-in the form waits for, as its Cancel
		// does, and leaves the form open.
		a.cancelSignIn()
		a.accountOpen = true
	}
}

// manualHeading heads the manual form, under the sign-in with a
// provider: the way for any other provider, and the fallback.
func (a *app) manualHeading(c *ui.Context) {
	ui.Text(c, locales.ManualSetup()).FontSize(14).Bold().Margin(6, 0, 0, 0)
}

// accountStatus draws the progress, error and success lines.
func (a *app) accountStatus(c *ui.Context, t *ui.Theme) {
	if a.accountBusy {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, a.accountNote).FontSize(12).TextColor(t.TextMuted)
		})
	}
	if a.accountErr != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, a.accountErr).FontSize(12).TextColor(t.Text).MaxLines(3)
		})
	}
	if a.accountNote != "" && !a.accountBusy {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
			ui.Text(c, a.accountNote).FontSize(12).TextColor(t.TextMuted)
		})
	}
}

// accountActions draws the form's actions, destructive on the left and
// the primary action on the right.
func (a *app) accountActions(c *ui.Context, modal bool) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		if a.accountForm.editing {
			ui.Button(c, locales.DeleteAccount()).OnClick(func() { a.deleteAccount() })
		}
		ui.Spacer(c)
		if modal {
			ui.Button(c, locales.Cancel()).OnClick(a.closeAccountForm)
		} else if len(a.accounts) > 0 {
			ui.Button(c, locales.Cancel()).OnClick(func() { a.showAccounts = false })
		}
		if a.oauthEditing() {
			// Its name alone changes, which needs no test of the servers.
			ui.PrimaryButton(c, locales.Save()).Disabled(a.accountBusy).OnClick(a.saveOAuthAccount)
			return
		}
		ui.PrimaryButton(c, locales.TestAndSave()).Disabled(a.accountBusy).
			OnClick(func() { a.saveAccount() })
	})
}

// applyProvider fills the server fields from a preset.
func (a *app) applyProvider(p mail.Provider) {
	a.accountForm.Host = p.IMAP.Host
	a.accountForm.Port = strconv.Itoa(p.IMAP.Port)
	a.accountForm.TLS = tlsLabel(p.IMAP.TLS)
	a.accountForm.SMTPHost = p.SMTP.Host
	a.accountForm.SMTPPort = strconv.Itoa(p.SMTP.Port)
	a.accountForm.SMTPTLS = tlsLabel(p.SMTP.TLS)
}

// accountsView is the first-run and account-management window.
func (a *app) accountsView(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	if a.addingWithProvider() {
		// Escape stops the sign-in, as its Cancel does.
		c.OnShortcut(0, ui.KeyEscape, a.cancelSignIn)
	}
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			if len(a.accounts) > 0 {
				a.iconButton(c, chevronLeftIcon, locales.Back(), locales.Back()).
					OnClick(func() {
						a.cancelAdding()
						a.showAccounts = false
					})
			}
			title := locales.AccountsTitle()
			if a.accountForm.editing {
				title = locales.EditAccount()
			}
			ui.Text(c, title).FontSize(17).Bold()
			ui.Spacer(c)
			a.languageMenu(c)
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(28, 32, 28, 32).Gap(16).MaxWidth(720).Children(func() {
				if len(a.accounts) == 0 {
					ui.Text(c, locales.AccountsIntro()).FontSize(14).TextColor(t.TextMuted)
				}
				if !a.accountForm.editing && a.providerSignIn(c, t) {
					if a.addingWithProvider() {
						return
					}
					a.manualHeading(c)
				}
				if a.oauthEditing() {
					a.oauthAccountFields(c, t)
				} else {
					a.accountFields(c, t)
				}
				a.accountStatus(c, t)
				a.sendingSections(c, t)
				a.accountActions(c, false)
			})
		})
		a.signOutAlert(c)
	})
}

// accountFields draws the form's inputs.
func (a *app) accountFields(c *ui.Context, t *ui.Theme) {
	f := &a.accountForm
	a.section(c, locales.Provider(), func() {
		ui.Select(c, &f.Provider, providerNames()).FillWidth().Label(locales.Provider()).
			OnChange(func() {
				for _, p := range mail.Providers {
					if p.Name == f.Provider {
						// A preset replaces the servers; Custom keeps
						// what the user entered.
						a.applyProvider(p)
						a.providerChanged()
						break
					}
				}
			})
	})
	a.field(c, locales.AccountName(), func() ui.Element {
		return ui.TextInput(c, &f.Name).
			Placeholder(locales.AccountNamePlaceholder()).Label(locales.AccountName())
	})
	a.field(c, locales.Email(), func() ui.Element {
		return ui.TextInput(c, &f.Email).Placeholder("name@example.com").Label(locales.Email()).
			OnChange(a.emailChanged).OnSubmit(a.discoverSettings)
	})
	// The status is always a sibling of the fields, even when it has
	// nothing to say: a row that came and went would move the inputs
	// below it and take their state, focus included, with it.
	ui.Column(c).Children(func() { a.discoveryStatus(c, t) })
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, locales.Password()).FontSize(11).Bold().TextColor(t.TextMuted)
		pw := ui.TextInput(c, &f.Password).Password().Label(locales.Password())
		if f.editing {
			pw.Placeholder(locales.PasswordKeep())
		}
		ui.Text(c, locales.PasswordAppNote()).FontSize(11).TextColor(t.TextMuted)
	})
	a.section(c, locales.ImapSection(), func() {
		a.field(c, locales.Host(), func() ui.Element {
			return ui.TextInput(c, &f.Host).Label(locales.Host()).OnChange(a.serversChanged)
		})
		ui.Row(c).Gap(12).Children(func() {
			a.fixedField(c, locales.Port(), 140, func() ui.Element {
				return ui.TextInput(c, &f.Port).Label(locales.Port()).OnChange(a.serversChanged)
			})
			a.fixedField(c, locales.Security(), 220, func() ui.Element {
				return ui.Select(c, &f.TLS, tlsNames()).Label(locales.Security()).OnChange(a.serversChanged)
			})
		})
	})
	a.section(c, locales.SmtpSection(), func() {
		a.field(c, locales.Host(), func() ui.Element {
			return ui.TextInput(c, &f.SMTPHost).Label(locales.Host()).OnChange(a.serversChanged)
		})
		ui.Row(c).Gap(12).Children(func() {
			a.fixedField(c, locales.Port(), 140, func() ui.Element {
				return ui.TextInput(c, &f.SMTPPort).Label(locales.Port()).OnChange(a.serversChanged)
			})
			a.fixedField(c, locales.Security(), 220, func() ui.Element {
				return ui.Select(c, &f.SMTPTLS, tlsNames()).Label(locales.Security()).OnChange(a.serversChanged)
			})
		})
	})
}

// emailChanged looks the address's servers up once the typing pauses:
// what it finds fills the fields, and the manual form stays.
func (a *app) emailChanged() {
	f := &a.accountForm
	if f.editing || a.after == nil {
		return
	}
	a.stopDiscovery()
	domain := typedDomain(f.Email)
	switch {
	case f.disc.domain != "" && domain != f.disc.domain:
		// Another domain's address: the servers on show, and the entry
		// the user made for them, belong to the address before this one.
		f.Host, f.Port, f.TLS = "", "", "TLS"
		f.SMTPHost, f.SMTPPort, f.SMTPTLS = "", "", "STARTTLS"
		f.disc.manual = false
	case f.disc.domain == "" && f.disc.source != "" && !f.disc.manual:
		// The form opened with a provider's servers, which belong to no
		// address yet.
		f.Host, f.Port, f.TLS = "", "", "TLS"
		f.SMTPHost, f.SMTPPort, f.SMTPTLS = "", "", "STARTTLS"
	}
	f.disc.note, f.disc.err, f.disc.source, f.disc.found = "", "", "", mail.Discovery{}
	if domain == "" {
		return
	}
	seq := f.disc.seq
	f.disc.timer = a.after(discoveryPause, func() {
		// The timer runs off the UI thread, and the lookup changes the
		// form and its status.
		a.post(func() {
			if f.disc.seq == seq {
				a.discoverSettings()
			}
		})
	})
}

// typedDomain reads the domain of the address being typed, or "" before
// the address has one.
func typedDomain(email string) string {
	email = strings.TrimSpace(email)
	at := strings.LastIndexByte(email, '@')
	if at < 0 || at == len(email)-1 {
		return ""
	}
	domain := strings.ToLower(email[at+1:])
	if strings.ContainsAny(domain, " \t/") {
		return ""
	}
	return domain
}

// discoverSettings looks up the servers of the address entered, and
// fills those the user has not entered: what is found shows in its
// fields, with where it came from, or why nothing complete was found.
func (a *app) discoverSettings() {
	f := &a.accountForm
	email := strings.TrimSpace(f.Email)
	f.disc.timer = nil
	if f.editing || !strings.Contains(email, "@") {
		return
	}
	a.stopDiscovery()
	ctx, cancel := a.ctx()
	seq := f.disc.seq
	f.disc.cancel = cancel
	f.disc.busy = true
	f.disc.domain = email[strings.LastIndexByte(email, '@')+1:]
	f.disc.note, f.disc.err, f.disc.source, f.disc.found = "", "", "", mail.Discovery{}
	a.run(func() {
		d, err := a.svc.Discover(ctx, email)
		a.post(func() {
			if f.disc.seq != seq {
				return
			}
			f.disc.busy, f.disc.cancel = false, nil
			f.disc.found = d
			if d.IMAP.Host != "" || d.SMTP.Host != "" {
				if f.disc.manual {
					f.disc.note = locales.DiscoverManualKept()
				} else {
					if d.IMAP.Host != "" {
						f.Host, f.Port, f.TLS = d.IMAP.Host, strconv.Itoa(d.IMAP.Port), tlsLabel(d.IMAP.TLS)
					}
					if d.SMTP.Host != "" {
						f.SMTPHost, f.SMTPPort, f.SMTPTLS = d.SMTP.Host, strconv.Itoa(d.SMTP.Port), tlsLabel(d.SMTP.TLS)
					}
					f.disc.source = d.Source
					f.disc.note = locales.DiscoverFound(discoverSource(d.Source))
					if d.Source == mail.DiscoveryPreset {
						// The select agrees with the fields on show.
						if p, ok := mail.ProviderFor(email); ok {
							f.Provider = p.Name
						}
					}
				}
			}
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
					// The user closed the form or typed another address.
					return
				}
				var found *mail.DiscoveryError
				switch {
				case errors.As(err, &found):
					f.disc.err = locales.DiscoverFailed(discoverStages(found))
				case errors.Is(err, context.DeadlineExceeded):
					f.disc.err = locales.DiscoverTimedOut()
				default:
					f.disc.err = locales.DiscoverFailed(err.Error())
				}
			}
		})
	})
}

// stopDiscovery ends the lookup on its way, if there is one, and drops
// what it would have shown.
func (a *app) stopDiscovery() {
	f := &a.accountForm
	if f.disc.timer != nil {
		f.disc.timer.Stop()
		f.disc.timer = nil
	}
	if f.disc.cancel != nil {
		f.disc.cancel()
		f.disc.cancel = nil
	}
	f.disc.seq++
	f.disc.busy = false
}

// serversChanged notes that the user entered the servers, which a lookup
// must not then replace.
func (a *app) serversChanged() {
	f := &a.accountForm
	if f.disc.manual {
		return
	}
	f.disc.manual = true
	f.disc.note, f.disc.err, f.disc.source = "", "", ""
}

// providerChanged notes that the servers on show are a provider's, which
// another address's lookup may replace.
func (a *app) providerChanged() {
	f := &a.accountForm
	f.disc.manual = false
	f.disc.source = mail.DiscoveryPreset
	f.disc.note, f.disc.err = "", ""
}

// discoverSource names where a lookup found the servers.
func discoverSource(source mail.DiscoverySource) string {
	switch source {
	case mail.DiscoveryPreset:
		return locales.DiscoverSourcePreset()
	case mail.DiscoveryAutoconfig:
		return locales.DiscoverSourceAutoconfig()
	case mail.DiscoveryAutodiscover:
		return locales.DiscoverSourceAutodiscover()
	case mail.DiscoverySRV:
		return locales.DiscoverSourceSRV()
	case mail.DiscoveryManual:
		return locales.DiscoverSourceManual()
	}
	return string(source)
}

// discoverStages says which sources were tried and what each reported.
func discoverStages(e *mail.DiscoveryError) string {
	parts := make([]string, 0, len(e.Steps))
	for _, s := range e.Steps {
		parts = append(parts, discoverSource(s.Source)+": "+s.Err)
	}
	return strings.Join(parts, "; ")
}

// discoveryStatus draws what the lookup of the address's servers found,
// or why it found nothing.
func (a *app) discoveryStatus(c *ui.Context, t *ui.Theme) {
	d := &a.accountForm.disc
	if a.accountForm.editing || d.domain == "" {
		return
	}
	switch {
	case d.busy:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(12, 12)
			ui.Text(c, locales.DiscoverLooking(d.domain)).FontSize(11).TextColor(t.TextMuted)
		})
	case d.err != "":
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Icon(c, alertIcon).FontSize(13).TextColor(t.Warning)
			ui.Text(c, d.err).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0).MaxLines(4)
		})
	case d.note != "":
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Icon(c, checkIcon).FontSize(13).TextColor(t.Success)
			ui.Text(c, d.note).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0).MaxLines(3)
		})
	}
}

// section draws a titled group of fields.
func (a *app) section(c *ui.Context, title string, fn func()) {
	t := c.Theme()
	ui.Column(c).Gap(8).Children(func() {
		ui.Divider(c)
		ui.Text(c, title).FontSize(12).Bold().TextColor(t.TextMuted)
		fn()
	})
}

// field labels one input and fills the form's width. The input is built
// inside the field, as MyGo elements attach to the scope that builds them.
func (a *app) field(c *ui.Context, label string, build func() ui.Element) {
	t := c.Theme()
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(11).Bold().TextColor(t.TextMuted)
		build().FillWidth()
	})
}

// fixedField places a labelled input at an explicit width, so a row of
// fields keeps its shape.
func (a *app) fixedField(c *ui.Context, label string, width float32, build func() ui.Element) {
	t := c.Theme()
	ui.Column(c).Width(width).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(11).Bold().TextColor(t.TextMuted)
		build().FillWidth()
	})
}

// saveAccount tests the connection, then adds or updates the account.
func (a *app) saveAccount() {
	f := a.accountForm
	acc, err := f.account()
	if err != nil {
		a.accountErr = err.Error()
		return
	}
	if err := acc.Validate(); err != nil {
		a.accountErr = err.Error()
		return
	}
	ctx, cancel := a.ctx()
	a.accountBusy = true
	a.accountErr = ""
	a.accountNote = locales.TestConnecting()
	a.run(func() {
		defer cancel()
		testErr := a.svc.TestConnection(ctx, acc, f.Password)
		if testErr != nil {
			a.post(func() {
				a.accountBusy = false
				a.accountNote = ""
				a.accountErr = locales.TestFailed(testErr.Error())
			})
			return
		}
		var saveErr error
		if f.editing {
			saveErr = a.svc.UpdateAccount(ctx, acc, f.Password)
		} else {
			saveErr = a.svc.AddAccount(ctx, acc, f.Password)
		}
		if saveErr == nil && !f.editing && (f.disc.manual || !f.disc.found.Complete()) {
			// The servers the user entered are the domain's from now on,
			// so that a later form does not look for others.
			_ = a.svc.RememberDiscovery(ctx, acc.Email, acc.IMAP, acc.SMTP)
		}
		a.post(func() {
			a.accountBusy = false
			if saveErr != nil {
				a.accountNote = ""
				a.accountErr = locales.SaveFailed(saveErr.Error())
				return
			}
			a.accountNote = locales.AccountSaved()
			a.afterAccountSaved(acc.ID)
		})
	})
}

// afterAccountSaved reloads the accounts and opens the new one.
func (a *app) afterAccountSaved(id string) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		accounts, _, err := a.svc.Accounts(ctx)
		a.post(func() {
			if err != nil {
				a.accountErr = err.Error()
				return
			}
			a.accounts = accounts
			a.showAccounts = false
			a.accountOpen = false
			a.active = id
			// The saved account is watched anew, with what was saved.
			a.rewatch(id)
			a.accountsChanged()
			a.beginAccount()
			a.run(func() {
				ctx, cancel := a.ctx()
				defer cancel()
				_ = a.svc.SetActive(ctx, id)
			})
		})
	})
}

// deleteAccount asks first, then removes the rows and the keychain entry.
func (a *app) deleteAccount() {
	id := a.accountForm.ID
	if id == "" {
		return
	}
	go func() {
		res, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: a.win, Type: mygo.MessageWarning,
			Title: locales.DeleteAccountTitle(), Message: locales.DeleteAccountConfirm(),
			Buttons:       []string{locales.Cancel(), locales.DeleteAccount()},
			DefaultButton: 0, CancelButton: 0, DestructiveButtons: []int{1},
		})
		if err != nil || res.Button != 1 {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		if err := a.svc.RemoveAccount(ctx, id); err != nil {
			a.post(func() { a.accountErr = err.Error() })
			return
		}
		a.run(func() {
			ctx, cancel := a.ctx()
			defer cancel()
			accounts, active, err := a.svc.Accounts(ctx)
			a.post(func() {
				if err != nil {
					a.accountErr = err.Error()
					return
				}
				a.accounts = accounts
				a.showAccounts = false
				a.accountOpen = false
				a.accountsChanged()
				if len(accounts) == 0 {
					a.openAccountForm(nil)
					return
				}
				a.active = active
				if a.account(a.active) == nil {
					a.active = accounts[0].ID
				}
				a.beginAccount()
			})
		})
	}()
}

// languageMenu is the manual language override, on every window.
func (a *app) languageMenu(c *ui.Context) {
	a.languageChoice(c).Width(280)
}

// languageChoice is the segmented language control.
func (a *app) languageChoice(c *ui.Context) ui.Element {
	labels := []string{locales.FollowSystem(), "English", "简体中文"}
	e := ui.Segmented(c, &a.lang, labels...).Label(locales.Language())
	if e.Changed() && a.setLocale != nil {
		switch a.lang {
		case 1:
			a.setLocale("en")
		case 2:
			a.setLocale("zh-Hans")
		default:
			a.setLocale("")
		}
	}
	return e
}
