package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// messageList draws the header, the search field, the banners and the
// virtual message list.
func (a *app) messageList(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(384).Shrink(0).MinWidth(0).Background(t.Background).Children(func() {
		ui.Column(c).Height(64).Shrink(0).Padding(8, 14, 10, 14).Gap(1).
			Justify(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			a.listHeader(c, t)
		})
		searching := strings.TrimSpace(a.query) != ""
		if searching {
			a.searchBar(c, t)
		}
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
			parts := []string{m.From.Name + " " + m.Subject}
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
		a.list.Selected = &a.selected
		a.list.Selection = &a.sel
		// The menu is the list's, after its rows: a right-click on a row
		// chooses it first, and a choice in the menu changes the rows
		// once they are built.
		ui.List(c.Key("messages"), &a.list, rows).Grow(1).Transition(listMotion).
			OnChange(func() {
				if a.sel.Len() == 0 {
					a.selected = -1
				}
				a.details = false
				a.bodyErr = ""
				a.reloadBody()
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
	})
}

// listHeader draws the folder title, its counts and the unread badge, or
// the number of results while searching.
func (a *app) listHeader(c *ui.Context, t *ui.Theme) {
	query := strings.TrimSpace(a.query)
	title := a.folderLabel(a.folder)
	chosen := a.sel.Len()
	switch {
	case chosen > 1 && a.threaded():
		title = locales.ConversationsSelected(chosen)
	case chosen > 1:
		title = locales.SelectedCount(chosen)
	case query != "":
		title = locales.SearchResults()
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(17).Bold().SingleLine()
		switch {
		case chosen > 1:
		case query != "":
			found := fmt.Sprint(len(a.all))
			if a.pager.more {
				found += "+"
			}
			ui.Badge(c, found).Background(t.Surface).TextColor(t.TextMuted)
		default:
			// A folder's badge is its unread count as the server keeps it,
			// the number the sidebar shows: the rows read so far are only
			// the newest pages. Other lists count their rows read.
			n, more := a.unread(), a.pager.more
			switch {
			case a.isServerFolder(a.folder):
				n, more = a.folderUnread(a.folder), false
			case a.isUnified():
				n, more = a.unifiedUnread(), false
			case a.shownLabel() != nil:
				n, more = a.shownLabel().Unread, false
			}
			if n > 0 {
				label := fmt.Sprint(n)
				if more {
					label += "+"
				}
				ui.Badge(c, label).Background(t.Accent).TextColor(t.AccentText)
			}
		}
		ui.Spacer(c)
		if a.loading {
			ui.Spinner(c).Size(14, 14)
		}
		a.viewToggle(c)
		if l := a.shownLabel(); l != nil && chosen <= 1 {
			a.labelHeaderMenu(c, *l)
		}
		if chosen > 1 {
			ui.Button(c, locales.ClearSelection()).OnClick(func() { a.clearSelection() })
		} else if a.ai != nil {
			// Drafts are the user's own unsent text: there is nothing in
			// them to triage, and no reason to send them to a model.
			ui.Button(c, "").Label(locales.TriageInbox()).Tooltip(locales.TriageInbox()).Padding(4, 6).
				Disabled(len(a.all) == 0 || a.draftsList()).OnClick(a.triageList).
				Children(func() { ui.Icon(c, sparklesIcon).FontSize(15) })
		}
	})
	ui.Text(c, a.listSummary()).FontSize(11).TextColor(t.TextMuted).SingleLine()
}

// isServerFolder reports whether id names one of the account's folders,
// not a list of Shu's own, as Starred and Drafts are.
func (a *app) isServerFolder(id string) bool {
	if id == "starred" || id == draftsFolder {
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

// undoBanner offers to take back the last archive, junk or delete.
func (a *app) undoBanner(c *ui.Context, t *ui.Theme) {
	ui.Row(c.Key("banner/undo")).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Success.Alpha(0.10)).Transition(bannerMotion).Children(func() {
		ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
		ui.Text(c, a.undo.label).FontSize(12).Bold().Grow(1)
		ui.Button(c, locales.Undo()).Label(locales.Undo()).
			OnClick(func() { a.undoLastMove() })
		a.iconButton(c, closeIcon, locales.Dismiss(), locales.Dismiss()).
			OnClick(func() { a.setUndo(nil) })
	})
}

// errorBanner shows the last failure with a retry action.
func (a *app) errorBanner(c *ui.Context, t *ui.Theme) {
	ui.Row(c.Key("banner/error")).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Warning.Alpha(0.12)).Transition(bannerMotion).Children(func() {
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
	ui.Column(c.Key("banner/pending")).Shrink(0).Gap(6).Padding(8, 14).
		Background(t.Warning.Alpha(0.10)).Transition(bannerMotion).Children(func() {
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

	muted := t.TextMuted
	nameColor := t.Text
	dotColor := t.Accent
	starColor := t.Warning
	if chosen {
		muted = t.AccentText.Alpha(0.78)
		nameColor = t.AccentText
		dotColor = t.AccentText
		starColor = t.AccentText
	}
	account, meta := a.rowPlace(m)
	who, title := a.rowHeading(m)
	ui.Box(c).Padding(9, 14).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			dot := ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Margin(6, 0, 0, 0)
			if unread {
				dot.Background(dotColor)
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
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
					ui.Text(c, a.messageTime(c, m.Date)).FontSize(11).TextColor(muted).Shrink(0)
				})
				// A row's labels end its subject's line, of which they take
				// at most half.
				if labels := a.rowLabelsOf(row.Index); len(labels) > 0 {
					ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
						subject := ui.Text(c, title).FontSize(13).SingleLine().Grow(1).MinWidth(0)
						if unread {
							subject.Bold()
						}
						a.labelChips(c, t, m.Account, labels, rowLabels, chosen).Shrink(0).MaxWidth(170)
					})
				} else {
					subject := ui.Text(c, title).FontSize(13).SingleLine()
					if unread {
						subject.Bold()
					}
				}
				if s, ok := a.suggestion(a.rowKey(row.Index)); ok {
					a.suggestionLine(c, t, s, chosen)
				} else {
					ui.Text(c, m.Preview).FontSize(12).SingleLine().TextColor(muted)
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
