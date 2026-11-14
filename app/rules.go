package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Rules in a window of their own. The sidebar's menu opens it beside the
// mail, as the address book opens: on one tab the rules in their order,
// each with what it does in words, a switch that pauses it, buttons that
// move it up and down (the rows drag too), edit it and delete it; on the
// other the log of what they did
// (rulelog.go). The editor (ruleeditor.go) checks a rule as it is written
// and shows beside it the mail it would match now, which saving leaves
// alone: a rule sorts the mail that arrives, and the mail there was only
// when the user applies it from its preview. Nothing in any of it can
// make a rule send, forward or delete mail: the window offers the
// engine's actions, mail.ActionKinds, and no others.

// The tabs of the rules view.
const (
	rulesTabList = iota
	rulesTabActivity
)

// ruleFocusNew is the button that makes a rule, among the controls
// ruleState.focus names; those of a row are named after its rule
// (ruleFocus).
const ruleFocusNew = "new"

// ruleState is the rules view, open or not.
type ruleState struct {
	open bool
	tab  int
	// list are the rules as last read, in their order, and loaded whether
	// they were read. reads counts the reads, so that one an earlier read
	// answers after is dropped, as are answers after the view closed.
	list    []mail.Rule
	loaded  bool
	reads   uint64
	readErr string
	rows    ui.ListState
	// busy is a change of the rules under way, and err why the last
	// change failed.
	busy bool
	err  string
	// deleting is the rule the question before deleting is about, asked
	// while asking is set.
	deleting mail.Rule
	asking   bool
	// focus is the control the keyboard is on, among the button that
	// makes a rule and the controls of the rows, which a change moves
	// back to.
	focus string
	// editor is the rule being written, nil while the list shows.
	editor *ruleEditor
	log    ruleLog
}

// ruleFocus names a control of a rule's row among those ruleState.focus
// names.
func ruleFocus(control string, id mail.RuleID) string {
	return control + "/" + strconv.FormatInt(int64(id), 10)
}

// rulesWindowOptions is the rules' window: its own, so that the mail stays
// readable beside them, and the message a rule is written for is on screen
// while the rule is.
func (a *app) rulesWindowOptions() mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:           locales.MailRules(),
		Width:           1040,
		Height:          720,
		MinWidth:        900,
		MinHeight:       560,
		TitleBarStyle:   mygo.TitleBarHiddenInset,
		BackgroundColor: "light-dark(#ffffff, #18181b)",
		Content:         ui.View(a.rulesView),
	}
}

// openRules shows the rules on a tab, read anew, with the labels and
// folders of every account that the rules name. One window serves every
// opening: a second call brings the open one forward, on that tab, and
// keeps the rule being written.
func (a *app) openRules(tab int) {
	if a.rules.open && a.rulesWin != nil {
		a.rules.tab = tab
		a.rulesWin.Show()
		a.rulesWin.Focus()
		a.rulesWin.Invalidate()
		return
	}
	a.rules = ruleState{open: true, tab: tab, reads: a.rules.reads, log: ruleLog{reads: a.rules.log.reads}}
	a.readRules()
	a.readLog()
	a.readRuleSources()
	if a.newWindow == nil {
		// Tests run the view without a window: the rules show in the main
		// view.
		return
	}
	win := a.newWindow(a.rulesWindowOptions())
	if win == nil {
		return
	}
	a.rulesWin = win
	win.OnClosed(func() {
		// Closed by the user, not by closeRules, which has already gone
		// back to the mail.
		if a.rulesWin == win {
			a.rulesWin = nil
			a.closeRules()
		}
	})
}

// closeRules closes the rules, or leaves their view in the main window.
// Reads and changes still under way change nothing once they answer.
func (a *app) closeRules() {
	a.rules = ruleState{reads: a.rules.reads + 1, log: ruleLog{reads: a.rules.log.reads + 1}}
	if win := a.rulesWin; win != nil {
		a.rulesWin = nil
		win.Close()
	}
}

