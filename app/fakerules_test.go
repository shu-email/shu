package app

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/textproto"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"shu/mail"
)

// The fake's rules: checked as the engine's CheckRule checks them, in its
// order of checks; matched as its rules match a message, from the fake's
// cache alone; logged, applied and undone as its log is, with the fake's
// own SetLabel, SetFlags and MoveMessages, so that what a rule changes is
// recorded in acts as any change of mail is.

func (f *fakeService) ruleCall(format string, args ...any) {
	f.ruleCalls = append(f.ruleCalls, fmt.Sprintf(format, args...))
}

// fakeRuleAt is a RuleError at a condition or an action, -1 for neither.
func fakeRuleAt(err error, condition, action int) error {
	return &mail.RuleError{Err: err, Condition: condition, Action: action}
}

// fakeRuleText reports whether a rule can hold text: not empty, not too
// long, and without control characters.
func fakeRuleText(s string, most int) bool {
	s = strings.TrimSpace(s)
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= most && strings.IndexFunc(s, unicode.IsControl) < 0
}

// fakeHeaderName reports whether a condition can read a header of that
// name: letters, digits and - _ ., at most 76 of them.
func fakeHeaderName(name string) bool {
	if name == "" || len(name) > 76 {
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

func fakeTextField(field mail.ConditionField) bool {
	return field == mail.CondSender || field == mail.CondRecipients || field == mail.CondSubject || field == mail.CondHeader
}

func fakeAccountField(field mail.ConditionField) bool {
	return field == mail.CondAccount || field == mail.CondFolder || field == mail.CondLabel
}

func fakeAccountAction(kind mail.ActionKind) bool {
	return kind == mail.ActionMove || kind == mail.ActionAddLabel || kind == mail.ActionRemoveLabel
}

// cleanFakeRule is a rule as the engine checks and saves it.
func cleanFakeRule(r mail.Rule) mail.Rule {
	r.Name = strings.TrimSpace(r.Name)
	if r.Match == "" {
		r.Match = mail.MatchAll
	}
	r.Conditions = slices.Clone(r.Conditions)
	for i := range r.Conditions {
		c := &r.Conditions[i]
		c.Value = strings.TrimSpace(c.Value)
		if c.Header = strings.TrimSpace(c.Header); fakeHeaderName(c.Header) {
			c.Header = textproto.CanonicalMIMEHeaderKey(c.Header)
		}
	}
	r.Actions = slices.Clone(r.Actions)
	return r
}

// checkRule refuses what the engine refuses of a cleaned rule.
func (f *fakeService) checkRule(r mail.Rule) error {
	if !fakeRuleText(r.Name, mail.MaxRuleName) {
		return fakeRuleAt(mail.ErrRuleName, -1, -1)
	}
	if len(r.Conditions) == 0 || len(r.Actions) == 0 {
		return fakeRuleAt(mail.ErrRuleEmpty, -1, -1)
	}
	if r.Match != mail.MatchAll && r.Match != mail.MatchAny {
		return fakeRuleAt(mail.ErrRuleCondition, -1, -1)
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
		case !slices.Contains(mail.ConditionFields, c.Field):
			return fakeRuleAt(mail.ErrRuleCondition, i, -1)
		case fakeTextField(c.Field):
			if !fakeRuleText(c.Value, mail.MaxRuleValue) || (c.Field == mail.CondHeader && !fakeHeaderName(c.Header)) {
				return fakeRuleAt(mail.ErrRuleCondition, i, -1)
			}
		case fakeAccountField(c.Field):
			if c.Account == "" || (c.Field == mail.CondFolder && c.Folder == "") || (c.Field == mail.CondLabel && c.Label == 0) {
				return fakeRuleAt(mail.ErrRuleCondition, i, -1)
			}
			if !named(c.Account) {
				return fakeRuleAt(mail.ErrRuleAccount, i, -1)
			}
		}
	}
	type said struct {
		kind  mail.ActionKind
		label mail.LabelID
	}
	seen := map[said]bool{}
	for i, a := range r.Actions {
		if !a.Kind.Valid() {
			return fakeRuleAt(mail.ErrRuleAction, -1, i)
		}
		if fakeAccountAction(a.Kind) {
			if a.Account == "" || (a.Kind == mail.ActionMove && a.Folder == "") || (a.Kind != mail.ActionMove && a.Label == 0) {
				return fakeRuleAt(mail.ErrRuleAction, -1, i)
			}
			if !named(a.Account) {
				return fakeRuleAt(mail.ErrRuleAccount, -1, i)
			}
		}
		key, undone := said{kind: a.Kind}, said{}
		switch a.Kind {
		case mail.ActionAddLabel:
			key.label, undone = a.Label, said{mail.ActionRemoveLabel, a.Label}
		case mail.ActionRemoveLabel:
			key.label, undone = a.Label, said{mail.ActionAddLabel, a.Label}
		case mail.ActionMarkRead:
			undone = said{kind: mail.ActionMarkUnread}
		case mail.ActionMarkUnread:
			undone = said{kind: mail.ActionMarkRead}
		}
		if seen[key] || (undone.kind != "" && seen[undone]) {
			return fakeRuleAt(mail.ErrRuleAction, -1, i)
		}
		seen[key] = true
	}
	if account == "" {
		return nil
	}
	if _, ok := f.fakeAccount(account); !ok {
		for i, c := range r.Conditions {
			if fakeAccountField(c.Field) {
				return fakeRuleAt(mail.ErrRuleAccount, i, -1)
			}
		}
		for i, a := range r.Actions {
			if fakeAccountAction(a.Kind) {
				return fakeRuleAt(mail.ErrRuleAccount, -1, i)
			}
		}
	}
	folderOK := func(name string) bool {
		fl, ok := f.fakeFolder(account, name)
		return ok && fl.SpecialUse != mail.SpecialDrafts
	}
	labelOK := func(id mail.LabelID) bool { return f.labelPlace(account, id) >= 0 }
	for i, c := range r.Conditions {
		switch {
		case c.Field == mail.CondFolder && !folderOK(c.Folder):
			return fakeRuleAt(mail.ErrRuleFolder, i, -1)
		case c.Field == mail.CondLabel && !labelOK(c.Label):
			return fakeRuleAt(mail.ErrRuleLabel, i, -1)
		}
	}
	for i, a := range r.Actions {
		switch {
		case a.Kind == mail.ActionMove && !folderOK(a.Folder):
			return fakeRuleAt(mail.ErrRuleFolder, -1, i)
		case (a.Kind == mail.ActionAddLabel || a.Kind == mail.ActionRemoveLabel) && !labelOK(a.Label):
			return fakeRuleAt(mail.ErrRuleLabel, -1, i)
		}
	}
	return nil
}

// fakeContains reports whether text contains what a condition looks for,
// in any case.
func fakeContains(text, value string) bool {
	return value != "" && strings.Contains(mail.LabelNameKey(text), mail.LabelNameKey(value))
}

// fakeHeaderValues are the values of a header among a message's header
// lines, their encoded words decoded.
func fakeHeaderValues(raw, name string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		value = strings.TrimSpace(value)
		if decoded, err := new(mime.WordDecoder).DecodeHeader(value); err == nil {
			value = decoded
		}
		out = append(out, value)
	}
	return out
}

