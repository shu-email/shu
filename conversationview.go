package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// viewToggle is the list header's switch between a row for each
// conversation and a row for each message. The Drafts entry lists drafts
// one by one either way, so it has none.
func (a *app) viewToggle(c *ui.Context) {
	if a.folder == draftsFolder {
		return
	}
	label := locales.GroupByConversation()
	ui.Toggle(c, &a.convToggle, "").Label(label).Tooltip(label).Padding(4, 6).
		OnChange(func() { a.setView(a.convToggle) }).
		Children(func() { ui.Icon(c, conversationsIcon).FontSize(15) })
}

// countBadge is how many messages a row of a conversation stands for.
func countBadge(c *ui.Context, n int, color ui.Color) {
	ui.Text(c, fmt.Sprint(n)).FontSize(10).Bold().TextColor(color).Shrink(0).
		Padding(0, 5).Radius(7).Border(1, color.Alpha(0.55)).Label(locales.MessageCount(n))
}

// toolButtonTip is a toolbar button whose tooltip says more than its
// label.
func (a *app) toolButtonTip(c *ui.Context, svg *ui.SVG, label, tip string, on bool, fn func()) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip).Disabled(!on).Padding(6, 8)
	if on && fn != nil {
		b = b.OnClick(fn)
	}
	return b.Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// threadToolbar is the reading toolbar in conversation view. Its actions
// act on the chosen conversations, every message their rows stand for,
// and are named so; the replies answer the newest message.
func (a *app) threadToolbar(c *ui.Context, t *ui.Theme) {
	targets := a.threadTargets()
	n := len(targets)
	has := n > 0
	allFlagged, allSeen := has, has
	for _, conv := range targets {
		allFlagged = allFlagged && conv.Flagged
		allSeen = allSeen && !conv.Unread
	}
	archive := a.folderBySpecial(mail.SpecialArchive)
	trash := a.folderBySpecial(mail.SpecialTrash)
	junk := a.folderBySpecial(mail.SpecialJunk)
	newest, _ := a.newestShown()
	ui.Toolbar(c, func() {
		a.toolButton(c, archiveIcon, locales.ArchiveConversations(n), has && archive != "", func() {
			a.moveThreads(mail.SpecialArchive)
		})
		a.toolButton(c, trashIcon, locales.DeleteConversations(n), has && trash != "", func() {
			a.moveThreads(mail.SpecialTrash)
		})
		a.toolButton(c, spamIcon, locales.JunkConversations(n), has && junk != "", func() {
			a.moveThreads(mail.SpecialJunk)
		})
		starLabel, star, starColor := locales.StarConversations(n), starIcon, t.Text
		if allFlagged {
			starLabel, star, starColor = locales.UnstarConversations(n), starFilledIcon, t.Warning
		}
		starButton := ui.Button(c, "").Label(starLabel).Tooltip(starLabel).Disabled(!has).Padding(6, 8)
		if has {
			starButton = starButton.OnClick(a.toggleThreadStar)
		}
		starButton.Children(func() { ui.Icon(c, star).FontSize(16).TextColor(starColor) })
		readLabel, readIcon := locales.MarkConversationsRead(n), mailOpenIcon
		if allSeen {
			readLabel, readIcon = locales.MarkConversationsUnread(n), mailIcon
		}
		a.toolButton(c, readIcon, readLabel, has, a.toggleThreadRead)
		a.toolDivider(c)
		reply := has && newest != nil
		a.toolButtonTip(c, replyIcon, locales.Reply(), locales.ReplyNewest(), reply, func() { a.reply(false) })
		a.toolButtonTip(c, replyAllIcon, locales.ReplyAll(), locales.ReplyAllNewest(), reply, func() { a.reply(true) })
		a.toolButtonTip(c, forwardIcon, locales.Forward(), locales.ForwardNewest(), reply, func() { a.forward() })
		if a.ai != nil {
			a.toolDivider(c)
			a.toolButton(c, sparklesIcon, locales.SummarizeConversation(), n == 1 && newest != nil, a.summarize)
		}
	}).Height(48).Padding(6, 12).Label(locales.ConversationActions()).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
}

