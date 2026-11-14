package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Leaving a mailing list and blocking a sender (issue: list unsubscribe
// and local blocks). Both are the user's choice: nothing is sent before a
// confirmation shows exactly where it goes, a block touches only the mail
// that arrives from then on, and both can be taken back.

// unsubForm is the question before a list is left: the message, what its
// headers offer, and the method and destination the confirmation names.
type unsubForm struct {
	open    bool
	account string
	folder  string
	uid     mail.UID
	method  mail.UnsubscribeMethod
	dest    string
}

// blockState is the Privacy tab's list of blocked senders, read as the
// tab opens, and the question before one is blocked.
type blockState struct {
	loaded bool
	err    string
	blocks map[string][]mail.BlockedSender
	form   blockForm
}

// blockForm is the question before a sender is blocked: whose mail, which
// of its addresses, and where its mail goes.
type blockForm struct {
	open    bool
	account string
	address string
	domain  string
	whole   bool
	folder  string
	busy    bool
	err     string
}

// unsubKey keys what this device kept of leaving lists: one record per
// sender and account.
func unsubKey(accountID, email string) string {
	return accountID + "\x00" + strings.ToLower(strings.TrimSpace(email))
}

// offerOf reads what a message's headers say its list offers.
func offerOf(msg *mail.Message) mail.UnsubscribeOffer {
	if msg == nil {
		return mail.UnsubscribeOffer{}
	}
	list, post := msg.Unsubscribe()
	return mail.ParseUnsubscribeOffer(list, post)
}

// openUnsubscribe asks before a list is left, showing the method and the
// exact destination. The preferred method is the one offered: a one-click
// request, else an email, else the list's page.
func (a *app) openUnsubscribe(m mail.Summary, offer mail.UnsubscribeOffer) {
	method, dest, ok := offer.Preferred()
	if !ok {
		return
	}
	a.unsubForm = unsubForm{
		open: true, account: m.Account, folder: m.Folder, uid: m.UID,
		method: method, dest: dest,
	}
}

// unsubscribeDialog is the question before a list is left, and it is the
// only way a request is made.
func (a *app) unsubscribeDialog(c *ui.Context) {
	f := &a.unsubForm
	if !f.open {
		return
	}
	message, confirm := locales.UnsubscribeOneClick(f.dest), locales.UnsubscribeConfirm()
	switch f.method {
	case mail.UnsubscribeEmail:
		message = locales.UnsubscribeEmail(f.dest)
	case mail.UnsubscribePage:
		message, confirm = locales.UnsubscribePage(f.dest), locales.UnsubscribeOpenPage()
	}
	if ui.AlertDialog(c, &f.open, locales.UnsubscribeTitle(), message, locales.Cancel(), confirm) == 1 {
		a.submitUnsubscribe()
	}
}

// submitUnsubscribe makes the request the user confirmed. A one-click
// request and an email are the engine's; a page the browser opens, so
// that the app itself sends nothing. Either way the choice is kept.
func (a *app) submitUnsubscribe() {
	f := a.unsubForm
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		rec, err := a.svc.Unsubscribe(ctx, f.account, f.folder, f.uid, f.method, f.dest)
		a.post(func() {
			if err == nil && f.method == mail.UnsubscribePage {
				// Opening the page is the action the user confirmed, not a
				// result of the list read that drew the message.
				a.services.OpenURL(f.dest)
			}
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.UnsubscribeFailed(failureText(err))
				return
			}
			if a.lists == nil {
				a.lists = map[string]mail.UnsubscribeRecord{}
			}
			a.lists[unsubKey(rec.AccountID, rec.Email)] = rec
		})
	})
}

// openBlockSender asks before a sender is blocked, with the account's
// Junk folder as where its mail goes when there is one, its Trash
// otherwise.
func (a *app) openBlockSender(m mail.Summary) {
	address := strings.ToLower(strings.TrimSpace(m.From.Email))
	if address == "" {
		return
	}
	folder := a.specialFolder(m.Account, mail.SpecialJunk)
	if folder == "" {
		folder = a.specialFolder(m.Account, mail.SpecialTrash)
	}
	a.blocks.form = blockForm{
		open: true, account: m.Account, address: address,
		domain: mail.AddressDomain(address), folder: folder,
	}
}

// blockSenderDialog asks before a sender is blocked: the exact sender, an
// option to block its whole domain, and the folder its new mail goes to.
// Nothing moves until Block is pressed.
func (a *app) blockSenderDialog(c *ui.Context) {
	f := &a.blocks.form
	if !f.open {
		return
	}
	t := c.Theme()
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(440).Gap(14).Children(func() {
			ui.Text(c, locales.BlockSenderTitle()).FontSize(17).Bold()
			ui.Text(c, locales.BlockSenderBody(f.address)).FontSize(12).TextColor(t.TextMuted).MaxLines(4)
			if f.domain != "" {
				ui.Checkbox(c, &f.whole, locales.BlockWholeDomain(f.domain))
			}
			ui.Column(c).Gap(6).Children(func() {
				caption := ui.Text(c, locales.BlockFolderCaption()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.RadioGroup(c, func() {
					if folder := a.specialFolder(f.account, mail.SpecialJunk); folder != "" {
						ui.Radio(c, &f.folder, folder, locales.BlockToJunk())
					}
					if folder := a.specialFolder(f.account, mail.SpecialTrash); folder != "" {
						ui.Radio(c, &f.folder, folder, locales.BlockToTrash())
					}
				}).LabelledBy(caption)
			})
			if f.folder == "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
					ui.Text(c, locales.BlockNoFolder()).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).
						TextColor(t.TextMuted).Role(ui.RoleStatus)
				})
			}
			if f.err != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
					ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
				})
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.busy {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				ui.Button(c, locales.Cancel()).Label(locales.Cancel()).OnClick(func() { f.open = false })
				ui.PrimaryButton(c, locales.BlockConfirm()).
					Disabled(f.busy || f.folder == "").OnClick(a.confirmBlockSender)
			})
		})
	})
}

