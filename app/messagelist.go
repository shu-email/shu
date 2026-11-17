package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// listBody draws what the list shows under its head: the search bar, the
// notes of what just happened, and the rows themselves, one page at a
// time. The shell draws it as its stream; the three-pane window draws it
// under its own head.
func (a *app) listBody(c *ui.Context, t *ui.Theme) {
	searching := strings.TrimSpace(a.query) != ""
	if searching {
		a.searchBar(c, t)
	}
	a.streamBand(c, t)
	a.syncNotice(c, t)
	if len(a.waiting) > 0 {
		a.waitingNotice(c, t)
	}
	if a.sendNote != "" {
		a.sendNoteBanner(c, t)
	}
	if a.undo != nil {
		a.undoBanner(c, t)
	}
	if a.labels.note != "" {
		a.labelNoteBanner(c, t)
	}
	if a.folderNote != "" {
		a.folderNoteBanner(c, t)
	}
	if a.triage != nil {
		a.triageBanner(c, t)
	}
	if a.err != "" {
		a.errorBanner(c, t)
	}
	a.signInBanners(c, t)
	if len(a.pending) > 0 && !a.pendingDismissed {
		a.pendingBanner(c, t)
	}
	if msg := a.emptyState(); msg != "" {
		ui.Column(c.Key("empty")).Grow(1).Center().Gap(8).Padding(24, 20).Children(func() {
			switch {
			case a.loading:
				ui.Spinner(c).Size(18, 18)
			case labelOf(a.folder) != 0 && !searching:
				ui.Icon(c, tagIcon).FontSize(30).TextColor(t.TextMuted)
			default:
				ui.Icon(c, inboxIcon).FontSize(30).TextColor(t.TextMuted)
			}
			ui.Text(c, msg).FontSize(13).TextColor(t.TextMuted).TextAlign(ui.Center)
			if searching && !a.loading {
				a.noResults(c, t)
			}
			if older := a.emptyOlder(); older != endNone {
				a.olderControls(c, t, older)
			}
			if searching {
				ui.Button(c, locales.SearchClear()).OnClick(func() {
					a.query = ""
					a.sel.Clear()
					a.search()
				})
			}
		})
		return
	}
	// After the last row comes one that says what follows: the next
	// page, older mail on the server, or nothing. It is a header,
	// which the list never chooses. The frame goes on with the rows it
	// was built with: a page added as it lays out shows in the next.
	n, end := len(a.all), a.endState()
	rows := n
	if end != endNone {
		rows++
	}
	keys := make([]string, n)
	for i := range keys {
		keys[i] = a.rowKey(i)
	}
	a.rows.update(a.listQuery(), keys, c.Now())
	a.list.Key = func(i int) any {
		if i >= n {
			return endKey
		}
		return a.rowKey(i)
	}
	a.list.Label = func(i int) string {
		if i >= n {
			return a.endText(end)
		}
		m := a.all[i]
		if a.draftsList() {
			who, title := a.rowHeading(m)
			return who + " " + title
		}
		who := m.From.Name
		if a.folder == followUpFolder {
			// A follow-up row is a message the user sent: its name says
			// whom it waits for, as its heading does.
			who, _ = a.rowHeading(m)
		}
		parts := []string{who + " " + m.Subject}
		account, folder := a.rowPlace(m)
		if folder != "" {
			parts = append(parts, locales.InFolder(folder))
		}
		if account != "" {
			parts = append(parts, locales.InAccount(account))
		}
		if labels := a.labelNames(m.Account, a.rowLabelsOf(i)); labels != "" {
			parts = append(parts, locales.RowLabels(labels))
		}
		if reason := a.rowReason(i); reason != "" {
			parts = append(parts, reason)
		}
		return strings.Join(parts, ", ")
	}
	a.list.Header = func(i int) bool { return i >= n }
	// Rows drag their messages, or the conversations they stand for,
	// onto a folder in the sidebar (folderDrop).
	a.list.Selected = &a.selected
	a.list.Selection = &a.sel
	// The menu is the list's, after its rows: a right-click on a row
	// chooses it first, and a choice in the menu changes the rows
	// once they are built.
	ui.Box(c.Key("list")).Grow(1).MinHeight(0).Children(func() {
		ui.List(c.Key("messages"), &a.list, rows).Grow(1).Transition(listMotion).
			OnChange(func() {
				// Choosing mail closes a search field opened and left
				// empty in a narrow pane.
				if strings.TrimSpace(a.query) == "" {
					a.searchOpen = false
				}
				if a.sel.Len() == 0 {
					a.selected = -1
				}
				a.details = false
				a.bodyErr = ""
				a.reloadBody()
				// In the shell, a chosen row is mail being read: the stream's
				// column is its own from now (shell.go).
				a.openReading()
			}).
			OnSubmit(a.openSelected).
			Rows(func(row ui.ListRow) {
				if row.Index >= n {
					a.endRow(row.Context, row.Context.Theme(), end)
					return
				}
				a.messageRow(row)
			}).
			PopoverMenu(a.rowMenu)
		// The caption of the group the rows on screen belong to stays at the
		// top as its cards scroll under it: the group's own caption goes with
		// its first card, and this takes its place from then on.
		if label := a.pinnedCaption(c.Now()); label != "" {
			ui.Row(c.Key("pinned-caption")).Absolute().Top(0).Left(0).Right(0).
				Padding(4, 16, 6, listInset).Background(t.Background).Children(func() {
				ui.Text(c, label).FontSize(11).Bold().TextColor(t.TextMuted).SingleLine()
			})
		}
	})
	// The next page is read as the user nears the end of the rows
	// read, by scrolling or with the keys, so that it is there when
	// they get to it.
	if _, last := a.list.Visible(); last >= len(a.all)-listAhead {
		a.loadMore()
	}
	// Put the keyboard in the list once mail loads, so J/K work
	// without a first click. A search field that took the focus keeps
	// it.
	if a.focusList && len(a.all) > 0 && !a.searchField.Focused(c) {
		a.focusList = false
		a.list.Handle.Focus(c)
	}
}

