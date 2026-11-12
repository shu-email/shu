package mail

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

// moveNews is a rule that moves the mail of a sender to a folder of a1.
func moveNews(folder string) Rule {
	return Rule{
		Name:       "Newsletters",
		Conditions: []Condition{{Field: CondSender, Value: "news@shop.example"}},
		Actions:    []Action{{Kind: ActionMove, Account: "a1", Folder: folder}},
	}
}

// What a rule may hold can be told from the rule alone: a name, a
// condition and an action at least, each of the set with what it needs,
// actions that neither repeat nor undo each other, and one account.
func TestRuleShape(t *testing.T) {
	long := strings.Repeat("x", MaxRuleValue+1)
	cond := func(c ...Condition) Rule {
		return Rule{Name: "R", Match: MatchAll, Conditions: c, Actions: []Action{{Kind: ActionStar}}}
	}
	act := func(a ...Action) Rule {
		return Rule{Name: "R", Match: MatchAll, Conditions: []Condition{{Field: CondAttachment}}, Actions: a}
	}
	for _, tc := range []struct {
		name string
		rule Rule
		err  error
		// at is the place of the condition, or of the action, at fault.
		cond, action int
	}{
		{"a rule that sorts", moveNews("News").clean(), nil, -1, -1},
		{"every condition", cond(
			Condition{Field: CondSender, Value: "a"}, Condition{Field: CondRecipients, Value: "b"},
			Condition{Field: CondSubject, Value: "c"}, Condition{Field: CondHeader, Header: "List-Id", Value: "d"},
			Condition{Field: CondAttachment}, Condition{Field: CondAccount, Account: "a1"},
			Condition{Field: CondFolder, Account: "a1", Folder: "Lists"}, Condition{Field: CondLabel, Account: "a1", Label: 3},
		), nil, -1, -1},
		{"every action", act(
			Action{Kind: ActionMove, Account: "a1", Folder: "News"}, Action{Kind: ActionAddLabel, Account: "a1", Label: 1},
			Action{Kind: ActionRemoveLabel, Account: "a1", Label: 2}, Action{Kind: ActionStar},
			Action{Kind: ActionMarkRead}, Action{Kind: ActionSkipNotification},
		), nil, -1, -1},
		{"no name", Rule{Name: " ", Match: MatchAll, Conditions: moveNews("N").Conditions, Actions: moveNews("N").Actions}, ErrRuleName, -1, -1},
		{"a name too long", Rule{Name: strings.Repeat("é", MaxRuleName+1), Match: MatchAll}, ErrRuleName, -1, -1},
		{"a control character in the name", Rule{Name: "a\nb", Match: MatchAll}, ErrRuleName, -1, -1},
		{"no condition", act(Action{Kind: ActionStar}).withConditions(), ErrRuleEmpty, -1, -1},
		{"no action", act(), ErrRuleEmpty, -1, -1},
		{"a mode that is none", Rule{Name: "R", Match: "most", Conditions: []Condition{{Field: CondAttachment}}, Actions: []Action{{Kind: ActionStar}}}, ErrRuleCondition, -1, -1},
		{"a field that is none", cond(Condition{Field: CondAttachment}, Condition{Field: "body", Value: "x"}), ErrRuleCondition, 1, -1},
		{"nothing to look for", cond(Condition{Field: CondSubject, Value: "  "}), ErrRuleCondition, 0, -1},
		{"too much to look for", cond(Condition{Field: CondSender, Value: long}), ErrRuleCondition, 0, -1},
		{"a header without a name", cond(Condition{Field: CondHeader, Value: "x"}), ErrRuleCondition, 0, -1},
		{"a header name with a space", cond(Condition{Field: CondHeader, Header: "List Id", Value: "x"}), ErrRuleCondition, 0, -1},
		{"a header name that ends a line", cond(Condition{Field: CondHeader, Header: "X-A\r\nX-B", Value: "x"}), ErrRuleCondition, 0, -1},
		{"a header name with a parenthesis", cond(Condition{Field: CondHeader, Header: "X-A)", Value: "x"}), ErrRuleCondition, 0, -1},
		{"a folder of no account", cond(Condition{Field: CondFolder, Folder: "Lists"}), ErrRuleCondition, 0, -1},
		{"a label that is none", cond(Condition{Field: CondLabel, Account: "a1"}), ErrRuleCondition, 0, -1},
		{"a move to nowhere", act(Action{Kind: ActionMove, Account: "a1"}), ErrRuleAction, -1, 0},
		{"a label action without a label", act(Action{Kind: ActionAddLabel, Account: "a1"}), ErrRuleAction, -1, 0},
		{"two moves", act(Action{Kind: ActionMove, Account: "a1", Folder: "A"}, Action{Kind: ActionMove, Account: "a1", Folder: "B"}), ErrRuleAction, -1, 1},
		{"read and unread", act(Action{Kind: ActionMarkRead}, Action{Kind: ActionMarkUnread}), ErrRuleAction, -1, 1},
		{"a label on and off", act(Action{Kind: ActionAddLabel, Account: "a1", Label: 1}, Action{Kind: ActionRemoveLabel, Account: "a1", Label: 1}), ErrRuleAction, -1, 1},
		{"a star twice", act(Action{Kind: ActionStar}, Action{Kind: ActionStar}), ErrRuleAction, -1, 1},
		{"a star twice with a stray label", act(Action{Kind: ActionStar}, Action{Kind: ActionStar, Label: 4}), ErrRuleAction, -1, 1},
		{"two accounts", Rule{Name: "R", Match: MatchAny,
			Conditions: []Condition{{Field: CondAccount, Account: "a1"}},
			Actions:    []Action{{Kind: ActionMove, Account: "a2", Folder: "News"}}}, ErrRuleAccount, -1, 0},
		{"two accounts in conditions", cond(Condition{Field: CondAccount, Account: "a1"}, Condition{Field: CondLabel, Account: "a2", Label: 1}), ErrRuleAccount, 1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkShape(tc.rule)
			if tc.err == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
			if err.Condition != tc.cond || err.Action != tc.action {
				t.Errorf("at condition %d, action %d; want %d, %d", err.Condition, err.Action, tc.cond, tc.action)
			}
		})
	}
}

