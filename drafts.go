package main

import (
	"errors"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// draftsFolder is the sidebar's Drafts entry. Like Starred it is no folder
// of the server: it lists the drafts on this device, and with them the
// server's own when it has a Drafts folder. So drafts written offline, or
// for a server without that folder, have a place to be found.
const draftsFolder = "drafts"

// draftSync is one account's draft sync: whether one runs, whether
// something changed that it may have missed, and whether the last one
// failed to reach the server, so that drafts wait for a connection.
type draftSync struct {
	running bool
	again   bool
	// againFull says the sync asked for meanwhile is one that takes the
	// drafts with large attachments too.
	againFull bool
	waiting   bool
}

// localDrafts counts the rows that are drafts on this device rather than
// messages of the server.
func localDrafts(list []mail.Summary) int {
	n := 0
	for _, m := range list {
		if m.DraftID != "" {
			n++
		}
	}
	return n
}

// syncDrafts uploads an account's drafts and applies what other mail
// clients did to them. It runs when a compose window closes and with
// every account sync. One sync runs at a time; a request made meanwhile
// runs it once more.
func (a *app) syncDrafts(accountID string) { a.runDraftSync(accountID, true) }

// syncDraftEdits is the sync that runs after edits settle. A draft with
// large attachments would be uploaded whole at every pause in the typing,
// so this one leaves those for syncDrafts.
func (a *app) syncDraftEdits(accountID string) { a.runDraftSync(accountID, false) }

func (a *app) runDraftSync(accountID string, full bool) {
	if a.account(accountID) == nil {
		return
	}
	state := a.draftSyncs[accountID]
	if state == nil {
		state = &draftSync{}
		a.draftSyncs[accountID] = state
	}
	if state.running {
		state.again = true
		state.againFull = state.againFull || full
		return
	}
	state.running = true
	// The server's own drafts are cached as any folder's mail is, when the
	// folder is the one on show.
	showing := a.active == accountID && a.folder == draftsFolder
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			result mail.DraftSyncResult
			err    error
		)
		if full {
			result, err = a.svc.SyncDrafts(ctx, accountID)
		} else {
			result, err = a.svc.SyncDraftEdits(ctx, accountID)
		}
		if err == nil && result.Folder != "" && showing {
			_ = a.svc.SyncFolder(ctx, accountID, result.Folder, 0)
		}
		a.post(func() {
			state.running = false
			// A failure is not put in the list's banner: the account sync
			// reports an unreachable server, and each compose window says
			// that its draft waits.
			state.waiting = err != nil
			for _, cs := range a.composes {
				if cs.accountID == accountID {
					cs.draftSynced(result, err)
				}
			}
			a.reloadDrafts(accountID)
			if state.again {
				again := state.againFull
				state.again, state.againFull = false, false
				a.runDraftSync(accountID, again)
			}
		})
	})
}

// networkBack runs when the computer is online again: the accounts whose
// drafts could not be synced try once more. Being online does not prove
// the server answers; an account that fails again goes on waiting.
func (a *app) networkBack() {
	for _, acc := range a.accounts {
		if state := a.draftSyncs[acc.ID]; state != nil && state.waiting {
			a.syncDrafts(acc.ID)
		}
	}
}

// reloadDrafts reads the Drafts folder again if it is the one on show. It
// runs at every save of a compose window, so it shows no progress.
func (a *app) reloadDrafts(accountID string) {
	if a.active == accountID && a.folder == draftsFolder {
		a.readMessages(a.gen, false)
	}
}

// loadDraftBody shows a draft of this device in the reading pane. It is
// read again each time, as a compose window may be changing it.
func (a *app) loadDraftBody(g uint64, m mail.Summary) {
	key := summaryKey(m)
	a.bodyErr = ""
	// What is on show stays until the draft is read, so that the pane
	// does not blink at every keystroke of a compose window.
	_, shown := a.bodies[key]
	a.bodyLocked = !shown
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		d, found, err := a.svc.Draft(ctx, m.DraftID)
		a.post(func() {
			if a.stale(g) || a.selectedKey() != key {
				return
			}
			a.bodyLocked = false
			switch {
			case err != nil:
				a.bodyErr = locales.BodyFailed(err.Error())
			case !found:
				// Sent or discarded meanwhile: its row goes with the next
				// read of the list.
				delete(a.bodies, key)
			default:
				a.bodies[key] = mail.FetchResult{Message: mail.Message{
					Summary: m, BodyText: d.Body, BodyCached: true, Attachments: d.Attachments,
				}}
			}
		})
	})
}

// editDraft resumes the draft on show in a compose window. A draft
// another mail client wrote becomes a draft of this device first, linked
// to its message on the server, so that editing it here replaces that
// message rather than adding one.
func (a *app) editDraft() {
	m, _ := a.current()
	if m == nil || a.folder != draftsFolder {
		return
	}
	summary := *m
	if cs := a.composeOf(summary.DraftID); cs != nil {
		cs.focus()
		return
	}
	accountID := summary.Account
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			d     mail.Draft
			found bool
			err   error
		)
		if summary.DraftID != "" {
			d, found, err = a.svc.Draft(ctx, summary.DraftID)
		} else {
			d, err = a.svc.AdoptDraft(ctx, accountID, summary.Folder, summary.UID)
			found = err == nil
		}
		a.post(func() {
			a.draftErr, a.draftErrKey = "", ""
			switch {
			case errors.Is(err, mail.ErrDraftHasAttachments):
				a.draftErr, a.draftErrKey = locales.DraftHasAttachments(), summaryKey(summary)
			case err != nil:
				a.draftErr, a.draftErrKey = locales.DraftOpenFailed(err.Error()), summaryKey(summary)
				if errors.Is(err, mail.ErrNotFound) {
					// The server no longer has it: another mail client
					// sent or deleted it. A sync takes its row away.
					a.syncDrafts(accountID)
				}
			case found:
				if summary.DraftID == "" {
					a.adoptRow(summary, d)
				}
				a.resumeDraft(d)
			}
			// An adopted draft is a row of another kind now, and one that
			// was not found is no row at all.
			if summary.DraftID == "" || !found {
				a.reloadDrafts(accountID)
			}
		})
	})
}