func fakeHolds(c mail.Condition, m mail.Summary, raw string, has func(mail.LabelID) bool) bool {
	switch c.Field {
	case mail.CondSender:
		return fakeContains(m.From.Name, c.Value) || fakeContains(m.From.Email, c.Value)
	case mail.CondRecipients:
		return slices.ContainsFunc(append(slices.Clone(m.To), m.Cc...), func(a mail.Address) bool {
			return fakeContains(a.Name, c.Value) || fakeContains(a.Email, c.Value)
		})
	case mail.CondSubject:
		return fakeContains(m.Subject, c.Value)
	case mail.CondHeader:
		return slices.ContainsFunc(fakeHeaderValues(raw, c.Header), func(v string) bool { return fakeContains(v, c.Value) })
	case mail.CondAttachment:
		return m.HasAttachments
	case mail.CondAccount:
		return m.Account == c.Account
	case mail.CondFolder:
		return m.Account == c.Account && m.Folder == c.Folder
	case mail.CondLabel:
		return m.Account == c.Account && has(c.Label)
	}
	return false
}

// fakeMatches reports whether a message meets a rule's conditions, as its
// mode combines them; a rule that names an account matches no other's.
func fakeMatches(r mail.Rule, m mail.Summary, raw string, has func(mail.LabelID) bool) bool {
	if a := r.Account(); (a != "" && m.Account != a) || len(r.Conditions) == 0 {
		return false
	}
	for _, c := range r.Conditions {
		held := fakeHolds(c, m, raw, has)
		if r.Match == mail.MatchAny && held {
			return true
		}
		if r.Match != mail.MatchAny && !held {
			return false
		}
	}
	return r.Match != mail.MatchAny
}

