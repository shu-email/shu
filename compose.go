package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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
	// active as the window opened, the answered message's, or the
	// draft's. The draft is stored
	// under it, so switching accounts in the mail window does not move
	// the message to another one.
	accountID string

	to      string
	cc      string
	bcc     string
	subject string
	body    string
	// showBcc shows the Bcc field, which stays folded away until the user
	// asks for it, or a draft comes with one; then it stays, also while
	// it is being emptied.
	showBcc bool
	// identity is the identity the message goes from, by ID: "" for the
	// account's own address. from is the From picker's choice, which
	// names it.
	identity string
	from     string
	// startBody is the body the window opened with, as the signature of
	// a new message: it is nothing the user wrote. caret is where the
	// body's caret goes as the window opens, in runes, until it is there,
	// and -1 then; lead is the body before it, which a reply written
	// below its quote starts with.
	startBody string
	caret     int
	lead      string
	// bodyFocused is whether the body had the keyboard in the last
	// build, to tell when it comes to it.
	bodyFocused bool

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

	// attachments is what the stored draft has attached, as the window
	// last heard. Only the engine changes a draft's files: attaching and
	// removing are jobs, which take turns with the saves, and the window
	// takes the list each one returns, the newest revision's winning.
	// removing is the files taken off here whose job has not answered,
	// which an older list must not bring back; attaching counts the
	// files being copied in.
	attachments []mail.Attachment
	attachRev   int64
	attachMsgID string
	removing    map[string]bool
	attaching   int
	picking     bool
	// focused is the control the keyboard is on, among the fields of
	// recipients and the body, the attach button and the attachments.
	focused string
	// rcpt is what the window keeps of the fields of recipients: the
	// contacts offered as they are typed, and what a send found in them
	// (recipients.go).
	rcpt recipientState

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
	// needsSignIn says the last send failed because the account has to
	// sign in again, which the window then offers.
	needsSignIn bool
	// A send under way: how much of the message has gone, of how much,
	// and the way to stop it.
	sendSent   int64
	sendTotal  int64
	sendCancel context.CancelFunc
	cancelling bool

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
	// offline says the last sync did not reach the server, and signIn
	// that it was because the account has to sign in again; localOnly that
	// it did, and found no Drafts folder to upload to.
	offline   bool
	signIn    bool
	localOnly bool
	// conflict says another mail client changed the draft, and that this
	// version was kept beside it.
	conflict bool
	// err is a save that failed.
	err string
}

// openReply opens a reply to a message, which an AI draft may read. The
// message's account writes it.
func (a *app) openReply(m mail.Message, all bool) {
	out, caret := a.replyDraft(m, all, false)
	cs := a.composeFrom(m.Account, out, caret)
	if strings.TrimSpace(m.BodyText) != "" {
		source := a.aiMessage(m.Summary, m.BodyText)
		cs.source = &source
	}
}

// openForward opens a forward of a message, with the message's
// attachments where the settings say so.
func (a *app) openForward(m mail.Message) {
	out, caret := a.replyDraft(m, false, true)
	cs := a.composeFrom(m.Account, out, caret)
	if a.composeSet.ForwardAttachments && len(m.Attachments) > 0 {
		cs.attachOriginal(m)
	}
}

// openCompose opens a compose window from the account on show for a
// message to write, or for a new one, which starts with the account's
// signature where it signs new mail.
func (a *app) openCompose(out *mail.Outgoing) *composeState {
	if out == nil {
		body := mail.NewBody(a.signatureOf(a.active))
		return a.composeFrom(a.active, &mail.Outgoing{Text: body.Text}, body.Caret)
	}
	return a.composeFrom(a.active, out, -1)
}

