package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// readingPane draws the toolbar, the header, attachments and the body.
func (a *app) readingPane(c *ui.Context, t *ui.Theme) {
	a.services = c.Services()
	ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
		a.readingToolbar(c, t)
		m, msg := a.current()
		if m == nil {
			ui.Column(c).Fill().Center().Gap(8).Children(func() {
				ui.Icon(c, mailOpenIcon).FontSize(36).TextColor(t.TextMuted)
				ui.Text(c, locales.NoMessageSelected()).FontSize(15).TextColor(t.TextMuted)
				ui.Text(c, locales.ShortcutsHint()).FontSize(11).TextColor(t.TextMuted.Alpha(0.7)).TextAlign(ui.Center)
			})
			return
		}
		subject := m.Subject
		if a.folder == draftsFolder {
			a.draftBanner(c, t, *m)
			if strings.TrimSpace(subject) == "" {
				subject = locales.NoSubject()
			}
		}
		ui.Scroll(c.Key(summaryKey(*m))).Grow(1).Children(func() {
			ui.Column(c).Padding(24, 30, 24, 30).Gap(16).MinWidth(0).Children(func() {
				ui.Text(c, subject).FontSize(21).Bold().Balance()
				a.senderHeader(c, t, *m)
				if msg != nil && len(msg.Attachments) > 0 {
					a.attachments(c, t, *m, msg.Attachments)
				}
				if job := a.summaries[summaryKey(*m)]; job != nil {
					a.summaryCard(c, t, summaryKey(*m), job)
				}
				ui.Divider(c)
				a.body(c, t, m, msg)
			})
		})
	})
}

// current returns the chosen summary and its body, if loaded.
func (a *app) current() (*mail.Summary, *mail.Message) {
	if a.selected < 0 || a.selected >= len(a.all) {
		return nil, nil
	}
	m := &a.all[a.selected]
	if b, ok := a.bodies[summaryKey(*m)]; ok {
		return m, &b.Message
	}
	return m, nil
}

// readingToolbar draws the message actions. Actions act on the whole
// selection, and their icons show that selection's state.
func (a *app) readingToolbar(c *ui.Context, t *ui.Theme) {
	targets := a.targets()
	has := len(targets) > 0
	allFlagged, allSeen := has, has
	for _, m := range targets {
		if !m.Flagged {
			allFlagged = false
		}
		if !m.Seen {
			allSeen = false
		}
	}
	archive := a.folderBySpecial(mail.SpecialArchive)
	trash := a.folderBySpecial(mail.SpecialTrash)
	junk := a.folderBySpecial(mail.SpecialJunk)
	// A draft of this device is no message of the server: it cannot be
	// moved, flagged or answered, only edited or discarded. The actions
	// for mail are off while one is among the chosen rows.
	drafts := localDrafts(targets)
	onlyDrafts := has && drafts == len(targets)
	has = has && drafts == 0
	ui.Toolbar(c, func() {
		a.toolButton(c, archiveIcon, locales.ArchiveMessage(), has && archive != "", func() {
			a.moveSelected(mail.SpecialArchive)
		})
		if onlyDrafts {
			// Deleting a draft is discarding it, which asks first.
			a.toolButton(c, trashIcon, locales.DiscardDraft(), true, a.discardSelected)
		} else {
			a.toolButton(c, trashIcon, locales.DeleteMessage(), has && trash != "", func() {
				a.moveSelected(mail.SpecialTrash)
			})
		}
		a.toolButton(c, spamIcon, locales.JunkMessage(), has && junk != "", func() {
			a.moveSelected(mail.SpecialJunk)
		})
		starLabel, star := locales.StarMessage(), starIcon
		starColor := t.Text
		if allFlagged {
			starLabel, star = locales.UnstarMessage(), starFilledIcon
			starColor = t.Warning
		}
		starButton := ui.Button(c, "").Label(starLabel).Tooltip(starLabel).Disabled(!has).
			Padding(6, 8)
		if has {
			starButton = starButton.OnClick(func() { a.toggleStar() })
		}
		starButton.Children(func() {
			ui.Icon(c, star).FontSize(16).TextColor(starColor)
		})
		readLabel, readIcon := locales.MarkRead(), mailOpenIcon
		if allSeen {
			readLabel, readIcon = locales.MarkUnread(), mailIcon
		}
		a.toolButton(c, readIcon, readLabel, has, func() { a.toggleRead() })
		a.toolDivider(c)
		a.toolButton(c, replyIcon, locales.Reply(), has, func() { a.reply(false) })
		a.toolButton(c, replyAllIcon, locales.ReplyAll(), has, func() { a.reply(true) })
		a.toolButton(c, forwardIcon, locales.Forward(), has, func() { a.forward() })
		if a.ai != nil {
			// A summary is of the one message open, once its body is here.
			_, msg := a.current()
			a.toolDivider(c)
			a.toolButton(c, sparklesIcon, locales.Summarize(), msg != nil && len(targets) == 1 && has, a.summarize)
		}
	}).Height(48).Padding(6, 12).Label(locales.MessageActions()).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
}

