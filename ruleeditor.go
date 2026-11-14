package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"shu/locales"
	"shu/mail"
)

// The editor of a rule: its name, all or any of its conditions, its
// actions and Stop, checked by the engine as they are written; beside
// them, the cached mail the rule would match now. Saving changes none of
// that mail. Once the rule is saved and unchanged, its preview offers to
// apply it to those messages, once, which asks first; that run is the
// only way the window has a rule act on mail that was there before it.

// rulePreviewPage is how many messages a page of the preview lists.
const rulePreviewPage = 20

// ruleRunTimeout bounds a run of a rule over its preview, and an undo of
// everything a rule did: each may move many messages.
const ruleRunTimeout = 2 * time.Minute

// standInName names a rule whose name is still to be typed as its preview
// is read, which does not depend on it. It is never shown or saved.
const standInName = "preview"

// ruleEditor is a rule being written: a new one (id 0) or one of the
// list.
type ruleEditor struct {
	id     mail.RuleID
	paused bool
	name   string
	// match is the place of all (0) or any (1) in its control.
	match int
	conds []*condRow
	acts  []*actRow
	stop  bool
	keys  int
	// fromMail is an editor a message's menu opened, which closing goes
	// back to the mail from.
	fromMail bool
	// dirty is set by a change since the rule was opened or saved, and
	// edits counts the changes: the preview is of a saved rule only while
	// nothing changed, which applying it needs.
	dirty bool
	edits int
	// tried is set once a save was asked for: the faults of what is
	// still to fill in show from then on.
	tried   bool
	saving  bool
	saveErr string
	status  string
	// seq is the live check the editor waits for, fault what the last one
	// found wrong.
	seq   uint64
	fault ruleFault
	// The preview the last check read: of the rule as written or, while
	// rows are still to fill in, of the rows that are complete (partial).
	// blocked is a fault of the complete rows, which there is no preview
	// of until it is mended.
	preview    mail.RulePreview
	previewed  bool
	partial    bool
	blocked    bool
	offset     int
	previewErr string
	// The run over the preview: confirming asks first about confirm, the
	// messages the preview found as the user asked, which are all the run
	// takes; running is under way, and ran and runErr say how it went.
	confirming bool
	confirm    []mail.RuleTarget
	running    bool
	ran        string
	runErr     string
	// focus is the control the keyboard is on, which adding and removing
	// rows moves.
	focus string
}

// condRow is a condition of the rule being written. Which of its fields
// count is its field's to say, as in mail.Condition.
type condRow struct {
	key     int
	field   mail.ConditionField
	value   string
	header  string
	account string
	folder  string
	label   mail.LabelID
}

// actRow is an action of the rule being written, of one of the engine's
// kinds.
type actRow struct {
	key     int
	kind    mail.ActionKind
	account string
	folder  string
	label   mail.LabelID
}

// ruleFault is what the engine found wrong with a rule, said in the
// user's words, and where: a condition's or an action's row, by its key,
// and the part of it; the name; the conditions or the actions, of which
// there are none; or the rule as a whole. blank is a fault of what is
// still to fill in, which shows once a save was asked for.
type ruleFault struct {
	text           string
	cond, act      int
	part           faultPart
	name           bool
	noConds        bool
	noActs         bool
	general, blank bool
}

// faultPart is the control of a row a fault is about.
type faultPart int

const (
	partKind faultPart = iota
	partHeader
	partValue
	partAccount
	partFolder
	partLabel
)

func (ed *ruleEditor) nextKey() int {
	ed.keys++
	return ed.keys
}

// shows reports whether the editor shows its fault.
func (ed *ruleEditor) shows() bool {
	return ed.fault.text != "" && (!ed.fault.blank || ed.tried)
}

// textCondition reports whether a condition of the field looks for text.
func textCondition(f mail.ConditionField) bool {
	return f == mail.CondSender || f == mail.CondRecipients || f == mail.CondSubject || f == mail.CondHeader
}

// accountCondition reports whether a condition of the field names an
// account.
func accountCondition(f mail.ConditionField) bool {
	return f == mail.CondAccount || f == mail.CondFolder || f == mail.CondLabel
}

