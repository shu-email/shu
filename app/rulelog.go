package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The activity log of the rules, newest first: each time a rule acted on
// a message, with the message, what the rule did and how that stands,
// waiting for the server, failed and why, done or undone. Each entry can
// be undone, and each rule paused, or paused with everything it did
// undone once the user confirmed. Filters narrow the log to a rule and to
// a state, a page at a time. It reads the log again after each change,
// and as the engine reports actions applied in the background.

// ruleLogPage is how many entries a page of the log lists.
const ruleLogPage = 50

// ruleLog is the log's tab.
type ruleLog struct {
	// entries are the page read, newest first, of total that the filters
	// rule and state find, from offset. reads counts the reads, so that
	// only the last one shows.
	entries []mail.RuleActivity
	total   int
	offset  int
	rule    mail.RuleID
	state   mail.ActivityState
	reads   uint64
	loaded  bool
	readErr string
	rows    ui.ListState
	// undoing is the entry an undo is under way of, and busy a pause or
	// an undo of a whole rule. err says why the last failed, note what it
	// did.
	undoing mail.ActivityID
	busy    bool
	err     string
	note    string
	// The question before a rule is paused and what it did undone:
	// asked while asking is set, of askRule, named askName, which is gone
	// unless askExists.
	asking    bool
	askRule   mail.RuleID
	askName   string
	askExists bool
	// focus is the control the keyboard is on, among the rows'.
	focus string
}

// logFocus names a control of an entry's row among those ruleLog.focus
// names.
func logFocus(control string, id mail.ActivityID) string {
	return control + "/" + strconv.FormatInt(int64(id), 10)
}

// readLog reads the page of the log the filters ask for.
func (a *app) readLog() {
	l := &a.rules.log
	l.reads++
	seq := l.reads
	q := mail.ActivityQuery{Rule: l.rule, State: l.state, Offset: l.offset, Limit: ruleLogPage}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		page, err := a.svc.RuleActivity(ctx, q)
		a.post(func() {
			if !a.rules.open || l.reads != seq {
				return
			}
			if err != nil {
				l.readErr = locales.RuleLogReadFailed(err.Error())
				return
			}
			if len(page.Entries) == 0 && q.Offset > 0 && page.Total > 0 {
				// The page emptied, as the log was bounded or a filter
				// left fewer: the last page there is.
				l.offset = (page.Total - 1) / ruleLogPage * ruleLogPage
				a.readLog()
				return
			}
			l.readErr, l.entries, l.total, l.loaded = "", page.Entries, page.Total, true
		})
	})
}

// filterLog shows the log of a rule, or every rule's, from its first page.
func (a *app) filterLog() {
	l := &a.rules.log
	l.offset = 0
	l.rows.ScrollTo(0, ui.Start)
	a.readLog()
}

// ruleOf is the rule of an ID as the window last read the rules.
func (a *app) ruleOf(id mail.RuleID) (mail.Rule, bool) {
	i := slices.IndexFunc(a.rules.list, func(r mail.Rule) bool { return r.ID == id })
	if i < 0 {
		return mail.Rule{}, false
	}
	return a.rules.list[i], true
}

// undoRuleEntry puts back what an entry of the log changed. The mail
// list and the folders show it once it is done.
func (a *app) undoRuleEntry(e mail.RuleActivity) {
	l := &a.rules.log
	if l.undoing != 0 || l.busy {
		return
	}
	l.undoing, l.err, l.note = e.ID, "", ""
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ruleRunTimeout)
		defer cancel()
		err := a.svc.UndoActivity(ctx, e.ID)
		a.post(func() {
			a.readList(a.gen, false, false)
			a.readFolders(e.Account)
			if !a.rules.open {
				return
			}
			l.undoing = 0
			if err != nil {
				l.err = locales.UndoEntryFailed(entrySubject(e), ruleFailure(err))
			} else {
				l.note = locales.UndoEntryDone(entrySubject(e))
			}
			l.focus = logFocus("more", e.ID)
			a.readLog()
		})
	})
}

// pauseRuleFromLog pauses a rule, or has it sort mail again, leaving what
// it did as it is.
func (a *app) pauseRuleFromLog(r mail.Rule, paused bool) {
	l := &a.rules.log
	if l.busy {
		return
	}
	l.busy, l.err, l.note = true, "", ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, err := a.svc.PauseRule(ctx, r.ID, paused)
		a.post(func() {
			if !a.rules.open {
				return
			}
			l.busy = false
			switch {
			case err != nil:
				l.err = locales.RulePauseFailed(r.Name, ruleFailure(err))
			case paused:
				l.note = locales.RulePausedNote(r.Name)
			default:
				l.note = locales.RuleResumedNote(r.Name)
			}
			a.readRules()
			a.readLog()
		})
	})
}