// toggleHTML switches between the plain text and the isolated web view.
func (a *app) toggleHTML() {
	a.htmlOn = !a.htmlOn
	a.htmlKey = ""
	if a.htmlOn {
		a.ensureWebView()
		a.syncHTMLView()
	}
}

// senderHeader draws who sent the message, to whom and when, with a
// details disclosure for the raw fields.
func (a *app) senderHeader(c *ui.Context, t *ui.Theme, m mail.Summary) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Avatar(c, displayName(m.From), nil)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, displayName(m.From)).FontSize(14).Bold().SingleLine()
			ui.Text(c, m.From.Email).FontSize(12).TextColor(t.TextMuted).SingleLine()
			if to := a.recipientLine(m); to != "" {
				ui.Text(c, to).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Column(c).Shrink(0).AlignItems(ui.End).Gap(2).Children(func() {
			ui.Text(c, a.longTime(c, m.Date)).FontSize(12).TextColor(t.TextMuted)
			if len(m.Cc) > 0 && !a.details {
				ui.Text(c, locales.Cc()+" "+recipients(m.Cc)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				if m, ok := a.bodies[summaryKey(m)]; ok && msgHasHTML(m.Message) {
					label := locales.ShowHTML()
					if a.htmlOn {
						label = locales.ShowPlain()
					}
					ui.ButtonBase(c).Label(label).OnClick(func() { a.toggleHTML() }).Children(func() {
						ui.Text(c, label).FontSize(11).TextColor(t.Accent)
					})
				}
				label := locales.Details()
				if a.details {
					label = locales.HideDetails()
				}
				ui.ButtonBase(c).Label(label).OnClick(func() { a.details = !a.details }).Children(func() {
					ui.Text(c, label).FontSize(11).TextColor(t.Accent)
				})
			})
		})
	})
	if !a.details {
		return
	}
	ui.Column(c).Gap(2).Padding(4, 0, 0, 0).Children(func() {
		detail := func(name, value string) {
			if strings.TrimSpace(value) == "" {
				return
			}
			ui.Text(c, name+": "+value).FontSize(11).TextColor(t.TextMuted).Selectable()
		}
		detail(locales.From(), m.From.String())
		detail(locales.To(), addressesText(m.To))
		detail(locales.Cc(), addressesText(m.Cc))
		detail(locales.ReplyTo(), addressesText(m.ReplyTo))
		detail(locales.MessageID(), m.MessageID)
	})
}

// msgHasHTML reports whether a loaded message has an HTML body to show.
func msgHasHTML(m mail.Message) bool { return strings.TrimSpace(m.BodyHTML) != "" }

// recipientLine writes "to me" when the account is the only recipient,
// else the recipients.
func (a *app) recipientLine(m mail.Summary) string {
	if len(m.To) == 1 && a.activeAccount() != nil &&
		strings.EqualFold(m.To[0].Email, a.activeAccount().Email) {
		return locales.ToMe()
	}
	if to := recipients(m.To); to != "" {
		return locales.To() + " " + to
	}
	return ""
}

// recipients writes an address list compactly.
func recipients(list []mail.Address) string {
	names := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name != "" {
			names = append(names, a.Name)
		} else {
			names = append(names, a.Email)
		}
	}
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + " …"
	}
	return strings.Join(names, ", ")
}