// readRuleSources reads what the rules' words and pickers name: each
// account's labels, and the folders of those the window does not know.
func (a *app) readRuleSources() {
	ids := make([]string, 0, len(a.accounts))
	for _, acc := range a.accounts {
		ids = append(ids, acc.ID)
		if a.foldersFor(acc.ID) == nil {
			a.readFolders(acc.ID)
		}
	}
	a.loadLabels(ids...)
}

// readFolders reads an account's folders from the cache, with their
// counts.
func (a *app) readFolders(id string) {
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		folders, err := a.svc.CachedFolders(ctx, id)
		a.post(func() {
			if err != nil || a.account(id) == nil {
				return
			}
			a.setAccountFolders(id, folders)
			a.updateBadge()
		})
	})
}

// readRules reads the rules, in their order.
func (a *app) readRules() {
	rs := &a.rules
	rs.reads++
	seq := rs.reads
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		list, err := a.svc.Rules(ctx)
		a.post(func() {
			if !rs.open || rs.reads != seq {
				return
			}
			if err != nil {
				rs.readErr = locales.RulesReadFailed(err.Error())
				return
			}
			rs.readErr, rs.list, rs.loaded = "", list, true
		})
	})
}

// changeRules runs a change of the rules in a job and reads them again
// once it is in, the keyboard then on the control focus names. One change
// runs at a time.
func (a *app) changeRules(change func(ctx context.Context) error, focus string, failed func(err error) string) {
	rs := &a.rules
	if rs.busy {
		return
	}
	rs.busy, rs.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := change(ctx)
		a.post(func() {
			if !rs.open {
				return
			}
			rs.busy = false
			if err != nil {
				rs.err = failed(err)
			} else if focus != "" {
				rs.focus = focus
			}
			a.readRules()
			a.readLog()
		})
	})
}

// pauseRule pauses a rule, or has it sort mail again. The row shows it at
// once.
func (a *app) pauseRule(r mail.Rule, paused bool) {
	rs := &a.rules
	if rs.busy {
		return
	}
	for i := range rs.list {
		if rs.list[i].ID == r.ID {
			rs.list[i].Paused = paused
		}
	}
	a.changeRules(func(ctx context.Context) error {
		_, err := a.svc.PauseRule(ctx, r.ID, paused)
		return err
	}, ruleFocus("pause", r.ID), func(err error) string { return locales.RulePauseFailed(r.Name, err.Error()) })
}

// moveRule puts a rule at a place of the order, as its buttons and a drag
// of its row do. The list shows it at once; the keyboard stays on the
// button pressed, or goes to the other once the rule reached an end.
func (a *app) moveRule(r mail.Rule, to int, button string) {
	rs := &a.rules
	from := slices.IndexFunc(rs.list, func(have mail.Rule) bool { return have.ID == r.ID })
	if rs.busy || from < 0 || to < 0 || to >= len(rs.list) || to == from {
		return
	}
	list := slices.Delete(slices.Clone(rs.list), from, from+1)
	rs.list = slices.Insert(list, to, r)
	focus := ""
	switch {
	case button == "up" && to == 0:
		focus = ruleFocus("down", r.ID)
	case button == "down" && to == len(rs.list)-1:
		focus = ruleFocus("up", r.ID)
	case button != "":
		focus = ruleFocus(button, r.ID)
	}
	a.changeRules(func(ctx context.Context) error { return a.svc.MoveRule(ctx, r.ID, to) },
		focus, func(err error) string { return locales.RuleMoveFailed(r.Name, err.Error()) })
}

// askDeleteRule asks before deleting a rule.
func (a *app) askDeleteRule(r mail.Rule) {
	a.rules.deleting, a.rules.asking = r, true
}

// deleteRule deletes a rule the user said to delete; its log stays, to be
// undone. The keyboard goes to the rule after it, or before it when it
// was the last, and to the button that makes one when it was the only
// one.
func (a *app) deleteRule(r mail.Rule) {
	rs := &a.rules
	focus := ruleFocusNew
	if i := slices.IndexFunc(rs.list, func(have mail.Rule) bool { return have.ID == r.ID }); i >= 0 {
		switch {
		case i+1 < len(rs.list):
			focus = ruleFocus("delete", rs.list[i+1].ID)
		case i > 0:
			focus = ruleFocus("delete", rs.list[i-1].ID)
		}
	}
	a.changeRules(func(ctx context.Context) error { return a.svc.DeleteRule(ctx, r.ID) },
		focus, func(err error) string { return locales.RuleDeleteFailed(r.Name, err.Error()) })
}

