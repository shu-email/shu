package main

import (
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// An account sends from its own address and from those the user adds,
// aliases its provider lets it use, and signs its mail with a signature
// of its own. The window keeps both for each account as the store last
// gave them, which compose writes with, and the account editor changes
// them.

// signatureForm is the account editor's signature, saved as it changes.
// edited says the user changed it since the editor opened, so that the
// signature read meanwhile does not replace what they wrote.
type signatureForm struct {
	account string
	sig     mail.Signature
	edited  bool
	err     string
}

// identityForm is the account editor's form of an address the account
// sends from, while one is added or edited: id is the one edited, "" for
// one being added. err says why the last change did not take.
type identityForm struct {
	open        bool
	id          string
	name, email string
	busy        bool
	err         string
}

// identitiesOf is what an account sends from: its own address, as the
// account has it now, then the addresses added to it.
func (a *app) identitiesOf(accountID string) []mail.Identity {
	acc := a.account(accountID)
	if acc == nil {
		return nil
	}
	own := mail.Identity{AccountID: acc.ID, Name: acc.Name, Email: acc.Email}
	return append([]mail.Identity{own}, a.aliases[accountID]...)
}

// signatureOf is an account's signature, the default until it is read.
func (a *app) signatureOf(accountID string) mail.Signature {
	if sig, ok := a.signatures[accountID]; ok {
		return sig
	}
	return mail.DefaultSignature()
}

// sendingChanged follows a.accounts: each account's signature and
// addresses are read once, and those of an account gone are forgotten.
func (a *app) sendingChanged() {
	for _, acc := range a.accounts {
		if _, known := a.signatures[acc.ID]; !known {
			a.loadSending(acc.ID)
		}
	}
	for id := range a.signatures {
		if a.account(id) == nil {
			delete(a.signatures, id)
		}
	}
	for id := range a.aliases {
		if a.account(id) == nil {
			delete(a.aliases, id)
		}
	}
}

// loadSending reads an account's signature and the addresses added to
// it. A save that answered first has newer ones.
func (a *app) loadSending(id string) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		sig, sigErr := a.svc.Signature(ctx, id)
		ids, idErr := a.svc.Identities(ctx, id)
		a.post(func() {
			if a.account(id) == nil {
				return
			}
			if _, known := a.signatures[id]; !known && sigErr == nil {
				a.signatures[id] = sig
				if f := &a.sigForm; f.account == id && !f.edited {
					f.sig = sig
				}
			}
			if _, known := a.aliases[id]; !known && idErr == nil {
				a.aliases[id] = added(ids)
			}
		})
	})
}

// added is the identities added to an account, without its own address.
func added(ids []mail.Identity) []mail.Identity {
	out := []mail.Identity{}
	for _, id := range ids {
		if id.ID != "" {
			out = append(out, id)
		}
	}
	return out
}

// openSending readies the account editor's signature and addresses for
// an account, or for none while one is added.
func (a *app) openSending(accountID string) {
	a.sigForm = signatureForm{account: accountID, sig: a.signatureOf(accountID)}
	a.idForm = identityForm{}
}

// saveSignature writes the editor's signature as it changes. The saves
// take turns, and one that a later save overtook does not write, so the
// last change is the one stored, which compose then signs with.
func (a *app) saveSignature() {
	f := &a.sigForm
	if f.account == "" {
		return
	}
	f.edited = true
	id, sig := f.account, f.sig
	seq := a.sigSaveSeq.Add(1)
	a.run(func() {
		a.sigSaveMu.Lock()
		defer a.sigSaveMu.Unlock()
		if a.sigSaveSeq.Load() != seq {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SaveSignature(ctx, id, sig)
		a.post(func() {
			if err == nil {
				a.signatures[id] = sig
			}
			if a.sigForm.account == id {
				a.sigForm.err = ""
				if err != nil {
					a.sigForm.err = locales.SignatureSaveFailed(err.Error())
				}
			}
		})
	})
}

// openIdentity opens the form of an address: id to edit, or a new one
// for an identity without an ID.
func (a *app) openIdentity(id mail.Identity) {
	a.idForm = identityForm{open: true, id: id.ID, name: id.Name, email: id.Email}
}

// saveIdentity adds the address of the form, or changes the one edited.
// The engine checks it, and what it refuses is said in the user's words.
func (a *app) saveIdentity() {
	f := a.idForm
	accountID := a.accountForm.ID
	if !f.open || f.busy || accountID == "" {
		return
	}
	a.idForm.busy, a.idForm.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		if f.id == "" {
			_, err = a.svc.AddIdentity(ctx, accountID, f.name, f.email)
		} else {
			_, err = a.svc.UpdateIdentity(ctx, mail.Identity{ID: f.id, AccountID: accountID, Name: f.name, Email: f.email})
		}
		var ids []mail.Identity
		listErr := err
		if err == nil {
			ids, listErr = a.svc.Identities(ctx, accountID)
		}
		a.post(func() {
			if listErr == nil {
				a.aliases[accountID] = added(ids)
			}
			if a.accountForm.ID != accountID {
				return
			}
			a.idForm.busy = false
			if err != nil {
				a.idForm.err = identityErrorText(err, f.email)
				return
			}
			a.idForm = identityForm{}
		})
	})
}

// removeIdentity removes an address the account sends from. The drafts
// written from it go from the account's own address from then on.
func (a *app) removeIdentity(id mail.Identity) {
	accountID := id.AccountID
	a.idForm.busy, a.idForm.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.DeleteIdentity(ctx, accountID, id.ID)
		ids, listErr := a.svc.Identities(ctx, accountID)
		a.post(func() {
			if listErr == nil {
				a.aliases[accountID] = added(ids)
			}
			if a.accountForm.ID != accountID {
				return
			}
			a.idForm.busy = false
			if err != nil {
				a.idForm.err = locales.IdentityRemoveFailed(err.Error())
			}
		})
	})
}

