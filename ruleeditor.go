package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

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
