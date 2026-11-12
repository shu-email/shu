package main

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// draftSyncDelay is how long the typing must pause before the draft is
// uploaded. Saving happens at every change; uploading replaces a message
// on the server each time, so it waits for a pause.
const draftSyncDelay = 3 * time.Second

// composeState is one compose window.
type composeState struct {
	app *app
	// accountID is the account the message is written from: the one
	// active as the window opened, or the draft's. The draft is stored
	// under it, so switching accounts in the mail window does not move
	// the message to another one.
	accountID string

	to      string
	cc      string
	bcc     string
	subject string
	body    string
	// showBcc keeps the Bcc field of a draft that came with one, also
	// while it is being emptied.
	showBcc bool

	inReplyTo  string
	references []string

	// The window's draft. draftID is empty until the window holds
	// something to keep. Every change saves in a job of its own: saveMu
	// makes the jobs take turns, saveSeq lets only the newest write, and
	// gone stops them once the draft was sent or discarded, so that a
	// save still waiting cannot bring it back.
	draftID   string
	saveMu    sync.Mutex
	saveSeq   atomic.Uint64
	gone      atomic.Bool
	draft     draftStatus
	syncTimer *time.Timer

	// source is the message being replied to, which an AI draft reads;
	// nil for a new message or a forward. guidance is what the user asks
	// the draft to say, job the draft in progress and review its payload
	// under inspection.
	source   *ai.Message
	guidance string
	job      *aiJob
	review   *aiReview

	sending bool
	sent    bool
	err     string
	note    string
	closed  bool

	win    *mygo.Window
	ctx    context.Context
	cancel context.CancelFunc
}

// draftStatus is what a compose window knows of where its draft is, for
// the status line.
type draftStatus struct {
	// revision is the last one saved and synced the last one the server
	// has. The draft is on the server while they are equal. They count
	// the saves of the message with this Message-ID.
	revision  int64
	synced    int64
	messageID string
	// offline says the last sync did not reach the server; localOnly that
	// it did, and found no Drafts folder to upload to.
	offline   bool
	localOnly bool
	// conflict says another mail client changed the draft, and that this
	// version was kept beside it.
	conflict bool
	// err is a save that failed.
	err string
}

// openReply opens a reply to a message, which an AI draft may read.
func (a *app) openReply(m mail.Summary, body string, all bool) {
	cs := a.openCompose(replyDraft(a, m, body, all, false))
	if strings.TrimSpace(body) != "" {
		source := a.aiMessage(m, body)
		cs.source = &source
	}
}

// openCompose opens a compose window for a message to write, or an empty
// one.
func (a *app) openCompose(out *mail.Outgoing) *composeState {
	cs := &composeState{app: a, accountID: a.active}
	if out != nil {
		cs.to = addressesText(out.To)
		cs.cc = addressesText(out.Cc)
		cs.subject = out.Subject
		cs.body = out.Text
		cs.inReplyTo = out.InReplyTo
		cs.references = append([]string(nil), out.References...)
	}
	return a.showCompose(cs)
}

// resumeDraft opens a draft for editing, every field as it was left. A
// draft already open in a window gets that window, not a second editor.
func (a *app) resumeDraft(d mail.Draft) *composeState {
	if cs := a.composeOf(d.ID); cs != nil {
		cs.focus()
		return cs
	}
	cs := &composeState{
		app: a, accountID: d.AccountID, draftID: d.ID,
		to: d.To, cc: d.Cc, bcc: d.Bcc, subject: d.Subject, body: d.Body,
		showBcc:    d.Bcc != "",
		inReplyTo:  d.InReplyTo,
		references: append([]string(nil), d.References...),
	}
	cs.noteDraft(d)
	return a.showCompose(cs)
}

// focus brings the window to the front.
func (cs *composeState) focus() {
	if cs.win != nil {
		cs.win.Focus()
	}
}

// composeOf returns the open window editing a draft, or nil.
func (a *app) composeOf(draftID string) *composeState {
	if draftID == "" {
		return nil
	}
	for _, cs := range a.composes {
		if cs.draftID == draftID && !cs.closed {
			return cs
		}
	}
	return nil
}

// showCompose opens the window of a compose state.
func (a *app) showCompose(cs *composeState) *composeState {
	cs.ctx, cs.cancel = context.WithCancel(context.Background())
	a.composes = append(a.composes, cs)
	if a.newWindow == nil {
		return cs
	}
	title := locales.ComposeTitle()
	lower := strings.ToLower(strings.TrimSpace(cs.subject))
	switch {
	case strings.HasPrefix(lower, "fwd:"):
		title = locales.ForwardTitle()
	case strings.HasPrefix(lower, "re:"):
		title = locales.ReplyTitle()
	}
	win := a.newWindow(mygo.WindowOptions{
		Title:           title,
		Width:           720,
		Height:          620,
		MinWidth:        520,
		MinHeight:       420,
		BackgroundColor: "light-dark(#ffffff, #18181b)",
		Content:         ui.View(cs.view),
	})
	if win != nil {
		cs.win = win
		win.OnClosed(cs.close)
	}
	return cs
}