// composeFrom opens a compose window writing from an account, with the
// body's caret at caret, in runes, or where the input puts it for -1.
// The keyboard starts where there is something to write: the
// recipients, or the text of a message whose recipients are known.
func (a *app) composeFrom(accountID string, out *mail.Outgoing, caret int) *composeState {
	cs := &composeState{app: a, accountID: accountID, caret: caret, focused: focusTo}
	if out != nil {
		cs.to = addressesText(out.To)
		cs.cc = addressesText(out.Cc)
		cs.bcc = addressesText(out.Bcc)
		cs.showBcc = cs.bcc != ""
		cs.subject = out.Subject
		cs.body = out.Text
		cs.identity = out.Identity
		cs.inReplyTo = out.InReplyTo
		cs.references = append([]string(nil), out.References...)
	}
	cs.startBody = cs.body
	if body := []rune(cs.body); caret >= 0 && caret <= len(body) {
		cs.lead = string(body[:caret])
	}
	if strings.TrimSpace(cs.to) != "" {
		cs.focused = focusBody
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
		identity:   d.Identity,
		caret:      -1,
		inReplyTo:  d.InReplyTo,
		references: append([]string(nil), d.References...),
	}
	cs.noteDraft(d)
	cs.takeAttachments(d)
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
	root := ui.Column(c).Fill().Background(t.Background).Padding(18, 20).Gap(10).
		OnShortcut(ui.Cmd, ui.KeyEnter, cs.send).
		OnShortcut(ui.Cmd|ui.Shift, ui.KeyA, cs.pickFiles).
		OnShortcut(0, ui.KeyEscape, cs.close)
	// The whole window takes files dropped on it, and says so while they
	// are over it. A message on its way takes none.
	dragOver := false
	if cs.canAttach() {
		if files := root.DroppedFiles(); files != nil {
			cs.attach(files)
		}
		dragOver = root.FileDragOver()
	}
	hasFiles := len(cs.attachments) > 0 || cs.attaching > 0
	ids := cs.app.identitiesOf(cs.accountID)
	drafting := cs.source != nil && cs.app.ai != nil
	root.Children(func() {
		// The sender is one of the account's addresses, the account's own
		// first; it stays on show with one, so the window says whom the
		// message goes from.
		if len(ids) > 0 {
			cs.fromRow(c, t, ids)
		}
		var fields [recipientFieldCount]ui.Element
		fields[fieldTo] = cs.recipientRow(c, fieldTo, locales.To(), "name@example.com", nil)
		fields[fieldCc] = cs.recipientRow(c, fieldCc, locales.Cc(), "", func() {
			if !cs.showBcc {
				cs.bccButton(c, t)
			}
		})
		if cs.showBcc {
			fields[fieldBcc] = cs.recipientRow(c, fieldBcc, locales.Bcc(), "", nil)
		}
		problems := cs.hasProblems()
		if problems {
			for f, msgs := range cs.recipientProblems(c, t) {
				for _, msg := range msgs {
					fields[f].ErrorMessage(msg)
				}
			}
		}
		cs.row(c, locales.Subject(), &cs.subject)
		if drafting {
			cs.draftRow(c, t)
		}
		ui.Divider(c)
		// The body takes the room there is, and gives it back rather than
		// push what is below off the window: it is the only part that
		// grows, so the fields, the attachments and the actions keep their
		// room whatever the fonts of the platform make of them. With
		// attachments it keeps room to write in, and above them the
		// problems of the recipients, which cannot wait, take it back to
		// whatever is left.
		bodyMin := float32(0)
		if hasFiles && !problems {
			bodyMin = 56
		}
		body := ui.TextArea(c, &cs.body).Grow(1).MinHeight(bodyMin).Label(locales.Body()).
			Placeholder(locales.ComposeBodyPlaceholder()).FocusBind(&cs.focused, focusBody).OnChange(cs.changed)
		if cs.caret >= 0 {
			// Once, as the window opens: the input keeps its caret from
			// then on.
			body.SetTextSelection(cs.caret, cs.caret)
			cs.caret = -1
		}
		focused := ui.FocusedValue(c, &cs.focused) == focusBody
		if focused && !cs.bodyFocused {
			// Tab selects all of a field it comes to, which in a body
			// with a signature or a quote the next key typed would
			// replace: the caret goes where the user writes instead.
			if start, end := body.TextSelection(); start == 0 && end > 0 && end == utf8.RuneCountInString(cs.body) {
				at := cs.writeAt()
				body.SetTextSelection(at, at)
			}
		}
		cs.bodyFocused = focused
		if hasFiles {
			cs.attachmentList(c, t)
		}
		if cs.draft.conflict {
			cs.conflictNotice(c, t)
		}
		if cs.sending && cs.sendTotal > 0 {
			cs.sendProgress(c, t)
		}
		if cs.err != "" {
			ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				// A status, so that a screen reader says why a file was
				// not attached or a message not sent.
				ui.Text(c, cs.err).FontSize(12).TextColor(t.Text).MaxLines(4).Grow(1).MinWidth(0).
					Role(ui.RoleStatus)
			})
			if acc := cs.app.account(cs.accountID); acc != nil && cs.needsSignIn {
				ui.Column(c).Shrink(0).Padding(0, 0, 0, 22).Children(func() {
					cs.app.signInControls(c, t, *acc)
				})
			}
		}
		ui.Row(c).Shrink(0).Gap(10).AlignItems(ui.Center).Children(func() {
			attach := cs.app.iconButton(c, paperclipIcon, locales.AttachFiles(), locales.AttachFilesTip(attachKey())).
				FocusBind(&cs.focused, focusAttach).KeyShortcuts(ui.Cmd|ui.Shift, ui.KeyA).
				Disabled(!cs.canAttach()).OnClick(cs.pickFiles)
			if !cs.canAttach() {
				// An icon does not grey out with its button as a label
				// does.
				attach.Opacity(0.45)
			}
			// Closing the window keeps the draft; this is the way to
			// throw it away.
			ui.Button(c, locales.DiscardDraft()).Disabled(cs.sending).OnClick(cs.discard)
			cs.statusLine(c, t)
			if !cs.sending {
				key := "Ctrl+Enter"
				if runtime.GOOS == "darwin" {
					key = "⌘↩"
				}
				ui.Text(c, locales.SendHint(key)).FontSize(11).TextColor(t.TextMuted).Shrink(0)
			}
			if cs.note != "" {
				ui.Text(c, cs.note).FontSize(12).TextColor(t.TextMuted)
			}
			if cs.sending {
				// The way to stop a message on its way, which with
				// attachments can take a while. It takes the hint's
				// place, to fit the window at its narrowest.
				label := locales.CancelSend()
				if cs.cancelling {
					label = locales.SendCancelling()
				}
				ui.Button(c, label).Label(locales.CancelSend()).Disabled(cs.cancelling).OnClick(cs.cancelSend)
			}
			label := locales.Send()
			if cs.sending {
				label = locales.Sending()
			}
			ui.PrimaryButton(c, label).Label(locales.Send()).Disabled(cs.sending).
				OnClick(func() { cs.send() })
		})
		if dragOver {
			cs.dropHint(c, t)
		}
	})
	aiReviewModal(c, &cs.review)
}