// accountAction reports whether an action of the kind names a folder or a
// label of an account.
func accountAction(k mail.ActionKind) bool {
	return k == mail.ActionMove || k == mail.ActionAddLabel || k == mail.ActionRemoveLabel
}

func (row *condRow) condition() mail.Condition {
	c := mail.Condition{Field: row.field}
	switch {
	case textCondition(row.field):
		c.Value = row.value
		if row.field == mail.CondHeader {
			c.Header = row.header
		}
	case row.field == mail.CondAccount:
		c.Account = row.account
	case row.field == mail.CondFolder:
		c.Account, c.Folder = row.account, row.folder
	case row.field == mail.CondLabel:
		c.Account, c.Label = row.account, row.label
	}
	return c
}

// complete reports whether the row says all its field needs.
func (row *condRow) complete() bool {
	switch row.field {
	case mail.CondSender, mail.CondRecipients, mail.CondSubject:
		return strings.TrimSpace(row.value) != ""
	case mail.CondHeader:
		return strings.TrimSpace(row.value) != "" && strings.TrimSpace(row.header) != ""
	case mail.CondAccount:
		return row.account != ""
	case mail.CondFolder:
		return row.account != "" && row.folder != ""
	case mail.CondLabel:
		return row.account != "" && row.label != 0
	}
	return true
}

func (row *actRow) action() mail.Action {
	act := mail.Action{Kind: row.kind}
	switch row.kind {
	case mail.ActionMove:
		act.Account, act.Folder = row.account, row.folder
	case mail.ActionAddLabel, mail.ActionRemoveLabel:
		act.Account, act.Label = row.account, row.label
	}
	return act
}

func (row *actRow) complete() bool {
	switch row.kind {
	case mail.ActionMove:
		return row.account != "" && row.folder != ""
	case mail.ActionAddLabel, mail.ActionRemoveLabel:
		return row.account != "" && row.label != 0
	}
	return true
}

// rule is the rule as written.
func (ed *ruleEditor) rule() mail.Rule {
	r := mail.Rule{ID: ed.id, Name: ed.name, Paused: ed.paused, Match: mail.MatchAll, Stop: ed.stop}
	if ed.match == 1 {
		r.Match = mail.MatchAny
	}
	for _, row := range ed.conds {
		r.Conditions = append(r.Conditions, row.condition())
	}
	for _, row := range ed.acts {
		r.Actions = append(r.Actions, row.action())
	}
	return r
}

// trial is the rule as written without the rows still to fill in, which
// its preview reads while there are some: a stand-in name while it has
// none, and an action that changes nothing while none is complete. conds
// and acts map its conditions and actions to the editor's rows, -1 for
// the stand-in. ok is false while no condition is complete, when there
// is nothing to preview.
func (ed *ruleEditor) trial() (r mail.Rule, conds, acts []int, ok bool) {
	r = ed.rule()
	if strings.TrimSpace(r.Name) == "" {
		r.Name = standInName
	}
	r.Conditions, r.Actions = nil, nil
	for i, row := range ed.conds {
		if row.complete() {
			r.Conditions = append(r.Conditions, row.condition())
			conds = append(conds, i)
		}
	}
	for i, row := range ed.acts {
		if row.complete() {
			r.Actions = append(r.Actions, row.action())
			acts = append(acts, i)
		}
	}
	if len(r.Actions) == 0 {
		r.Actions, acts = []mail.Action{{Kind: mail.ActionSkipNotification}}, []int{-1}
	}
	return r, conds, acts, len(r.Conditions) > 0
}

// load fills the editor with a saved rule.
func (ed *ruleEditor) load(r mail.Rule) {
	ed.id, ed.paused, ed.name, ed.stop = r.ID, r.Paused, r.Name, r.Stop
	ed.match = 0
	if r.Match == mail.MatchAny {
		ed.match = 1
	}
	for _, c := range r.Conditions {
		ed.conds = append(ed.conds, &condRow{key: ed.nextKey(), field: c.Field, value: c.Value, header: c.Header,
			account: c.Account, folder: c.Folder, label: c.Label})
	}
	for _, act := range r.Actions {
		ed.acts = append(ed.acts, &actRow{key: ed.nextKey(), kind: act.Kind, account: act.Account, folder: act.Folder, label: act.Label})
	}
}