// askPauseAndUndo asks before a rule is paused and everything it did
// undone.
func (a *app) askPauseAndUndo(id mail.RuleID, name string) {
	l := &a.rules.log
	_, exists := a.ruleOf(id)
	l.asking, l.askRule, l.askName, l.askExists = true, id, name, exists
}

// pauseAndUndo pauses a rule, so that it sorts no more mail, then puts
// back everything it did that is not undone yet. A rule deleted since is
// only undone. It says how many changes went back, and why some did not.
func (a *app) pauseAndUndo(id mail.RuleID, name string) {
	l := &a.rules.log
	if l.busy {
		return
	}
	l.busy, l.err, l.note = true, "", ""
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ruleRunTimeout)
		defer cancel()
		_, err := a.svc.PauseRule(ctx, id, true)
		paused := err == nil
		if errors.Is(err, mail.ErrNotFound) {
			err = nil
		}
		var n int
		if err == nil {
			n, err = a.svc.UndoRule(ctx, id)
		}
		a.post(func() {
			a.readList(a.gen, false, false)
			for _, acc := range a.accounts {
				a.readFolders(acc.ID)
			}
			if !a.rules.open {
				return
			}
			l.busy = false
			switch {
			case err != nil && !paused:
				l.err = locales.RulePauseFailed(name, ruleFailure(err))
			case err != nil:
				l.err = locales.UndoAllFailed(name, n, ruleFailure(err))
			case paused:
				l.note = locales.PauseUndoDone(name, n)
			default:
				l.note = locales.UndoAllDone(name, n)
			}
			a.readRules()
			a.readLog()
		})
	})
}

// ruleLogAlerts is the question before a rule is paused and what it did
// undone.
func (a *app) ruleLogAlerts(c *ui.Context) {
	l := &a.rules.log
	title, message, button := locales.PauseUndoTitle(l.askName), locales.PauseUndoMessage(), locales.PauseUndoButton()
	if !l.askExists {
		title, message, button = locales.UndoAllTitle(l.askName), locales.UndoAllMessage(), locales.UndoAllButton()
	}
	if ui.AlertDialog(c, &l.asking, title, message, locales.Cancel(), button) == 1 {
		a.pauseAndUndo(l.askRule, l.askName)
	}
}

// ruleLogView is the log's tab: what it is, its filters and pages, the
// actions on the rule it is narrowed to, and its entries.
func (a *app) ruleLogView(c *ui.Context, t *ui.Theme) {
	l := &a.rules.log
	ui.Column(c).Grow(1).MinHeight(0).MaxWidth(1000).Padding(16, 32, 0, 32).Gap(10).Children(func() {
		ui.Text(c, locales.RuleLogIntro()).FontSize(12).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			a.logFilters(c, t)
			ui.Spacer(c)
			a.logPager(c, t)
		})
		if l.rule != 0 {
			a.logRuleActions(c, t)
		}
		for _, text := range []string{l.err, l.readErr} {
			if text != "" {
				ruleWarning(c, t, text)
			}
		}
		if l.note != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success).Shrink(0)
				ui.Text(c, l.note).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
			})
		}
		a.logList(c, t)
	})
}

// logFilters are the pickers of the rule and the state the log shows.
func (a *app) logFilters(c *ui.Context, t *ui.Theme) {
	l := &a.rules.log
	rules := []mail.RuleID{0}
	for _, r := range a.rules.list {
		rules = append(rules, r.ID)
	}
	if l.rule != 0 && !slices.Contains(rules, l.rule) {
		rules = append(rules, l.rule)
	}
	ruleName := func(id mail.RuleID) string {
		if id == 0 {
			return locales.AllRules()
		}
		return a.logRuleName(id)
	}
	// The captions name the pickers, so a click on one opens its picker.
	caption := ui.Text(c, locales.LogRuleFilter()).FontSize(12).TextColor(t.TextMuted)
	rulePicker(c, t, &l.rule, rules, ruleName, locales.LogRuleFilter()).Width(200).LabelledBy(caption).OnChange(a.filterLog)
	states := []mail.ActivityState{"", mail.ActivityPending, mail.ActivityApplied, mail.ActivityFailed, mail.ActivityUndone}
	stateName := func(s mail.ActivityState) string {
		if s == "" {
			return locales.AnyState()
		}
		return stateWord(s)
	}
	caption = ui.Text(c, locales.LogStateFilter()).FontSize(12).TextColor(t.TextMuted)
	rulePicker(c, t, &l.state, states, stateName, locales.LogStateFilter()).Width(200).LabelledBy(caption).OnChange(a.filterLog)
}

