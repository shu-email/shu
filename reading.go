package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// readingPane draws the toolbar, the header, attachments and the body.
func (a *app) readingPane(c *ui.Context, t *ui.Theme, head float32) {
	a.services = c.Services()
	ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
		// The toolbar floats on glass over what is read, which scrolls
		// under it and fades out at its edge.
		pane := ui.Box(c).Grow(1).MinHeight(0)
		// The pane's width as the last frame laid it out decides whether
		// the search field fits beside the actions.
		a.readingWidth = pane.LayoutBounds().W
		pane.Children(func() {
			if a.threaded() {
				a.conversationPane(c, t)
			} else {
				a.messagePane(c, t)
			}
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(readingInset + 10).
				PassThrough().Material(glass.ScrollEdge{Hard: true})
			// The top of the pane, between the bar's groups, drags the
			// window, as the top of the list does.
			ui.Box(c).Absolute().Top(0).Left(0).Right(0).Height(head).DragWindow()
			a.readingToolbar(c, t)
		})
	})
}

// messagePane is the reading pane of one message.
func (a *app) messagePane(c *ui.Context, t *ui.Theme) {
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
	inset := readingInset
	if a.draftsList() {
		// The draft's banner sits under the toolbar, and what is read
		// scrolls under the banner.
		ui.Column(c).Shrink(0).Padding(readingInset, 0, 0, 0).Children(func() {
			a.draftBanner(c, t, *m)
		})
		inset = 0
		if strings.TrimSpace(subject) == "" {
			subject = locales.NoSubject()
		}
	}
	ui.Scroll(c.Key(summaryKey(*m))).Grow(1).ScrollbarInsets(inset, 0, 0).Transition(readingMotion).Children(func() {
		ui.Column(c).Padding(24+inset, 30, 24, 30).Gap(16).MinWidth(0).Children(func() {
			ui.Column(c).Gap(4).MinWidth(0).Children(func() {
				ui.Text(c, subject).FontSize(21).Bold().Balance()
				if place := a.placeLine(*m); place != "" {
					ui.Text(c, place).FontSize(12).TextColor(t.TextMuted).SingleLine()
				}
				a.messageLabels(c, t, *m)
			})
			a.senderHeader(c, t, *m)
			a.securityNotice(c, t, *m, msg)
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
	if a.threaded() {
		a.threadToolbar(c, t)
		return
	}
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
	places := make([]mailPlace, 0, 1)
	for _, m := range targets {
		places = append(places, mailPlace{m.Account, m.Folder})
	}
	archive := a.canMove(places, mail.SpecialArchive)
	trash := a.canMove(places, mail.SpecialTrash)
	junk := a.canMove(places, mail.SpecialJunk)
	// A draft of this device is no message of the server: it cannot be
	// moved, flagged or answered, only edited or discarded. The actions
	// for mail are off while one is among the chosen rows.
	drafts := localDrafts(targets)
	onlyDrafts := has && drafts == len(targets)
	has = has && drafts == 0
	lt := a.labelTargets()
	compact, field := a.readingCompact(), a.readingSearchShown()
	var labels ui.Element
	hasLabels := false
	a.readingBar(c, locales.MessageActions(), func() {
		a.glassGroup(c, locales.ReplyActions(), func() {
			for _, all := range a.replyOrder() {
				icon, label := replyLook(all)
				a.toolButton(c, icon, label, has, func() { a.reply(all) })
			}
			a.toolButton(c, forwardIcon, locales.Forward(), has, func() { a.forward() })
		})
		// A narrow bar folds its groups into menus, and gives the search
		// in use the rest of its room.
		if compact {
			if field {
				return
			}
			a.compactMessageMoves(c, has, onlyDrafts, archive, trash, junk)
			labels, hasLabels = a.compactMessageMarks(c, has, allFlagged, allSeen, lt)
		} else {
			a.glassGroup(c, locales.MoveActions(), func() {
				a.messageMoveButtons(c, has, onlyDrafts, archive, trash, junk)
			})
			a.glassGroup(c, locales.MarkActions(), func() {
				starLabel, star := locales.StarMessage(), starIcon
				starColor := t.Text
				if allFlagged {
					starLabel, star = locales.UnstarMessage(), starFilledIcon
					starColor = t.Warning
				}
				starButton := ui.Button(c, "").Label(starLabel).Tooltip(starLabel).Disabled(!has).
					Padding(barPad, barPad).Radius(barButton)
				if has {
					starButton = starButton.OnClick(func() { a.toggleStar() })
				}
				starButton.Children(func() {
					ui.Icon(c, star).FontSize(16).TextColor(starColor).ContentTransition(swapIcon)
				})
				readLabel, readIcon := locales.MarkRead(), mailOpenIcon
				if allSeen {
					readLabel, readIcon = locales.MarkUnread(), mailIcon
				}
				a.toolButton(c, readIcon, readLabel, has, func() { a.toggleRead() })
				labels, hasLabels = a.labelButton(c, lt), true
			})
		}
		if a.ai != nil {
			// A summary is of the one message open, once its body is here.
			_, msg := a.current()
			a.barCapsule(c, func() ui.Element {
				return a.toolButton(c, sparklesIcon, locales.Summarize(), msg != nil && len(targets) == 1 && has, a.summarize)
			})
		}
	})
	if hasLabels {
		a.labelPicker(c, t, labels, lt)
	}
}

// messageMoveButtons are a message toolbar's archive, delete and junk.
func (a *app) messageMoveButtons(c *ui.Context, has, onlyDrafts, archive, trash, junk bool) {
	a.toolButton(c, archiveIcon, locales.ArchiveMessage(), has && archive, func() {
		a.moveSelected(mail.SpecialArchive)
	})
	if onlyDrafts {
		// Deleting a draft is discarding it, which asks first.
		a.toolButton(c, trashIcon, locales.DiscardDraft(), true, a.discardSelected)
	} else {
		a.toolButton(c, trashIcon, locales.DeleteMessage(), has && trash, func() {
			a.moveSelected(mail.SpecialTrash)
		})
	}
	a.toolButton(c, spamIcon, locales.JunkMessage(), has && junk, func() {
		a.moveSelected(mail.SpecialJunk)
	})
}

// compactMessageMoves folds the move actions into the menu of one button.
func (a *app) compactMessageMoves(c *ui.Context, has, onlyDrafts, archive, trash, junk bool) {
	a.glassGroup(c, "", func() {
		a.barMenuButton(c, archiveIcon, locales.MoveActions(), locales.MoveActions(), func(m *ui.Menu) {
			if m.Item(locales.ArchiveMessage()).Disabled(!has || !archive).Chosen() {
				a.moveSelected(mail.SpecialArchive)
			}
			if onlyDrafts {
				if m.Item(locales.DiscardDraft()).Chosen() {
					a.discardSelected()
				}
			} else if m.Item(locales.DeleteMessage()).Disabled(!has || !trash).Chosen() {
				a.moveSelected(mail.SpecialTrash)
			}
			if m.Item(locales.JunkMessage()).Disabled(!has || !junk).Chosen() {
				a.moveSelected(mail.SpecialJunk)
			}
		})
	})
}

// compactMessageMarks folds star, read and labels into one button's menu.
// While the checklist of labels is open it returns the button it hangs
// from, which the capsule then holds.
func (a *app) compactMessageMarks(c *ui.Context, has, allFlagged, allSeen bool, lt labelTargets) (ui.Element, bool) {
	starLabel, star := locales.StarMessage(), starIcon
	if allFlagged {
		starLabel, star = locales.UnstarMessage(), starFilledIcon
	}
	readLabel := locales.MarkRead()
	if allSeen {
		readLabel = locales.MarkUnread()
	}
	var anchor ui.Element
	picker := false
	a.glassGroup(c, "", func() {
		anchor = a.barMenuButton(c, star, locales.MarkActions(), locales.MarkActions(), func(m *ui.Menu) {
			if m.Item(starLabel).Disabled(!has).Chosen() {
				a.toggleStar()
			}
			if m.Item(readLabel).Disabled(!has).Chosen() {
				a.toggleRead()
			}
			m.Separator()
			m.Submenu(locales.Labels(), func(m *ui.Menu) { a.labelMenuItems(m, lt) })
		})
		if a.labels.picker {
			anchor, picker = a.labelButton(c, lt), true
		}
	})
	return anchor, picker
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

// recipientLine writes "to me" when the message's account is the only
// recipient, else the recipients.
func (a *app) recipientLine(m mail.Summary) string {
	if acc := a.account(m.Account); len(m.To) == 1 && acc != nil && strings.EqualFold(m.To[0].Email, acc.Email) {
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

// attachments lists the message's attachments with a Save as button. A
// draft of this device shows what it has attached and no more: its files
// are changed in its compose window.
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
					if warning, icon := attachmentWarning(att); warning != "" {
						ui.Row(c).Gap(4).AlignItems(ui.Center).Label(warning).Children(func() {
							ui.Icon(c, icon).FontSize(10).TextColor(t.Warning)
							ui.Text(c, warning).FontSize(10).TextColor(t.Warning).SingleLine()
						})
					}
				})
				if m.DraftID != "" {
					return
				}
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
	accountID, folder, uid, part := m.Account, m.Folder, m.UID, att.Part
	name := safeFileName(att.Filename)
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, data, err := a.svc.Attachment(ctx, accountID, folder, uid, part)
		if err != nil {
			a.post(func() {
				// The banner of an account that has to sign in again says
				// that downloads wait for it.
				if !a.signInRequired(accountID, err) && !a.stale(g) {
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
// the one row chosen. In conversation view they are the rows' newest
// messages, and the actions act on threadTargets.
func (a *app) targets() []mail.Summary {
	if a.selected >= 0 && a.selected < len(a.all) && a.sel.Len() == 1 && a.sel.Has(a.rowKey(a.selected)) {
		// The usual case, which a long list need not be walked for.
		return []mail.Summary{a.all[a.selected]}
	}
	if a.sel.Len() > 0 {
		chosen := map[string]bool{}
		for k := range a.sel.All() {
			chosen[k] = true
		}
		var out []mail.Summary
		for i, m := range a.all {
			if chosen[a.rowKey(i)] {
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
	if a.threaded() {
		a.toggleThreadStar()
		return
	}
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
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, m := range targets {
			if err = a.svc.SetFlag(ctx, m.Account, m.Folder, m.UID, mail.FlagFlagged, on); err != nil {
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
				a.err = failureText(err)
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
	if a.threaded() {
		a.moveThreads(special)
		return
	}
	all := a.targets()
	// The keys reach here whatever the toolbar offers: a draft of this
	// device is in no folder of the server to be moved from.
	if len(all) == 0 || localDrafts(all) > 0 {
		return
	}
	// Each message goes to the folder of its own account, and stays where
	// that account has none or where it is in that folder already.
	var targets []mail.Summary
	dests := map[string]string{}
	for _, m := range all {
		if a.moves(mailPlace{m.Account, m.Folder}, special) {
			dests[m.Account] = a.specialFolder(m.Account, special)
			targets = append(targets, m)
		}
	}
	if len(targets) == 0 {
		return
	}
	chosen := map[string]bool{}
	for _, m := range targets {
		chosen[summaryKey(m)] = true
		if !m.Seen {
			a.addUnread(m.Account, m.Folder, -1)
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
	a.updateBadge()
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			first mail.MoveResult
			err   error
		)
		for i, m := range targets {
			result, moveErr := a.svc.Move(ctx, m.Account, m.Folder, m.UID, dests[m.Account])
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
				a.err = failureText(err)
			} else if len(targets) == 1 && first.DestUID != 0 {
				label := locales.Archived()
				switch special {
				case mail.SpecialTrash:
					label = locales.Deleted()
				case mail.SpecialJunk:
					label = locales.MovedToJunk()
				}
				m := targets[0]
				a.setUndo(&undoMove{accountID: m.Account, source: m.Folder, dest: dests[m.Account], destUID: first.DestUID, label: label})
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
	if len(u.moved) > 0 {
		a.undoThreadMove(u)
		return
	}
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
				a.err = failureText(err)
				return
			}
			a.syncMovedBack(g, u.accountID, u.source)
		})
	})
}

// replyOrder is the order of the reply actions: the default reply's,
// which R does, then the other kind's, which Shift+R does. Each says
// whether it answers everyone.
func (a *app) replyOrder() [2]bool {
	all := a.composeSet.Reply == mail.ReplyAll
	return [2]bool{all, !all}
}

// replyLook is the icon and the name of a reply action.
func replyLook(all bool) (*ui.SVG, string) {
	if all {
		return replyAllIcon, locales.ReplyAll()
	}
	return replyIcon, locales.Reply()
}

// reply opens a reply or reply-all, quoting the body. In conversation
// view it answers the conversation's newest message.
func (a *app) reply(all bool) {
	m, msg := a.current()
	if a.threaded() {
		m, msg = a.newestShown()
	}
	a.replyTo(m, msg, all)
}

// replyTo opens a reply or reply-all to a message, fetching its body
// first when it is not loaded.
func (a *app) replyTo(m *mail.Summary, msg *mail.Message, all bool) {
	a.withBody(m, msg, func(full mail.Message) { a.openReply(full, all) })
}

// withBody opens a window on a message with its body, fetching the body
// first when it is not loaded. A draft of this device is not answered or
// forwarded: it is edited.
func (a *app) withBody(m *mail.Summary, msg *mail.Message, open func(mail.Message)) {
	if m == nil || m.DraftID != "" {
		return
	}
	summary := *m
	if msg != nil && msg.BodyText != "" {
		open(withSummary(summary, *msg))
		return
	}
	accountID, folder, uid := summary.Account, summary.Folder, summary.UID
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		result, err := a.svc.FetchMessage(ctx, accountID, folder, uid, false)
		a.post(func() {
			if a.stale(g) {
				return
			}
			full := mail.Message{Summary: summary}
			if err == nil {
				full = withSummary(summary, result.Message)
				a.bodies[summaryKey(summary)] = result
			}
			open(full)
		})
	})
}

// withSummary is a loaded message under the list's summary of it, which
// says the account, folder and message it is.
func withSummary(m mail.Summary, msg mail.Message) mail.Message {
	msg.Summary = m
	return msg
}

// forward opens a forwarded message with the original quoted. In
// conversation view it forwards the conversation's newest message.
func (a *app) forward() {
	m, msg := a.current()
	if a.threaded() {
		m, msg = a.newestShown()
	}
	a.forwardMessage(m, msg)
}

// forwardMessage opens a forward of a message, fetching its body first
// when it is not loaded.
func (a *app) forwardMessage(m *mail.Summary, msg *mail.Message) {
	a.withBody(m, msg, a.openForward)
}

// replyDraft is a reply to m, or a forward of it, as its window opens:
// the recipients, the subject, the identity it goes from, and the body,
// with the quote and the account's signature as the settings put them,
// and where in it the caret goes. The account the message is in answers
// it, from the address it reached: the one it was addressed to, or for
// the user's own message, the one it went from.
func (a *app) replyDraft(m mail.Message, all, forward bool) (*mail.Outgoing, int) {
	ids := a.identitiesOf(m.Account)
	from := mail.ReplyIdentity(ids, m)
	for _, id := range ids {
		if email := strings.TrimSpace(m.From.Email); email != "" && strings.EqualFold(email, id.Email) {
			from = id
		}
	}
	out := &mail.Outgoing{Identity: from.ID}
	sig := a.signatureOf(m.Account)
	var body mail.Body
	if forward {
		out.Subject = withPrefix(m.Subject, "Fwd: ")
		body = mail.ForwardBody(sig, m.Summary, m.BodyText, quoteWords())
	} else {
		out.Subject = withPrefix(m.Subject, "Re: ")
		out.To = replyRecipients(m.Summary, ids, all)
		body = mail.ReplyBody(a.composeSet, sig, m.Summary, m.BodyText, quoteWords())
	}
	out.Text = body.Text
	if m.MessageID != "" {
		out.InReplyTo = m.MessageID
		out.References = append(append([]string(nil), m.References...), m.MessageID)
	}
	return out, body.Caret
}

// replyRecipients are whom a reply to m goes to: its sender, and for all
// everyone else it went to, each address once. The user's own addresses,
// those of all the account's identities, are left out. Answering one's
// own message follows it up: it goes to the people the message went to,
// as a conversation's newest message so often is the user's.
func replyRecipients(m mail.Summary, ids []mail.Identity, all bool) []mail.Address {
	own := map[string]bool{}
	for _, id := range ids {
		own[strings.ToLower(strings.TrimSpace(id.Email))] = true
	}
	var to []mail.Address
	seen := map[string]bool{}
	add := func(list ...mail.Address) {
		for _, addr := range list {
			key := strings.ToLower(strings.TrimSpace(addr.Email))
			if key == "" || own[key] || seen[key] {
				continue
			}
			seen[key] = true
			to = append(to, addr)
		}
	}
	mine := own[strings.ToLower(strings.TrimSpace(m.From.Email))]
	if !mine {
		add(m.From)
	}
	if mine || all {
		add(m.To...)
	}
	if all {
		add(m.Cc...)
	}
	return to
}

// quoteWords are the words a reply quotes with, and a forward passes a
// message on with, in the user's language: the headers' names, and the
// message's date as the window writes dates, in this computer's time.
func quoteWords() mail.QuoteWords {
	return mail.QuoteWords{
		Wrote:     locales.OnWrote,
		Original:  locales.QuoteOriginal(),
		Forwarded: locales.ForwardedHeader(),
		From:      locales.From(),
		To:        locales.To(),
		Cc:        locales.Cc(),
		Date:      locales.QuoteDate(),
		Subject:   locales.Subject(),
		FormatDate: func(t time.Time) string {
			return i18n.FormatDate(i18n.Locale(), t.Local(), i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
		},
	}
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
	if a.threaded() {
		a.toggleThreadRead()
		return
	}
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
	var changed []mail.Summary
	for i := range a.all {
		m := &a.all[i]
		if !chosen[summaryKey(*m)] || m.Seen == on {
			continue
		}
		m.Seen = on
		a.addUnread(m.Account, m.Folder, unreadDelta(on))
		a.labelUnread(*m, unreadDelta(on))
		changed = append(changed, *m)
	}
	a.updateBadge()
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, m := range targets {
			if err = a.svc.SetFlag(ctx, m.Account, m.Folder, m.UID, mail.FlagSeen, on); err != nil {
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
				for _, m := range changed {
					a.addUnread(m.Account, m.Folder, -unreadDelta(on))
					a.labelUnread(m, -unreadDelta(on))
				}
				a.err = failureText(err)
				a.updateBadge()
			}
		})
	})
}