// ruleAccountFor is the account a new row of the editor names: the one
// the rule names already, else the account on screen.
func (a *app) ruleAccountFor(ed *ruleEditor) string {
	if acc := ed.rule().Account(); acc != "" && a.account(acc) != nil {
		return acc
	}
	if a.account(a.active) != nil {
		return a.active
	}
	if len(a.accounts) > 0 {
		return a.accounts[0].ID
	}
	return ""
}

// openRuleEditor opens the editor of a rule of the list, or of a new one
// with a condition and an action to fill in.
func (a *app) openRuleEditor(r *mail.Rule, fromMail bool) {
	ed := &ruleEditor{fromMail: fromMail}
	if r != nil {
		ed.load(*r)
	} else {
		ed.conds = []*condRow{{key: ed.nextKey(), field: mail.CondSender}}
		ed.acts = []*actRow{{key: ed.nextKey(), kind: mail.ActionMove, account: a.ruleAccountFor(ed)}}
	}
	a.rules.editor = ed
	a.checkRuleEditor(0)
}

// ruleFromMessage opens the rules on a new rule made from a message: one
// for its mailing list where the cache has its List-Id, else for its
// sender's address, which moves the mail to a folder of its account still
// to choose. Closing it goes back to the mail.
func (a *app) ruleFromMessage(m mail.Summary) {
	a.openRules(rulesTabList)
	ed := &ruleEditor{fromMail: true}
	name := displayName(m.From)
	if utf8.RuneCountInString(name) > mail.MaxRuleName {
		name = string([]rune(name)[:mail.MaxRuleName])
	}
	ed.name = name
	cond := &condRow{key: ed.nextKey(), field: mail.CondSender, value: m.From.Email}
	if b, ok := a.bodies[summaryKey(m)]; ok {
		if id := listID(b.Message.RawHeaders); id != "" {
			cond = &condRow{key: cond.key, field: mail.CondHeader, header: "List-Id", value: id}
		}
	}
	ed.conds = []*condRow{cond}
	ed.acts = []*actRow{{key: ed.nextKey(), kind: mail.ActionMove, account: m.Account}}
	a.rules.editor = ed
	a.checkRuleEditor(0)
}