// fakeWatches reports whether a rule sorts the mail of an account's
// folder: the inbox, and the folders its conditions name, never Drafts.
func fakeWatches(r mail.Rule, account string, fl mail.Folder) bool {
	if a := r.Account(); (a != "" && a != account) || fl.SpecialUse == mail.SpecialDrafts {
		return false
	}
	if fl.IsInbox() {
		return true
	}
	return slices.ContainsFunc(r.Conditions, func(c mail.Condition) bool {
		return c.Field == mail.CondFolder && c.Account == account && c.Folder == fl.Name
	})
}

// fakeStops reports whether a rule ends the sorting of a message it
// matched: it says so, or moves the message.
func fakeStops(r mail.Rule) bool {
	return r.Stop || slices.ContainsFunc(r.Actions, func(a mail.Action) bool { return a.Kind == mail.ActionMove })
}

func cloneRule(r mail.Rule) mail.Rule {
	r.Conditions, r.Actions = slices.Clone(r.Conditions), slices.Clone(r.Actions)
	return r
}

func (f *fakeService) fakeRule(id mail.RuleID) (int, bool) {
	i := slices.IndexFunc(f.rules, func(r mail.Rule) bool { return r.ID == id })
	return i, i >= 0
}

func (f *fakeService) Rules(ctx context.Context) ([]mail.Rule, error) {
	if f.ruleErr != nil {
		return nil, f.ruleErr
	}
	out := make([]mail.Rule, 0, len(f.rules))
	for _, r := range f.rules {
		out = append(out, cloneRule(r))
	}
	return out, nil
}

func (f *fakeService) CheckRule(ctx context.Context, r mail.Rule) error {
	if f.ruleErr != nil {
		return f.ruleErr
	}
	return f.checkRule(cleanFakeRule(r))
}

func (f *fakeService) SaveRule(ctx context.Context, r mail.Rule) (mail.Rule, error) {
	f.ruleCall("SaveRule %s", r.Name)
	if f.ruleErr != nil {
		return mail.Rule{}, f.ruleErr
	}
	r = cleanFakeRule(r)
	if err := f.checkRule(r); err != nil {
		return mail.Rule{}, err
	}
	if r.ID == 0 {
		f.nextRule++
		r.ID = f.nextRule
		f.rules = append(f.rules, cloneRule(r))
		return r, nil
	}
	i, ok := f.fakeRule(r.ID)
	if !ok {
		return mail.Rule{}, mail.ErrNotFound
	}
	f.rules[i] = cloneRule(r)
	return r, nil
}

// addRule saves a rule as the user made it, failing the test's setup on
// a rule the engine would refuse.
func (f *fakeService) addRule(r mail.Rule) mail.Rule {
	saved, err := f.SaveRule(context.Background(), r)
	if err != nil {
		panic(err)
	}
	f.ruleCalls = nil
	return saved
}

func (f *fakeService) PauseRule(ctx context.Context, id mail.RuleID, paused bool) (mail.Rule, error) {
	f.ruleCall("PauseRule %d %v", id, paused)
	if f.ruleErr != nil {
		return mail.Rule{}, f.ruleErr
	}
	i, ok := f.fakeRule(id)
	if !ok {
		return mail.Rule{}, mail.ErrNotFound
	}
	f.rules[i].Paused = paused
	if paused {
		f.dropPending(id)
	}
	return cloneRule(f.rules[i]), nil
}

