package main

import (
	"fmt"
	"strconv"
	"strings"

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
		editing: true,
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
	a.showSettings = false
	a.accountErr = ""
	a.accountNote = ""
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
			}
		}
		return
	}
	a.accountForm = formFromAccount(*acc)
}

// closeAccountForm dismisses the floating form.
func (a *app) closeAccountForm() {
	a.accountOpen = false
	a.accountErr = ""
	a.accountNote = ""
}

// accountModal floats the account form over the mail window.
func (a *app) accountModal(c *ui.Context) {
	if !a.accountOpen {
		return
	}
	t := c.Theme()
	ui.Modal(c, &a.accountOpen, func() {
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
			ui.Scroll(c).Height(430).KeepScrollbars(true).Children(func() {
				ui.Column(c).Padding(2, 2).PaddingEnd(10).Gap(14).Children(func() {
					a.accountFields(c, t)
					a.accountStatus(c, t)
				})
			})
			ui.Divider(c)
			a.accountActions(c, true)
		})
	})
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
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			if len(a.accounts) > 0 {
				a.iconButton(c, chevronLeftIcon, locales.Back(), locales.Back()).
					OnClick(func() { a.showAccounts = false })
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
				a.accountFields(c, t)
				a.accountStatus(c, t)
				a.accountActions(c, false)
			})
		})
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
						a.applyProvider(p)
						return
					}
				}
			})
	})
	a.field(c, locales.AccountName(), func() ui.Element {
		return ui.TextInput(c, &f.Name).
			Placeholder(locales.AccountNamePlaceholder()).Label(locales.AccountName())
	})
	a.field(c, locales.Email(), func() ui.Element {
		return ui.TextInput(c, &f.Email).Placeholder("name@example.com").Label(locales.Email())
	})
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
			return ui.TextInput(c, &f.Host).Label(locales.Host())
		})
		ui.Row(c).Gap(12).Children(func() {
			a.fixedField(c, locales.Port(), 140, func() ui.Element {
				return ui.TextInput(c, &f.Port).Label(locales.Port())
			})
			a.fixedField(c, locales.Security(), 220, func() ui.Element {
				return ui.Select(c, &f.TLS, tlsNames()).Label(locales.Security())
			})
		})
	})
	a.section(c, locales.SmtpSection(), func() {
		a.field(c, locales.Host(), func() ui.Element {
			return ui.TextInput(c, &f.SMTPHost).Label(locales.Host())
		})
		ui.Row(c).Gap(12).Children(func() {
			a.fixedField(c, locales.Port(), 140, func() ui.Element {
				return ui.TextInput(c, &f.SMTPPort).Label(locales.Port())
			})
			a.fixedField(c, locales.Security(), 220, func() ui.Element {
				return ui.Select(c, &f.SMTPTLS, tlsNames()).Label(locales.Security())
			})
		})
	})
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