// listID is the identifier a message's List-Id header gives, among the
// header lines the cache keeps of it, or "".
func listID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), "List-Id") {
			continue
		}
		value = strings.TrimSpace(value)
		if i, j := strings.LastIndex(value, "<"), strings.LastIndex(value, ">"); i >= 0 && j > i+1 {
			value = value[i+1 : j]
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// closeRuleEditor leaves the rule as it was last saved: back to the list,
// or to the mail when a message opened the editor.
func (a *app) closeRuleEditor() {
	rs := &a.rules
	ed := rs.editor
	if ed == nil {
		return
	}
	rs.editor = nil
	a.ruleCheckSeq.Add(1)
	if ed.fromMail {
		a.closeRules()
		return
	}
	rs.focus = ruleFocusNew
	if ed.id != 0 {
		rs.focus = ruleFocus("edit", ed.id)
	}
	a.readRules()
}

// ruleEdited follows a change of the rule being written: it is checked
// and previewed again, from its first page.
func (a *app) ruleEdited() {
	ed := a.rules.editor
	if ed == nil {
		return
	}
	ed.dirty, ed.edits, ed.status, ed.saveErr, ed.ran, ed.runErr = true, ed.edits+1, "", "", "", ""
	a.checkRuleEditor(0)
}

// ruleCheck is what a live check of the rule being written found.
type ruleCheck struct {
	// err is the engine's word on the rule as written; trialErr its word
	// on the complete rows, asked when err is not nil and some are, whose
	// conditions and actions conds and acts map to the editor's rows.
	err, trialErr error
	conds, acts   []int
	preview       mail.RulePreview
	previewed     bool
	partial       bool
	previewErr    error
}

// checkRuleEditor checks the rule as written as SaveRule would, and reads
// a page of its preview from offset: a job after each change. The jobs
// take turns, one a later check overtook does not run, and the editor
// takes the last one's answer only.
func (a *app) checkRuleEditor(offset int) {
	ed := a.rules.editor
	if ed == nil {
		return
	}
	rule := ed.rule()
	trial, conds, acts, ok := ed.trial()
	seq := a.ruleCheckSeq.Add(1)
	ed.seq, ed.offset = seq, offset
	a.run(func() {
		a.ruleCheckMu.Lock()
		defer a.ruleCheckMu.Unlock()
		if a.ruleCheckSeq.Load() != seq {
			return
		}
		ctx, cancel := a.ctx()
		defer cancel()
		res := ruleCheck{conds: conds, acts: acts}
		res.err = a.svc.CheckRule(ctx, rule)
		used := rule
		if res.err != nil && ok {
			res.trialErr = a.svc.CheckRule(ctx, trial)
			used, res.partial = trial, true
		}
		if res.err == nil || (ok && res.trialErr == nil) {
			res.preview, res.previewErr = a.svc.PreviewRule(ctx, used, offset, rulePreviewPage)
			res.previewed = res.previewErr == nil
		}
		a.post(func() { a.ruleChecked(ed, seq, rule, trial, res) })
	})
}

// ruleChecked takes a live check's answer, when it is the one the editor
// waits for.
func (a *app) ruleChecked(ed *ruleEditor, seq uint64, rule, trial mail.Rule, res ruleCheck) {
	if a.rules.editor != ed || ed.seq != seq {
		return
	}
	var re *mail.RuleError
	switch {
	case res.err == nil:
		ed.fault = ruleFault{}
	case res.trialErr != nil && errors.As(res.trialErr, &re):
		ed.fault = a.faultOf(ed, trial, re, res.conds, res.acts, false)
	case res.trialErr != nil:
		ed.fault = ruleFault{text: locales.RuleCheckFailed(res.trialErr.Error()), general: true}
	case errors.As(res.err, &re):
		ed.fault = a.faultOf(ed, rule, re, nil, nil, true)
	default:
		ed.fault = ruleFault{text: locales.RuleCheckFailed(res.err.Error()), general: true}
	}
	ed.previewed, ed.partial, ed.blocked, ed.previewErr = res.previewed, res.partial, res.trialErr != nil, ""
	ed.preview = mail.RulePreview{}
	if res.previewed {
		ed.preview = res.preview
	}
	if res.previewErr != nil && !errors.As(res.previewErr, &re) {
		ed.previewErr = locales.RulePreviewFailed(res.previewErr.Error())
	}
}

// faultOf places a fault the engine found in r, whose conditions and
// actions conds and acts map to the editor's rows when r is its trial.
func (a *app) faultOf(ed *ruleEditor, r mail.Rule, re *mail.RuleError, conds, acts []int, blank bool) ruleFault {
	f := ruleFault{text: a.ruleFaultText(r, re), blank: blank}
	switch {
	case re.Condition >= 0 && re.Condition < len(r.Conditions):
		i := re.Condition
		if conds != nil {
			i = conds[i]
		}
		if i >= 0 && i < len(ed.conds) {
			f.cond, f.part = ed.conds[i].key, conditionPart(re.Err, r.Conditions[re.Condition])
		} else {
			f.general = true
		}
	case re.Action >= 0 && re.Action < len(r.Actions):
		i := re.Action
		if acts != nil {
			i = acts[i]
		}
		if i >= 0 && i < len(ed.acts) {
			f.act, f.part = ed.acts[i].key, actionPart(re.Err, r.Actions[re.Action])
		} else {
			f.general = true
		}
	case errors.Is(re, mail.ErrRuleName):
		f.name = true
	case errors.Is(re, mail.ErrRuleEmpty) && len(r.Conditions) == 0:
		f.noConds = true
	case errors.Is(re, mail.ErrRuleEmpty):
		f.noActs = true
	default:
		f.general = true
	}
	return f
}

// conditionPart is the control of a condition's row a fault is about.
func conditionPart(err error, c mail.Condition) faultPart {
	switch {
	case errors.Is(err, mail.ErrRuleAccount):
		return partAccount
	case errors.Is(err, mail.ErrRuleFolder):
		return partFolder
	case errors.Is(err, mail.ErrRuleLabel):
		return partLabel
	case c.Field == mail.CondHeader && !ruleHeaderName(c.Header):
		return partHeader
	case textCondition(c.Field):
		return partValue
	case c.Field == mail.CondAccount:
		return partAccount
	case c.Field == mail.CondFolder && c.Account == "":
		return partAccount
	case c.Field == mail.CondFolder:
		return partFolder
	case c.Field == mail.CondLabel && c.Account == "":
		return partAccount
	case c.Field == mail.CondLabel:
		return partLabel
	}
	return partKind
}

// actionPart is the control of an action's row a fault is about.
func actionPart(err error, act mail.Action) faultPart {
	switch {
	case errors.Is(err, mail.ErrRuleAccount), accountAction(act.Kind) && act.Account == "":
		return partAccount
	case errors.Is(err, mail.ErrRuleFolder), act.Kind == mail.ActionMove && act.Folder == "":
		return partFolder
	case errors.Is(err, mail.ErrRuleLabel), accountAction(act.Kind) && act.Kind != mail.ActionMove && act.Label == 0:
		return partLabel
	}
	return partKind
}

// ruleHeaderName reports whether a condition can read a header of that
// name, as the engine reads names: the letters, digits and - _ . that
// real headers are made of, at most 76. The engine decides; this only
// picks the words of its refusal.
func ruleHeaderName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 76 {
		return false
	}
	return strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.") == ""
}

