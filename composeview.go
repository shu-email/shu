package main

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

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
		// message goes from. In a small window, a single address folds
		// away as the files take the room: it gives no choice to make.
		h := c.Root().Bounds().H
		roomy := h < 1 || h >= fromRowMinHeight
		if len(ids) > 1 || len(ids) > 0 && (len(cs.attachments) == 0 || roomy) {
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
// fromRowMinHeight is the height below which a single sender address
// folds away to leave the files their room; a choice of addresses keeps
// its row at any height.
const fromRowMinHeight = 480

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
