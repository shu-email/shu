package mail

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rules sort the mail that arrives. A rule has conditions on a message and
// actions on it, and the rules are in an order: each message that arrives
// in a folder they watch is run through them, from the cache alone (see
// arrivals.go), and every match is logged (RuleActivity) so that it can be
// undone. The actions are a closed set that moves, labels, stars and marks
// a message: none sends, forwards or deletes one, and nothing a rule can
// hold would.

// RuleID names a rule. IDs are never given again.
type RuleID int64

// MatchMode is how a rule's conditions combine.
type MatchMode string

const (
	// MatchAll matches a message that meets every condition, MatchAny one
	// that meets one at least.
	MatchAll MatchMode = "all"
	MatchAny MatchMode = "any"
)

// ConditionField is what a condition looks at.
type ConditionField string

const (
	// CondSender is the sender's name or address, CondRecipients the name
	// or address of one of the To and Cc, CondSubject the subject, and
	// CondHeader the header Condition.Header names: each contains
	// Condition.Value, in any case.
	CondSender     ConditionField = "sender"
	CondRecipients ConditionField = "recipients"
	CondSubject    ConditionField = "subject"
	CondHeader     ConditionField = "header"
	// CondAttachment is a message with attachments.
	CondAttachment ConditionField = "attachment"
	// CondAccount is a message of Condition.Account, CondFolder one in
	// that account's Condition.Folder, and CondLabel one with that
	// account's Condition.Label.
	CondAccount ConditionField = "account"
	CondFolder  ConditionField = "folder"
	CondLabel   ConditionField = "label"
)

// ConditionFields are the conditions a rule can have, in the order the
// window offers them.
var ConditionFields = []ConditionField{
	CondSender, CondRecipients, CondSubject, CondHeader, CondAttachment, CondAccount, CondFolder, CondLabel,
}

// Condition is one condition of a rule. Which of its fields count is the
// Field's to say.
type Condition struct {
	Field ConditionField `json:"field"`
	// Value is what a text condition looks for, and Header the name of
	// the header a header condition reads.
	Value  string `json:"value,omitempty"`
	Header string `json:"header,omitempty"`
	// Account is the account of an account, folder or label condition,
	// Folder the folder's name and Label the label.
	Account string  `json:"account,omitempty"`
	Folder  string  `json:"folder,omitempty"`
	Label   LabelID `json:"label,omitempty"`
}

// namesAccount reports whether a condition of the field names an account.
func (f ConditionField) namesAccount() bool {
	return f == CondAccount || f == CondFolder || f == CondLabel
}

// text reports whether a condition of the field looks for text.
func (f ConditionField) text() bool {
	return f == CondSender || f == CondRecipients || f == CondSubject || f == CondHeader
}

// ActionKind is what an action does to a message. The set is closed: a
// rule moves a message to another folder of its account, puts a label on
// it or takes one off, stars it, marks it read or unread, or keeps it from
// being announced. A move to the Trash is a move, which the window warns
// of; nothing sends, forwards or deletes.
type ActionKind string

const (
	ActionMove             ActionKind = "move"
	ActionAddLabel         ActionKind = "addLabel"
	ActionRemoveLabel      ActionKind = "removeLabel"
	ActionStar             ActionKind = "star"
	ActionMarkRead         ActionKind = "markRead"
	ActionMarkUnread       ActionKind = "markUnread"
	ActionSkipNotification ActionKind = "skipNotification"
)

// ActionKinds are the actions a rule can take, in the order the window
// offers them, and none other.
var ActionKinds = []ActionKind{
	ActionMove, ActionAddLabel, ActionRemoveLabel, ActionStar, ActionMarkRead, ActionMarkUnread, ActionSkipNotification,
}

// Valid reports whether the kind is one of ActionKinds.
func (k ActionKind) Valid() bool { return slices.Contains(ActionKinds, k) }

// namesAccount reports whether an action of the kind names a folder or a
// label, which are an account's.
func (k ActionKind) namesAccount() bool {
	return k == ActionMove || k == ActionAddLabel || k == ActionRemoveLabel
}

// Action is one action of a rule: a move to Folder of Account, or a label
// of Account put on or taken off; the other kinds take nothing more.
type Action struct {
	Kind    ActionKind `json:"kind"`
	Account string     `json:"account,omitempty"`
	Folder  string     `json:"folder,omitempty"`
	Label   LabelID    `json:"label,omitempty"`
}

