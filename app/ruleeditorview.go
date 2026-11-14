package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// ruleEditorView is the editor: its header, the rule's form and, beside
// it, the preview.
func (a *app) ruleEditorView(c *ui.Context, t *ui.Theme) {
	ed := a.rules.editor
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	c.OnShortcut(0, ui.KeyEscape, a.closeRuleEditor)
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			title := locales.NewRuleTitle()
			if ed.id != 0 {
				title = locales.EditRuleTitle()
			}
			ui.Text(c, title).FontSize(17).Bold()
			ui.Spacer(c)
			if ed.saving {
				ui.Spinner(c).Size(14, 14)
			}
			if ed.id != 0 && !ed.dirty {
				ui.Button(c, locales.Done()).Label(locales.CloseRuleEditor()).OnClick(a.closeRuleEditor)
			} else {
				ui.Button(c, locales.Cancel()).Label(locales.CancelRuleEditor()).OnClick(a.closeRuleEditor)
			}
			ui.PrimaryButton(c, locales.SaveRule()).Disabled(ed.saving || (ed.id != 0 && !ed.dirty)).
				OnClick(a.saveRuleEditor)
		})
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Scroll(c).Grow(1).MinWidth(0).Children(func() {
				ui.Column(c).Padding(20, 24, 28, 32).Gap(12).MaxWidth(640).Children(func() { a.ruleForm(c, t, ed) })
			})
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			ui.Column(c).Width(360).Shrink(0).Background(t.Surface.Alpha(0.5)).Children(func() { a.rulePreview(c, t, ed) })
		})
	})
	n := len(ed.confirm)
	if ui.AlertDialog(c, &ed.confirming, locales.ApplyRuleTitle(strings.TrimSpace(ed.name), n),
		locales.ApplyRuleMessage(a.actionsText(ed.rule())), locales.Cancel(), locales.ApplyRuleButton(n)) == 1 {
		a.runRule(ed)
	}
}

// actionsText says a rule's actions in words.
func (a *app) actionsText(r mail.Rule) string {
	acts := make([]string, 0, len(r.Actions))
	for _, act := range r.Actions {
		acts = append(acts, a.actionText(act))
	}
	return strings.Join(acts, locales.ListSeparator())
}

// faultLine is a fault said below what it is about, which screen readers
// read as the item's error and as it appears.
func faultLine(c *ui.Context, t *ui.Theme, text string) ui.Element {
	var msg ui.Element
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, alertIcon).FontSize(13).TextColor(t.Danger).Shrink(0)
		msg = ui.Text(c, text).FontSize(12).TextColor(t.Danger).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
	})
	return msg
}

// ruleForm is the rule's name, its conditions and its actions, each fault
// at its item.
func (a *app) ruleForm(c *ui.Context, t *ui.Theme, ed *ruleEditor) {
	f, show := ed.fault, ed.shows()
	if ed.saveErr != "" {
		ruleWarning(c, t, ed.saveErr)
	}
	if ed.status != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
			ui.Text(c, ed.status).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
		})
	}
	ui.Column(c).Gap(4).Children(func() {
		// The caption names the input, so a click on it focuses it.
		caption := ui.Text(c, locales.RuleName()).FontSize(11).Bold().TextColor(t.TextMuted)
		name := ui.TextInput(c, &ed.name).LabelledBy(caption).Placeholder(locales.RuleNamePlaceholder()).
			FillWidth().AutoFocus().MaxLength(mail.MaxRuleName).OnChange(a.ruleEdited).OnSubmit(a.saveRuleEditor)
		if show && f.name {
			name.Invalid(true).ErrorMessage(faultLine(c, t, f.text))
		}
	})
	ui.Row(c).Gap(10).AlignItems(ui.Center).Margin(6, 0, 0, 0).Children(func() {
		caption := ui.Text(c, locales.RuleWhen()).FontSize(13).Bold()
		ui.Segmented(c, &ed.match, locales.MatchAll(), locales.MatchAny()).LabelledBy(caption).OnChange(a.ruleEdited)
	})
	for i, row := range ed.conds {
		a.conditionRow(c, t, ed, i, row)
	}
	if show && f.noConds {
		faultLine(c, t, f.text)
	}
	ruleAddButton(c, t, locales.AddCondition(), &ed.focus, "addCond", a.addCondition)
	ui.Text(c, locales.RuleThen()).FontSize(13).Bold().Margin(6, 0, 0, 0)
	for i, row := range ed.acts {
		a.actionRow(c, t, ed, i, row)
	}
	if show && f.noActs {
		faultLine(c, t, f.text)
	}
	ruleAddButton(c, t, locales.AddAction(), &ed.focus, "addAct", a.addAction)
	ui.Column(c).Gap(4).Margin(6, 0, 0, 0).Children(func() {
		ui.Checkbox(c, &ed.stop, locales.RuleStop()).OnChange(a.ruleEdited)
		ui.Text(c, locales.RuleStopDetail()).FontSize(11).TextColor(t.TextMuted).Padding(0, 0, 0, 26)
	})
	if show && f.general {
		faultLine(c, t, f.text)
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Margin(8, 0, 0, 0).Children(func() {
		ui.Icon(c, alertIcon).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, locales.RuleNeverSends()).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0)
	})
}