// identityErrorText says why an address was not saved.
func identityErrorText(err error, email string) string {
	switch {
	case errors.Is(err, mail.ErrIdentityAddress):
		return locales.IdentityNotAddress()
	case errors.Is(err, mail.ErrIdentityExists):
		return locales.IdentityExists(strings.TrimSpace(email))
	}
	return locales.IdentitySaveFailed(err.Error())
}

// sendingSections are the account editor's addresses the account sends
// from and its signature. An account being added has neither yet.
func (a *app) sendingSections(c *ui.Context, t *ui.Theme) {
	acc := a.account(a.accountForm.ID)
	if !a.accountForm.editing || acc == nil {
		return
	}
	a.identitiesSection(c, t, *acc)
	a.signatureSection(c, t)
}

// identitiesSection lists the addresses the account sends from, its own
// and those added, with the ways to add, edit and remove one.
func (a *app) identitiesSection(c *ui.Context, t *ui.Theme, acc mail.Account) {
	f := &a.idForm
	a.section(c, locales.IdentitiesSection(), func() {
		ui.Text(c, locales.IdentitiesDetail()).FontSize(12).TextColor(t.TextMuted)
		for _, id := range a.identitiesOf(acc.ID) {
			ui.Column(c.Key("identity/" + id.ID)).Children(func() {
				if f.open && f.id != "" && f.id == id.ID {
					a.identityFields(c, t)
					return
				}
				a.identityRow(c, t, id)
			})
		}
		if f.open && f.id == "" {
			a.identityFields(c, t)
		} else {
			ui.Row(c).Children(func() {
				ui.Button(c, locales.AddIdentity()).Disabled(f.open || f.busy).
					OnClick(func() { a.openIdentity(mail.Identity{}) })
			})
		}
		if f.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
			})
		}
	})
}

// identityRow is an address the account sends from, and for one added,
// the ways to edit and remove it.
func (a *app) identityRow(c *ui.Context, t *ui.Theme, id mail.Identity) {
	f := &a.idForm
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(6, 6, 6, 10).Radius(8).Border(1, t.Border).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			name := id.Name
			if name == "" {
				name = id.Email
			}
			ui.Text(c, name).FontSize(13).Bold().SingleLine()
			ui.Text(c, id.Email).FontSize(12).TextColor(t.TextMuted).SingleLine()
		})
		if id.ID == "" {
			// It changes with the account.
			ui.Text(c, locales.IdentityOwn()).FontSize(11).TextColor(t.TextMuted).Shrink(0).Padding(0, 4)
			return
		}
		ui.Button(c, locales.Edit()).Label(locales.EditIdentity(id.Email)).Disabled(f.open || f.busy).
			OnClick(func() { a.openIdentity(id) })
		ui.Button(c, locales.Remove()).Label(locales.RemoveIdentity(id.Email)).Disabled(f.open || f.busy).
			OnClick(func() { a.removeIdentity(id) })
	})
}

// identityFields is the form of an address: the name recipients see and
// the address.
func (a *app) identityFields(c *ui.Context, t *ui.Theme) {
	f := &a.idForm
	ui.Column(c).Gap(10).Padding(10).Radius(8).Border(1, t.Accent.Alpha(0.6)).Children(func() {
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				// The captions name the inputs, so a click on one focuses
				// its field.
				caption := ui.Text(c, locales.IdentityName()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.TextInput(c, &f.name).Placeholder(locales.IdentityNamePlaceholder()).LabelledBy(caption).
					FillWidth().AutoFocus().OnSubmit(a.saveIdentity)
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				caption := ui.Text(c, locales.IdentityEmail()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.TextInput(c, &f.email).Placeholder("name@example.com").LabelledBy(caption).
					FillWidth().OnSubmit(a.saveIdentity)
			})
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if f.busy {
				ui.Spinner(c).Size(14, 14)
			}
			ui.Spacer(c)
			ui.Button(c, locales.Cancel()).Label(locales.IdentityCancel()).Disabled(f.busy).
				OnClick(func() { a.idForm = identityForm{} })
			ui.PrimaryButton(c, locales.IdentitySave()).Disabled(f.busy || strings.TrimSpace(f.email) == "").
				OnClick(a.saveIdentity)
		})
	})
}

// signatureSection is the account's signature, saved as it changes, and
// the kinds of message it goes into.
func (a *app) signatureSection(c *ui.Context, t *ui.Theme) {
	f := &a.sigForm
	a.section(c, locales.SignatureSection(), func() {
		ui.TextArea(c, &f.sig.Text).Lines(3, 8).FillWidth().Label(locales.SignatureText()).
			Placeholder(locales.SignaturePlaceholder()).OnChange(a.saveSignature)
		ui.Text(c, locales.SignatureDetail()).FontSize(12).TextColor(t.TextMuted)
		ui.Column(c).Gap(10).Padding(2, 0, 0, 0).Children(func() {
			a.switchLine(c, t, locales.SignatureNew(), "", &f.sig.New, false, a.saveSignature)
			a.switchLine(c, t, locales.SignatureReply(), "", &f.sig.Reply, false, a.saveSignature)
			a.switchLine(c, t, locales.SignatureForward(), "", &f.sig.Forward, false, a.saveSignature)
		})
		if f.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
			})
		}
	})
}