// isServerFolder reports whether id names one of the account's folders,
// not a list of Shu's own, as Starred and Drafts are.
func (a *app) isServerFolder(id string) bool {
	if id == "starred" || id == draftsFolder || id == remindersFolder || id == followUpFolder {
		return false
	}
	for _, f := range a.folders {
		if f.Name == id {
			return true
		}
	}
	return false
}

// listSummary is the line under the folder title. A folder's counts are
// of its messages, as the server keeps them, in either view, with how
// many of them are on this device when not all are; the unified inbox's
// are each account's unread mail. Other lists count their rows read, and
// say when there are more, and a search where it looked.
func (a *app) listSummary() string {
	query := strings.TrimSpace(a.query)
	if query == "" && a.isUnified() {
		return a.unifiedCounts()
	}
	if query == "" && a.folder != "starred" && a.folder != draftsFolder {
		for _, f := range a.folders {
			if f.Name == a.folder {
				return a.cacheLine(f)
			}
		}
	}
	var count string
	switch more := a.pager.more; {
	case a.threaded() && more:
		count = locales.ConversationCountMore(len(a.all))
	case a.threaded():
		count = locales.ConversationCount(len(a.all))
	case more:
		count = locales.MessageCountMore(len(a.all))
	default:
		count = locales.MessageCount(len(a.all))
	}
	if query != "" {
		return count + " · " + a.scopeWhere()
	}
	if l := a.shownLabel(); l != nil && l.Local {
		return count + " · " + locales.LabelLocal()
	}
	return count
}

// unread counts the unread rows of the list: messages, or conversations
// with an unread message.
func (a *app) unread() int {
	n := 0
	for i, m := range a.all {
		if c := a.convAt(i); c != nil {
			if c.Unread {
				n++
			}
		} else if !m.Seen {
			n++
		}
	}
	return n
}

// emptyState explains why the list shows no rows, or returns "".
func (a *app) emptyState() string {
	switch {
	case a.folder == "":
		return locales.NoFolders()
	case a.loading && len(a.all) == 0:
		return locales.Loading()
	case len(a.all) > 0:
		return ""
	case a.smartFolderGone():
		return locales.SmartFolderGone(a.shownSmart().Name)
	case strings.TrimSpace(a.query) != "":
		return noResultsText(a.query)
	case a.emptyOlder() != endNone:
		return locales.NoneHere()
	case labelOf(a.folder) != 0:
		return locales.LabelEmpty()
	default:
		return locales.EmptyFolder()
	}
}

// undoBanner offers to take back the last change: a move, a label, or a
// flag.
func (a *app) undoBanner(c *ui.Context, t *ui.Theme) {
	bannerRow(c, "banner/undo", t.Success.Alpha(0.10)).Children(func() {
		ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
		ui.Text(c, a.undo.label).FontSize(12).Bold().Grow(1)
		ui.Button(c, locales.Undo()).Label(locales.Undo()).
			OnClick(func() { a.undoLastAction() })
		a.iconButton(c, closeIcon, locales.Dismiss(), locales.Dismiss()).
			OnClick(func() { a.setUndo(nil) })
	})
}

// errorBanner shows the last failure with a retry action.
func (a *app) errorBanner(c *ui.Context, t *ui.Theme) {
	bannerRow(c, "banner/error", t.Warning.Alpha(0.12)).Children(func() {
		ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		ui.Text(c, a.err).FontSize(12).TextColor(t.Text).Grow(1).MaxLines(2)
		if ui.Button(c, locales.Retry()).OnClick(func() {
			a.err = ""
			a.refresh()
		}).Clicked() {
		}
	})
}