// withConditions returns the rule with these conditions instead.
func (r Rule) withConditions(c ...Condition) Rule {
	r.Conditions = c
	return r
}

// The actions are a closed set: none sends, forwards, replies or deletes,
// and a rule that says one, as a database written by other means might
// hold, is refused and sorts nothing.
func TestRuleActionsAreAClosedSet(t *testing.T) {
	want := []ActionKind{ActionMove, ActionAddLabel, ActionRemoveLabel, ActionStar, ActionMarkRead, ActionMarkUnread, ActionSkipNotification}
	if !slices.Equal(ActionKinds, want) {
		t.Fatalf("the actions are %v", ActionKinds)
	}
	for _, kind := range []ActionKind{"send", "forward", "redirect", "reply", "delete", "deletePermanently", "expunge", "flag", ""} {
		if kind.Valid() {
			t.Errorf("%q is an action", kind)
		}
		var r Rule
		raw := `{"name":"Bad","match":"all","conditions":[{"field":"attachment"}],"actions":[{"kind":"` + string(kind) + `","folder":"Trash"}]}`
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		if err := checkShape(r); err == nil || !errors.Is(err, ErrRuleAction) {
			t.Errorf("a rule that would %q: %v", kind, err)
		}
	}

	ctx := context.Background()
	svc, store, _ := newTestService()
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	bad := Rule{Name: "Forward", Match: MatchAll, Conditions: []Condition{{Field: CondAttachment}}, Actions: []Action{{Kind: "forward"}}}
	if _, err := svc.SaveRule(ctx, bad); !errors.Is(err, ErrRuleAction) {
		t.Fatalf("saved a rule that forwards: %v", err)
	}
	// One that reached the database anyway is no rule that sorts.
	if _, err := store.SaveRule(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if rules, err := svc.sortingRules(ctx); err != nil || len(rules) != 0 {
		t.Errorf("rules that sort = %+v, %v", rules, err)
	}
}

// What can be told only from the cache: that the account a rule names is
// there with the folders and labels it names, none of them its Drafts.
func TestCheckRuleAgainstTheCache(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	for _, a := range []Account{testAccount(), {ID: "a2", Name: "Bo", Email: "bo@example.org", Username: "bo",
		IMAP: Server{Host: "imap.example.org", Port: 993, TLS: TLSImplicit}, SMTP: Server{Host: "smtp.example.org", Port: 465, TLS: TLSImplicit}}} {
		if err := svc.AddAccount(ctx, a, "secret"); err != nil {
			t.Fatal(err)
		}
	}
	id := testAccount().ID
	if err := store.SaveFolders(ctx, id, []Folder{{Name: "INBOX", SpecialUse: SpecialInbox}, {Name: "News"}, {Name: "Drafts", SpecialUse: SpecialDrafts}}); err != nil {
		t.Fatal(err)
	}
	mine, err := svc.CreateLabel(ctx, id, "Mine", "")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := svc.CreateLabel(ctx, "a2", "Theirs", "")
	if err != nil {
		t.Fatal(err)
	}
	base := Rule{Name: " Sort ", Conditions: []Condition{{Field: CondSubject, Value: " sale "}}}
	for _, tc := range []struct {
		name    string
		actions []Action
		conds   []Condition
		err     error
	}{
		{"a move to a folder there is", []Action{{Kind: ActionMove, Account: id, Folder: "News"}}, nil, nil},
		{"a label of the account", []Action{{Kind: ActionAddLabel, Account: id, Label: mine.ID}}, nil, nil},
		{"a move to a folder there is not", []Action{{Kind: ActionMove, Account: id, Folder: "Nowhere"}}, nil, ErrRuleFolder},
		{"a move to the drafts", []Action{{Kind: ActionMove, Account: id, Folder: "Drafts"}}, nil, ErrRuleFolder},
		{"the drafts watched", []Action{{Kind: ActionStar}}, []Condition{{Field: CondFolder, Account: id, Folder: "Drafts"}}, ErrRuleFolder},
		{"a label of another account", []Action{{Kind: ActionAddLabel, Account: id, Label: theirs.ID}}, nil, ErrRuleLabel},
		{"a label condition of another account", []Action{{Kind: ActionStar}}, []Condition{{Field: CondLabel, Account: id, Label: theirs.ID}}, ErrRuleLabel},
		{"an account there is not", []Action{{Kind: ActionMove, Account: "gone", Folder: "News"}}, nil, ErrRuleAccount},
	} {
		r := base
		r.Actions = tc.actions
		r.Conditions = append(slices.Clone(base.Conditions), tc.conds...)
		err := svc.CheckRule(ctx, r)
		if tc.err == nil {
			if err != nil {
				t.Errorf("%s: refused: %v", tc.name, err)
			}
			continue
		}
		var at *RuleError
		if !errors.Is(err, tc.err) || !errors.As(err, &at) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.err)
			continue
		}
		if _, err := svc.SaveRule(ctx, r); !errors.Is(err, tc.err) {
			t.Errorf("%s: saved: %v", tc.name, err)
		}
	}
	saved, err := svc.SaveRule(ctx, Rule{Name: " Sort ", Conditions: []Condition{{Field: CondHeader, Header: "list-id", Value: " shop "}},
		Actions: []Action{{Kind: ActionMove, Account: id, Folder: "News"}}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 || saved.Name != "Sort" || saved.Match != MatchAll || saved.Conditions[0].Value != "shop" ||
		saved.Conditions[0].Header != "List-Id" || saved.Account() != id {
		t.Errorf("saved %+v", saved)
	}
	if rules, _ := svc.Rules(ctx); len(rules) != 1 || rules[0].ID != saved.ID {
		t.Errorf("rules = %+v", rules)
	}
}

// ruleMessage is a message of account a1's INBOX for the tests of
// conditions.
func ruleMessage() Message {
	return Message{
		Summary: Summary{
			Account: "a1", Folder: "INBOX", UID: 7, Subject: "Ihr Rabatt: 20 % auf ALLES",
			From:   Address{Name: "Ünïcode Shop", Email: "News@Shop.Example"},
			To:     []Address{{Name: "Ann", Email: "ann@example.com"}},
			Cc:     []Address{{Name: "Team Lead", Email: "lead@example.com"}},
			Labels: []MessageLabel{{ID: 3}}, HasAttachments: true,
		},
		RawHeaders: "Delivered-To: ann@example.com\r\nList-Id: =?utf-8?q?Caf=C3=A9_News?= <news.shop.example>\r\nX-Tag: one\r\nX-Tag: two\r\n",
	}
}

// Each condition, in any case and of each part it looks at.
func TestConditionsMatch(t *testing.T) {
	m := ruleMessage()
	for _, tc := range []struct {
		c    Condition
		want bool
	}{
		{Condition{Field: CondSender, Value: "news@shop"}, true},
		{Condition{Field: CondSender, Value: "ÜNÏCODE"}, true},
		{Condition{Field: CondSender, Value: "shop <news"}, false},
		{Condition{Field: CondSender, Value: "ann@"}, false},
		{Condition{Field: CondRecipients, Value: "ANN@EXAMPLE"}, true},
		{Condition{Field: CondRecipients, Value: "team lead"}, true},
		{Condition{Field: CondRecipients, Value: "shop"}, false},
		{Condition{Field: CondSubject, Value: "rabatt: 20"}, true},
		{Condition{Field: CondSubject, Value: "rabatt 20"}, false},
		{Condition{Field: CondHeader, Header: "List-Id", Value: "café news"}, true},
		{Condition{Field: CondHeader, Header: "list-id", Value: "NEWS.SHOP.EXAMPLE"}, true},
		{Condition{Field: CondHeader, Header: "X-Tag", Value: "two"}, true},
		{Condition{Field: CondHeader, Header: "X-Tag", Value: "three"}, false},
		{Condition{Field: CondHeader, Header: "Delivered-To", Value: "ann@"}, true},
		{Condition{Field: CondHeader, Header: "Precedence", Value: "bulk"}, false},
		{Condition{Field: CondAttachment}, true},
		{Condition{Field: CondAccount, Account: "a1"}, true},
		{Condition{Field: CondAccount, Account: "a2"}, false},
		{Condition{Field: CondFolder, Account: "a1", Folder: "INBOX"}, true},
		{Condition{Field: CondFolder, Account: "a1", Folder: "Lists"}, false},
		{Condition{Field: CondFolder, Account: "a2", Folder: "INBOX"}, false},
		{Condition{Field: CondLabel, Account: "a1", Label: 3}, true},
		{Condition{Field: CondLabel, Account: "a1", Label: 4}, false},
	} {
		if got := tc.c.holds(m, m.HasLabel); got != tc.want {
			t.Errorf("%+v holds: %v, want %v", tc.c, got, tc.want)
		}
	}
	plain := m
	plain.HasAttachments = false
	if (Condition{Field: CondAttachment}).holds(plain, plain.HasLabel) {
		t.Error("a message without attachments has some")
	}
}

// All needs every condition and any one of them; a rule that names an
// account matches no other account's message, whatever its mode.
func TestRuleMatchModes(t *testing.T) {
	m := ruleMessage()
	yes, no := Condition{Field: CondSubject, Value: "rabatt"}, Condition{Field: CondSubject, Value: "invoice"}
	for _, tc := range []struct {
		match MatchMode
		conds []Condition
		want  bool
	}{
		{MatchAll, []Condition{yes, yes}, true},
		{MatchAll, []Condition{yes, no}, false},
		{MatchAny, []Condition{no, yes}, true},
		{MatchAny, []Condition{no, no}, false},
		{MatchAll, nil, false},
		{MatchAny, nil, false},
	} {
		r := Rule{Name: "R", Match: tc.match, Conditions: tc.conds, Actions: []Action{{Kind: ActionStar}}}
		if got := r.matches(m, m.HasLabel); got != tc.want {
			t.Errorf("%s of %+v: %v, want %v", tc.match, tc.conds, got, tc.want)
		}
	}
	other := Rule{Name: "R", Match: MatchAny, Conditions: []Condition{yes}, Actions: []Action{{Kind: ActionMove, Account: "a2", Folder: "News"}}}
	if other.matches(m, m.HasLabel) {
		t.Error("a rule of another account matched")
	}
}

// Rules are tried in order. A move ends the sorting of a message, as does
// a rule that says to stop; a paused rule, or one that does not watch the
// folder, is passed over; a label a rule puts on counts for the rules
// after it, and one it takes off does not.
func TestSortMessageInOrder(t *testing.T) {
	m := ruleMessage()
	inbox := Folder{Name: "INBOX", SpecialUse: SpecialInbox}
	sale := []Condition{{Field: CondSubject, Value: "rabatt"}}
	rule := func(id RuleID, conds []Condition, actions ...Action) Rule {
		return Rule{ID: id, Name: "R", Match: MatchAll, Conditions: conds, Actions: actions}
	}
	ids := func(list []Rule) []RuleID {
		var out []RuleID
		for _, r := range list {
			out = append(out, r.ID)
		}
		return out
	}
	star := Action{Kind: ActionStar}
	move := Action{Kind: ActionMove, Account: "a1", Folder: "News"}
	labelled := []Condition{{Field: CondLabel, Account: "a1", Label: 9}}
	for _, tc := range []struct {
		name   string
		rules  []Rule
		folder Folder
		want   []RuleID
	}{
		{"all that match, in order", []Rule{rule(1, sale, star), rule(2, sale, Action{Kind: ActionMarkRead})}, inbox, []RuleID{1, 2}},
		{"a move ends it", []Rule{rule(1, sale, move), rule(2, sale, star)}, inbox, []RuleID{1}},
		{"stop ends it", []Rule{func() Rule { r := rule(1, sale, star); r.Stop = true; return r }(), rule(2, sale, star)}, inbox, []RuleID{1}},
		{"a rule that does not match goes on", []Rule{rule(1, []Condition{{Field: CondSubject, Value: "zzz"}}, move), rule(2, sale, star)}, inbox, []RuleID{2}},
		{"a paused rule is passed over", []Rule{func() Rule { r := rule(1, sale, move); r.Paused = true; return r }(), rule(2, sale, star)}, inbox, []RuleID{2}},
		{"another folder than the inbox is not watched", []Rule{rule(1, sale, star)}, Folder{Name: "Lists"}, nil},
		{"a folder a condition names is", []Rule{rule(1, []Condition{{Field: CondFolder, Account: "a1", Folder: "Lists"}}, star)}, Folder{Name: "Lists"}, []RuleID{1}},
		{"a label put on counts after", []Rule{rule(1, sale, Action{Kind: ActionAddLabel, Account: "a1", Label: 9}), rule(2, labelled, star)}, inbox, []RuleID{1, 2}},
		{"a label taken off counts after", []Rule{rule(1, sale, Action{Kind: ActionRemoveLabel, Account: "a1", Label: 3}),
			rule(2, []Condition{{Field: CondLabel, Account: "a1", Label: 3}}, star)}, inbox, []RuleID{1}},
	} {
		msg := m
		msg.Folder = tc.folder.Name
		if got := ids(sortMessage(tc.rules, msg, tc.folder)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: matched %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A header's lines are kept unfolded, one each, in valid UTF-8 and cut to
// a line's length, whatever case the rule names it in.
func TestHeaderLines(t *testing.T) {
	raw := "List-Id: Shop\r\n News <news.shop.example>\r\nX-Bin: caf\xe9\r\nx-tag: one\r\nX-Tag: two\r\nX-Long: " + strings.Repeat("é", 600) + "\r\n"
	got := headerLines([]byte(raw), []string{"list-id", "X-Bin", "X-Tag", "X-Long", "X-None"})
	lines := strings.Split(strings.TrimSuffix(got, "\r\n"), "\r\n")
	if len(lines) != 5 || lines[0] != "List-Id: Shop News <news.shop.example>" || lines[1] != "X-Bin: caf\uFFFD" ||
		lines[2] != "X-Tag: one" || lines[3] != "X-Tag: two" {
		t.Fatalf("lines = %q", lines)
	}
	if long := lines[4]; len(long) > len("X-Long: ")+maxHeaderLine || !strings.HasPrefix(long, "X-Long: éé") || !strings.HasSuffix(long, "é") {
		t.Errorf("the long line is %d bytes: %q…", len(long), long[:20])
	}
	if got := headerValues(got, "x-tag"); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("X-Tag = %v", got)
	}
}