// body draws the loading, error and dual-mode body.
func (a *app) body(c *ui.Context, t *ui.Theme, m *mail.Summary, msg *mail.Message) {
	key := summaryKey(*m)
	switch {
	case a.bodyLocked:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.BodyLoading()).FontSize(13).TextColor(t.TextMuted)
		})
	case a.bodyErr != "":
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, a.bodyErr).FontSize(12).Grow(1)
			})
			ui.Button(c, locales.Retry()).OnClick(func() {
				delete(a.bodies, key)
				a.loadBody(a.gen)
			})
		})
	case msg == nil:
		ui.Text(c, locales.Loading()).FontSize(13).TextColor(t.TextMuted)
	case a.htmlOn && msg.BodyHTML != "":
		ui.Column(c).Radius(10).Border(1, t.Border).Background(t.Background).Children(func() {
			if a.webview != nil {
				ui.WebView(c, a.webview).Height(480)
			} else {
				a.bodyText(c, t, msg.BodyText)
			}
		})
	case strings.TrimSpace(msg.BodyText) == "":
		ui.Text(c, locales.NoBody()).FontSize(13).TextColor(t.TextMuted)
	default:
		ui.Column(c).Padding(18, 20).Radius(10).Border(1, t.Border).
			Background(t.Background).Children(func() {
			a.bodyText(c, t, msg.BodyText)
		})
	}
}

// bodyText draws the plain text body, keeping paragraphs and selecting.
func (a *app) bodyText(c *ui.Context, t *ui.Theme, text string) {
	ui.Column(c).Gap(10).MaxWidth(680).Children(func() {
		for _, paragraph := range strings.Split(text, "\n\n") {
			paragraph = strings.TrimRight(paragraph, "\n")
			if strings.TrimSpace(paragraph) == "" {
				continue
			}
			ui.Text(c, paragraph).FontSize(14).LineHeight(1.6).TextColor(t.Text).Selectable()
		}
	})
}

// attachments lists the message's attachments with a Save as button.
func (a *app) attachments(c *ui.Context, t *ui.Theme, m mail.Summary, list []mail.Attachment) {
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, locales.Attachments()).FontSize(11).Bold().TextColor(t.TextMuted)
		for _, att := range list {
			att := att
			ui.Row(c).MaxWidth(420).Padding(8, 10).Gap(10).AlignItems(ui.Center).Radius(8).
				Background(t.Surface).Border(1, t.Border).Children(func() {
				ui.Box(c).Size(30, 30).Radius(7).Center().Background(t.Background).
					Border(1, t.Border).Children(func() {
					ui.Icon(c, paperclipIcon).FontSize(14).TextColor(t.TextMuted)
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					name := att.Filename
					if name == "" {
						name = locales.Attachment()
					}
					ui.Text(c, name).FontSize(12).Bold().SingleLine()
					ui.Text(c, fmt.Sprintf("%s · %s", att.ContentType, formatSize(att.Size))).
						FontSize(11).TextColor(t.TextMuted).SingleLine()
				})
				ui.Button(c, "").Label(locales.SaveAs()).Tooltip(locales.SaveAs()).
					Padding(6, 8).OnClick(func() { a.saveAttachment(m, att) }).
					Children(func() { ui.Icon(c, downloadIcon).FontSize(15) })
			})
		}
	})
}

// formatSize writes a byte count as a person reads it.
func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// saveAttachment fetches the bytes and asks where to save them.
func (a *app) saveAttachment(m mail.Summary, att mail.Attachment) {
	if a.saveFile == nil {
		return
	}
	accountID, folder, uid, part := a.active, m.Folder, m.UID, att.Part
	name := safeFileName(att.Filename)
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, data, err := a.svc.Attachment(ctx, accountID, folder, uid, part)
		if err != nil {
			a.post(func() {
				if !a.stale(g) {
					a.err = locales.SaveFileFailed(err.Error())
				}
			})
			return
		}
		path, err := a.saveFile(a.win, name)
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			a.post(func() {
				if !a.stale(g) {
					a.err = locales.SaveFileFailed(err.Error())
				}
			})
		}
	})
}

// safeFileName keeps a message's filename from escaping the chosen
// directory.
func safeFileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

// targets is the chosen messages: those selected with Cmd or Shift, or
// the one row chosen.
func (a *app) targets() []mail.Summary {
	if a.sel.Len() > 0 {
		chosen := map[string]bool{}
		for k := range a.sel.All() {
			chosen[k] = true
		}
		var out []mail.Summary
		for _, m := range a.all {
			if chosen[summaryKey(m)] {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if a.selected >= 0 && a.selected < len(a.all) {
		return []mail.Summary{a.all[a.selected]}
	}
	return nil
}

// toggleStar stars or unstars the chosen messages, optimistically.
func (a *app) toggleStar() {
	targets := a.targets()
	if len(targets) == 0 || localDrafts(targets) > 0 {
		return
	}
	allFlagged := true
	chosen := map[string]bool{}
	for _, m := range targets {
		chosen[summaryKey(m)] = true
		if !m.Flagged {
			allFlagged = false
		}
	}
	on := !allFlagged
	for i := range a.all {
		if chosen[summaryKey(a.all[i])] {
			a.all[i].Flagged = on
		}
	}
	g := a.gen
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, m := range targets {
			if err = a.svc.SetFlag(ctx, accountID, m.Folder, m.UID, mail.FlagFlagged, on); err != nil {
				break
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				for i := range a.all {
					if chosen[summaryKey(a.all[i])] {
						a.all[i].Flagged = !on
					}
				}
				a.err = err.Error()
				return
			}
			if a.folder == "starred" && !on {
				a.loadMessages(g)
			}
		})
	})
}