// pendingBanner reports unsent or unknown outbox entries and offers the
// manual retry the engine never does on its own.
func (a *app) pendingBanner(c *ui.Context, t *ui.Theme) {
	unknown := false
	for _, p := range a.pending {
		if p.State == mail.StateUnknown {
			unknown = true
		}
	}
	message := locales.PendingSends()
	if unknown {
		message = locales.DeliveryUnknown(len(a.pending))
	}
	bannerColumn(c, "banner/pending", t.Warning.Alpha(0.10)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, message).FontSize(12).Bold().Grow(1)
		})
		if unknown {
			ui.Text(c, locales.DeliveryUnknownDetail()).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c, locales.RetrySend()).OnClick(func() {
				a.retryPending()
			})
			ui.Button(c, locales.Dismiss()).OnClick(func() { a.pendingDismissed = true })
		})
	})
}

// retryPending asks the engine to send the first unsent entry again.
func (a *app) retryPending() {
	if len(a.pending) == 0 {
		return
	}
	id, accountID := a.pending[0].ID, a.active
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, err := a.svc.RetryPending(ctx, id)
		a.post(func() {
			if a.stale(g) || a.active != accountID {
				return
			}
			if err != nil && !a.signInRequired(accountID, err) {
				a.err = locales.SendFailed(err.Error())
			}
			a.loadPending(g)
			a.loadMessages(g)
		})
	})
}

// messageRow builds one row of the virtual list. Rows stay dense: a dot,
// sender and meta, subject, preview.
func (a *app) messageRow(row ui.ListRow) {
	c := row.Context
	t := c.Theme()
	if row.Index < 0 || row.Index >= len(a.all) {
		return
	}
	m := a.all[row.Index]
	chosen := row.Selected()
	// A conversation's row shows its newest message, and what all its
	// messages in the list add up to.
	unread, flagged, attached, count := !m.Seen, m.Flagged, m.HasAttachments, 0
	if conv := a.convAt(row.Index); conv != nil {
		unread, flagged, attached, count = conv.Unread, conv.Flagged, conv.HasAttachments, conv.Count
	}
	reason := a.rowReason(row.Index)

	// A chosen row is tinted with the accent rather than filled with it:
	// mail stays readable while it is chosen, and a mail list is mostly
	// read rather than acted on.
	muted := t.TextMuted
	nameColor := t.Text
	dotColor := t.Accent
	starColor := t.Warning
	if chosen {
		nameColor = t.Text
	}
	account, meta := a.rowPlace(m)
	who, title := a.rowHeading(m)
	// A row is a card on the pane, under the caption of the day it is
	// from, so that a list reads as a few groups of a few cards rather
	// than as one ribbon of text.
	now := c.Now()
	first, last := a.startsGroup(row.Index, now), a.endsGroup(row.Index, now)
	// The cards of a group are one block: they touch, and only the block's
	// own ends are rounded.
	outer := ui.Box(c).Background(t.Background).Padding(0, 10)
	switch {
	case first:
		outer.Padding(12, 10, 0, 10)
	case last:
		outer.Padding(0, 10, 12, 10)
	}
	a.rows.row(outer, a.rowKey(row.Index), chosen, now)
	outer.Children(func() {
		if first {
			ui.Row(c).Padding(4, 6, 6, 14).Children(func() {
				ui.Text(c, a.rowGroupName(row.Index, now)).FontSize(11).Bold().
					TextColor(t.TextMuted).SingleLine()
			})
		}
		a.messageCard(c, t, row, m, chosen, unread, flagged, attached, count, first, last,
			reason, who, title, account, meta, muted, nameColor, dotColor, starColor)
	})
}