// Rule is one of the user's rules.
type Rule struct {
	ID   RuleID `json:"id"`
	Name string `json:"name"`
	// Paused is a rule the user disabled: it sorts nothing until enabled
	// again, and what it did stays in the log.
	Paused     bool        `json:"paused,omitempty"`
	Match      MatchMode   `json:"match"`
	Conditions []Condition `json:"conditions"`
	Actions    []Action    `json:"actions"`
	// Stop ends the sorting of a message the rule matched: the rules after
	// it are not tried. A rule that moves a message ends it too.
	Stop bool `json:"stop,omitempty"`
}

// Account is the account the rule names, in a condition or an action, ""
// for none. A rule that names one sorts that account's mail alone and goes
// with the account; one that names none sorts every account's. CheckRule
// lets a rule name one account at most.
func (r Rule) Account() string {
	for _, c := range r.Conditions {
		if c.Field.namesAccount() && c.Account != "" {
			return c.Account
		}
	}
	for _, a := range r.Actions {
		if a.Kind.namesAccount() && a.Account != "" {
			return a.Account
		}
	}
	return ""
}

// watches reports whether the rule sorts the mail that arrives in a folder
// of an account: the inbox of each account it sorts, and each folder one
// of its conditions names, never the Drafts folder, whose messages are the
// drafts' (see AGENTS.md).
func (r Rule) watches(accountID string, f Folder) bool {
	if a := r.Account(); (a != "" && a != accountID) || f.SpecialUse == SpecialDrafts {
		return false
	}
	if f.IsInbox() {
		return true
	}
	return slices.ContainsFunc(r.Conditions, func(c Condition) bool {
		return c.Field == CondFolder && c.Account == accountID && c.Folder == f.Name
	})
}

// moves returns the folder the rule moves a message to, "" for none.
func (r Rule) moves() string {
	for _, a := range r.Actions {
		if a.Kind == ActionMove {
			return a.Folder
		}
	}
	return ""
}

// stops reports whether no rule after this one is tried on a message it
// matched: it says so, or it moves the message away.
func (r Rule) stops() bool { return r.Stop || r.moves() != "" }

// quiet reports whether a message the rule matched is not to be
// announced: the rule says so, moves it out of the folder, or marks it
// read, as the window announces only unread mail.
func (r Rule) quiet() bool {
	return slices.ContainsFunc(r.Actions, func(a Action) bool {
		return a.Kind == ActionSkipNotification || a.Kind == ActionMove || a.Kind == ActionMarkRead
	})
}

// headers returns the names of the headers the rule's conditions read.
func (r Rule) headers() []string {
	var out []string
	for _, c := range r.Conditions {
		if c.Field == CondHeader && !slices.Contains(out, c.Header) {
			out = append(out, c.Header)
		}
	}
	return out
}

// Limits of what a rule holds.
const (
	// MaxRuleName is the most characters a rule's name has, and
	// MaxRuleValue the most a condition looks for.
	MaxRuleName  = 64
	MaxRuleValue = 256
	// maxHeaderName is the longest name of a header a condition reads.
	maxHeaderName = 76
)

var (
	// ErrRuleName is a name no rule can have: empty, longer than
	// MaxRuleName, or with control characters.
	ErrRuleName = errors.New("mail: a rule's name is empty, too long or has control characters")
	// ErrRuleEmpty is a rule without a condition or without an action.
	ErrRuleEmpty = errors.New("mail: a rule needs a condition and an action")
	// ErrRuleCondition is a condition no rule can have, or one with
	// nothing to look for: an empty text, a header name that is none, or
	// a match mode that is neither all nor any.
	ErrRuleCondition = errors.New("mail: the condition is not one a rule can have")
	// ErrRuleAction is an action no rule can take, which sending,
	// forwarding and deleting are, or one that undoes another of the same
	// rule or says the same again.
	ErrRuleAction = errors.New("mail: the action is not one a rule can take")
	// ErrRuleAccount is an account the app does not have, or a second
	// account in one rule.
	ErrRuleAccount = errors.New("mail: a rule names an account there is not, or two accounts")
	// ErrRuleFolder is a folder the account does not have, or its Drafts
	// folder, whose messages are the drafts' (see AGENTS.md).
	ErrRuleFolder = errors.New("mail: the account has no such folder for a rule")
	// ErrRuleLabel is a label that is not one of the account's.
	ErrRuleLabel = errors.New("mail: the label is not one of the account's")
	// ErrRulePaused is a run of a paused rule.
	ErrRulePaused = errors.New("mail: the rule is paused")
	// ErrRuleRunTooLarge is a run of more messages than MaxRuleRun.
	ErrRuleRunTooLarge = errors.New("mail: too many messages for one run of a rule")
)