// ruleActivity follows what the rules did in the background to an
// account's mail: the list shows it, the folders count it, and the log
// and a preview on screen read it again.
func (a *app) ruleActivity(accountID string) {
	if a.account(accountID) == nil {
		return
	}
	if a.onScreen(accountID) || a.spansAccounts() {
		a.readList(a.gen, false, false)
	}
	a.readFolders(accountID)
	if !a.rules.open {
		return
	}
	a.readLog()
	if ed := a.rules.editor; ed != nil {
		a.checkRuleEditor(ed.offset)
	}
}

// rulesView is the rules: the editor of one, or the tabs of the list and
// the log.
func (a *app) rulesView(c *ui.Context) {
	t := c.Theme()
	rs := &a.rules
	if rs.editor != nil {
		a.ruleEditorView(c, t)
		return
	}
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	c.OnShortcut(0, ui.KeyEscape, a.closeRules)
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			ui.Text(c, locales.MailRules()).FontSize(17).Bold()
			ui.Spacer(c)
			ui.Button(c, locales.Done()).OnClick(a.closeRules)
		})
		ui.Row(c).Shrink(0).Padding(10, 32, 0, 32).Children(func() {
			ui.Tabs(c, &rs.tab, locales.RulesTab(), locales.ActivityTab()).Label(locales.MailRules()).
				OnChange(func() {
					if rs.tab == rulesTabActivity {
						a.readLog()
					}
				})
		})
		if rs.tab == rulesTabActivity {
			a.ruleLogView(c, t)
			return
		}
		a.ruleListView(c, t)
	})
	if ui.AlertDialog(c, &rs.asking, locales.DeleteRuleTitle(rs.deleting.Name), locales.DeleteRuleMessage(),
		locales.Cancel(), locales.DeleteRuleButton()) == 1 {
		a.deleteRule(rs.deleting)
	}
	a.ruleLogAlerts(c)
}

// ruleListView is the tab of the rules: what they are, the button that
// makes one, and the rules in their order.
func (a *app) ruleListView(c *ui.Context, t *ui.Theme) {
	rs := &a.rules
	ui.Column(c).Grow(1).MinHeight(0).MaxWidth(900).Padding(16, 32, 0, 32).Gap(12).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.RulesIntro()).FontSize(12).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			ui.Button(c, "").Label(locales.NewRule()).FocusBind(&rs.focus, ruleFocusNew).
				OnClick(func() { a.openRuleEditor(nil, false) }).Children(func() {
				ui.Icon(c, plusIcon).FontSize(14)
				ui.Text(c, locales.NewRule())
			})
		})
		for _, text := range []string{rs.err, rs.readErr} {
			if text != "" {
				ruleWarning(c, t, text)
			}
		}
		a.ruleList(c, t)
	})
}

// ruleWarning is a line that says something went wrong, read out to
// screen readers as it appears.
func ruleWarning(c *ui.Context, t *ui.Theme, text string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
	})
}

// ruleList is the rules in their order, a row each, which drag to another
// place; while there are none, it says what a rule can do.
func (a *app) ruleList(c *ui.Context, t *ui.Theme) {
	rs := &a.rules
	rs.rows.Key = func(i int) any {
		if i >= len(rs.list) {
			return i
		}
		return rs.list[i].ID
	}
	rs.rows.Label = func(i int) string {
		if i >= len(rs.list) {
			return ""
		}
		return rs.list[i].Name
	}
	rs.rows.Reorder = func(rows []int, to int) {
		if len(rows) != 1 || rows[0] >= len(rs.list) {
			return
		}
		from := rows[0]
		if to > from {
			to--
		}
		a.moveRule(rs.list[from], to, "")
	}
	ui.List(c, &rs.rows, len(rs.list)).Grow(1).Label(locales.RulesTab()).
		Rows(func(row ui.ListRow) { a.ruleRow(row.Context, t, row.Index, rs.list[row.Index]) }).
		Children(func() {
			if len(rs.list) > 0 || !rs.loaded {
				return
			}
			ui.Text(c, locales.RulesEmpty()).FontSize(13).TextColor(t.TextMuted).Padding(24, 12).MaxLines(4)
		})
}