// addressesText joins addresses for a text field.
func addressesText(list []mail.Address) string {
	if len(list) == 0 {
		return ""
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.String())
	}
	return strings.Join(out, ", ")
}

// view draws the compose window.
func (cs *composeState) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Background).Padding(18, 20).Gap(10).
		OnShortcut(ui.Cmd, ui.KeyEnter, cs.send).
		OnShortcut(0, ui.KeyEscape, cs.close).Children(func() {
		cs.row(c, locales.To(), "name@example.com", &cs.to)
		cs.row(c, locales.Cc(), "", &cs.cc)
		if cs.showBcc {
			// Compose has no way to add a Bcc yet, but one that came with
			// a draft must show: it will be sent to.
			cs.row(c, locales.Bcc(), "", &cs.bcc)
		}
		cs.row(c, locales.Subject(), "", &cs.subject)
		if cs.source != nil && cs.app.ai != nil {
			cs.draftRow(c, t)
		}
		ui.Divider(c)
		// The body takes the room there is. Its minimum leaves the notices
		// and the actions below it on screen at the window's smallest.
		ui.TextArea(c, &cs.body).Grow(1).MinHeight(120).Label(locales.Body()).
			Placeholder(locales.ComposeBodyPlaceholder()).OnChange(cs.changed)
		if cs.draft.conflict {
			cs.conflictNotice(c, t)
		}
		if cs.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, cs.err).FontSize(12).TextColor(t.Text).MaxLines(3).Grow(1)
			})
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			// Closing the window keeps the draft; this is the way to
			// throw it away.
			ui.Button(c, locales.DiscardDraft()).Disabled(cs.sending).OnClick(cs.discard)
			cs.statusLine(c, t)
			key := "Ctrl+Enter"
			if runtime.GOOS == "darwin" {
				key = "⌘↩"
			}
			ui.Text(c, locales.SendHint(key)).FontSize(11).TextColor(t.TextMuted).Shrink(0)
			if cs.note != "" {
				ui.Text(c, cs.note).FontSize(12).TextColor(t.TextMuted)
			}
			label := locales.Send()
			if cs.sending {
				label = locales.Sending()
			}
			ui.PrimaryButton(c, label).Label(locales.Send()).Disabled(cs.sending).
				OnClick(func() { cs.send() })
		})
	})
	aiReviewModal(c, &cs.review)
}

// row is a compact compose field with its label inline, as mail apps
// show recipients and the subject.
func (cs *composeState) row(c *ui.Context, label, placeholder string, value *string) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		// The caption names the input, so a click on it focuses the field.
		caption := ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(52)
		ui.TextInput(c, value).Grow(1).LabelledBy(caption).Placeholder(placeholder).OnChange(cs.changed)
	})
}

// statusLine says quietly where the draft is: on this device, on the
// server too, or waiting for a connection.
func (cs *composeState) statusLine(c *ui.Context, t *ui.Theme) {
	text, icon, color := cs.draftNote(t)
	ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
		if text == "" {
			return
		}
		ui.Icon(c, icon).FontSize(12).TextColor(color)
		// It is no live region: it changes with every pause in the
		// typing, which a screen reader should not read out each time.
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
	})
}

// draftNote is the status line's text, with its icon.
func (cs *composeState) draftNote(t *ui.Theme) (string, *ui.SVG, ui.Color) {
	d := cs.draft
	switch {
	case d.err != "":
		return locales.DraftSaveFailed(d.err), alertIcon, t.Warning
	case cs.draftID == "" || d.revision == 0:
		return "", nil, t.TextMuted
	case d.synced >= d.revision:
		return locales.DraftSynced(), checkIcon, t.Success
	case d.localOnly:
		return locales.DraftLocalOnly(), draftIcon, t.TextMuted
	case d.offline:
		return locales.DraftOffline(), draftIcon, t.TextMuted
	}
	return locales.DraftSaved(), draftIcon, t.TextMuted
}

// conflictNotice says that another mail client changed the draft, and
// that nothing was overwritten.
func (cs *composeState) conflictNotice(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(8).
		Background(t.Warning.Alpha(0.12)).Children(func() {
		ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		// A status, so that a screen reader says it once as it appears.
		ui.Text(c, locales.DraftConflict()).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).
			Role(ui.RoleStatus)
		cs.app.iconButton(c, closeIcon, locales.DraftConflictDismiss(), locales.DraftConflictDismiss()).
			OnClick(cs.dismissConflict)
	})
}