// adoptRow turns the row of a server draft into the row of the draft it
// was adopted as, so that it stays the one chosen when the list is read
// again under its new identity.
func (a *app) adoptRow(server mail.Summary, d mail.Draft) {
	if a.active != d.AccountID || a.folder != draftsFolder {
		return
	}
	old := summaryKey(server)
	for i := range a.all {
		if summaryKey(a.all[i]) != old {
			continue
		}
		a.all[i].DraftID = d.ID
		if a.sel.Has(old) {
			a.sel.Remove(old)
			a.sel.Add(summaryKey(a.all[i]))
		}
		return
	}
}

// openSelected is a double click or Enter on a row. Only a draft has
// something to open: other mail is read in the pane beside the list.
func (a *app) openSelected() {
	if a.folder == draftsFolder {
		a.editDraft()
	}
}

// deleteSelected is the Delete key: it discards drafts of this device and
// moves any other mail to the trash.
func (a *app) deleteSelected() {
	targets := a.targets()
	if n := localDrafts(targets); n > 0 && n == len(targets) {
		a.discardSelected()
		return
	}
	a.moveSelected(mail.SpecialTrash)
}

// discardSelected throws away the chosen drafts of this device, after
// asking.
func (a *app) discardSelected() {
	targets := a.targets()
	ids := make([]string, 0, len(targets))
	for _, m := range targets {
		// A message of the server among them: Delete moves those, and one
		// action does not do both. Nor does one discard reach two
		// accounts.
		if m.DraftID == "" || m.Account != targets[0].Account {
			return
		}
		ids = append(ids, m.DraftID)
	}
	if len(ids) == 0 {
		return
	}
	a.discardDrafts(targets[0].Account, ids, a.win, func(err error) {
		a.err = locales.DraftDiscardFailed(err.Error())
	})
}

// discardDrafts deletes drafts from this device and from the server, once
// the user has confirmed. Their compose windows close without saving.
// With no way to ask, nothing is deleted.
func (a *app) discardDrafts(accountID string, ids []string, parent *mygo.Window, failed func(error)) {
	confirm := a.confirmDiscard
	if len(ids) == 0 || confirm == nil {
		return
	}
	var open []*composeState
	for _, id := range ids {
		if cs := a.composeOf(id); cs != nil {
			open = append(open, cs)
		}
	}
	a.run(func() {
		if !confirm(parent, len(ids)) {
			return
		}
		for _, cs := range open {
			// A save in flight finishes first, and none starts after:
			// the draft must not come back.
			cs.saveMu.Lock()
			cs.gone.Store(true)
			cs.saveMu.Unlock()
		}
		// The windows and the rows go at once. Removing the server's copy
		// takes as long as the server does, and nobody should watch that.
		a.post(func() {
			for _, cs := range open {
				cs.close()
			}
			if a.active == accountID && a.folder == draftsFolder {
				a.dropDraftRows(ids)
			}
		})
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, id := range ids {
			if e := a.svc.DiscardDraft(ctx, id); e != nil && err == nil {
				err = e
			}
		}
		a.post(func() {
			if err != nil {
				failed(err)
			}
			// What the engine kept, after a failure, shows again.
			a.reloadDrafts(accountID)
		})
	})
}

// dropDraftRows takes discarded drafts out of the list on show, leaving
// the choice on the row that follows them, as archiving a message does.
func (a *app) dropDraftRows(ids []string) {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := make([]mail.Summary, 0, len(a.all))
	for _, m := range a.all {
		if m.DraftID == "" || !gone[m.DraftID] {
			kept = append(kept, m)
		}
	}
	if len(kept) == len(a.all) {
		return
	}
	a.all = kept
	a.sel.Clear()
	if a.selected >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	if a.selected >= 0 {
		a.sel.Add(summaryKey(a.all[a.selected]))
	}
}

// askDiscard is the native confirmation before drafts are deleted. It
// blocks until answered, so it is called from a job, never from a view.
func askDiscard(parent *mygo.Window, count int) bool {
	res, err := mygo.Dialog.Message(mygo.MessageOptions{
		Parent: parent, Type: mygo.MessageWarning,
		Title: locales.DiscardDraftTitle(count), Message: locales.DiscardDraftConfirm(),
		Buttons:       []string{locales.Cancel(), locales.DiscardDraft()},
		DefaultButton: 0, CancelButton: 0, DestructiveButtons: []int{1},
	})
	return err == nil && res.Button == 1
}

// draftBanner says the message on show is a draft, and offers to go on
// writing it.
func (a *app) draftBanner(c *ui.Context, t *ui.Theme, m mail.Summary) {
	text := locales.DraftNotSent()
	if m.DraftID == "" {
		text = locales.DraftOnServer()
	}
	ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Accent.Alpha(0.07)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Icon(c, draftIcon).FontSize(14).TextColor(t.Accent)
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).MaxLines(2)
		ui.PrimaryButton(c, locales.EditDraft()).OnClick(a.editDraft)
	})
	// Why this draft did not open is said here, beside the draft, rather
	// than in the list's banner, whose Retry would sync the account.
	if a.draftErr != "" && a.draftErrKey == summaryKey(m) {
		ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
			Background(t.Warning.Alpha(0.12)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, a.draftErr).FontSize(12).Grow(1).MinWidth(0).MaxLines(4)
		})
	}
}