// RuleError is what is wrong with a rule CheckRule refused, and where:
// the place of the condition or the action at fault among the rule's, -1
// for neither. Err is one of the ErrRule errors, which errors.Is finds.
type RuleError struct {
	Err       error
	Condition int
	Action    int
}

func (e *RuleError) Error() string {
	switch {
	case e.Condition >= 0:
		return fmt.Sprintf("%v (condition %d)", e.Err, e.Condition+1)
	case e.Action >= 0:
		return fmt.Sprintf("%v (action %d)", e.Err, e.Action+1)
	}
	return e.Err.Error()
}

func (e *RuleError) Unwrap() error { return e.Err }

func ruleErr(err error) *RuleError { return &RuleError{Err: err, Condition: -1, Action: -1} }

func conditionErr(i int, err error) *RuleError {
	return &RuleError{Err: err, Condition: i, Action: -1}
}

func actionErr(i int, err error) *RuleError { return &RuleError{Err: err, Condition: -1, Action: i} }

// cleanRuleText returns text a rule holds without the spaces around it, or
// false for text it cannot hold: empty, longer than most, or with control
// characters.
func cleanRuleText(s string, most int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > most || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", false
	}
	return s, true
}

// headerNameOK reports whether a name is one a condition can read: the
// letters, digits and - _ . that the names of real headers are made of,
// which go into a fetch on the wire as they are.
func headerNameOK(name string) bool {
	if name == "" || len(name) > maxHeaderName {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// clean returns the rule as it is checked and saved: its name, the text of
// its conditions and their header names without the spaces around them,
// header names in their canonical case, and all as the mode of a rule
// that says none.
func (r Rule) clean() Rule {
	r.Name = strings.TrimSpace(r.Name)
	if r.Match == "" {
		r.Match = MatchAll
	}
	r.Conditions = slices.Clone(r.Conditions)
	for i := range r.Conditions {
		c := &r.Conditions[i]
		c.Value = strings.TrimSpace(c.Value)
		if c.Header = strings.TrimSpace(c.Header); headerNameOK(c.Header) {
			c.Header = textproto.CanonicalMIMEHeaderKey(c.Header)
		}
	}
	r.Actions = slices.Clone(r.Actions)
	return r
}

// checkShape reports what is wrong with a rule that can be told without
// the cache: its name, that it has conditions and actions, each of them
// one of the set with what it needs, actions that contradict or repeat
// each other, and a second account.
func checkShape(r Rule) *RuleError {
	if _, ok := cleanRuleText(r.Name, MaxRuleName); !ok {
		return ruleErr(ErrRuleName)
	}
	if len(r.Conditions) == 0 || len(r.Actions) == 0 {
		return ruleErr(ErrRuleEmpty)
	}
	if r.Match != MatchAll && r.Match != MatchAny {
		return ruleErr(ErrRuleCondition)
	}
	account := ""
	named := func(a string) bool {
		if account == "" {
			account = a
		}
		return a == account
	}
	for i, c := range r.Conditions {
		switch {
		case !slices.Contains(ConditionFields, c.Field):
			return conditionErr(i, ErrRuleCondition)
		case c.Field.text():
			if _, ok := cleanRuleText(c.Value, MaxRuleValue); !ok {
				return conditionErr(i, ErrRuleCondition)
			}
			if c.Field == CondHeader && !headerNameOK(c.Header) {
				return conditionErr(i, ErrRuleCondition)
			}
		case c.Field.namesAccount():
			if c.Account == "" || (c.Field == CondFolder && c.Folder == "") || (c.Field == CondLabel && c.Label == 0) {
				return conditionErr(i, ErrRuleCondition)
			}
			if !named(c.Account) {
				return conditionErr(i, ErrRuleAccount)
			}
		}
	}
	// said is what an action says, which another may say again or undo.
	type said struct {
		kind  ActionKind
		label LabelID
	}
	seen := map[said]bool{}
	for i, a := range r.Actions {
		if !a.Kind.Valid() {
			return actionErr(i, ErrRuleAction)
		}
		if a.Kind.namesAccount() {
			if a.Account == "" || (a.Kind == ActionMove && a.Folder == "") || (a.Kind != ActionMove && a.Label == 0) {
				return actionErr(i, ErrRuleAction)
			}
			if !named(a.Account) {
				return actionErr(i, ErrRuleAccount)
			}
		}
		key, undone := said{kind: a.Kind}, said{}
		switch a.Kind {
		case ActionAddLabel:
			key.label, undone = a.Label, said{ActionRemoveLabel, a.Label}
		case ActionRemoveLabel:
			key.label, undone = a.Label, said{ActionAddLabel, a.Label}
		case ActionMarkRead:
			undone = said{kind: ActionMarkUnread}
		case ActionMarkUnread:
			undone = said{kind: ActionMarkRead}
		}
		if seen[key] || (undone.kind != "" && seen[undone]) {
			return actionErr(i, ErrRuleAction)
		}
		seen[key] = true
	}
	return nil
}

// checkRule checks a cleaned rule: its shape, and that the account it
// names is there with the folders and labels it names, none of them its
// Drafts folder.
func (s *Service) checkRule(ctx context.Context, r Rule) error {
	if err := checkShape(r); err != nil {
		return err
	}
	account := r.Account()
	if account == "" {
		return nil
	}
	if _, ok, err := s.store.Account(ctx, account); err != nil {
		return err
	} else if !ok {
		for i, c := range r.Conditions {
			if c.Field.namesAccount() {
				return conditionErr(i, ErrRuleAccount)
			}
		}
		for i, a := range r.Actions {
			if a.Kind.namesAccount() {
				return actionErr(i, ErrRuleAccount)
			}
		}
	}
	folders, err := s.store.Folders(ctx, account)
	if err != nil {
		return err
	}
	labels, err := s.store.Labels(ctx, account)
	if err != nil {
		return err
	}
	folderOK := func(name string) bool {
		return slices.ContainsFunc(folders, func(f Folder) bool { return f.Name == name && f.SpecialUse != SpecialDrafts })
	}
	labelOK := func(id LabelID) bool {
		return slices.ContainsFunc(labels, func(l Label) bool { return l.ID == id })
	}
	for i, c := range r.Conditions {
		switch {
		case c.Field == CondFolder && !folderOK(c.Folder):
			return conditionErr(i, ErrRuleFolder)
		case c.Field == CondLabel && !labelOK(c.Label):
			return conditionErr(i, ErrRuleLabel)
		}
	}
	for i, a := range r.Actions {
		switch {
		case a.Kind == ActionMove && !folderOK(a.Folder):
			return actionErr(i, ErrRuleFolder)
		case (a.Kind == ActionAddLabel || a.Kind == ActionRemoveLabel) && !labelOK(a.Label):
			return actionErr(i, ErrRuleLabel)
		}
	}
	return nil
}

// sortingRules returns the rules that sort mail: the enabled ones whose
// shape holds, as a rule written to the database by other means than
// SaveRule might not.
func (s *Service) sortingRules(ctx context.Context) ([]Rule, error) {
	rules, err := s.store.Rules(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rules, func(r Rule) bool { return r.Paused || checkShape(r) != nil }), nil
}

// Rules lists the rules in their order, the first tried first.
func (s *Service) Rules(ctx context.Context) ([]Rule, error) { return s.store.Rules(ctx) }

// rule returns the rule of an ID, ErrNotFound when there is none.
func (s *Service) rule(ctx context.Context, id RuleID) (Rule, error) {
	rules, err := s.store.Rules(ctx)
	if err != nil {
		return Rule{}, err
	}
	for _, r := range rules {
		if r.ID == id {
			return r, nil
		}
	}
	return Rule{}, ErrNotFound
}

// CheckRule reports what SaveRule would refuse of a rule: a *RuleError
// that names the condition or the action at fault and wraps one of the
// ErrRule errors. It is nil for a rule SaveRule takes.
func (s *Service) CheckRule(ctx context.Context, r Rule) error {
	return s.checkRule(ctx, r.clean())
}

// SaveRule writes a rule the user made or changed, which CheckRule must
// take: a new one (ID 0) after the others, or the rule of its ID,
// ErrNotFound when there is none. It returns the rule as saved, its name
// and texts without the spaces around them. Saving touches no mail: a
// rule sorts the mail that arrives from then on, and the mail there was
// only when the user runs it (RunRule).
func (s *Service) SaveRule(ctx context.Context, r Rule) (Rule, error) {
	r = r.clean()
	if err := s.checkRule(ctx, r); err != nil {
		return Rule{}, err
	}
	return s.store.SaveRule(ctx, r)
}

// PauseRule disables a rule, or enables it again: the window's "disable".
// A paused rule sorts nothing, and what it has pending that had not begun
// is dropped (marked undone). What it did stays in the log, where
// UndoRule takes it back. A rule whose folder or label is gone can be
// paused all the same.
func (s *Service) PauseRule(ctx context.Context, id RuleID, paused bool) (Rule, error) {
	r, err := s.rule(ctx, id)
	if err != nil {
		return Rule{}, err
	}
	r.Paused = paused
	if r, err = s.store.SaveRule(ctx, r); err != nil {
		return Rule{}, err
	}
	if paused {
		return r, s.dropPending(ctx, id)
	}
	return r, nil
}

// DeleteRule deletes a rule, after which it sorts nothing; what it has
// pending that had not begun is dropped. Its entries of the log stay, to
// be undone.
func (s *Service) DeleteRule(ctx context.Context, id RuleID) error {
	if err := s.store.DeleteRule(ctx, id); err != nil {
		return err
	}
	return s.dropPending(ctx, id)
}

// MoveRule puts a rule at a place of the order, 0 being the first tried.
// A place past the end is the end.
func (s *Service) MoveRule(ctx context.Context, id RuleID, index int) error {
	return s.store.MoveRule(ctx, id, index)
}

// RuleTarget is a message a rule matched, where the cache has it.
type RuleTarget struct {
	Account     string `json:"account"`
	Folder      string `json:"folder"`
	UIDValidity uint32 `json:"uidValidity"`
	UID         UID    `json:"uid"`
}

// RulePreview is what a rule would do to the mail there is: how many of the
// cached messages it matches, a page of them, newest first, and all of
// them as targets for RunRule.
type RulePreview struct {
	Total    int          `json:"total"`
	Messages []Summary    `json:"messages"`
	Targets  []RuleTarget `json:"targets"`
}

// ruleScope returns the folders of each account whose mail a rule sorts,
// as the cache has them: the inbox of the accounts it sorts, and the
// folders its conditions name.
func (s *Service) ruleScope(ctx context.Context, r Rule) ([]AccountFolder, error) {
	accounts := []string{r.Account()}
	if accounts[0] == "" {
		all, err := s.store.Accounts(ctx)
		if err != nil {
			return nil, err
		}
		accounts = accounts[:0]
		for _, a := range all {
			accounts = append(accounts, a.ID)
		}
		sort.Strings(accounts)
	}
	var out []AccountFolder
	for _, account := range accounts {
		folders, err := s.store.Folders(ctx, account)
		if err != nil {
			return nil, err
		}
		for _, f := range folders {
			if r.watches(account, f) {
				out = append(out, AccountFolder{AccountID: account, Folder: f})
			}
		}
	}
	return out, nil
}

// newestFirst orders messages as lists do: the newest first, and then by
// account, folder and UID so that the order is total.
func newestFirst(list []Message) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case !a.Date.Equal(b.Date):
			return a.Date.After(b.Date)
		case a.Account != b.Account:
			return a.Account < b.Account
		case a.Folder != b.Folder:
			return a.Folder < b.Folder
		}
		return a.UID > b.UID
	})
}