// moveSelected archives, junks or deletes the chosen messages.
func (a *app) moveSelected(special string) {
	targets := a.targets()
	// The keys reach here whatever the toolbar offers: a draft of this
	// device is in no folder of the server to be moved from.
	if len(targets) == 0 || localDrafts(targets) > 0 {
		return
	}
	dest := a.folderBySpecial(special)
	if dest == "" {
		return
	}
	chosen := map[string]bool{}
	unread := map[string]int{}
	for _, m := range targets {
		chosen[summaryKey(m)] = true
		if !m.Seen {
			unread[m.Folder]++
		}
	}
	kept := make([]mail.Summary, 0, len(a.all)-len(targets))
	for _, m := range a.all {
		if !chosen[summaryKey(m)] {
			kept = append(kept, m)
		}
	}
	a.all = kept
	a.sel.Clear()
	a.setUndo(nil)
	if a.selected >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	for folder, n := range unread {
		for i := range a.folders {
			if a.folders[i].Name == folder {
				a.folders[i].Unread = max(0, a.folders[i].Unread-n)
			}
		}
	}
	a.updateBadge()
	g := a.gen
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			first mail.MoveResult
			err   error
		)
		for i, m := range targets {
			result, moveErr := a.svc.Move(ctx, accountID, m.Folder, m.UID, dest)
			if moveErr != nil {
				err = moveErr
				break
			}
			if i == 0 {
				first = result
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = err.Error()
			} else if len(targets) == 1 && first.DestUID != 0 {
				label := locales.Archived()
				switch special {
				case mail.SpecialTrash:
					label = locales.Deleted()
				case mail.SpecialJunk:
					label = locales.MovedToJunk()
				}
				a.setUndo(&undoMove{accountID: accountID, source: targets[0].Folder, dest: dest, destUID: first.DestUID, label: label})
			}
			a.loadMessages(g)
		})
	})
}

// undoLastMove moves the message back from the folder it went to, then
// syncs the source folder so its row returns.
func (a *app) undoLastMove() {
	u := a.undo
	if u == nil {
		return
	}
	a.setUndo(nil)
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.MoveBack(ctx, u.accountID, u.dest, u.source, u.destUID)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = err.Error()
				return
			}
			a.syncFolder(g)
		})
	})
}

// reply opens a reply or reply-all, quoting the body.
func (a *app) reply(all bool) {
	m, msg := a.current()
	if m == nil || m.DraftID != "" {
		return
	}
	summary := *m
	var body string
	if msg != nil {
		body = msg.BodyText
	}
	if body == "" {
		// Fetch the body first, then open the window.
		accountID, folder, uid := a.active, summary.Folder, summary.UID
		g := a.gen
		a.run(func() {
			ctx, cancel := a.ctx()
			defer cancel()
			result, err := a.svc.FetchMessage(ctx, accountID, folder, uid, false)
			a.post(func() {
				if a.stale(g) {
					return
				}
				body := ""
				if err == nil {
					body = result.Message.BodyText
					a.bodies[summaryKey(summary)] = result
				}
				a.openReply(summary, body, all)
			})
		})
		return
	}
	a.openReply(summary, body, all)
}

// forward opens a forwarded message with the original quoted.
func (a *app) forward() {
	m, msg := a.current()
	if m == nil || m.DraftID != "" {
		return
	}
	summary := *m
	var body string
	if msg != nil {
		body = msg.BodyText
	}
	if body == "" {
		accountID, folder, uid := a.active, summary.Folder, summary.UID
		g := a.gen
		a.run(func() {
			ctx, cancel := a.ctx()
			defer cancel()
			result, err := a.svc.FetchMessage(ctx, accountID, folder, uid, false)
			a.post(func() {
				if a.stale(g) {
					return
				}
				body := ""
				if err == nil {
					body = result.Message.BodyText
					a.bodies[summaryKey(summary)] = result
				}
				a.openCompose(replyDraft(a, summary, body, false, true))
			})
		})
		return
	}
	a.openCompose(replyDraft(a, summary, body, false, true))
}

