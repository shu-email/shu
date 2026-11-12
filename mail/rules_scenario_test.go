package mail

import (
	"context"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// RuleLogBound fills the rules' log of store past MaxRuleActivity: it
// keeps the newest entries, and the pending ones however old. It is for
// the tests on the in-memory and the SQLite stores (rules_sqlite_test.go).
func RuleLogBound(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveAccount(ctx, testAccount()); err != nil {
		t.Fatal(err)
	}
	entry := func(subject string, state ActivityState) RuleActivity {
		return RuleActivity{At: ruleNow, Rule: 1, RuleName: "R", Account: "a1", Folder: "INBOX", UIDValidity: 1, UID: 1,
			Subject: subject, Actions: []Action{{Kind: ActionStar}}, State: state}
	}
	first, err := store.AddRuleActivity(ctx, []RuleActivity{
		entry("old pending 1", ActivityPending), entry("old applied", ActivityApplied),
		entry("old pending 2", ActivityPending), entry("old failed", ActivityFailed),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 || first[0].ID == 0 || first[1].ID <= first[0].ID {
		t.Fatalf("written %+v", first)
	}
	more := make([]RuleActivity, MaxRuleActivity-1)
	for i := range more {
		more[i] = entry("new", ActivityApplied)
	}
	if _, err := store.AddRuleActivity(ctx, more); err != nil {
		t.Fatal(err)
	}
	page, err := store.RuleActivity(ctx, ActivityQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// The newest MaxRuleActivity are the 1999 new ones and the old
	// failed; the old pending ones stay besides, and the old applied goes.
	if page.Total != MaxRuleActivity+2 || len(page.Entries) != page.Total {
		t.Fatalf("the log holds %d entries, reads %d", page.Total, len(page.Entries))
	}
	var old []string
	for _, e := range page.Entries {
		if e.Subject != "new" {
			old = append(old, e.Subject)
		}
	}
	if !slices.Equal(old, []string{"old failed", "old pending 2", "old pending 1"}) {
		t.Errorf("of the old entries the log keeps %v", old)
	}
	if page, err := store.RuleActivity(ctx, ActivityQuery{State: ActivityPending, Offset: 1, Limit: 5}); err != nil ||
		page.Total != 2 || len(page.Entries) != 1 || page.Entries[0].Subject != "old pending 1" {
		t.Errorf("the second pending entry = %+v, %v", page, err)
	}
	// An entry settled since is the bound's like any.
	settled := first[0]
	settled.State, settled.Changes = ActivityApplied, RuleChanges{Flags: []FlagChange{{Flag: FlagFlagged, Was: false}}}
	if err := store.UpdateRuleActivity(ctx, []RuleActivity{settled}); err != nil {
		t.Fatal(err)
	}
	if page, err := store.RuleActivity(ctx, ActivityQuery{ID: settled.ID}); err != nil || len(page.Entries) != 1 ||
		page.Entries[0].State != ActivityApplied || len(page.Entries[0].Changes.Flags) != 1 {
		t.Fatalf("settled = %+v, %v", page, err)
	}
	if _, err := store.AddRuleActivity(ctx, []RuleActivity{entry("newest", ActivityApplied)}); err != nil {
		t.Fatal(err)
	}
	if page, err := store.RuleActivity(ctx, ActivityQuery{Limit: 1}); err != nil || page.Total != MaxRuleActivity+1 ||
		page.Entries[0].Subject != "newest" {
		t.Errorf("after one more the log holds %d, the newest %+v, %v", page.Total, page.Entries, err)
	}
}

// RulesScenario lives the rules of an account on store, as the app does,
// on a fake server: rules made and put in order, mail that arrives sorted
// by them with a header fetched for one, each match logged with what it
// changed; a restart, after which the rules, the log and what this app
// moved are as they were; an undo that stays undone across it; a preview
// and a run over mail that was there; and the account removed with its
// rules and log. reopen gives the store as the app finds it as it starts,
// the same one or the database opened anew. It is for the tests on the
// in-memory and the SQLite stores (rules_sqlite_test.go).
func RulesScenario(t *testing.T, reopen func() Store) {
	t.Helper()
	r := newRuleRigOn(t, allCaps(), reopen)
	r.deliver("INBOX", news("Old sale", "old@shop.example", 0))
	r.deliver("INBOX", ruleMail("Old digest", "Club <club@lists.example.org>", "od@lists.example.org", "List-Id: <club.lists.example.org>\r\n", 1))
	r.sync("INBOX")
	shop, err := r.svc.CreateLabel(r.ctx, r.a.ID, "Shop", "")
	if err != nil {
		t.Fatal(err)
	}
	newsRule := r.save(r.newsRule("Newsletters"))
	lists := r.save(Rule{Name: "Lists", Match: MatchAny,
		Conditions: []Condition{{Field: CondHeader, Header: "List-Id", Value: "lists.example.org"}, {Field: CondFolder, Account: r.a.ID, Folder: "Lists"}},
		Actions:    []Action{{Kind: ActionAddLabel, Account: r.a.ID, Label: shop.ID}, {Kind: ActionStar}}, Stop: true})
	r.save(Rule{Name: "Unread", Conditions: []Condition{{Field: CondSubject, Value: "news"}}, Actions: []Action{{Kind: ActionMarkUnread}}})
	if err := r.svc.MoveRule(r.ctx, lists.ID, 0); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		rules, err := r.svc.Rules(r.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, rule := range rules {
			out = append(out, rule.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Lists", "Newsletters", "Unread"}) {
		t.Fatalf("rules in order: %v", got)
	}

	r.deliver("INBOX", news("Sale news", "s1@shop.example", 2))
	r.deliver("INBOX", ruleMail("Digest news", "Club <club@lists.example.org>", "d1@lists.example.org", "List-Id: Club <club.lists.example.org>\r\n", 3))
	r.deliver("INBOX", ruleMail("Hi", "Bob <bob@example.org>", "h1@example.org", "", 4))
	r.sync("INBOX")
	// The digest is the Lists rule's, which stops there; the sale is moved
	// before the rule after could mark it.
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale news: applied", "Digest news: applied"}) {
		t.Fatalf("log = %v", got)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Digest news", "Hi", "Old digest", "Old sale"}) {
		t.Errorf("INBOX holds %v", got)
	}
	digest := r.serverMessage("INBOX", "Digest news")
	if !digest.hasFlag(imap.FlagFlagged) || !slices.Equal(keywordsOn(t, r.f, "INBOX")["Digest news"], []string{"shop"}) ||
		r.serverMessage("INBOX", "Old digest").hasFlag(imap.FlagFlagged) {
		t.Errorf("the digest has %v", digest.Flags)
	}
	if got := headerValues(r.message("INBOX", "Digest news").RawHeaders, "List-Id"); !slices.Equal(got, []string{"Club <club.lists.example.org>"}) {
		t.Errorf("the digest keeps List-Id %v", got)
	}
	sale := r.log(ActivityQuery{Rule: newsRule.ID})[0]
	if sale.Changes.MovedTo != "Newsletters" || sale.Changes.DestUID == 0 || !sale.Quiet || !sale.At.Equal(ruleNow) {
		t.Errorf("the sale's entry = %+v", sale)
	}

	// An undo, then a restart: the sale stays in the inbox.
	if err := r.svc.UndoActivity(r.ctx, sale.ID); err != nil {
		t.Fatal(err)
	}
	r.restart()
	if got := names(); !slices.Equal(got, []string{"Lists", "Newsletters", "Unread"}) {
		t.Errorf("after a restart the rules are %v", got)
	}
	r.sync("INBOX")
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Digest news", "Hi", "Old digest", "Old sale", "Sale news"}) {
		t.Errorf("after the undo and a restart INBOX holds %v", got)
	}
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale news: undone", "Digest news: applied"}) {
		t.Errorf("after the restart the log is %v", got)
	}

	// A preview and a run over the mail there was: the old sale only, as
	// the sale that came back was not that rule's to take again.
	preview, err := r.svc.PreviewRule(r.ctx, Rule{Name: "Old", Conditions: []Condition{{Field: CondSubject, Value: "old"}},
		Actions: []Action{{Kind: ActionStar}}}, 0, 10)
	if err != nil || preview.Total != 2 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	old := r.save(Rule{Name: "Old", Conditions: []Condition{{Field: CondSubject, Value: "old sale"}}, Actions: []Action{{Kind: ActionStar}}})
	ran, err := r.svc.RunRule(r.ctx, old.ID, preview.Targets)
	if err != nil || len(ran) != 1 || ran[0].Subject != "Old sale" || ran[0].State != ActivityApplied || !ran[0].Manual {
		t.Fatalf("ran %+v, %v", ran, err)
	}
	if !r.serverMessage("INBOX", "Old sale").hasFlag(imap.FlagFlagged) {
		t.Error("the run did not star the old sale")
	}
	if n, err := r.svc.UndoRule(r.ctx, old.ID); err != nil || n != 1 || r.serverMessage("INBOX", "Old sale").hasFlag(imap.FlagFlagged) {
		t.Errorf("undid the run: %d, %v", n, err)
	}

	// The account goes, with the rules that name it and its log.
	if err := r.svc.RemoveAccount(r.ctx, r.a.ID); err != nil {
		t.Fatal(err)
	}
	r.restart()
	if got := names(); !slices.Equal(got, []string{"Unread", "Old"}) {
		t.Errorf("rules after the account went: %v", got)
	}
	if page, err := r.svc.RuleActivity(r.ctx, ActivityQuery{}); err != nil || page.Total != 0 {
		t.Errorf("the log after the account went: %+v, %v", page, err)
	}
}

func TestRulesScenario(t *testing.T) {
	store := newMemStore()
	RulesScenario(t, func() Store { return store })
}