// PreviewRule lists the cached messages a rule, saved or not, matches now
// in the folders it watches (the inbox of each account it sorts and the
// folders its conditions name): limit of them after offset, newest first
// (DefaultPageSize when limit is not above zero), how many in all, and
// all of them as targets for RunRule. It reads the cache alone and changes
// nothing. A header condition finds the messages whose header the cache
// has: Delivered-To, and the headers rules named as a message arrived or
// was opened. The rule is checked as SaveRule checks it; that it is paused
// does not matter here.
func (s *Service) PreviewRule(ctx context.Context, r Rule, offset, limit int) (RulePreview, error) {
	r = r.clean()
	if err := s.checkRule(ctx, r); err != nil {
		return RulePreview{}, err
	}
	scope, err := s.ruleScope(ctx, r)
	if err != nil {
		return RulePreview{}, err
	}
	var found []Message
	validity := map[string]uint32{}
	for _, af := range scope {
		msgs, err := s.store.MessageHeaders(ctx, af.AccountID, af.Folder.Name, nil)
		if err != nil {
			return RulePreview{}, err
		}
		validity[af.AccountID+"\x00"+af.Folder.Name] = af.Folder.UIDValidity
		for _, m := range msgs {
			if r.matches(m, m.HasLabel) {
				found = append(found, m)
			}
		}
	}
	newestFirst(found)
	if limit <= 0 {
		limit = DefaultPageSize
	}
	offset = min(max(offset, 0), len(found))
	out := RulePreview{Total: len(found), Messages: []Summary{}, Targets: make([]RuleTarget, 0, len(found))}
	for i, m := range found {
		out.Targets = append(out.Targets, RuleTarget{
			Account: m.Account, Folder: m.Folder, UIDValidity: validity[m.Account+"\x00"+m.Folder], UID: m.UID,
		})
		if i >= offset && i < offset+limit {
			out.Messages = append(out.Messages, m.Summary)
		}
	}
	return out, nil
}