// ruleAddButton is a flat button that adds a row.
func ruleAddButton(c *ui.Context, t *ui.Theme, label string, focus *string, name string, add func()) {
	b := ui.ButtonBase(c).Label(label).Padding(5, 8).Radius(t.Radius).AlignSelf(ui.Start).
		FocusBind(focus, name).OnClick(add)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, plusIcon).FontSize(14).TextColor(t.Accent)
			ui.Text(c, label).FontSize(13).TextColor(t.Accent)
		})
	})
}

// conditionRow is a condition: what it looks at, then what it looks for,
// a header's name or an account's folder or label, and the button that
// removes it; its fault below.
func (a *app) conditionRow(c *ui.Context, t *ui.Theme, ed *ruleEditor, i int, row *condRow) {
	n := i + 1
	fault := ed.shows() && ed.fault.cond == row.key
	ui.Column(c.Key("cond/" + strconv.Itoa(row.key))).Gap(4).Children(func() {
		controls := map[faultPart]ui.Element{}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			controls[partKind] = rulePicker(c, t, &row.field, mail.ConditionFields, conditionFieldName, locales.ConditionOf(n)).
				Width(150).Shrink(0).FocusBind(&ed.focus, "cond/"+strconv.Itoa(row.key)).
				OnChange(func() { a.conditionFieldChanged(row) })
			switch row.field {
			case mail.CondHeader:
				controls[partHeader] = ui.TextInput(c, &row.header).Width(120).Shrink(0).Label(locales.HeaderOf(n)).
					Placeholder("List-Id").OnChange(a.ruleEdited)
				fallthrough
			case mail.CondSender, mail.CondRecipients, mail.CondSubject:
				ui.Text(c, locales.Contains()).FontSize(12).TextColor(t.TextMuted).Shrink(0)
				controls[partValue] = ui.TextInput(c, &row.value).Grow(1).MinWidth(0).Label(locales.ValueOf(n)).
					Placeholder(conditionPlaceholder(row.field)).MaxLength(mail.MaxRuleValue).OnChange(a.ruleEdited)
			case mail.CondAttachment:
				ui.Text(c, locales.CondAttachmentDetail()).FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			default:
				a.accountParts(c, t, controls, &row.account, &row.folder, &row.label, row.field == mail.CondAccount,
					row.field == mail.CondFolder, n, true)
			}
			remove := locales.RemoveCondition(n)
			ui.Button(c, "").Label(remove).Tooltip(remove).Padding(5, 7).Shrink(0).OnClick(func() { a.removeCondition(row) }).
				Children(func() { ui.Icon(c, closeIcon).FontSize(14) })
		})
		if fault {
			markFault(c, t, controls, ed.fault)
		}
	})
}

// markFault says a fault below its row and marks the control it is about,
// or the first of the row where that one is not there.
func markFault(c *ui.Context, t *ui.Theme, controls map[faultPart]ui.Element, f ruleFault) {
	msg := faultLine(c, t, f.text)
	el, ok := controls[f.part]
	if !ok {
		el = controls[partKind]
	}
	el.Invalid(true).ErrorMessage(msg)
}