// The fields and the attach button among the controls cs.focused names.
const (
	focusTo     = "to"
	focusCc     = "cc"
	focusBcc    = "bcc"
	focusBody   = "body"
	focusAttach = "attach"
)

// attachmentFocus names an attachment among them. A draft has a file
// once under a name, so the two tell its attachments apart.
func attachmentFocus(att mail.Attachment) string {
	return "file:" + att.ContentHash + "/" + att.Filename
}

// attachKey is the shortcut that opens the file picker, as the platform
// writes it.
func attachKey() string {
	if runtime.GOOS == "darwin" {
		return "⇧⌘A"
	}
	return "Ctrl+Shift+A"
}

// attachmentsHeight is the room the attachments take at most: two rows
// of them and the top of a third, which shows there are more. Those
// scroll, so that the actions stay on screen however many files a
// message has.
const (
	attachmentsHeight   = 106
	attachmentRowHeight = 48
	attachmentRing      = 3
)

// wholeMB writes a limit that is a round number of megabytes as one.
func wholeMB(n int64) string { return fmt.Sprintf("%d MB", n>>20) }

// attachmentList shows what is attached: the count and the size together,
// a warning when the message is likely to be refused for its size, and a
// card for each file.
func (cs *composeState) attachmentList(c *ui.Context, t *ui.Theme) {
	total := cs.attachmentBytes()
	// The list gives way before the actions do: with a warning, a notice
	// and a send's progress all showing in a small window, it keeps one
	// row and scrolls.
	ui.Column(c).MinHeight(0).Gap(6).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, paperclipIcon).FontSize(12).TextColor(t.TextMuted)
			if len(cs.attachments) > 0 {
				ui.Text(c, locales.AttachmentsTotal(len(cs.attachments), formatSize(total))).
					FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
			if cs.attaching > 0 {
				ui.Spinner(c).Size(11, 11)
				ui.Text(c, locales.Attaching(cs.attaching)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		if encoded := mail.EncodedSize(total); encoded > mail.AttachmentWarnBytes {
			// Only the server knows its limit: this warns, and stops
			// nothing.
			ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(6, 10).Radius(8).
				Background(t.Warning.Alpha(0.12)).Children(func() {
				ui.Icon(c, alertIcon).FontSize(13).TextColor(t.Warning)
				ui.Text(c, locales.AttachmentsWarning(formatSize(encoded), wholeMB(mail.AttachmentWarnBytes))).
					FontSize(11).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
			})
		}
		if len(cs.attachments) == 0 {
			return
		}
		// The scroll area cuts off what is drawn outside it, and the
		// focus ring of a card is: the cards stand in from its edge by
		// the ring's width, and the area out by as much, to line up.
		ui.Scroll(c).Margin(-attachmentRing).MaxHeight(attachmentsHeight).MinHeight(attachmentRowHeight).Children(func() {
			ui.Row(c).Wrap().Gap(6).Padding(attachmentRing).Children(func() {
				for _, att := range cs.attachments {
					cs.attachmentCard(c, t, att)
				}
			})
		})
	})
}