// conversationPane is the reading pane in conversation view: the chosen
// conversation, its messages oldest first.
func (a *app) conversationPane(c *ui.Context, t *ui.Theme) {
	row := a.convAt(a.selected)
	cv := a.conv
	switch {
	case row == nil:
		ui.Column(c).Fill().Center().Gap(8).Children(func() {
			ui.Icon(c, mailOpenIcon).FontSize(36).TextColor(t.TextMuted)
			ui.Text(c, locales.NoConversationSelected()).FontSize(15).TextColor(t.TextMuted)
			ui.Text(c, locales.ShortcutsHint()).FontSize(11).TextColor(t.TextMuted.Alpha(0.7)).TextAlign(ui.Center)
		})
		return
	case cv == nil || cv.thread != row.Thread:
		ui.Row(c).Padding(24, 24).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.Loading()).FontSize(13).TextColor(t.TextMuted)
		})
		return
	case cv.err != "":
		ui.Column(c).Padding(24, 24).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, cv.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3)
			})
			ui.Row(c).Children(func() {
				ui.Button(c, locales.Retry()).OnClick(func() {
					a.conv = nil
					a.reloadBody()
				})
			})
		})
		return
	}
	subject := ""
	if len(cv.msgs) > 0 {
		subject = cv.msgs[0].Subject
	}
	if strings.TrimSpace(subject) == "" {
		subject = locales.NoSubject()
	}
	ui.Scroll(c.Key(threadKey(cv.thread))).Grow(1).Children(func() {
		ui.Column(c).Padding(20, 20, 24, 20).Gap(10).MinWidth(0).Children(func() {
			ui.Text(c, subject).FontSize(21).Bold().Balance()
			ui.Text(c, a.scopeLine(cv)).FontSize(12).TextColor(t.TextMuted)
			if job := a.summaries[threadKey(cv.thread)]; job != nil {
				a.summaryCard(c, t, threadKey(cv.thread), job)
			}
			for i, m := range cv.msgs {
				a.threadMessage(c, t, cv, i, m)
			}
		})
	})
}

// scopeLine says what the toolbar acts on: the messages the row stands
// for, in the list's folder, and where the others shown are.
func (a *app) scopeLine(cv *convView) string {
	parts := []string{locales.ConversationScope(len(cv.members), a.folderLabel(cv.scope))}
	member := map[string]bool{}
	for _, m := range cv.members {
		member[summaryKey(m)] = true
	}
	var order []string
	count := map[string]int{}
	for _, m := range cv.msgs {
		if member[summaryKey(m)] {
			continue
		}
		if count[m.Folder] == 0 {
			order = append(order, m.Folder)
		}
		count[m.Folder]++
	}
	for _, folder := range order {
		parts = append(parts, locales.ConversationElsewhere(count[folder], a.folderLabel(folder)))
	}
	return strings.Join(parts, " · ")
}

// threadMessage draws one message of the conversation on show: a line
// that expands it, or expanded, its header, its own actions, its
// attachments and its body.
func (a *app) threadMessage(c *ui.Context, t *ui.Theme, cv *convView, i int, m mail.Summary) {
	key := summaryKey(m)
	open := cv.open[key]
	n, total := i+1, len(cv.msgs)
	ui.Column(c.Key(key)).Radius(10).Border(1, t.Border).Background(t.Background).MinWidth(0).Children(func() {
		a.threadHeader(c, t, cv, n, total, m, open)
		if !open {
			return
		}
		ui.Column(c).Padding(0, 14, 14, 14).Gap(12).MinWidth(0).Children(func() {
			a.threadActions(c, t, cv, n, total, m)
			if cv.details[key] {
				a.messageFields(c, t, m)
			}
			body, loaded := a.bodies[key]
			if loaded && len(body.Message.Attachments) > 0 {
				a.attachments(c, t, m, body.Message.Attachments)
			}
			a.threadBody(c, t, cv, n, total, m)
		})
	})
}