// ruleFaultText says in the user's words what the engine refused of a
// rule.
func (a *app) ruleFaultText(r mail.Rule, re *mail.RuleError) string {
	switch {
	case re.Condition >= 0 && re.Condition < len(r.Conditions):
		return a.conditionFault(re.Err, r, r.Conditions[re.Condition])
	case re.Action >= 0 && re.Action < len(r.Actions):
		return a.actionFault(re.Err, r, re.Action)
	case errors.Is(re, mail.ErrRuleName):
		return locales.RuleNameInvalid(mail.MaxRuleName)
	case errors.Is(re, mail.ErrRuleEmpty) && len(r.Conditions) == 0:
		return locales.RuleNeedsCondition()
	case errors.Is(re, mail.ErrRuleEmpty):
		return locales.RuleNeedsAction()
	}
	return locales.RuleInvalid()
}

// accountFault says why a rule cannot name an account: it is gone, or
// the rule names another.
func (a *app) accountFault(r mail.Rule, account string) string {
	if a.account(account) == nil {
		return locales.RuleAccountGone()
	}
	return locales.RuleOneAccount(a.accountLabel(r.Account()))
}

func (a *app) conditionFault(err error, r mail.Rule, c mail.Condition) string {
	value := strings.TrimSpace(c.Value)
	switch {
	case errors.Is(err, mail.ErrRuleAccount):
		return a.accountFault(r, c.Account)
	case errors.Is(err, mail.ErrRuleFolder):
		return locales.RuleFolderGone(a.folderLabelOf(c.Account, c.Folder))
	case errors.Is(err, mail.ErrRuleLabel):
		return locales.RuleLabelGone()
	case c.Field == mail.CondHeader && strings.TrimSpace(c.Header) == "":
		return locales.RuleHeaderMissing()
	case c.Field == mail.CondHeader && !ruleHeaderName(c.Header):
		return locales.RuleHeaderInvalid()
	case textCondition(c.Field) && value == "":
		return locales.RuleValueMissing()
	case textCondition(c.Field) && utf8.RuneCountInString(value) > mail.MaxRuleValue:
		return locales.RuleValueTooLong(mail.MaxRuleValue)
	case textCondition(c.Field):
		return locales.RuleValueInvalid()
	case accountCondition(c.Field) && c.Account == "":
		return locales.RuleChooseAccount()
	case c.Field == mail.CondFolder:
		return locales.RuleChooseFolder()
	case c.Field == mail.CondLabel:
		return locales.RuleChooseLabel()
	}
	return locales.RuleConditionInvalid()
}