// attachmentCard is one attached file: its name, its type and size, and
// the button that takes it off. The card is the keyboard's stop, where
// Delete and Backspace remove the file; the button is the pointer's.
func (cs *composeState) attachmentCard(c *ui.Context, t *ui.Theme, att mail.Attachment) {
	name := att.Filename
	if name == "" {
		name = locales.Attachment()
	}
	remove := func() { cs.removeAttachment(att) }
	ui.Row(c.Key(attachmentFocus(att))).Width(232).Padding(5, 6, 5, 10).Gap(6).AlignItems(ui.Center).
		Radius(8).Background(t.Surface).Border(1, t.Border).
		FocusBind(&cs.focused, attachmentFocus(att)).
		KeyShortcuts(0, ui.KeyDelete).
		OnShortcut(0, ui.KeyDelete, remove).
		OnShortcut(0, ui.KeyBackspace, remove).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, name).FontSize(12).Bold().SingleLine()
			ui.Text(c, att.ContentType+" · "+formatSize(att.Size)).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		ui.Button(c, "").Label(locales.RemoveAttachment(name)).Tooltip(locales.RemoveAttachment(name)).
			Padding(4, 5).TabStop(false).Disabled(cs.sending).OnClick(remove).
			Children(func() { ui.Icon(c, closeIcon).FontSize(13) })
	})
}

// sendProgress shows how much of the message has gone to the server.
func (cs *composeState) sendProgress(c *ui.Context, t *ui.Theme) {
	text := locales.SendProgress(formatSize(cs.sendSent), formatSize(cs.sendTotal))
	// The engine keeps the last byte back until the server has taken the
	// message: all of it is out, and the wait is for the answer.
	if cs.sendSent >= cs.sendTotal-1 {
		text = locales.SendFinishing()
	}
	ui.Column(c).Shrink(0).Gap(5).Children(func() {
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine()
		ui.Progress(c, float64(cs.sendSent)/float64(cs.sendTotal)).Label(locales.Sending())
	})
}

// dropHint covers the window while files are dragged over it, to say
// that it takes them.
func (cs *composeState) dropHint(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Absolute().Top(8).Right(8).Bottom(8).Left(8).Center().Gap(8).Radius(12).
		Background(t.Background.Alpha(0.92)).Border(2, t.Accent).Children(func() {
		ui.Icon(c, paperclipIcon).FontSize(26).TextColor(t.Accent)
		ui.Text(c, locales.DropToAttach()).FontSize(14).Bold().TextColor(t.Accent)
	})
}

// row is a compact compose field with its label inline, as mail apps
// show the subject; the recipients' are recipientRow.
func (cs *composeState) row(c *ui.Context, label string, value *string) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		// The caption names the input, so a click on it focuses the field.
		caption := ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(52)
		ui.TextInput(c, value).Grow(1).LabelledBy(caption).OnChange(cs.changed)
	})
}

// bccButton unfolds the Bcc field, which a message seldom needs, and
// puts the keyboard in it.
func (cs *composeState) bccButton(c *ui.Context, t *ui.Theme) {
	ui.ButtonBase(c).Label(locales.ShowBcc()).Tooltip(locales.ShowBcc()).Padding(4, 8).Radius(6).
		OnClick(func() { cs.showBcc, cs.focused = true, focusBcc }).
		Children(func() { ui.Text(c, locales.Bcc()).FontSize(12).TextColor(t.Accent) })
}

// writeAt is where in the body the user writes, in runes: after the
// quote of a reply written below it, while the body still starts with
// that quote, and at the start otherwise.
func (cs *composeState) writeAt() int {
	if cs.lead != "" && strings.HasPrefix(cs.body, cs.lead) {
		return utf8.RuneCountInString(cs.lead)
	}
	return 0
}

// identityLabel is an identity as the From picker names it: its name and
// address, written for people rather than for a header. An account has
// an address once, so the labels tell its identities apart.
func identityLabel(id mail.Identity) string {
	if strings.TrimSpace(id.Name) == "" {
		return id.Email
	}
	return id.Name + " <" + id.Email + ">"
}