// replyDraft builds the recipient, subject and quoted body of a reply or
// forward.
func replyDraft(a *app, m mail.Summary, body string, all, forward bool) *mail.Outgoing {
	out := &mail.Outgoing{}
	own := ""
	if acc := a.activeAccount(); acc != nil {
		own = strings.ToLower(acc.Email)
	}
	quote := func() string {
		if body == "" {
			return ""
		}
		date := ""
		if !m.Date.IsZero() {
			date = m.Date.Format(time.RFC1123Z)
		}
		var b strings.Builder
		if forward {
			b.WriteString("\n\n" + locales.ForwardedHeader() + "\n")
			b.WriteString(locales.From() + ": " + m.From.String() + "\n")
			b.WriteString(locales.Subject() + ": " + m.Subject + "\n")
			b.WriteString(date + "\n\n")
			b.WriteString(body)
			return b.String()
		}
		b.WriteString("\n\n" + locales.OnWrote(date, displayName(m.From)) + "\n")
		for _, line := range strings.Split(body, "\n") {
			b.WriteString("> " + line + "\n")
		}
		return b.String()
	}
	switch {
	case forward:
		out.Subject = withPrefix(m.Subject, "Fwd: ")
	default:
		out.Subject = withPrefix(m.Subject, "Re: ")
		if strings.ToLower(m.From.Email) != own {
			out.To = append(out.To, m.From)
		}
		if all {
			seen := map[string]bool{strings.ToLower(own): true}
			for _, addr := range out.To {
				seen[strings.ToLower(addr.Email)] = true
			}
			for _, list := range [][]mail.Address{m.To, m.Cc} {
				for _, addr := range list {
					key := strings.ToLower(addr.Email)
					if seen[key] {
						continue
					}
					seen[key] = true
					if key == strings.ToLower(m.From.Email) {
						continue
					}
					out.To = append(out.To, addr)
				}
			}
		}
	}
	out.Text = quote()
	if m.MessageID != "" {
		out.InReplyTo = m.MessageID
		out.References = append(append([]string(nil), m.References...), m.MessageID)
	}
	return out
}

// withPrefix adds a subject prefix once.
func withPrefix(subject, prefix string) string {
	trimmed := strings.TrimSpace(subject)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, strings.ToLower(strings.TrimSpace(prefix))) {
		return trimmed
	}
	if trimmed == "" {
		return strings.TrimSpace(prefix)
	}
	return prefix + trimmed
}

// stepMessage moves the choice by delta rows, as J and K do.
func (a *app) stepMessage(delta int) {
	if len(a.all) == 0 {
		return
	}
	next := a.selected + delta
	if a.selected < 0 {
		next = 0
	}
	next = max(0, min(next, len(a.all)-1))
	a.chooseRow(next)
	a.list.Choose(next)
	a.list.ScrollTo(next, ui.Center)
	a.bodyErr = ""
	a.reloadBody()
}

// toggleRead marks the chosen messages read or unread, optimistically.
func (a *app) toggleRead() {
	targets := a.targets()
	if len(targets) == 0 || localDrafts(targets) > 0 {
		return
	}
	allSeen := true
	chosen := map[string]bool{}
	for _, m := range targets {
		chosen[summaryKey(m)] = true
		if !m.Seen {
			allSeen = false
		}
	}
	on := !allSeen
	delta := map[string]int{}
	for i := range a.all {
		m := &a.all[i]
		if !chosen[summaryKey(*m)] || m.Seen == on {
			continue
		}
		m.Seen = on
		if on {
			delta[m.Folder]--
		} else {
			delta[m.Folder]++
		}
	}
	for folder, d := range delta {
		for i := range a.folders {
			if a.folders[i].Name == folder {
				a.folders[i].Unread = max(0, a.folders[i].Unread+d)
			}
		}
	}
	a.updateBadge()
	g := a.gen
	accountID := a.active
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, m := range targets {
			if err = a.svc.SetFlag(ctx, accountID, m.Folder, m.UID, mail.FlagSeen, on); err != nil {
				break
			}
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				for i := range a.all {
					if chosen[summaryKey(a.all[i])] {
						a.all[i].Seen = !on
					}
				}
				for folder, d := range delta {
					for i := range a.folders {
						if a.folders[i].Name == folder {
							a.folders[i].Unread = max(0, a.folders[i].Unread-d)
						}
					}
				}
				a.err = err.Error()
				a.updateBadge()
			}
		})
	})
}