func (a *app) actionFault(err error, r mail.Rule, i int) string {
	act := r.Actions[i]
	switch {
	case errors.Is(err, mail.ErrRuleAccount):
		return a.accountFault(r, act.Account)
	case errors.Is(err, mail.ErrRuleFolder):
		return locales.RuleFolderGone(a.folderLabelOf(act.Account, act.Folder))
	case errors.Is(err, mail.ErrRuleLabel):
		return locales.RuleLabelGone()
	case !act.Kind.Valid():
		return locales.RuleActionNotAllowed()
	case accountAction(act.Kind) && act.Account == "":
		return locales.RuleChooseAccount()
	case act.Kind == mail.ActionMove && act.Folder == "":
		return locales.RuleChooseFolder()
	case (act.Kind == mail.ActionAddLabel || act.Kind == mail.ActionRemoveLabel) && act.Label == 0:
		return locales.RuleChooseLabel()
	}
	for _, before := range r.Actions[:i] {
		pair := []mail.ActionKind{before.Kind, act.Kind}
		switch {
		case before.Kind == mail.ActionMove && act.Kind == mail.ActionMove:
			return locales.RuleOneMove()
		case slices.Contains(pair, mail.ActionMarkRead) && slices.Contains(pair, mail.ActionMarkUnread):
			return locales.RuleReadAndUnread()
		case before.Label == act.Label && slices.Contains(pair, mail.ActionAddLabel) && slices.Contains(pair, mail.ActionRemoveLabel):
			return locales.RuleLabelBothWays()
		case before.Kind == act.Kind && before.Label == act.Label:
			return locales.RuleActionTwice()
		}
	}
	return locales.RuleActionInvalid()
}

// saveRuleEditor saves the rule as written, which touches no mail. What
// the engine refuses shows at its item; once saved, the preview offers to
// apply the rule to the mail it found.
func (a *app) saveRuleEditor() {
	ed := a.rules.editor
	if ed == nil || ed.saving {
		return
	}
	ed.tried = true
	r, edits := ed.rule(), ed.edits
	ed.saving, ed.saveErr, ed.status, ed.ran, ed.runErr = true, "", "", "", ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		saved, err := a.svc.SaveRule(ctx, r)
		a.post(func() {
			if a.rules.editor != ed {
				return
			}
			ed.saving = false
			unchanged := ed.edits == edits
			var re *mail.RuleError
			switch {
			case errors.As(err, &re):
				if unchanged {
					ed.fault = a.faultOf(ed, r, re, nil, nil, true)
				}
			case err != nil:
				ed.saveErr = locales.RuleSaveFailed(ruleFailure(err))
			default:
				ed.id, ed.paused = saved.ID, saved.Paused
				if unchanged {
					ed.name = saved.Name
					for i, c := range saved.Conditions {
						if i < len(ed.conds) {
							ed.conds[i].value, ed.conds[i].header = c.Value, c.Header
						}
					}
					ed.dirty = false
				}
				ed.status = locales.RuleSaved()
				a.readRules()
				a.checkRuleEditor(0)
			}
		})
	})
}

// canApply reports whether the preview offers to apply the rule: it is
// saved, unchanged since, and the preview is of it whole.
func (ed *ruleEditor) canApply() bool {
	return ed.id != 0 && !ed.dirty && ed.previewed && !ed.partial && ed.preview.Total > 0
}

// applyWhyNot says why a rule that can be applied cannot be now, or "".
func (ed *ruleEditor) applyWhyNot() string {
	switch {
	case ed.paused:
		return locales.RuleApplyPaused()
	case ed.preview.Total > mail.MaxRuleRun:
		return locales.RuleRunTooLarge(mail.MaxRuleRun)
	}
	return ""
}

// askApply asks before the saved rule is applied to the messages its
// preview lists now, which the question counts.
func (a *app) askApply(ed *ruleEditor) {
	if !ed.canApply() || ed.applyWhyNot() != "" || ed.running {
		return
	}
	ed.confirm, ed.confirming = slices.Clone(ed.preview.Targets), true
}