// fromRow chooses the identity the message goes from: the account's own
// address or one added to it. An identity deleted meanwhile is the
// account's own, as the engine sends it.
func (cs *composeState) fromRow(c *ui.Context, t *ui.Theme, ids []mail.Identity) {
	labels := make([]string, len(ids))
	chosen := 0
	for i, id := range ids {
		labels[i] = identityLabel(id)
		if id.ID == cs.identity {
			chosen = i
		}
	}
	// The picker's own choice stands until its change is taken below.
	if !slices.Contains(labels, cs.from) {
		cs.from = labels[chosen]
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		caption := ui.Text(c, locales.From()).FontSize(12).TextColor(t.TextMuted).Width(52)
		ui.Select(c, &cs.from, labels).Grow(1).LabelledBy(caption).Disabled(cs.sending).OnChange(func() {
			if i := slices.Index(labels, cs.from); i >= 0 && ids[i].ID != cs.identity {
				cs.identity = ids[i].ID
				cs.changed()
			}
		})
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
		// Two lines, as beside the attach button a narrow window has no
		// room to say on one where the draft is.
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Grow(1).MinWidth(0)
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
	case d.signIn:
		return locales.DraftWaitsSignIn(), draftIcon, t.TextMuted
	case d.offline:
		return locales.DraftOffline(), draftIcon, t.TextMuted
	case cs.attachmentBytes() > mail.DraftAutoUploadLimit:
		// A pause in the typing does not upload this one: saying it is
		// only saved here would leave out when it goes up.
		return locales.DraftUploadsLater(), draftIcon, t.TextMuted
	}
	return locales.DraftSaved(), draftIcon, t.TextMuted
}