// actionRow is an action: its kind, of the engine's alone, then the
// folder or label it names, and the button that removes it; a warning
// below a move to the Trash, and its fault.
func (a *app) actionRow(c *ui.Context, t *ui.Theme, ed *ruleEditor, i int, row *actRow) {
	n := i + 1
	fault := ed.shows() && ed.fault.act == row.key
	ui.Column(c.Key("act/" + strconv.Itoa(row.key))).Gap(4).Children(func() {
		controls := map[faultPart]ui.Element{}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			controls[partKind] = rulePicker(c, t, &row.kind, mail.ActionKinds, actionKindName, locales.ActionOf(n)).
				Width(170).Shrink(0).FocusBind(&ed.focus, "act/"+strconv.Itoa(row.key)).
				OnChange(func() { a.actionKindChanged(row) })
			if accountAction(row.kind) {
				a.accountParts(c, t, controls, &row.account, &row.folder, &row.label, false, row.kind == mail.ActionMove, n, false)
			} else {
				ui.Spacer(c)
			}
			remove := locales.RemoveAction(n)
			ui.Button(c, "").Label(remove).Tooltip(remove).Padding(5, 7).Shrink(0).OnClick(func() { a.removeAction(row) }).
				Children(func() { ui.Icon(c, closeIcon).FontSize(14) })
		})
		if row.kind == mail.ActionMove && a.isTrash(row.account, row.folder) {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(8).Background(t.Warning.Alpha(0.12)).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning).Shrink(0)
				ui.Text(c, locales.RuleTrashWarning()).FontSize(12).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
			})
		}
		if fault {
			markFault(c, t, controls, ed.fault)
		}
	})
}

// accountParts are the pickers of a row that names an account: the
// account, where the app has several, and its folder or label, or the
// account alone (onlyAccount), each put in controls by its part. The
// account chosen anew forgets the folder and label of the one before.
func (a *app) accountParts(c *ui.Context, t *ui.Theme, controls map[faultPart]ui.Element, account, folder *string,
	label *mail.LabelID, onlyAccount, isFolder bool, n int, isCond bool) {
	accountLabel, folderLabel, labelLabel := locales.ActionAccountOf(n), locales.ActionFolderOf(n), locales.ActionLabelOf(n)
	if isCond {
		accountLabel, folderLabel, labelLabel = locales.AccountOf(n), locales.FolderOf(n), locales.LabelOf(n)
	}
	if onlyAccount || len(a.accounts) > 1 {
		ids := make([]string, 0, len(a.accounts))
		for _, acc := range a.accounts {
			ids = append(ids, acc.ID)
		}
		p := rulePicker(c, t, account, ids, a.ruleAccountName, accountLabel).MinWidth(0).
			OnChange(func() {
				*folder, *label = "", 0
				a.ruleEdited()
			})
		if onlyAccount {
			p.Grow(1)
		} else {
			p.Width(130).Shrink(0)
		}
		controls[partAccount] = p
	}
	if onlyAccount {
		return
	}
	if isFolder {
		names := a.ruleFolderChoices(*account)
		folderName := func(name string) string { return a.ruleFolderChoice(*account, name) }
		controls[partFolder] = rulePicker(c, t, folder, names, folderName, folderLabel).Grow(1).MinWidth(0).OnChange(a.ruleEdited)
		return
	}
	labels := a.labels.of[*account]
	ids := make([]mail.LabelID, 0, len(labels))
	for _, l := range labels {
		ids = append(ids, l.ID)
	}
	if len(ids) == 0 && *label == 0 {
		ui.Text(c, locales.RuleNoLabels()).FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0).MaxLines(2)
		return
	}
	labelName := func(id mail.LabelID) string {
		if id == 0 {
			return locales.RuleChooseLabelPlaceholder()
		}
		return a.ruleLabelName(*account, id)
	}
	controls[partLabel] = rulePicker(c, t, label, ids, labelName, labelLabel).Grow(1).MinWidth(0).OnChange(a.ruleEdited)
}

// ruleAccountName names an account in a picker, or asks for one.
func (a *app) ruleAccountName(id string) string {
	if id == "" {
		return locales.RuleChooseAccountPlaceholder()
	}
	return a.accountLabel(id)
}

// ruleFolderChoices are the folders of an account a rule can name, by
// name: every one but Drafts, the inbox and the others of a special use
// first, as the sidebar lists them.
func (a *app) ruleFolderChoices(account string) []string {
	var special, others []string
	order := []string{mail.SpecialInbox, mail.SpecialArchive, mail.SpecialJunk, mail.SpecialTrash, mail.SpecialSent}
	for _, use := range order {
		for _, f := range a.foldersFor(account) {
			if f.SpecialUse == use {
				special = append(special, f.Name)
			}
		}
	}
	for _, f := range a.foldersFor(account) {
		if f.SpecialUse != mail.SpecialDrafts && !slices.Contains(special, f.Name) && !slices.Contains(others, f.Name) {
			others = append(others, f.Name)
		}
	}
	return append(special, others...)
}

