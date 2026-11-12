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
		a.list.Key = func(i int) any { return summaryKey(a.all[i]) }
		a.list.Label = func(i int) string {
			m := a.all[i]
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
		if chosen > 1 {
			ui.Button(c, locales.ClearSelection()).OnClick(func() { a.clearSelection() })
		}
	})
	ui.Text(c, a.listSummary()).FontSize(11).TextColor(t.TextMuted).SingleLine()
}

// listSummary is the line under the folder title.
func (a *app) listSummary() string {
	query := strings.TrimSpace(a.query)
	if query != "" || a.folder == "starred" {
		return locales.MessageCount(len(a.all))
	}
	for _, f := range a.folders {
		if f.Name == a.folder {
			total := f.Total
			if total == 0 {
				total = len(a.all)
			}
			return locales.MailboxSummary(total, f.Unread)
		}
	}
	return locales.MessageCount(len(a.all))
}

// unread counts the unseen messages of the list.
func (a *app) unread() int {
	n := 0
	for _, m := range a.all {
		if !m.Seen {
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
	if a.folder == "starred" || strings.TrimSpace(a.query) != "" {
		meta = a.folderLabel(m.Folder)
	}
	ui.Box(c).Padding(9, 14).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			dot := ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Margin(6, 0, 0, 0)
			if !m.Seen {
				dot.Background(dotColor)
			}
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					name := ui.Text(c, displayName(m.From)).FontSize(13).SingleLine().Grow(1)
					if !m.Seen {
						name.Bold()
					}
					name.TextColor(nameColor)
					if meta != "" {
						ui.Text(c, meta).FontSize(11).TextColor(muted).Shrink(0).SingleLine()
					}
					if m.HasAttachments {
						ui.Icon(c, paperclipIcon).FontSize(11).TextColor(muted)
					}
					if m.Flagged {
						ui.Icon(c, starFilledIcon).FontSize(11).TextColor(starColor)
					}
					ui.Text(c, a.messageTime(c, m.Date)).FontSize(11).TextColor(muted).Shrink(0)
				})
				subject := ui.Text(c, m.Subject).FontSize(13).SingleLine()
				if !m.Seen {
					subject.Bold()
				}
				ui.Text(c, m.Preview).FontSize(12).SingleLine().TextColor(muted)
			})
		})
	})
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