// runRule applies the saved rule once to the messages its preview found
// as the user asked, which they confirmed: the engine checks each again,
// logs each as an entry the activity log can undo, and applies their
// actions.
func (a *app) runRule(ed *ruleEditor) {
	if a.rules.editor != ed || ed.running || ed.dirty || ed.id == 0 || len(ed.confirm) == 0 {
		return
	}
	id, targets := ed.id, ed.confirm
	ed.running, ed.ran, ed.runErr, ed.confirm = true, "", "", nil
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), ruleRunTimeout)
		defer cancel()
		entries, err := a.svc.RunRule(ctx, id, targets)
		a.post(func() {
			accounts := map[string]bool{}
			for _, e := range entries {
				accounts[e.Account] = true
			}
			for account := range accounts {
				a.readFolders(account)
			}
			a.readList(a.gen, false, false)
			if a.rules.editor != ed {
				return
			}
			ed.running = false
			if err != nil {
				ed.runErr = locales.RuleRunFailed(ruleFailure(err))
			} else {
				ed.ran = runSummary(entries)
			}
			a.readLog()
			a.checkRuleEditor(0)
		})
	})
}

// runSummary says how a run went: how many messages it changed, how many
// wait for the server, and how many it could not change.
func runSummary(entries []mail.RuleActivity) string {
	var done, waiting, failed int
	for _, e := range entries {
		switch e.State {
		case mail.ActivityPending:
			waiting++
		case mail.ActivityFailed:
			failed++
		default:
			done++
		}
	}
	parts := []string{locales.RuleRanDone(done)}
	if waiting > 0 {
		parts = append(parts, locales.RuleRanWaiting(waiting))
	}
	if failed > 0 {
		parts = append(parts, locales.RuleRanFailed(failed))
	}
	return strings.Join(parts, " ")
}

// showRuleActivity leaves the editor for the log of the rule it saved.
func (a *app) showRuleActivity(id mail.RuleID) {
	rs := &a.rules
	rs.editor = nil
	a.ruleCheckSeq.Add(1)
	rs.tab = rulesTabActivity
	rs.log.rule, rs.log.state, rs.log.offset = id, "", 0
	a.readRules()
	a.readLog()
}

// addCondition adds a condition to fill in, which takes the keyboard.
func (a *app) addCondition() {
	ed := a.rules.editor
	row := &condRow{key: ed.nextKey(), field: mail.CondSender}
	ed.conds = append(ed.conds, row)
	ed.focus = "cond/" + strconv.Itoa(row.key)
	a.ruleEdited()
}

// addAction adds an action to fill in, which takes the keyboard. Its kind
// is the first of the engine's the rule does not have yet.
func (a *app) addAction() {
	ed := a.rules.editor
	kind := mail.ActionKinds[0]
	for _, k := range mail.ActionKinds {
		if !slices.ContainsFunc(ed.acts, func(row *actRow) bool { return row.kind == k }) {
			kind = k
			break
		}
	}
	row := &actRow{key: ed.nextKey(), kind: kind}
	if accountAction(kind) {
		row.account = a.ruleAccountFor(ed)
	}
	ed.acts = append(ed.acts, row)
	ed.focus = "act/" + strconv.Itoa(row.key)
	a.ruleEdited()
}

// removeCondition takes a condition out; the keyboard goes to the button
// that adds one.
func (a *app) removeCondition(row *condRow) {
	ed := a.rules.editor
	ed.conds = slices.DeleteFunc(ed.conds, func(have *condRow) bool { return have == row })
	ed.focus = "addCond"
	a.ruleEdited()
}

func (a *app) removeAction(row *actRow) {
	ed := a.rules.editor
	ed.acts = slices.DeleteFunc(ed.acts, func(have *actRow) bool { return have == row })
	ed.focus = "addAct"
	a.ruleEdited()
}

// conditionFieldChanged follows a condition's field: one that names an
// account names the rule's.
func (a *app) conditionFieldChanged(row *condRow) {
	if accountCondition(row.field) && row.account == "" {
		row.account = a.ruleAccountFor(a.rules.editor)
	}
	a.ruleEdited()
}

func (a *app) actionKindChanged(row *actRow) {
	if accountAction(row.kind) && row.account == "" {
		row.account = a.ruleAccountFor(a.rules.editor)
	}
	a.ruleEdited()
}
