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
		ui.Scroll(c.Key(summaryKey(*m))).Grow(1).Children(func() {
			ui.Column(c).Padding(24, 30, 24, 30).Gap(16).MinWidth(0).Children(func() {
				ui.Text(c, m.Subject).FontSize(21).Bold().Balance()
				a.senderHeader(c, t, *m)
				if msg != nil && len(msg.Attachments) > 0 {
					a.attachments(c, t, *m, msg.Attachments)
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

// readingToolbar draws the message actions.
func (a *app) readingToolbar(c *ui.Context, t *ui.Theme) {
	_, msg := a.current()
	has := msg != nil
	html := has && msg.BodyHTML != ""
	ui.Toolbar(c, func() {
		a.toolButton(c, archiveIcon, locales.ArchiveMessage(), a.folderBySpecial(mail.SpecialArchive) != "", func() {
			a.moveSelected(mail.SpecialArchive)
		})
		a.toolButton(c, trashIcon, locales.DeleteMessage(), a.folderBySpecial(mail.SpecialTrash) != "", func() {
			a.moveSelected(mail.SpecialTrash)
		})
		a.toolButton(c, spamIcon, locales.JunkMessage(), a.folderBySpecial(mail.SpecialJunk) != "", func() {
			a.moveSelected(mail.SpecialJunk)
		})
		starLabel := locales.StarMessage()
		starred := false
		if a.selected >= 0 && a.selected < len(a.all) {
			starred = a.all[a.selected].Flagged
		}
		if starred {
			starLabel = locales.UnstarMessage()
		}
		a.toolButton(c, starIcon, starLabel, has, func() { a.toggleStar() })
		readLabel, readIcon := locales.MarkRead(), mailOpenIcon
		if a.selected >= 0 && a.selected < len(a.all) && a.all[a.selected].Seen {
			readLabel, readIcon = locales.MarkUnread(), mailIcon
		}
		a.toolButton(c, readIcon, readLabel, has, func() { a.toggleRead() })
		a.toolDivider(c)
		a.toolButton(c, replyIcon, locales.Reply(), has, func() { a.reply(false) })
		a.toolButton(c, replyAllIcon, locales.ReplyAll(), has, func() { a.reply(true) })
		a.toolButton(c, forwardIcon, locales.Forward(), has, func() { a.forward() })
		ui.Spacer(c)
		if html {
			label := locales.ShowHTML()
			if a.htmlOn {
				label = locales.ShowPlain()
			}
			ui.Button(c, label).Label(label).OnClick(func() { a.toggleHTML() })
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

// senderHeader draws who sent the message, to whom and when.
func (a *app) senderHeader(c *ui.Context, t *ui.Theme, m mail.Summary) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Avatar(c, displayName(m.From), nil)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, displayName(m.From)).FontSize(14).Bold().SingleLine()
			ui.Text(c, m.From.Email).FontSize(12).TextColor(t.TextMuted).SingleLine()
			if to := recipients(m.To); to != "" {
				ui.Text(c, locales.To()+" "+to).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Column(c).Shrink(0).AlignItems(ui.End).Gap(1).Children(func() {
			ui.Text(c, a.longTime(c, m.Date)).FontSize(12).TextColor(t.TextMuted)
			if len(m.Cc) > 0 {
				ui.Text(c, locales.Cc()+" "+recipients(m.Cc)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
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
		if a.webview != nil {
			ui.WebView(c, a.webview).Grow(1).MinHeight(240)
		} else {
			a.bodyText(c, t, msg.BodyText)
		}
	case strings.TrimSpace(msg.BodyText) == "":
		ui.Text(c, locales.NoBody()).FontSize(13).TextColor(t.TextMuted)
	default:
		a.bodyText(c, t, msg.BodyText)
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

// toggleStar flips the chosen message's flag, optimistically.
func (a *app) toggleStar() {
	if a.selected < 0 || a.selected >= len(a.all) {
		return
	}
	idx := a.selected
	m := a.all[idx]
	key := summaryKey(m)
	on := !m.Flagged
	a.all[idx].Flagged = on
	g := a.gen
	accountID, folder, uid := a.active, m.Folder, m.UID
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SetFlag(ctx, accountID, folder, uid, mail.FlagFlagged, on)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				for i := range a.all {
					if summaryKey(a.all[i]) == key {
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

// moveSelected archives, junks or deletes the chosen message.
func (a *app) moveSelected(special string) {
	if a.selected < 0 || a.selected >= len(a.all) {
		return
	}
	dest := a.folderBySpecial(special)
	if dest == "" {
		return
	}
	idx := a.selected
	m := a.all[idx]
	g := a.gen
	accountID, folder, uid := a.active, m.Folder, m.UID
	// Optimistic: hide the row, move the choice to its neighbour.
	a.all = append(a.all[:idx], a.all[idx+1:]...)
	if idx >= len(a.all) {
		a.selected = len(a.all) - 1
	}
	if !m.Seen {
		for i := range a.folders {
			if a.folders[i].Name == folder {
				a.folders[i].Unread = max(0, a.folders[i].Unread-1)
			}
		}
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.Move(ctx, accountID, folder, uid, dest)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = err.Error()
			}
			a.loadMessages(g)
		})
	})
}

// reply opens a reply or reply-all, quoting the body.
func (a *app) reply(all bool) {
	m, msg := a.current()
	if m == nil {
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
				a.openCompose(replyDraft(a, summary, body, all, false))
			})
		})
		return
	}
	a.openCompose(replyDraft(a, summary, body, all, false))
}

// forward opens a forwarded message with the original quoted.
func (a *app) forward() {
	m, msg := a.current()
	if m == nil {
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
	a.list.Choose(next)
	a.list.ScrollTo(next, ui.Center)
	a.bodyErr = ""
	a.reloadBody()
}

// toggleRead marks the chosen message read or unread, optimistically.
func (a *app) toggleRead() {
	if a.selected < 0 || a.selected >= len(a.all) {
		return
	}
	idx := a.selected
	m := a.all[idx]
	key := summaryKey(m)
	on := !m.Seen
	accountID, folder, uid := a.active, m.Folder, m.UID
	folderIndex := -1
	for i := range a.folders {
		if a.folders[i].Name == folder {
			folderIndex = i
		}
	}
	if on {
		a.all[idx].Seen = true
		if folderIndex >= 0 {
			a.folders[folderIndex].Unread = max(0, a.folders[folderIndex].Unread-1)
		}
	} else {
		a.all[idx].Seen = false
		if folderIndex >= 0 {
			a.folders[folderIndex].Unread++
		}
	}
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SetFlag(ctx, accountID, folder, uid, mail.FlagSeen, on)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				for i := range a.all {
					if summaryKey(a.all[i]) == key {
						a.all[i].Seen = !on
					}
				}
				if folderIndex >= 0 {
					if on {
						a.folders[folderIndex].Unread++
					} else {
						a.folders[folderIndex].Unread = max(0, a.folders[folderIndex].Unread-1)
					}
				}
				a.err = err.Error()
			}
		})
	})
}