// ruleFolderChoice names a folder in a picker as the sidebar names it,
// with the server's name after it where two would read the same.
func (a *app) ruleFolderChoice(account, name string) string {
	if name == "" {
		return locales.RuleChooseFolderPlaceholder()
	}
	shown := a.folderLabelOf(account, name)
	for _, other := range a.ruleFolderChoices(account) {
		if other != name && a.folderLabelOf(account, other) == shown {
			return locales.FolderNamed(shown, name)
		}
	}
	return shown
}

// conditionFieldName is what a condition looks at, in words.
func conditionFieldName(f mail.ConditionField) string {
	switch f {
	case mail.CondSender:
		return locales.RuleFieldSender()
	case mail.CondRecipients:
		return locales.RuleFieldRecipients()
	case mail.CondSubject:
		return locales.RuleFieldSubject()
	case mail.CondHeader:
		return locales.RuleFieldHeader()
	case mail.CondAttachment:
		return locales.RuleFieldAttachment()
	case mail.CondAccount:
		return locales.RuleFieldAccount()
	case mail.CondFolder:
		return locales.RuleFieldFolder()
	case mail.CondLabel:
		return locales.RuleFieldLabel()
	}
	return string(f)
}

// conditionPlaceholder is an example of what a text condition looks for.
func conditionPlaceholder(f mail.ConditionField) string {
	switch f {
	case mail.CondSender:
		return "news@shop.example"
	case mail.CondRecipients:
		return "team@example.com"
	case mail.CondHeader:
		return "news.shop.example"
	}
	return locales.SubjectPlaceholder()
}

// actionKindName is what an action of a kind does, in words.
func actionKindName(k mail.ActionKind) string {
	switch k {
	case mail.ActionMove:
		return locales.KindMove()
	case mail.ActionAddLabel:
		return locales.KindAddLabel()
	case mail.ActionRemoveLabel:
		return locales.KindRemoveLabel()
	case mail.ActionStar:
		return locales.KindStar()
	case mail.ActionMarkRead:
		return locales.KindMarkRead()
	case mail.ActionMarkUnread:
		return locales.KindMarkUnread()
	case mail.ActionSkipNotification:
		return locales.KindSkipNotification()
	}
	return string(k)
}

// rulePicker is a drop-down choosing one of options into *v, each named
// by name, which also names the zero value while none is chosen. The
// choice is kept by its value, not by its words: two folders that read
// alike stay two.
func rulePicker[T comparable](c *ui.Context, t *ui.Theme, v *T, options []T, name func(T) string, label string) ui.Element {
	var zero T
	sel := ui.SelectBase(c, v)
	shown := name(*v)
	color := t.Text
	if *v == zero {
		color = t.TextMuted
	}
	sel.Trigger.Label(label).Value(shown).Gap(6).Padding(5, 8, 5, 10).Radius(t.Radius).Border(1, t.Border).
		Background(t.Background).Children(func() {
		ui.Text(c, shown).FontSize(13).TextColor(color).SingleLine().Grow(1).MinWidth(0)
		ui.Icon(c, chevronDownIcon).FontSize(12).TextColor(t.TextMuted).Shrink(0)
	})
	if sel.Trigger.Hovered() {
		sel.Trigger.Background(t.SurfaceHover)
	}
	sel.Popup(func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18)).Label(label)
		ui.Scroll(c).MaxHeight(300).Children(func() {
			ui.Column(c).Padding(4).Gap(1).Children(func() {
				for _, o := range options {
					item := sel.Item(o).Padding(6, 10).Radius(t.Radius)
					switch {
					case item.Highlighted():
						item.Background(t.Accent).TextColor(t.AccentText)
					case o == *v:
						item.Background(t.Surface)
					}
					text := name(o)
					item.Children(func() { ui.Text(c, text).FontSize(13).SingleLine() })
				}
			})
		})
	})
	return sel.Trigger
}