// confirmBlockSender keeps the block the user confirmed and offers the
// list's one undo banner to take it back.
func (a *app) confirmBlockSender() {
	f := &a.blocks.form
	if f.busy || f.folder == "" {
		return
	}
	account, value, domain, folder := f.account, f.address, f.whole, f.folder
	if domain {
		value = f.domain
	}
	f.busy = true
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		// A block of the same sender is replaced, so the undo puts back the
		// folder the sender's mail went to before.
		var prev *mail.BlockedSender
		if existing, err := a.svc.BlockedSenders(ctx, account); err == nil {
			for i := range existing {
				if existing[i].Value == value && existing[i].Domain == domain {
					prev = &existing[i]
				}
			}
		}
		b, err := a.svc.BlockSender(ctx, account, value, domain, folder)
		var list []mail.BlockedSender
		if err == nil {
			list, _ = a.svc.BlockedSenders(ctx, account)
		}
		a.post(func() {
			f.busy = false
			if a.stale(g) {
				return
			}
			if err != nil {
				f.err = locales.BlockFailed(failureText(err))
				return
			}
			f.open, f.err = false, ""
			if a.blocks.blocks == nil {
				a.blocks.blocks = map[string][]mail.BlockedSender{}
			}
			a.blocks.blocks[account] = list
			a.setUndo(&undoAction{label: locales.SenderBlocked(), block: &b, prevBlock: prev})
		})
	})
}

// undoBlock takes a block back, and puts back the one it replaced.
func (a *app) undoBlock(u *undoAction) {
	b := u.block
	if b == nil {
		return
	}
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.UnblockSender(ctx, b.AccountID, b.Value, b.Domain)
		if err == nil && u.prevBlock != nil {
			prev := u.prevBlock
			_, err = a.svc.BlockSender(ctx, prev.AccountID, prev.Value, prev.Domain, prev.Folder)
		}
		var list []mail.BlockedSender
		if err == nil {
			list, _ = a.svc.BlockedSenders(ctx, b.AccountID)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.UnblockFailed(failureText(err))
				return
			}
			if a.blocks.blocks != nil {
				a.blocks.blocks[b.AccountID] = list
			}
		})
	})
}

// loadBlocks reads every account's blocked senders for the Privacy tab.
func (a *app) loadBlocks() {
	ids := make([]string, 0, len(a.accounts))
	for _, acc := range a.accounts {
		ids = append(ids, acc.ID)
	}
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		blocks := map[string][]mail.BlockedSender{}
		var err error
		for _, id := range ids {
			var list []mail.BlockedSender
			if list, err = a.svc.BlockedSenders(ctx, id); err != nil {
				break
			}
			blocks[id] = list
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.blocks.loaded = true
			if err != nil {
				a.blocks.err = failureText(err)
				return
			}
			a.blocks.err = ""
			a.blocks.blocks = blocks
		})
	})
}

// unblockSender takes a block away, as the Privacy tab offers; the mail it
// held back stays where it went.
func (a *app) unblockSender(b mail.BlockedSender) {
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.UnblockSender(ctx, b.AccountID, b.Value, b.Domain)
		var list []mail.BlockedSender
		if err == nil {
			list, _ = a.svc.BlockedSenders(ctx, b.AccountID)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.blocks.err = locales.UnblockFailed(failureText(err))
				return
			}
			a.blocks.err = ""
			if a.blocks.blocks != nil {
				a.blocks.blocks[b.AccountID] = list
			}
		})
	})
}

// blockLabel names a block: an address, or a whole domain after its '@'.
func blockLabel(b mail.BlockedSender) string {
	if b.Domain {
		return "@" + b.Value
	}
	return b.Value
}

// privacySettingsView draws the Privacy tab: the blocked senders of every
// account, and the way to unblock each.
func (a *app) privacySettingsView(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, shieldIcon).FontSize(20).TextColor(t.Accent)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, locales.BlockedSenders()).FontSize(15).Bold()
			ui.Text(c, locales.BlockedSendersHint()).FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		})
	})
	if a.blocks.err != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, a.blocks.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
		})
	}
	if !a.blocks.loaded {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.Loading()).FontSize(13).TextColor(t.TextMuted)
		})
		return
	}
	any := false
	for _, acc := range a.accounts {
		list := a.blocks.blocks[acc.ID]
		if len(list) == 0 {
			continue
		}
		any = true
		ui.Text(c, acc.Email).FontSize(11).Bold().TextColor(t.TextMuted)
		for _, b := range list {
			b := b
			ui.Row(c.Key(acc.ID+"\x00"+blockLabel(b))).Gap(10).AlignItems(ui.Center).Padding(8, 10).Radius(8).
				Border(1, t.Border).Children(func() {
				ui.Icon(c, spamIcon).FontSize(14).TextColor(t.TextMuted)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, blockLabel(b)).FontSize(13).MaxLines(1).Selectable()
					ui.Text(c, locales.BlockedFiledTo(a.folderLabelOf(acc.ID, b.Folder))).
						FontSize(11).TextColor(t.TextMuted)
				})
				ui.Button(c, locales.Unblock()).OnClick(func() { a.unblockSender(b) })
			})
		}
	}
	if !any {
		ui.Text(c, locales.NoBlockedSenders()).FontSize(12).TextColor(t.TextMuted)
	}
}