// threadHeader is the line of a message that expands and collapses it,
// by a click or by Enter or Space while it has the focus.
func (a *app) threadHeader(c *ui.Context, t *ui.Theme, cv *convView, n, total int, m mail.Summary, open bool) {
	key := summaryKey(m)
	name := displayName(m.From)
	ui.ButtonBase(c).Label(locales.MessageHeading(n, total, name)).Expanded(open).
		OnClick(func() { a.toggleMessage(key) }).Padding(10, 14).Children(func() {
		ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).MinWidth(0).Children(func() {
			ui.Avatar(c, name, nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
					if !m.Seen {
						ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Background(t.Accent)
					}
					who := ui.Text(c, name).FontSize(13).SingleLine().Shrink(1)
					if open || !m.Seen {
						who.Bold()
					}
					if m.Folder != cv.scope {
						ui.Text(c, a.folderLabel(m.Folder)).FontSize(10).TextColor(t.TextMuted).Shrink(0).
							Padding(0, 5).Radius(6).Background(t.Surface).Border(1, t.Border)
					}
				})
				detail := m.Preview
				if open {
					if detail = a.recipientLine(m); detail == "" {
						detail = m.From.Email
					}
				}
				ui.Text(c, detail).FontSize(12).TextColor(t.TextMuted).SingleLine()
			})
			ui.Column(c).Shrink(0).AlignItems(ui.End).Gap(3).Children(func() {
				when := a.messageTime(c, m.Date)
				if open {
					when = a.longTime(c, m.Date)
				}
				ui.Text(c, when).FontSize(11).TextColor(t.TextMuted)
				if m.HasAttachments || m.Flagged {
					ui.Row(c).Gap(4).Children(func() {
						if m.HasAttachments {
							ui.Icon(c, paperclipIcon).FontSize(11).TextColor(t.TextMuted).Label(locales.HasAttachments())
						}
						if m.Flagged {
							ui.Icon(c, starFilledIcon).FontSize(11).TextColor(t.Warning)
						}
					})
				}
			})
		})
	})
}

// threadActions are an expanded message's own actions, which act on it
// alone. Their names say which message: each expanded message has them.
func (a *app) threadActions(c *ui.Context, t *ui.Theme, cv *convView, n, total int, m mail.Summary) {
	key := summaryKey(m)
	ui.Row(c).Gap(2).AlignItems(ui.Center).MinWidth(0).Children(func() {
		a.iconButton(c, replyIcon, locales.ForMessage(locales.Reply(), n, total), locales.ReplyToThis()).
			OnClick(func() { a.replyToShown(key, false) })
		a.iconButton(c, replyAllIcon, locales.ForMessage(locales.ReplyAll(), n, total), locales.ReplyAllToThis()).
			OnClick(func() { a.replyToShown(key, true) })
		a.iconButton(c, forwardIcon, locales.ForMessage(locales.Forward(), n, total), locales.ForwardThis()).
			OnClick(func() { a.forwardShown(key) })
		ui.Button(c, "").Label(locales.ForMessage(locales.MoreActions(), n, total)).Tooltip(locales.MoreActionsThis()).
			Padding(6, 8).Children(func() { ui.Icon(c, moreIcon).FontSize(16) }).
			PopoverMenuButton(func(menu *ui.Menu) { a.messageMenu(menu, key) })
		ui.Spacer(c)
		if b, ok := a.bodies[key]; ok && msgHasHTML(b.Message) {
			label := locales.ShowHTML()
			if cv.html == key {
				label = locales.ShowPlain()
			}
			ui.ButtonBase(c).Label(locales.ForMessage(label, n, total)).Padding(4, 6).
				OnClick(func() { a.toggleThreadHTML(key) }).Children(func() {
				ui.Text(c, label).FontSize(11).TextColor(t.Accent)
			})
		}
		label := locales.Details()
		if cv.details[key] {
			label = locales.HideDetails()
		}
		ui.ButtonBase(c).Label(locales.ForMessage(label, n, total)).Padding(4, 6).
			OnClick(func() { cv.details[key] = !cv.details[key] }).Children(func() {
			ui.Text(c, label).FontSize(11).TextColor(t.Accent)
		})
	})
}