// rulePreview is the panel beside the form: how many of the messages in
// the mail the rule matches now and a page of them, the promise that
// saving changes none of them, and, for the rule saved, the offer to
// apply it to them once.
func (a *app) rulePreview(c *ui.Context, t *ui.Theme, ed *ruleEditor) {
	p := ed.preview
	ui.Column(c).Fill().Padding(18, 20, 16, 20).Gap(10).Children(func() {
		ui.Text(c, locales.RulePreviewTitle()).FontSize(15).Bold()
		switch {
		case ed.previewErr != "":
			ruleWarning(c, t, ed.previewErr)
		case ed.blocked:
			ui.Text(c, locales.RulePreviewBlocked()).FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		case !ed.previewed:
			ui.Text(c, locales.RulePreviewIncomplete()).FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		default:
			ui.Text(c, locales.RulePreviewCount(p.Total)).FontSize(13).Bold().MaxLines(3).Role(ui.RoleStatus)
			if ed.partial {
				ui.Text(c, locales.RulePreviewPartial()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			}
		}
		note := locales.RuleNewMailOnly()
		if ed.id != 0 && !ed.dirty {
			note = locales.RuleSavedNewMailOnly()
		}
		ui.Row(c).Gap(8).AlignItems(ui.Start).Padding(8, 10).Radius(8).Background(t.Accent.Alpha(0.08)).Children(func() {
			ui.Icon(c, clockIcon).FontSize(14).TextColor(t.Accent).Shrink(0)
			ui.Text(c, note).FontSize(12).Grow(1).MinWidth(0).MaxLines(5)
		})
		if !ed.previewed || p.Total == 0 {
			ui.Spacer(c)
		} else {
			ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
				ui.Column(c).Label(locales.RulePreviewMessages()).Children(func() {
					for _, m := range p.Messages {
						a.previewRow(c, t, m)
					}
				})
			})
			a.previewPager(c, t, ed)
		}
		if ed.canApply() {
			a.applyOffer(c, t, ed)
		}
		a.runResult(c, t, ed)
	})
}

// previewRow is a message the rule matches: its subject, sender, date and
// folder.
func (a *app) previewRow(c *ui.Context, t *ui.Theme, m mail.Summary) {
	subject := m.Subject
	if strings.TrimSpace(subject) == "" {
		subject = locales.NoSubject()
	}
	ui.Column(c.Key(summaryKey(m))).Gap(2).Padding(7, 0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, subject).FontSize(12).Bold().SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, a.messageTime(c, m.Date)).FontSize(11).TextColor(t.TextMuted).Shrink(0)
		})
		ui.Text(c, locales.PreviewFrom(displayName(m.From), a.ruleFolderName(m.Account, m.Folder))).FontSize(11).
			TextColor(t.TextMuted).SingleLine()
	})
}

// previewPager pages through what the rule matches.
func (a *app) previewPager(c *ui.Context, t *ui.Theme, ed *ruleEditor) {
	total := ed.preview.Total
	if total <= rulePreviewPage {
		return
	}
	first, last := ed.offset+1, min(ed.offset+rulePreviewPage, total)
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.PageRange(first, last, total)).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0)
		ui.Button(c, locales.PreviousPage()).Label(locales.PreviousPreviewPage()).Padding(4, 8).Disabled(ed.offset == 0).
			OnClick(func() { a.checkRuleEditor(max(ed.offset-rulePreviewPage, 0)) })
		ui.Button(c, locales.NextPage()).Label(locales.NextPreviewPage()).Padding(4, 8).Disabled(last >= total).
			OnClick(func() { a.checkRuleEditor(ed.offset + rulePreviewPage) })
	})
}

// applyOffer is the offer to apply the saved rule to the messages its
// preview found, which asks first, and how the last run went.
func (a *app) applyOffer(c *ui.Context, t *ui.Theme, ed *ruleEditor) {
	why := ed.applyWhyNot()
	ui.Column(c).Gap(6).Padding(10, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c, locales.ApplyRuleNow(ed.preview.Total)).Disabled(why != "" || ed.running).
				OnClick(func() { a.askApply(ed) })
			if ed.running {
				ui.Spinner(c).Size(14, 14)
			}
		})
		detail := locales.ApplyRuleNowDetail()
		if why != "" {
			detail = why
		}
		ui.Text(c, detail).FontSize(11).TextColor(t.TextMuted).MaxLines(4)
	})
}

// runResult says how the last run over the preview went, which stays
// when the run took every message out of the preview, with the way to
// its entries in the log.
func (a *app) runResult(c *ui.Context, t *ui.Theme, ed *ruleEditor) {
	if ed.runErr != "" {
		ruleWarning(c, t, ed.runErr)
	}
	if ed.ran == "" {
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success).Shrink(0)
		ui.Text(c, ed.ran).FontSize(12).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
	})
	ui.Button(c, locales.ShowRuleActivity()).AlignSelf(ui.Start).OnClick(func() { a.showRuleActivity(ed.id) })
}