// MaxRuleRun is the most messages one run of a rule takes.
const MaxRuleRun = 1000

// RunRule runs a saved, enabled rule once over mail there was before it,
// as the user asks after its preview: over the targets of that preview
// (RulePreview.Targets), and of those only the messages the cache still
// has where the preview found them and the rule still matches, so that it
// never acts beyond what was shown. Each match is logged as one of new
// mail is, Manual, so that it can be undone, and its actions are applied
// before RunRule returns; what the server could not take now stays
// pending and is tried after a later sync. It returns the entries logged,
// as they ended. ErrRulePaused for a paused rule, ErrRuleRunTooLarge for
// more than MaxRuleRun targets.
func (s *Service) RunRule(ctx context.Context, id RuleID, targets []RuleTarget) ([]RuleActivity, error) {
	r, err := s.rule(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Paused {
		return nil, ErrRulePaused
	}
	if len(targets) > MaxRuleRun {
		return nil, ErrRuleRunTooLarge
	}
	type place struct{ account, folder string }
	asked := map[place]map[UID]uint32{}
	var order []place
	for _, t := range targets {
		p := place{t.Account, t.Folder}
		if asked[p] == nil {
			asked[p] = map[UID]uint32{}
			order = append(order, p)
		}
		asked[p][t.UID] = t.UIDValidity
	}
	byAccount := map[string][]RuleActivity{}
	var accounts []string
	now := s.now()
	for _, p := range order {
		f, ok, err := s.store.Folder(ctx, p.account, p.folder)
		if err != nil {
			return nil, err
		}
		if !ok || !r.watches(p.account, f) {
			continue
		}
		uids := make([]UID, 0, len(asked[p]))
		for uid, validity := range asked[p] {
			if validity == f.UIDValidity {
				uids = append(uids, uid)
			}
		}
		slices.Sort(uids)
		msgs, err := s.store.MessageHeaders(ctx, p.account, p.folder, uids)
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			if !r.matches(m, m.HasLabel) {
				continue
			}
			if byAccount[p.account] == nil {
				accounts = append(accounts, p.account)
			}
			e := newActivity(r, m, f.UIDValidity, now)
			e.Manual = true
			byAccount[p.account] = append(byAccount[p.account], e)
		}
	}
	var out []RuleActivity
	for _, account := range accounts {
		saved, err := s.store.AddRuleActivity(ctx, byAccount[account])
		if err != nil {
			return out, err
		}
		lock := s.ruleLock(account)
		lock.Lock()
		out = append(out, s.applyEntries(ctx, account, saved)...)
		lock.Unlock()
	}
	return out, nil
}
