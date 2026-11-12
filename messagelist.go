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
		if a.undo != nil {
			a.undoBanner(c, t)
		}
		if a.triage != nil {
			a.triageBanner(c, t)
		}
		if a.err != "" {
			a.errorBanner(c, t)
		}
		if len(a.pending) > 0 && !a.pendingDismissed {
			a.pendingBanner(c, t)
		}
		if msg := a.emptyState(); msg != "" {
			ui.Column(c).Grow(1).Center().Gap(8).Padding(24, 20).Children(func() {
				if a.loading {
					ui.Spinner(c).Size(18, 18)
				} else {
					ui.Icon(c, inboxIcon).FontSize(30).TextColor(t.TextMuted)
				}
				ui.Text(c, msg).FontSize(13).TextColor(t.TextMuted).TextAlign(ui.Center)
				if strings.TrimSpace(a.query) != "" {
					ui.Button(c, locales.SearchClear()).OnClick(func() {
						a.query = ""
						a.sel.Clear()
						a.search()
					})
				}
			})
			return
		}
		a.list.Key = func(i int) any { return a.rowKey(i) }
		a.list.Label = func(i int) string {
			m := a.all[i]
			if a.folder == draftsFolder {
				who, title := a.rowHeading(m)
				return who + " " + title
			}
			return m.From.Name + " " + m.Subject
		}
		a.list.Selected = &a.selected
		a.list.Selection = &a.sel
		ui.List(c, &a.list, len(a.all)).Grow(1).
			OnChange(func() {
				if a.sel.Len() == 0 {
					a.selected = -1
				}
				a.details = false
				a.bodyErr = ""
				a.reloadBody()
			}).
			OnSubmit(a.openSelected).
			Rows(a.messageRow)
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
			ui.Badge(c, fmt.Sprint(len(a.all))).Background(t.Surface).TextColor(t.TextMuted)
		default:
			if n := a.unread(); n > 0 {
				ui.Badge(c, fmt.Sprint(n)).Background(t.Accent).TextColor(t.AccentText)
			}
		}
		ui.Spacer(c)
		if a.loading {
			ui.Spinner(c).Size(14, 14)
		}
		a.viewToggle(c)
		if chosen > 1 {
			ui.Button(c, locales.ClearSelection()).OnClick(func() { a.clearSelection() })
		} else if a.ai != nil {
			// Drafts are the user's own unsent text: there is nothing in
			// them to triage, and no reason to send them to a model.
			ui.Button(c, "").Label(locales.TriageInbox()).Tooltip(locales.TriageInbox()).Padding(4, 6).
				Disabled(len(a.all) == 0 || a.folder == draftsFolder).OnClick(a.triageList).
				Children(func() { ui.Icon(c, sparklesIcon).FontSize(15) })
		}
	})
	ui.Text(c, a.listSummary()).FontSize(11).TextColor(t.TextMuted).SingleLine()
}

// listSummary is the line under the folder title. A folder's counts are
// of its messages, as the server keeps them, in either view.
func (a *app) listSummary() string {
	query := strings.TrimSpace(a.query)
	if query == "" && a.folder != "starred" && a.folder != draftsFolder {
		for _, f := range a.folders {
			if f.Name == a.folder {
				total := f.Total
				if total == 0 {
					total = len(a.all)
				}
				return locales.MailboxSummary(total, f.Unread)
			}
		}
	}
	if a.threaded() {
		return locales.ConversationCount(len(a.all))
	}
	return locales.MessageCount(len(a.all))
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
		return locales.SearchNoResults(strings.TrimSpace(a.query))
	default:
		return locales.EmptyFolder()
	}
}

// undoBanner offers to take back the last archive, junk or delete.
func (a *app) undoBanner(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Success.Alpha(0.10)).Children(func() {
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
	ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Warning.Alpha(0.12)).Children(func() {
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
	ui.Column(c).Shrink(0).Gap(6).Padding(8, 14).
		Background(t.Warning.Alpha(0.10)).Children(func() {
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
			if err != nil {
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
	meta := ""
	if (a.folder == "starred" || strings.TrimSpace(a.query) != "") && m.DraftID == "" {
		meta = a.folderLabel(m.Folder)
	}
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
						ui.Text(c, meta).FontSize(11).TextColor(muted).Shrink(0).SingleLine()
					}
					if attached {
						ui.Icon(c, paperclipIcon).FontSize(11).TextColor(muted).Label(locales.HasAttachments())
					}
					if flagged {
						ui.Icon(c, starFilledIcon).FontSize(11).TextColor(starColor)
					}
					ui.Text(c, a.messageTime(c, m.Date)).FontSize(11).TextColor(muted).Shrink(0)
				})
				subject := ui.Text(c, title).FontSize(13).SingleLine()
				if unread {
					subject.Bold()
				}
				if s, ok := a.suggestion(a.rowKey(row.Index)); ok {
					a.suggestionLine(c, t, s, chosen)
				} else {
					ui.Text(c, m.Preview).FontSize(12).SingleLine().TextColor(muted)
				}
			})
		})
	})
}

// rowHeading is the two lines that head a row: who and what. In Drafts
// every row is from the user, so whom the draft is to tells the rows
// apart; a draft may have neither that nor a subject yet.
func (a *app) rowHeading(m mail.Summary) (who, title string) {
	if a.folder != draftsFolder {
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