// attachmentBytes is the size of the files attached, together.
func (cs *composeState) attachmentBytes() int64 {
	var total int64
	for _, att := range cs.attachments {
		total += att.Size
	}
	return total
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

// empty reports a window with nothing written in it and nothing
// attached. The body it opened with, as a new message's signature, is
// nothing written.
func (cs *composeState) empty() bool {
	for _, field := range []string{cs.to, cs.cc, cs.bcc, cs.subject} {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	if body := strings.TrimSpace(cs.body); body != "" && body != strings.TrimSpace(cs.startBody) {
		return false
	}
	return len(cs.attachments) == 0 && cs.attaching == 0
}

// canAttach reports whether the window takes files now: not once its
// message is on its way or gone.
func (cs *composeState) canAttach() bool {
	return !cs.sending && !cs.sent && !cs.closed && !cs.gone.Load()
}

// attachTimeout is how long copying one file into the draft may take. It
// is long: the file may be large and on a slow disk, and nobody waits
// for it but the copy.
const attachTimeout = 10 * time.Minute

// pickFiles asks which files to attach. The dialog blocks, so a job
// shows it.
func (cs *composeState) pickFiles() {
	a := cs.app
	if a.pickFiles == nil || cs.picking || !cs.canAttach() {
		return
	}
	cs.picking = true
	a.run(func() {
		paths, _ := a.pickFiles(cs.win)
		a.post(func() {
			cs.picking = false
			cs.attach(paths)
			cs.redraw()
		})
	})
}

// attach copies files into the draft, one after another. Each is in the
// draft, and stays with it, as soon as its copy is done; one that cannot
// be attached says why and leaves the others alone.
func (cs *composeState) attach(paths []string) {
	if len(paths) == 0 || !cs.canAttach() {
		return
	}
	a := cs.app
	if cs.draftID == "" {
		// As for a first edit: the jobs queued behind this one write the
		// same draft.
		cs.draftID = mail.NewDraftID()
	}
	cs.err = ""
	cs.attaching += len(paths)
	// The engine makes the draft from this if it is not stored yet, and
	// leaves the text of a stored one to the saves.
	d := cs.snapshot()
	accountID := cs.accountID
	a.run(func() {
		for _, path := range paths {
			var (
				saved mail.Draft
				err   error
			)
			cs.saveMu.Lock()
			gone := cs.gone.Load()
			if !gone {
				// Not the window's context: a file dropped just before
				// the window closes is still attached.
				ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
				saved, _, err = a.svc.AttachFile(ctx, d, path)
				cancel()
			}
			cs.saveMu.Unlock()
			a.post(func() {
				cs.attaching--
				switch {
				case gone || cs.gone.Load():
				case err != nil:
					text := attachErrorText(path, err)
					if cs.err != "" {
						text = cs.err + "\n" + text
					}
					cs.err = text
				default:
					cs.draft.err = ""
					cs.noteDraft(saved)
					cs.takeAttachments(saved)
					a.reloadDrafts(accountID)
				}
				cs.redraw()
			})
		}
		a.post(func() { cs.attachmentsChanged() })
	})
}

// attachmentsChanged plans the upload of a draft whose files changed. A
// window closed meanwhile made its last sync before the change was in.
func (cs *composeState) attachmentsChanged() {
	switch {
	case cs.gone.Load() || cs.sent:
	case cs.closed:
		cs.app.syncDrafts(cs.accountID)
	default:
		cs.scheduleSync()
	}
}

// attachOriginal attaches to a forward the files of the message it
// passes on, copied by the engine, as attaching files does: the window
// shows them coming and Send waits for them. One that could not be
// copied says why, and the others stay attached.
func (cs *composeState) attachOriginal(m mail.Message) {
	a := cs.app
	if cs.draftID == "" {
		cs.draftID = mail.NewDraftID()
	}
	n := len(m.Attachments)
	cs.attaching += n
	// The engine makes the draft from this, as it is not stored yet.
	d := cs.snapshot()
	accountID, folder, uid := m.Account, m.Folder, m.UID
	a.run(func() {
		var (
			saved    mail.Draft
			failures []mail.AttachFailure
			err      error
		)
		cs.saveMu.Lock()
		gone := cs.gone.Load()
		if !gone {
			ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
			saved, failures, err = a.svc.AttachFromMessage(ctx, d, accountID, folder, uid)
			cancel()
		}
		cs.saveMu.Unlock()
		a.post(func() {
			cs.attaching -= n
			var texts []string
			switch {
			case gone || cs.gone.Load():
			case errors.Is(err, mail.ErrNotFound):
				texts = append(texts, locales.ForwardOriginalMissing())
			case err != nil:
				a.signInRequired(accountID, err)
				texts = append(texts, locales.ForwardAttachmentsFailed(failureText(err)))
			default:
				cs.draft.err = ""
				cs.noteDraft(saved)
				cs.takeAttachments(saved)
				a.reloadDrafts(accountID)
				for _, f := range failures {
					texts = append(texts, forwardFailureText(f))
				}
			}
			if len(texts) > 0 {
				if cs.err != "" {
					texts = append([]string{cs.err}, texts...)
				}
				cs.err = strings.Join(texts, "\n")
			}
			cs.attachmentsChanged()
			cs.redraw()
		})
	})
}

// forwardFailureText says why an attachment of a forwarded message was
// not attached.
func forwardFailureText(f mail.AttachFailure) string {
	name := f.Attachment.Filename
	if name == "" {
		name = locales.Attachment()
	}
	switch {
	case errors.Is(f.Err, mail.ErrAttachTooLarge):
		return locales.AttachTooLarge(name, wholeMB(mail.MaxAttachmentBytes))
	case errors.Is(f.Err, mail.ErrNotFound):
		return locales.ForwardAttachMissing(name)
	}
	return locales.ForwardAttachFailed(name, failureText(f.Err))
}

// attachErrorText says why a file was not attached.
func attachErrorText(path string, err error) string {
	name := filepath.Base(path)
	switch {
	case errors.Is(err, mail.ErrAttachDirectory):
		return locales.AttachFolder(name)
	case errors.Is(err, mail.ErrAttachTooLarge):
		return locales.AttachTooLarge(name, wholeMB(mail.MaxAttachmentBytes))
	case errors.Is(err, fs.ErrNotExist):
		return locales.AttachMissing(name)
	case errors.Is(err, fs.ErrPermission):
		return locales.AttachDenied(name)
	}
	return locales.AttachFailed(name, err.Error())
}

// takeAttachments takes what a stored draft has attached. The jobs
// answer out of order, so the list of an older revision is ignored,
// except that a draft kept apart from another client's version is a new
// message, whose count starts again. A file taken off here stays off
// while its job has not answered.
func (cs *composeState) takeAttachments(d mail.Draft) {
	if d.MessageID == cs.attachMsgID && d.Revision < cs.attachRev {
		return
	}
	cs.attachMsgID, cs.attachRev = d.MessageID, d.Revision
	cs.attachments = cs.attachments[:0:0]
	for _, att := range d.Attachments {
		if !cs.removing[attachmentFocus(att)] {
			cs.attachments = append(cs.attachments, att)
		}
	}
}

// removeAttachment takes a file off the draft. It goes from the window
// at once, and the keyboard goes to the file after it, or before it, or
// to the attach button when it was the last: never nowhere.
func (cs *composeState) removeAttachment(att mail.Attachment) {
	key := attachmentFocus(att)
	i := slices.IndexFunc(cs.attachments, func(have mail.Attachment) bool { return attachmentFocus(have) == key })
	if i < 0 || cs.draftID == "" || !cs.canAttach() {
		return
	}
	cs.attachments = slices.Delete(slices.Clone(cs.attachments), i, i+1)
	switch {
	case i < len(cs.attachments):
		cs.focused = attachmentFocus(cs.attachments[i])
	case i > 0:
		cs.focused = attachmentFocus(cs.attachments[i-1])
	default:
		cs.focused = focusAttach
	}
	if cs.removing == nil {
		cs.removing = map[string]bool{}
	}
	cs.removing[key] = true
	cs.err = ""
	a := cs.app
	id, accountID := cs.draftID, cs.accountID
	a.run(func() {
		var (
			saved mail.Draft
			err   error
		)
		cs.saveMu.Lock()
		gone := cs.gone.Load()
		if !gone {
			ctx, cancel := a.ctx()
			saved, err = a.svc.RemoveAttachment(ctx, id, att)
			cancel()
		}
		cs.saveMu.Unlock()
		a.post(func() {
			delete(cs.removing, key)
			switch {
			case gone || cs.gone.Load():
			case err != nil:
				// It is still attached, so it shows again.
				name := att.Filename
				if name == "" {
					name = locales.Attachment()
				}
				cs.err = locales.AttachRemoveFailed(name, err.Error())
				if !slices.ContainsFunc(cs.attachments, func(have mail.Attachment) bool {
					return attachmentFocus(have) == key
				}) {
					cs.attachments = append(cs.attachments, att)
				}
			default:
				cs.noteDraft(saved)
				cs.takeAttachments(saved)
				a.reloadDrafts(accountID)
				cs.attachmentsChanged()
			}
			cs.redraw()
		})
	})
}

// snapshot is the draft as the window holds it now.
func (cs *composeState) snapshot() mail.Draft {
	return mail.Draft{
		ID: cs.draftID, AccountID: cs.accountID,
		To: cs.to, Cc: cs.cc, Bcc: cs.bcc, Subject: cs.subject, Body: cs.body,
		InReplyTo:  cs.inReplyTo,
		References: append([]string(nil), cs.references...),
		Identity:   cs.identity,
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
			// A draft with large attachments waits for the window to
			// close, or for a sync of the account: the status line says
			// so.
			a.syncDraftEdits(cs.accountID)
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
			cs.draft.signIn = errors.Is(syncErr, mail.ErrSignInRequired)
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
	// A field net/mail reads whole is what CheckRecipients would return
	// for it. One that it does not read goes to the engine, which tells
	// which of its entries are no address and what contact a name may be:
	// the message waits until they are fixed.
	to, toErr := parseRecipients(cs.to)
	cc, ccErr := parseRecipients(cs.cc)
	bcc, bccErr := parseRecipients(cs.bcc)
	if toErr != nil || ccErr != nil || bccErr != nil {
		cs.err = ""
		cs.checkRecipients(true, recipientFields[:]...)
		return
	}
	cs.rcpt.checks = [recipientFieldCount]*fieldCheck{}
	if len(to) == 0 {
		cs.err = locales.SendFailed(locales.NoRecipients())
		return
	}
	if cs.attaching > 0 || len(cs.removing) > 0 {
		// What is attached is still changing: the message would go
		// without a file, or with one just taken off.
		cs.err = locales.AttachBusy()
		return
	}
	out := mail.Outgoing{
		To: to, Cc: cc, Bcc: bcc, Subject: strings.TrimSpace(cs.subject), Text: cs.body,
		InReplyTo: cs.inReplyTo, References: cs.references,
		Attachments: slices.Clone(cs.attachments),
		DraftID:     cs.draftID,
		Identity:    cs.identity,
	}
	if set := cs.app.composeSet; set.SendDelay > 0 {
		cs.schedule(out, set)
		return
	}
	cs.sending = true
	cs.err = ""
	cs.note = ""
	cs.needsSignIn = false
	cs.sendSent, cs.sendTotal, cs.cancelling = 0, 0, false
	accountID := cs.accountID
	app := cs.app
	draft := cs.snapshot()
	// The saves still waiting give way to the one below.
	seq := cs.saveSeq.Add(1)
	// The Cancel button stops the send, and so does closing the window.
	// A minute is enough for a message of text; one with attachments
	// gets what a slow line needs for them. A line that stops moving
	// ends the send sooner: the engine does not wait on it this long.
	sendCtx, stop := context.WithCancel(cs.ctx)
	cs.sendCancel = stop
	timeout := time.Minute + time.Duration(mail.EncodedSize(cs.attachmentBytes())/sendSlowestRate)*time.Second
	app.run(func() {
		defer stop()
		ctx, cancel := context.WithTimeout(sendCtx, timeout)
		defer cancel()
		ctx = mail.WithSendProgress(ctx, func(sent, total int64) {
			app.post(func() {
				cs.sendSent, cs.sendTotal = sent, total
				cs.redraw()
			})
		})
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
			cs.sending, cs.sendCancel, cs.cancelling = false, nil, false
			if err != nil {
				cs.sendFailed(err)
				return
			}
			cs.sent = true
			if result.Warning != nil {
				cs.note = locales.SentCopyWarning()
				app.err = cs.note
			}
			app.loadMessages(app.gen)
			app.loadFollowUps(app.gen)
			app.loadPending(app.gen)
			cs.close()
		})
	})
}

// sendFailed says why the message did not go. Whatever stopped it, the
// window stays as it was, with the draft and its attachments, to send
// again or not.
func (cs *composeState) sendFailed(err error) {
	app := cs.app
	if app.signInRequired(cs.accountID, err) {
		// Nothing went out. The window offers the sign-in, and the
		// message waits for the user to send it again.
		cs.err, cs.needsSignIn = locales.SendNeedsSignIn(app.accountLabel(cs.accountID)), true
	} else {
		cs.err = sendErrorText(err)
	}
	app.loadPending(app.gen)
	// The failure belongs to this window, which the mail window's update
	// does not redraw.
	cs.redraw()
}

// schedule sends the message with the settings' delay. The draft is
// saved with exactly what is scheduled, which Undo gives back, and the
// engine writes the message to the outbox; then the window closes, and
// the mail window counts the delay down with Undo while a job sends the
// message once it is due. Nothing leaves before then.
func (cs *composeState) schedule(out mail.Outgoing, set mail.ComposeSettings) {
	app := cs.app
	if cs.draftID == "" {
		// Undo gives the message back as a draft, this window's.
		cs.draftID = mail.NewDraftID()
		out.DraftID = cs.draftID
	}
	cs.sending = true
	cs.err, cs.note, cs.needsSignIn = "", "", false
	accountID := cs.accountID
	draft := cs.snapshot()
	// The saves still waiting give way to the one below, which writes what
	// is scheduled whatever was saved before it.
	cs.saveSeq.Add(1)
	app.run(func() {
		ctx, cancel := app.ctx()
		defer cancel()
		cs.saveMu.Lock()
		_, err := app.svc.SaveDraft(ctx, draft)
		var p mail.PendingSend
		if err == nil {
			p, err = app.svc.Schedule(ctx, accountID, out, set.Delay())
		}
		if err == nil {
			// The draft says what was scheduled: no save of this window
			// changes it any more.
			cs.gone.Store(true)
		}
		cs.saveMu.Unlock()
		app.post(func() {
			cs.sending = false
			if err != nil {
				cs.sendFailed(err)
				return
			}
			cs.sent = true
			app.waitForSend(p, set.SendDelay)
			app.reloadDrafts(accountID)
			cs.close()
		})
		if err == nil {
			// Not from the post: the message leaves when it is due even
			// with the mail window closed.
			app.sendWhenDue(p)
		}
	})
}

// sendSlowestRate is the bytes a second a send is given time for: a line
// slower than this may run into the send's time limit.
const sendSlowestRate = 32 << 10

// cancelSend stops the message on its way. The send answers with what
// became of it.
func (cs *composeState) cancelSend() {
	if !cs.sending || cs.sendCancel == nil {
		return
	}
	cs.cancelling = true
	cs.sendCancel()
}

// sendErrorText says why a message was not sent, in the server's words
// where it has any, and whether it may have been delivered all the same.
func sendErrorText(err error) string {
	var (
		over     *mail.SizeLimitError
		rejected *mail.RejectedError
	)
	switch {
	case errors.As(err, &over):
		return locales.SendOverLimit(formatSize(over.Size), formatSize(over.Limit))
	case errors.As(err, &rejected) && rejected.TooLarge():
		return locales.SendRejectedSize(rejected.Reply)
	case errors.As(err, &rejected):
		return locales.SendRejected(rejected.Reply)
	case errors.Is(err, mail.ErrDeliveryUnknown) && errors.Is(err, context.Canceled):
		return locales.SendCancelledUnknown()
	case errors.Is(err, mail.ErrDeliveryUnknown):
		return locales.SendInterrupted(err.Error())
	case errors.Is(err, context.Canceled):
		return locales.SendCancelled()
	case errors.Is(err, mail.ErrAttachmentMissing):
		return locales.SendAttachmentMissing()
	}
	return locales.SendFailed(err.Error())
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