// logRuleName names a rule of the log: as the rules are now, or, for one
// deleted since, as an entry of it names it, said to be deleted.
func (a *app) logRuleName(id mail.RuleID) string {
	if r, ok := a.ruleOf(id); ok {
		return r.Name
	}
	return locales.RuleDeletedName(a.loggedRuleName(id))
}

// loggedRuleName is the name of a rule as the rules are now, or as an
// entry of it names it.
func (a *app) loggedRuleName(id mail.RuleID) string {
	if r, ok := a.ruleOf(id); ok {
		return r.Name
	}
	for _, e := range a.rules.log.entries {
		if e.Rule == id {
			return e.RuleName
		}
	}
	return strconv.FormatInt(int64(id), 10)
}

// logPager pages through the log.
func (a *app) logPager(c *ui.Context, t *ui.Theme) {
	l := &a.rules.log
	if l.total <= ruleLogPage {
		return
	}
	first, last := l.offset+1, min(l.offset+ruleLogPage, l.total)
	ui.Text(c, locales.PageRange(first, last, l.total)).FontSize(12).TextColor(t.TextMuted)
	ui.Button(c, locales.PreviousPage()).Label(locales.PreviousLogPage()).Padding(4, 10).Disabled(l.offset == 0).
		OnClick(func() {
			l.offset = max(l.offset-ruleLogPage, 0)
			a.readLog()
		})
	ui.Button(c, locales.NextPage()).Label(locales.NextLogPage()).Padding(4, 10).Disabled(last >= l.total).
		OnClick(func() {
			l.offset += ruleLogPage
			a.readLog()
		})
}

// logRuleActions are the actions on the rule the log is narrowed to:
// pause it, or have it sort mail again, and pause it with everything it
// did undone.
func (a *app) logRuleActions(c *ui.Context, t *ui.Theme) {
	l := &a.rules.log
	name := a.logRuleName(l.rule)
	r, exists := a.ruleOf(l.rule)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 12).Radius(8).Background(t.Surface).Children(func() {
		ui.Text(c, name).FontSize(13).Bold().SingleLine().Grow(1).MinWidth(0)
		if l.busy {
			ui.Spinner(c).Size(14, 14)
		}
		if exists {
			label := locales.PauseRule()
			if r.Paused {
				label = locales.ResumeRule()
			}
			ui.Button(c, label).Disabled(l.busy).OnClick(func() { a.pauseRuleFromLog(r, !r.Paused) })
		}
		undo := locales.PauseAndUndoEllipsis()
		if !exists {
			undo = locales.UndoAllEllipsis()
		}
		id := l.rule
		ui.Button(c, undo).Disabled(l.busy).OnClick(func() { a.askPauseAndUndo(id, a.loggedRuleName(id)) })
	})
}

// logList is the page of entries, a row each; while there are none, it
// says why.
func (a *app) logList(c *ui.Context, t *ui.Theme) {
	l := &a.rules.log
	l.rows.Key = func(i int) any {
		if i >= len(l.entries) {
			return i
		}
		return l.entries[i].ID
	}
	l.rows.Label = func(i int) string {
		if i >= len(l.entries) {
			return ""
		}
		return entrySubject(l.entries[i])
	}
	ui.List(c, &l.rows, len(l.entries)).Grow(1).Label(locales.ActivityTab()).
		Rows(func(row ui.ListRow) { a.entryRow(row.Context, t, l.entries[row.Index]) }).
		Children(func() {
			if len(l.entries) > 0 || !l.loaded {
				return
			}
			text := locales.RuleLogEmpty()
			if l.rule != 0 || l.state != "" {
				text = locales.RuleLogNoMatch()
			}
			ui.Text(c, text).FontSize(13).TextColor(t.TextMuted).Padding(24, 12).MaxLines(4)
		})
}

// entrySubject is the subject of an entry's message, or that it has none.
func entrySubject(e mail.RuleActivity) string {
	if s := strings.TrimSpace(e.Subject); s != "" {
		return s
	}
	return locales.NoSubject()
}

// stateWord is a state of an entry in words.
func stateWord(s mail.ActivityState) string {
	switch s {
	case mail.ActivityPending:
		return locales.StatePending()
	case mail.ActivityApplied:
		return locales.StateApplied()
	case mail.ActivityFailed:
		return locales.StateFailed()
	case mail.ActivityUndone:
		return locales.StateUndone()
	}
	return string(s)
}