// dropPending marks undone a rule's pending entries that changed nothing
// yet, as pausing or deleting it does.
func (f *fakeService) dropPending(id mail.RuleID) {
	for i := range f.ruleLog {
		e := &f.ruleLog[i]
		if e.Rule == id && e.State == mail.ActivityPending && fakeUnchanged(e.Changes) {
			e.State, e.Error = mail.ActivityUndone, ""
		}
	}
}

func fakeUnchanged(c mail.RuleChanges) bool {
	return c.MovedTo == "" && len(c.Flags) == 0 && len(c.LabelsAdded) == 0 && len(c.LabelsRemoved) == 0
}

func (f *fakeService) DeleteRule(ctx context.Context, id mail.RuleID) error {
	f.ruleCall("DeleteRule %d", id)
	if f.ruleErr != nil {
		return f.ruleErr
	}
	i, ok := f.fakeRule(id)
	if !ok {
		return mail.ErrNotFound
	}
	f.rules = slices.Delete(f.rules, i, i+1)
	f.dropPending(id)
	return nil
}

func (f *fakeService) MoveRule(ctx context.Context, id mail.RuleID, index int) error {
	f.ruleCall("MoveRule %d %d", id, index)
	if f.ruleErr != nil {
		return f.ruleErr
	}
	i, ok := f.fakeRule(id)
	if !ok {
		return mail.ErrNotFound
	}
	r := f.rules[i]
	f.rules = slices.Delete(f.rules, i, i+1)
	f.rules = slices.Insert(f.rules, min(max(index, 0), len(f.rules)), r)
	return nil
}

// ruleAccounts are the accounts whose mail a rule sorts, in the order of
// their IDs.
func (f *fakeService) ruleAccounts(r mail.Rule) []string {
	if a := r.Account(); a != "" {
		return []string{a}
	}
	var out []string
	for _, a := range f.accounts {
		out = append(out, a.ID)
	}
	sort.Strings(out)
	return out
}