// messageCard is one message as a card: the sender's picture, who and
// when, the subject, a preview and what the message offers to copy.
func (a *app) messageCard(c *ui.Context, t *ui.Theme, row ui.ListRow, m mail.Summary,
	chosen, unread, flagged, attached bool, count int, first, last bool,
	reason, who, title, account, meta string,
	muted, nameColor, dotColor, starColor ui.Color) {
	card := ui.Box(c).Padding(12, 14)
	// The group is one block, so an edge between two of its cards is
	// drawn once, by the card above it.
	card.BorderWidth(1, 1, 0, 1)
	switch {
	case first && last:
		card.Radius(t.Radius)
		card.BorderWidth(1, 1, 1, 1)
	case first:
		card.Radius(t.Radius, t.Radius, 0, 0)
	case last:
		card.Radius(0, 0, t.Radius, t.Radius)
		card.BorderWidth(1, 1, 1, 1)
	default:
		card.Radius(0)
	}
	// The tints are mixed with the pane's own color rather than left
	// translucent: a translucent fill would show the window's material
	// through the list, and the card would take the desktop's color.
	switch {
	case chosen:
		card.Background(t.Accent.Alpha(0.10).Over(t.Surface)).BorderColor(t.Accent.Alpha(0.35))
	case row.Hovered():
		card.Background(t.SurfaceHover).BorderColor(t.Border)
	default:
		card.Background(t.Surface).BorderColor(t.Border.Alpha(0.7))
	}
	card.Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Start).Children(func() {
			// The sender's icon heads the row, in place of the dot: the
			// name says who wrote, so the picture needs no name of its own.
			ui.Avatar(c, who, a.senderAvatar(a.rowAvatar(m))).AccessHidden(true)
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if unread {
						ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Background(dotColor)
					}
					name := ui.Text(c, who).FontSize(13).SingleLine().Grow(1)
					if unread {
						name.Bold()
					}
					name.TextColor(nameColor)
					if count > 1 {
						countBadge(c, count, muted)
					}
					if meta != "" {
						ui.Text(c, meta).FontSize(11).TextColor(muted).Shrink(0).MaxWidth(110).SingleLine()
					}
					if account != "" {
						accountTag(c, account, muted)
					}
					if attached {
						ui.Icon(c, paperclipIcon).FontSize(11).TextColor(muted).Label(locales.HasAttachments())
					}
					if flagged {
						ui.Icon(c, starFilledIcon).FontSize(11).TextColor(starColor)
					}
					ui.Text(c, a.rowTime(c, m)).FontSize(11).TextColor(muted).Shrink(0)
				})
				// A row's labels end its subject's line, of which they take
				// at most half.
				if labels := a.rowLabelsOf(row.Index); len(labels) > 0 {
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						subject := ui.Text(c, title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
						if unread {
							subject.Bold()
						}
						a.labelChips(c, t, m.Account, labels, rowLabels, false).Shrink(0).MaxWidth(170)
					})
				} else {
					subject := ui.Text(c, title).FontSize(13).SingleLine()
					if unread {
						subject.Bold()
					}
				}
				switch {
				case a.copied != "" && a.copied == a.rowKey(row.Index):
					ui.Text(c, locales.CopiedExtract()).FontSize(12).SingleLine().TextColor(t.Accent)
				default:
					if s, ok := a.suggestion(a.rowKey(row.Index)); ok {
						a.suggestionLine(c, t, s, chosen)
					} else if len(rowExtracts(m)) > 0 {
						ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
							ui.Text(c, m.Preview).FontSize(12).SingleLine().TextColor(muted).Grow(1).MinWidth(0)
							a.extractChips(c, t, m, false, a.rowKey(row.Index))
						})
					} else {
						ui.Text(c, m.Preview).FontSize(12).SingleLine().TextColor(muted)
					}
				}
				if reason != "" {
					ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).Children(func() {
						ui.Icon(c, searchIcon).FontSize(10).TextColor(muted)
						ui.Text(c, reason).FontSize(11).SingleLine().TextColor(muted).Shrink(1)
					})
				}
			})
		})
	})
}

// rowReason says why a search found row i: where in the message, or in
// which messages of the conversation, its terms are.
func (a *app) rowReason(i int) string {
	if strings.TrimSpace(a.query) == "" || i < 0 || i >= len(a.all) {
		return ""
	}
	if conv := a.convAt(i); conv != nil {
		return convMatchedLine(*conv)
	}
	return matchedLine(a.all[i].Matched)
}

// rowHeading is the two lines that head a row: who and what. In Drafts
// every row is from the user, so whom the draft is to tells the rows
// apart; a draft may have neither that nor a subject yet.
func (a *app) rowHeading(m mail.Summary) (who, title string) {
	if a.folder == followUpFolder {
		// The row is a message the user sent: whom they wrote to tells the
		// conversations apart.
		if who = recipients(m.To); who == "" {
			who = locales.NoRecipient()
		}
		return who, m.Subject
	}
	if !a.draftsList() {
		return displayName(m.From), m.Subject
	}
	if who = recipients(m.To); who == "" {
		who = locales.NoRecipientsYet()
	}
	if title = m.Subject; strings.TrimSpace(title) == "" {
		title = locales.NoSubject()
	}
	return who, title
}

// rowAvatar is the address whose icon heads a row: the sender's, or the
// recipient's where the row names the recipient instead of the sender.
func (a *app) rowAvatar(m mail.Summary) mail.Address {
	if a.folder == followUpFolder || a.draftsList() {
		if len(m.To) > 0 {
			return m.To[0]
		}
		return mail.Address{}
	}
	return m.From
}

// displayName is the sender's name, else address.
func displayName(a mail.Address) string {
	if a.Name != "" {
		return a.Name
	}
	if a.Email != "" {
		return a.Email
	}
	return "?"
}