// empty reports a window with nothing written in it.
func (cs *composeState) empty() bool {
	for _, field := range []string{cs.to, cs.cc, cs.bcc, cs.subject, cs.body} {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

// snapshot is the draft as the window holds it now.
func (cs *composeState) snapshot() mail.Draft {
	return mail.Draft{
		ID: cs.draftID, AccountID: cs.accountID,
		To: cs.to, Cc: cs.cc, Bcc: cs.bcc, Subject: cs.subject, Body: cs.body,
		InReplyTo:  cs.inReplyTo,
		References: append([]string(nil), cs.references...),
	}
}

// changed saves the draft after an edit and plans its upload. A window
// that never held anything leaves no draft.
func (cs *composeState) changed() {
	if cs.closed || cs.sent || cs.gone.Load() {
		return
	}
	if cs.draftID == "" {
		if cs.empty() {
			return
		}
		// The ID is made here, not by the first save, so that the saves
		// queued behind it write the same draft.
		cs.draftID = mail.NewDraftID()
	}
	cs.save(nil)
	cs.scheduleSync()
}

// save writes the draft as it is now, without the network. Each change
// saves in a job of its own, so that typing never waits. The jobs take
// turns, and one that a later save has overtaken does not write: the
// last thing typed is the last thing saved. then runs once the save is
// in.
func (cs *composeState) save(then func()) {
	a := cs.app
	d := cs.snapshot()
	seq := cs.saveSeq.Add(1)
	a.run(func() {
		cs.saveMu.Lock()
		if cs.saveSeq.Load() != seq || cs.gone.Load() {
			cs.saveMu.Unlock()
			return
		}
		ctx, cancel := a.ctx()
		saved, err := a.svc.SaveDraft(ctx, d)
		cancel()
		cs.saveMu.Unlock()
		a.post(func() {
			if cs.gone.Load() {
				return
			}
			if err != nil {
				cs.draft.err = err.Error()
				cs.redraw()
				return
			}
			cs.draft.err = ""
			cs.noteDraft(saved)
			a.reloadDrafts(cs.accountID)
			cs.redraw()
			if then != nil {
				then()
			}
		})
	})
}

// noteDraft takes a draft row's sync state for the status line. Results
// arrive out of order, and a revision never goes back, except that a
// draft kept apart from another client's version is a new message, whose
// count starts again.
func (cs *composeState) noteDraft(d mail.Draft) {
	if d.MessageID != cs.draft.messageID {
		cs.draft.messageID = d.MessageID
		cs.draft.revision, cs.draft.synced = d.Revision, d.SyncedRevision
	} else {
		cs.draft.revision = max(cs.draft.revision, d.Revision)
		cs.draft.synced = max(cs.draft.synced, d.SyncedRevision)
	}
	cs.draft.conflict = d.Conflict
}

// scheduleSync uploads the draft once the typing has paused.
func (cs *composeState) scheduleSync() {
	a := cs.app
	if cs.syncTimer != nil {
		cs.syncTimer.Stop()
		cs.syncTimer = nil
	}
	if a.after == nil {
		return
	}
	seq := cs.saveSeq.Load()
	cs.syncTimer = a.after(draftSyncDelay, func() {
		a.post(func() {
			// A timer that fired although a later edit replaced it gives
			// way to that edit's.
			if cs.closed || cs.gone.Load() || cs.saveSeq.Load() != seq {
				return
			}
			a.syncDrafts(cs.accountID)
		})
	})
}

// draftSynced updates the window after a sync of its account's drafts.
func (cs *composeState) draftSynced(result mail.DraftSyncResult, syncErr error) {
	if cs.draftID == "" || cs.closed || cs.gone.Load() {
		return
	}
	a := cs.app
	id := cs.draftID
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		d, found, err := a.svc.Draft(ctx, id)
		a.post(func() {
			if cs.closed || cs.gone.Load() || cs.draftID != id {
				return
			}
			cs.draft.offline = syncErr != nil
			cs.draft.localOnly = syncErr == nil && result.Folder == ""
			switch {
			case err != nil:
			case !found:
				// Another mail client changed or deleted the draft, and
				// the sync let it go: it had no edits to lose. The window
				// still shows the text, though, so it is saved again, as
				// a draft of its own, and uploaded beside the other one.
				cs.save(func() { a.syncDrafts(cs.accountID) })
			default:
				cs.noteDraft(d)
			}
			cs.redraw()
		})
	})
}