// fakeNewest orders messages newest first, then by account, folder and
// UID, as the engine's preview does.
func fakeNewest(list []mail.Summary) {
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

func (f *fakeService) PreviewRule(ctx context.Context, r mail.Rule, offset, limit int) (mail.RulePreview, error) {
	if f.ruleErr != nil {
		return mail.RulePreview{}, f.ruleErr
	}
	r = cleanFakeRule(r)
	if err := f.checkRule(r); err != nil {
		return mail.RulePreview{}, err
	}
	var found []mail.Summary
	validity := map[string]uint32{}
	for _, account := range f.ruleAccounts(r) {
		for _, fl := range f.folders[account] {
			if !fakeWatches(r, account, fl) {
				continue
			}
			validity[account+"\x00"+fl.Name] = fl.UIDValidity
			for _, m := range f.messages[listKey(account, fl.Name)] {
				m.Account = account
				m = f.withLabels(account, m)
				if fakeMatches(r, m, f.rawHeaders[bodyKey(account, fl.Name, m.UID)], m.HasLabel) {
					found = append(found, m)
				}
			}
		}
	}
	fakeNewest(found)
	if limit <= 0 {
		limit = mail.DefaultPageSize
	}
	offset = min(max(offset, 0), len(found))
	out := mail.RulePreview{Total: len(found), Messages: []mail.Summary{}, Targets: make([]mail.RuleTarget, 0, len(found))}
	for i, m := range found {
		out.Targets = append(out.Targets, mail.RuleTarget{
			Account: m.Account, Folder: m.Folder, UIDValidity: validity[m.Account+"\x00"+m.Folder], UID: m.UID,
		})
		if i >= offset && i < offset+limit {
			out.Messages = append(out.Messages, m)
		}
	}
	return out, nil
}

// fakeEntry is the entry of a rule's match of a message, pending unless
// all it does is keep the message quiet.
func (f *fakeService) fakeEntry(r mail.Rule, m mail.Summary, validity uint32) mail.RuleActivity {
	quiet := slices.ContainsFunc(r.Actions, func(a mail.Action) bool {
		return a.Kind == mail.ActionSkipNotification || a.Kind == mail.ActionMove || a.Kind == mail.ActionMarkRead
	})
	e := mail.RuleActivity{
		At: f.now(), Rule: r.ID, RuleName: r.Name, Account: m.Account, Folder: m.Folder, UIDValidity: validity, UID: m.UID,
		MessageID: m.MessageID, Key: mail.LabelKey(m), Subject: m.Subject, From: m.From,
		Actions: slices.Clone(r.Actions), Quiet: quiet, State: mail.ActivityApplied,
	}
	if slices.ContainsFunc(r.Actions, func(a mail.Action) bool { return a.Kind != mail.ActionSkipNotification }) {
		e.State = mail.ActivityPending
	}
	return e
}

// logEntries writes entries to the log and returns them with their IDs.
func (f *fakeService) logEntries(entries []mail.RuleActivity) []mail.RuleActivity {
	for i := range entries {
		f.nextEntry++
		entries[i].ID = f.nextEntry
		f.ruleLog = append(f.ruleLog, entries[i])
	}
	return entries
}

// entry is the log's entry of an ID.
func (f *fakeService) entry(id mail.ActivityID) *mail.RuleActivity {
	for i := range f.ruleLog {
		if f.ruleLog[i].ID == id {
			return &f.ruleLog[i]
		}
	}
	return nil
}

// applyEntry applies the actions of a pending entry from what the cache
// says of its message, labels and flags before the move, recording what
// each changed; runWaits leaves it pending, as a server out of reach.
func (f *fakeService) applyEntry(e *mail.RuleActivity) {
	if e.State != mail.ActivityPending {
		return
	}
	if f.runWaits != nil {
		e.Error = f.runWaits.Error()
		return
	}
	m, ok := f.find(e.Account, e.Folder, e.UID)
	if !ok {
		e.State, e.Error = mail.ActivityFailed, mail.ErrRuleMessageGone.Error()
		return
	}
	ctx := context.Background()
	refs := []mail.MessageRef{{Folder: e.Folder, UID: e.UID}}
	var failed error
	fail := func(err error) {
		if failed == nil {
			failed = err
		}
	}
	flag := func(fl mail.Flag, on bool) {
		if err := f.SetFlags(ctx, e.Account, refs, fl, on); err != nil {
			fail(err)
			return
		}
		e.Changes.Flags = append(e.Changes.Flags, mail.FlagChange{Flag: fl, Was: !on})
	}
	for _, a := range e.Actions {
		switch {
		case a.Kind == mail.ActionAddLabel && !m.HasLabel(a.Label):
			if err := f.SetLabel(ctx, e.Account, refs, a.Label, true); err != nil {
				fail(err)
			} else {
				e.Changes.LabelsAdded = append(e.Changes.LabelsAdded, a.Label)
			}
		case a.Kind == mail.ActionRemoveLabel && m.HasLabel(a.Label):
			if err := f.SetLabel(ctx, e.Account, refs, a.Label, false); err != nil {
				fail(err)
			} else {
				e.Changes.LabelsRemoved = append(e.Changes.LabelsRemoved, a.Label)
			}
		case a.Kind == mail.ActionStar && !m.Flagged:
			flag(mail.FlagFlagged, true)
		case a.Kind == mail.ActionMarkRead && !m.Seen:
			flag(mail.FlagSeen, true)
		case a.Kind == mail.ActionMarkUnread && m.Seen:
			flag(mail.FlagSeen, false)
		}
	}
	for _, a := range e.Actions {
		if a.Kind != mail.ActionMove || a.Folder == e.Folder {
			continue
		}
		moved, err := f.MoveMessages(ctx, e.Account, refs, a.Folder)
		switch {
		case err != nil:
			fail(err)
		case len(moved) == 1:
			e.Changes.MovedTo, e.Changes.DestUID = a.Folder, moved[0].DestUID
		}
	}
	if failed != nil {
		e.State, e.Error = mail.ActivityFailed, failed.Error()
		return
	}
	e.State, e.Error = mail.ActivityApplied, ""
}

func (f *fakeService) RunRule(ctx context.Context, id mail.RuleID, targets []mail.RuleTarget) ([]mail.RuleActivity, error) {
	f.ruleCall("RunRule %d %d", id, len(targets))
	if f.ruleErr != nil {
		return nil, f.ruleErr
	}
	i, ok := f.fakeRule(id)
	if !ok {
		return nil, mail.ErrNotFound
	}
	r := f.rules[i]
	if r.Paused {
		return nil, mail.ErrRulePaused
	}
	if len(targets) > mail.MaxRuleRun {
		return nil, mail.ErrRuleRunTooLarge
	}
	// As the engine: folder by folder in the order the targets name them,
	// and in each the messages oldest first.
	type place struct{ account, folder string }
	var order []place
	byPlace := map[place][]mail.RuleTarget{}
	for _, t := range targets {
		p := place{t.Account, t.Folder}
		if byPlace[p] == nil {
			order = append(order, p)
		}
		if !slices.Contains(byPlace[p], t) {
			byPlace[p] = append(byPlace[p], t)
		}
	}
	targets = targets[:0:0]
	for _, p := range order {
		slices.SortFunc(byPlace[p], func(x, y mail.RuleTarget) int { return int(x.UID) - int(y.UID) })
		targets = append(targets, byPlace[p]...)
	}
	var entries []mail.RuleActivity
	for _, t := range targets {
		fl, ok := f.fakeFolder(t.Account, t.Folder)
		if !ok || fl.UIDValidity != t.UIDValidity || !fakeWatches(r, t.Account, fl) {
			continue
		}
		m, ok := f.find(t.Account, t.Folder, t.UID)
		if !ok || !fakeMatches(r, m, f.rawHeaders[bodyKey(t.Account, t.Folder, t.UID)], m.HasLabel) {
			continue
		}
		e := f.fakeEntry(r, m, fl.UIDValidity)
		e.Manual = true
		entries = append(entries, e)
	}
	entries = f.logEntries(entries)
	out := make([]mail.RuleActivity, 0, len(entries))
	for _, e := range entries {
		logged := f.entry(e.ID)
		f.applyEntry(logged)
		out = append(out, *logged)
	}
	return out, nil
}

func (f *fakeService) RuleActivity(ctx context.Context, q mail.ActivityQuery) (mail.ActivityPage, error) {
	if f.ruleErr != nil {
		return mail.ActivityPage{}, f.ruleErr
	}
	found := []mail.RuleActivity{}
	for _, e := range slices.Backward(f.ruleLog) {
		switch {
		case q.ID != 0 && e.ID != q.ID, q.Rule != 0 && e.Rule != q.Rule, q.Account != "" && e.Account != q.Account,
			q.State != "" && e.State != q.State, q.Folder != "" && e.Folder != q.Folder,
			q.UIDValidity != 0 && e.UIDValidity != q.UIDValidity, len(q.UIDs) > 0 && !slices.Contains(q.UIDs, e.UID):
			continue
		}
		found = append(found, e)
	}
	total := len(found)
	found = found[min(max(q.Offset, 0), total):]
	if q.Limit > 0 && len(found) > q.Limit {
		found = found[:q.Limit]
	}
	return mail.ActivityPage{Entries: slices.Clone(found), Total: total}, nil
}

// undoEntry puts back what an entry changed and marks it undone: a move
// goes back when the message is where the rule left it, then its flags
// and labels, on the message the entry's key names.
func (f *fakeService) undoEntry(e *mail.RuleActivity) error {
	if e.State == mail.ActivityUndone {
		return nil
	}
	ctx := context.Background()
	c := e.Changes
	if c.MovedTo != "" && !c.MovedBack {
		m, ok := f.find(e.Account, c.MovedTo, c.DestUID)
		if !ok || mail.LabelKey(m) != e.Key {
			e.Error = mail.ErrRuleMessageGone.Error()
			return mail.ErrRuleMessageGone
		}
		f.act(e.Account, "MoveBack %s/%d->%s", c.MovedTo, c.DestUID, e.Folder)
		f.moveOne(e.Account, c.MovedTo, c.DestUID, e.Folder, e.UID)
		e.Changes.MovedBack, e.Changes.BackUID = true, e.UID
	}
	if len(c.Flags) > 0 || len(c.LabelsAdded) > 0 || len(c.LabelsRemoved) > 0 {
		m, ok := f.find(e.Account, e.Folder, e.UID)
		if !ok || mail.LabelKey(m) != e.Key {
			e.Error = mail.ErrRuleMessageGone.Error()
			return mail.ErrRuleMessageGone
		}
		refs := []mail.MessageRef{{Folder: e.Folder, UID: e.UID}}
		for _, fc := range c.Flags {
			if err := f.SetFlags(ctx, e.Account, refs, fc.Flag, fc.Was); err != nil {
				e.Error = err.Error()
				return err
			}
		}
		// A label gone since took with it what there was to put back.
		for _, l := range c.LabelsAdded {
			if err := f.SetLabel(ctx, e.Account, refs, l, false); err != nil && !errors.Is(err, mail.ErrNotFound) {
				e.Error = err.Error()
				return err
			}
		}
		for _, l := range c.LabelsRemoved {
			if err := f.SetLabel(ctx, e.Account, refs, l, true); err != nil && !errors.Is(err, mail.ErrNotFound) {
				e.Error = err.Error()
				return err
			}
		}
	}
	e.State, e.Error = mail.ActivityUndone, ""
	return nil
}

func (f *fakeService) UndoActivity(ctx context.Context, id mail.ActivityID) error {
	f.ruleCall("UndoActivity %d", id)
	if f.undoErr != nil {
		return f.undoErr
	}
	e := f.entry(id)
	if e == nil {
		return mail.ErrNotFound
	}
	return f.undoEntry(e)
}

func (f *fakeService) UndoRule(ctx context.Context, id mail.RuleID) (int, error) {
	f.ruleCall("UndoRule %d", id)
	if f.undoErr != nil {
		return 0, f.undoErr
	}
	undone := 0
	var first error
	for i := len(f.ruleLog) - 1; i >= 0; i-- {
		e := &f.ruleLog[i]
		if e.Rule != id || e.State == mail.ActivityUndone {
			continue
		}
		if err := f.undoEntry(e); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		undone++
	}
	return undone, first
}

func (f *fakeService) OnRuleActivity(report func(accountID string)) { f.ruleReport = report }

// arrive has messages arrive in an account's folder, as a sync of the
// mail above its cursor brings them: the rules that watch the folder sort
// them, from the cache, in order, each match logged pending. It returns
// the entries, which applyPending then applies.
func (f *fakeService) arrive(account, folder string, msgs ...mail.Summary) []mail.RuleActivity {
	key := listKey(account, folder)
	for _, m := range msgs {
		m.Account, m.Folder = account, folder
		f.messages[key] = append(f.messages[key], m)
	}
	sortSummaries(f.messages[key])
	f.recount(account, folder)
	fl, _ := f.fakeFolder(account, folder)
	slices.SortFunc(msgs, func(x, y mail.Summary) int { return int(x.UID) - int(y.UID) })
	var entries []mail.RuleActivity
	for _, m := range msgs {
		m, _ = f.find(account, folder, m.UID)
		labels := map[mail.LabelID]bool{}
		for _, l := range m.Labels {
			labels[l.ID] = true
		}
		has := func(id mail.LabelID) bool { return labels[id] }
		for _, r := range f.rules {
			if r.Paused || f.checkRule(r) != nil || !fakeWatches(r, account, fl) ||
				!fakeMatches(r, m, f.rawHeaders[bodyKey(account, folder, m.UID)], has) {
				continue
			}
			entries = append(entries, f.fakeEntry(r, m, fl.UIDValidity))
			for _, a := range r.Actions {
				switch a.Kind {
				case mail.ActionAddLabel:
					labels[a.Label] = true
				case mail.ActionRemoveLabel:
					delete(labels, a.Label)
				}
			}
			if fakeStops(r) {
				break
			}
		}
	}
	return f.logEntries(entries)
}

// applyPending applies an account's pending entries of enabled rules,
// oldest first, as the engine's goroutine does, and then tells the window
// as the engine does.
func (f *fakeService) applyPending(account string) {
	enabled := map[mail.RuleID]bool{}
	for _, r := range f.rules {
		enabled[r.ID] = !r.Paused
	}
	for i := range f.ruleLog {
		if e := &f.ruleLog[i]; e.Account == account && enabled[e.Rule] {
			f.applyEntry(e)
		}
	}
	if f.ruleReport != nil {
		f.ruleReport(account)
	}
}

// fakeRuleClock is the time the rules' tests log their entries at.
var fakeRuleClock = time.Date(2026, 3, 2, 10, 30, 0, 0, time.UTC)