// messageMenu is the menu of an expanded message's own actions.
func (a *app) messageMenu(menu *ui.Menu, key string) {
	cv := a.conv
	if cv == nil {
		return
	}
	m, ok := cv.message(key)
	if !ok {
		return
	}
	starLabel := locales.StarThisMessage()
	if m.Flagged {
		starLabel = locales.UnstarThisMessage()
	}
	if menu.Item(starLabel).Chosen() {
		a.flagMessage(m, mail.FlagFlagged, !m.Flagged)
	}
	readLabel := locales.MarkThisUnread()
	if !m.Seen {
		readLabel = locales.MarkThisRead()
	}
	if menu.Item(readLabel).Chosen() {
		a.flagMessage(m, mail.FlagSeen, !m.Seen)
	}
	menu.Separator()
	archive := a.folderBySpecial(mail.SpecialArchive)
	if menu.Item(locales.ArchiveThisMessage()).Disabled(archive == "" || m.Folder == archive).Chosen() {
		a.moveMessage(m, mail.SpecialArchive)
	}
	trash := a.folderBySpecial(mail.SpecialTrash)
	if menu.Item(locales.DeleteThisMessage()).Disabled(trash == "" || m.Folder == trash).Chosen() {
		a.moveMessage(m, mail.SpecialTrash)
	}
}

// messageFields are the raw sender and recipient fields of a message.
func (a *app) messageFields(c *ui.Context, t *ui.Theme, m mail.Summary) {
	ui.Column(c).Gap(2).Children(func() {
		field := func(name, value string) {
			if strings.TrimSpace(value) == "" {
				return
			}
			ui.Text(c, name+": "+value).FontSize(11).TextColor(t.TextMuted).Selectable()
		}
		field(locales.From(), m.From.String())
		field(locales.To(), addressesText(m.To))
		field(locales.Cc(), addressesText(m.Cc))
		field(locales.ReplyTo(), addressesText(m.ReplyTo))
		field(locales.MessageID(), m.MessageID)
	})
}

// threadBody draws an expanded message's body, or its progress or
// failure.
func (a *app) threadBody(c *ui.Context, t *ui.Theme, cv *convView, n, total int, m mail.Summary) {
	key := summaryKey(m)
	b, loaded := a.bodies[key]
	switch {
	case cv.bodyErr[key] != "":
		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, cv.bodyErr[key]).FontSize(12).Grow(1).MinWidth(0).MaxLines(3)
			})
			ui.Button(c, locales.Retry()).Label(locales.ForMessage(locales.Retry(), n, total)).
				OnClick(func() { a.retryBody(key) })
		})
	case !loaded:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.BodyLoading()).FontSize(13).TextColor(t.TextMuted)
		})
	case cv.html == key && b.Message.BodyHTML != "":
		ui.Column(c).Radius(8).Border(1, t.Border).Background(t.Background).Children(func() {
			if a.webview != nil {
				ui.WebView(c, a.webview).Height(480)
			} else {
				a.bodyText(c, t, b.Message.BodyText)
			}
		})
	case strings.TrimSpace(b.Message.BodyText) == "":
		ui.Text(c, locales.NoBody()).FontSize(13).TextColor(t.TextMuted)
	default:
		a.bodyText(c, t, b.Message.BodyText)
	}
}

// toggleThreadHTML shows a message of the conversation as HTML, or as
// plain text again. One message shows as HTML at a time: the window has
// one web view.
func (a *app) toggleThreadHTML(key string) {
	cv := a.conv
	if cv == nil {
		return
	}
	a.htmlKey = ""
	if cv.html == key {
		cv.html = ""
		return
	}
	cv.html = key
	a.ensureWebView()
	a.syncHTMLView()
}

// replyToShown answers a message of the conversation on show.
func (a *app) replyToShown(key string, all bool) {
	if cv := a.conv; cv != nil {
		if m, ok := cv.message(key); ok {
			a.replyTo(&m, a.bodyOf(key), all)
		}
	}
}

// forwardShown forwards a message of the conversation on show.
func (a *app) forwardShown(key string) {
	if cv := a.conv; cv != nil {
		if m, ok := cv.message(key); ok {
			a.forwardMessage(&m, a.bodyOf(key))
		}
	}
}

// bodyOf is a loaded message, or nil.
func (a *app) bodyOf(key string) *mail.Message {
	if b, ok := a.bodies[key]; ok {
		return &b.Message
	}
	return nil
}