// dismissConflict closes the conflict notice for good.
func (cs *composeState) dismissConflict() {
	cs.draft.conflict = false
	a := cs.app
	id := cs.draftID
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_ = a.svc.DismissDraftConflict(ctx, id)
	})
}

// send validates and sends the draft. The window stays open with an
// inline error when the server refuses.
func (cs *composeState) send() {
	if cs.sending || cs.closed {
		return
	}
	to, err := parseRecipients(cs.to)
	if err != nil {
		cs.err = locales.SendFailed(err.Error())
		return
	}
	if len(to) == 0 {
		cs.err = locales.SendFailed(locales.NoRecipients())
		return
	}
	cc, err := parseRecipients(cs.cc)
	if err != nil {
		cs.err = locales.SendFailed(err.Error())
		return
	}
	bcc, err := parseRecipients(cs.bcc)
	if err != nil {
		cs.err = locales.SendFailed(err.Error())
		return
	}
	out := mail.Outgoing{
		To: to, Cc: cc, Bcc: bcc, Subject: strings.TrimSpace(cs.subject), Text: cs.body,
		InReplyTo: cs.inReplyTo, References: cs.references,
		DraftID: cs.draftID,
	}
	cs.sending = true
	cs.err = ""
	cs.note = ""
	accountID := cs.accountID
	app := cs.app
	draft := cs.snapshot()
	// The saves still waiting give way to the one below.
	seq := cs.saveSeq.Add(1)
	app.run(func() {
		ctx, cancel := context.WithTimeout(cs.ctx, 60*time.Second)
		defer cancel()
		cs.saveMu.Lock()
		if draft.ID != "" && cs.saveSeq.Load() == seq {
			// The draft holds what is being sent before it goes, so that
			// a message the server refuses is still a draft. Closing the
			// window cancels the send, not this.
			saveCtx, saveCancel := app.ctx()
			_, _ = app.svc.SaveDraft(saveCtx, draft)
			saveCancel()
		}
		result, err := app.svc.Send(ctx, accountID, out)
		if err == nil {
			// The engine removed the draft with the send.
			cs.gone.Store(true)
		}
		cs.saveMu.Unlock()
		app.post(func() {
			cs.sending = false
			if err != nil {
				cs.err = locales.SendFailed(err.Error())
				app.loadPending(app.gen)
				// The failure belongs to this window, which the mail
				// window's update does not redraw.
				cs.redraw()
				return
			}
			cs.sent = true
			if result.Warning != nil {
				cs.note = locales.SentCopyWarning()
				app.err = cs.note
			}
			app.loadMessages(app.gen)
			app.loadPending(app.gen)
			cs.close()
		})
	})
}

// parseRecipients parses one text field of addresses.
func parseRecipients(s string) ([]mail.Address, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return mail.ParseAddressList([]string{s})
}

// discard throws the draft away after asking, and closes the window.
func (cs *composeState) discard() {
	if cs.closed || cs.sending {
		return
	}
	if cs.draftID == "" {
		// Nothing was written, so there is nothing to ask about.
		cs.close()
		return
	}
	a := cs.app
	a.discardDrafts(cs.accountID, []string{cs.draftID}, cs.win, func(err error) {
		// The window is closed by now; the mail window says what failed.
		a.err = locales.DraftDiscardFailed(err.Error())
	})
}

// close cancels an in-flight send and closes the window. What was
// written stays as a draft: closing asks nothing.
func (cs *composeState) close() {
	if cs.closed {
		return
	}
	cs.closed = true
	cs.cancel()
	if cs.syncTimer != nil {
		cs.syncTimer.Stop()
		cs.syncTimer = nil
	}
	cs.keepDraft()
	if cs.win != nil {
		cs.win.Close()
	}
}

// keepDraft is the last save of a closing window, and the upload that
// follows it. A draft emptied again has nothing to keep and goes.
func (cs *composeState) keepDraft() {
	if cs.draftID == "" || cs.gone.Load() {
		return
	}
	a := cs.app
	accountID := cs.accountID
	draft := cs.snapshot()
	empty := cs.empty()
	seq := cs.saveSeq.Add(1)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		cs.saveMu.Lock()
		switch {
		case cs.gone.Load():
		case empty:
			cs.gone.Store(true)
			_ = a.svc.DiscardDraft(ctx, draft.ID)
		case cs.saveSeq.Load() == seq:
			_, _ = a.svc.SaveDraft(ctx, draft)
		}
		cs.saveMu.Unlock()
		a.post(func() {
			a.reloadDrafts(accountID)
			if !empty {
				a.syncDrafts(accountID)
			}
		})
	})
}