// ruleRow is a rule: its name and what it does, whether it is paused, the
// switch that pauses it, and the buttons that move it, edit it and delete
// it, each named for the rule, for assistive technology.
func (a *app) ruleRow(c *ui.Context, t *ui.Theme, i int, r mail.Rule) {
	rs := &a.rules
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(10, 8, 10, 2).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		// It shows that the row drags; the buttons move it by keyboard.
		ui.Icon(c, gripVerticalIcon).FontSize(16).TextColor(t.TextMuted).Shrink(0).AccessHidden(true)
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, r.Name).FontSize(13).Bold().SingleLine().MinWidth(0)
				if r.Paused {
					ui.Text(c, locales.RulePaused()).FontSize(10).Bold().TextColor(t.TextMuted).Shrink(0).
						Padding(1, 6).Radius(999).Background(t.Surface).Border(1, t.Border)
				}
			})
			ui.Text(c, a.ruleSummary(r)).FontSize(12).TextColor(t.TextMuted).MaxLines(2)
		})
		on := !r.Paused
		ui.Switch(c, &on).Label(locales.UseRule(r.Name)).Tooltip(locales.UseRuleTip()).
			FocusBind(&rs.focus, ruleFocus("pause", r.ID)).Disabled(rs.busy).
			OnClick(func() { a.pauseRule(r, !on) })
		up, down := locales.MoveRuleUp(r.Name), locales.MoveRuleDown(r.Name)
		ui.Button(c, "").Label(up).Tooltip(up).Padding(6, 8).Disabled(i == 0 || rs.busy).
			FocusBind(&rs.focus, ruleFocus("up", r.ID)).OnClick(func() { a.moveRule(r, i-1, "up") }).
			Children(func() { ui.Icon(c, arrowUpIcon).FontSize(16) })
		ui.Button(c, "").Label(down).Tooltip(down).Padding(6, 8).Disabled(i == len(rs.list)-1 || rs.busy).
			FocusBind(&rs.focus, ruleFocus("down", r.ID)).OnClick(func() { a.moveRule(r, i+1, "down") }).
			Children(func() { ui.Icon(c, arrowDownIcon).FontSize(16) })
		edit := locales.EditRule(r.Name)
		ui.Button(c, "").Label(edit).Tooltip(edit).Padding(6, 8).
			FocusBind(&rs.focus, ruleFocus("edit", r.ID)).OnClick(func() { a.openRuleEditor(&r, false) }).
			Children(func() { ui.Icon(c, pencilIcon).FontSize(16) })
		del := locales.DeleteRule(r.Name)
		ui.Button(c, "").Label(del).Tooltip(del).Padding(6, 8).Disabled(rs.busy).
			FocusBind(&rs.focus, ruleFocus("delete", r.ID)).OnClick(func() { a.askDeleteRule(r) }).
			Children(func() { ui.Icon(c, trashIcon).FontSize(16) })
	})
}

// ruleMenuItem is the item of a menu of messages that makes a rule from
// the one chosen: a message, or a conversation's newest in the list.
func (a *app) ruleMenuItem(m *ui.Menu, lt labelTargets) {
	msg, ok := ruleSource(lt)
	m.Separator()
	if m.Item(locales.MakeRuleEllipsis()).Disabled(!ok).Chosen() {
		a.ruleFromMessage(msg)
	}
}

// ruleSource is the message of rows a rule is made from: the one message
// chosen, or the newest of the one conversation, but no draft.
func ruleSource(lt labelTargets) (mail.Summary, bool) {
	switch {
	case len(lt.msgs) == 1 && len(lt.convs) == 0 && lt.msgs[0].DraftID == "":
		return lt.msgs[0], true
	case len(lt.convs) == 1 && len(lt.msgs) == 0:
		m := lt.convs[0].Latest
		if m.Account == "" {
			m.Account = lt.convs[0].Account
		}
		return m, true
	}
	return mail.Summary{}, false
}