// entryReason says why an entry failed, or why it waits, in the user's
// words where the engine's error has some.
func entryReason(e mail.RuleActivity) string {
	switch e.Error {
	case "":
		return ""
	case mail.ErrRuleMessageGone.Error():
		return locales.RuleMessageGone()
	}
	return e.Error
}

// entryRow is an entry of the log: the message, the rule and when, what
// the rule did and how that stands, the button that undoes it, and the
// menu of its rule.
func (a *app) entryRow(c *ui.Context, t *ui.Theme, e mail.RuleActivity) {
	l := &a.rules.log
	subject := entrySubject(e)
	when := a.messageTime(c, e.At.In(c.Now().Location()))
	r, exists := a.ruleOf(e.Rule)
	ruleName := e.RuleName
	if exists {
		ruleName = r.Name
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(10, 6, 10, 4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, subject).FontSize(13).Bold().SingleLine()
			ui.Text(c, locales.EntryLine(displayName(e.From), ruleName, when)).FontSize(12).TextColor(t.TextMuted).SingleLine()
			did := a.actionsText(mail.Rule{Actions: e.Actions})
			if e.Manual {
				did = locales.EntryManual(did)
			}
			ui.Text(c, did).FontSize(12).MaxLines(2)
		})
		a.entryState(c, t, e)
		ui.Button(c, "").Label(locales.UndoEntry(subject)).Padding(5, 10).
			Disabled(e.State == mail.ActivityUndone || l.undoing != 0 || l.busy).
			FocusBind(&l.focus, logFocus("undo", e.ID)).OnClick(func() { a.undoRuleEntry(e) }).
			Children(func() {
				if l.undoing == e.ID {
					ui.Spinner(c).Size(12, 12)
				}
				ui.Text(c, locales.Undo())
			})
		more := locales.EntryActions(subject)
		ui.Button(c, "").Label(more).Tooltip(more).Padding(5, 7).FocusBind(&l.focus, logFocus("more", e.ID)).
			Children(func() { ui.Icon(c, moreIcon).FontSize(15) }).
			PopoverMenuButton(func(m *ui.Menu) { a.entryMenu(m, e, r, exists) })
	})
}

// entryState is how an entry stands: an icon and a word, and the reason
// of one that failed or waits.
func (a *app) entryState(c *ui.Context, t *ui.Theme, e mail.RuleActivity) {
	var icon *ui.SVG
	color := t.TextMuted
	switch e.State {
	case mail.ActivityApplied:
		icon, color = checkIcon, t.Success
	case mail.ActivityPending:
		icon, color = clockIcon, t.Accent
	case mail.ActivityFailed:
		icon, color = alertIcon, t.Warning
	}
	ui.Column(c).Width(200).Shrink(0).Gap(2).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			// The icons stand in a column of their own, so that the words
			// line up.
			ui.Row(c).Width(14).Shrink(0).Children(func() {
				if icon != nil {
					ui.Icon(c, icon).FontSize(14).TextColor(color)
				}
			})
			word := ui.Text(c, stateWord(e.State)).FontSize(12).Bold().SingleLine()
			if e.State == mail.ActivityUndone {
				word.TextColor(t.TextMuted)
			}
		})
		if reason := entryReason(e); reason != "" && (e.State == mail.ActivityFailed || e.State == mail.ActivityPending) {
			ui.Text(c, reason).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Padding(0, 0, 0, 20)
		}
	})
}

// entryMenu is the menu of an entry's rule: pause it or have it sort mail
// again, pause it and undo everything it did, and show its entries alone.
func (a *app) entryMenu(m *ui.Menu, e mail.RuleActivity, r mail.Rule, exists bool) {
	l := &a.rules.log
	name := e.RuleName
	if exists {
		name = r.Name
		label := locales.PauseRuleNamed(name)
		if r.Paused {
			label = locales.ResumeRuleNamed(name)
		}
		if m.Item(label).Disabled(l.busy).Chosen() {
			a.pauseRuleFromLog(r, !r.Paused)
		}
		if m.Item(locales.PauseAndUndoNamed(name)).Disabled(l.busy).Chosen() {
			a.askPauseAndUndo(e.Rule, name)
		}
	} else if m.Item(locales.UndoAllNamed(name)).Disabled(l.busy).Chosen() {
		a.askPauseAndUndo(e.Rule, name)
	}
	m.Separator()
	if m.Item(locales.ShowRuleOnly(name)).Disabled(l.rule == e.Rule).Chosen() {
		l.rule = e.Rule
		a.filterLog()
	}
}