// ruleSummary says in a line what a rule looks for and what it does.
func (a *app) ruleSummary(r mail.Rule) string {
	conds := make([]string, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		conds = append(conds, a.conditionText(c))
	}
	join := locales.RuleAnd()
	if r.Match == mail.MatchAny {
		join = locales.RuleOr()
	}
	acts := make([]string, 0, len(r.Actions))
	for _, act := range r.Actions {
		acts = append(acts, a.actionText(act))
	}
	if r.Stop && !slices.ContainsFunc(r.Actions, func(act mail.Action) bool { return act.Kind == mail.ActionMove }) {
		acts = append(acts, locales.RuleStops())
	}
	return locales.RuleSummary(strings.Join(conds, join), strings.Join(acts, locales.ListSeparator()))
}

// conditionText says a condition in words.
func (a *app) conditionText(c mail.Condition) string {
	switch c.Field {
	case mail.CondSender:
		return locales.CondSenderText(c.Value)
	case mail.CondRecipients:
		return locales.CondRecipientsText(c.Value)
	case mail.CondSubject:
		return locales.CondSubjectText(c.Value)
	case mail.CondHeader:
		return locales.CondHeaderText(c.Header, c.Value)
	case mail.CondAttachment:
		return locales.CondAttachmentText()
	case mail.CondAccount:
		return locales.CondAccountText(a.accountLabel(c.Account))
	case mail.CondFolder:
		return locales.CondFolderText(a.ruleFolderName(c.Account, c.Folder))
	case mail.CondLabel:
		return locales.CondLabelText(a.ruleLabelName(c.Account, c.Label))
	}
	return string(c.Field)
}

// actionText says an action in words.
func (a *app) actionText(act mail.Action) string {
	switch act.Kind {
	case mail.ActionMove:
		return locales.ActMoveText(a.ruleFolderName(act.Account, act.Folder))
	case mail.ActionAddLabel:
		return locales.ActAddLabelText(a.ruleLabelName(act.Account, act.Label))
	case mail.ActionRemoveLabel:
		return locales.ActRemoveLabelText(a.ruleLabelName(act.Account, act.Label))
	case mail.ActionStar:
		return locales.ActStarText()
	case mail.ActionMarkRead:
		return locales.ActMarkReadText()
	case mail.ActionMarkUnread:
		return locales.ActMarkUnreadText()
	case mail.ActionSkipNotification:
		return locales.ActSkipNotificationText()
	}
	return string(act.Kind)
}

// ruleFolderName is an account's folder as the rules name it: as the
// sidebar names it, and with the account's name where the app has
// several.
func (a *app) ruleFolderName(account, folder string) string {
	name := a.folderLabelOf(account, folder)
	if len(a.accounts) > 1 {
		return locales.RuleFolderInAccount(name, a.accountLabel(account))
	}
	return name
}

// ruleLabelName is a label of an account by its name, or a label gone
// since said to be so.
func (a *app) ruleLabelName(account string, id mail.LabelID) string {
	if l := a.findLabel(account, id); l != nil {
		return l.Name
	}
	return locales.LabelGone()
}

// isTrash reports whether an account's folder is its Trash, which a rule
// moving mail there warns of.
func (a *app) isTrash(account, folder string) bool {
	return folder != "" && a.specialFolder(account, mail.SpecialTrash) == folder
}

// ruleFailure says why a call about rules failed, in the user's words
// where the engine's error has some.
func ruleFailure(err error) string {
	switch {
	case errors.Is(err, mail.ErrRulePaused):
		return locales.RuleIsPaused()
	case errors.Is(err, mail.ErrRuleRunTooLarge):
		return locales.RuleRunTooLarge(mail.MaxRuleRun)
	case errors.Is(err, mail.ErrRuleMessageGone):
		return locales.RuleMessageGone()
	case errors.Is(err, mail.ErrNotFound):
		return locales.RuleGone()
	case errors.Is(err, mail.ErrSignInRequired):
		return locales.ActionNeedsSignIn()
	}
	return err.Error()
}
