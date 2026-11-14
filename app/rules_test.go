package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// shopNews sends the newsletters of the tests of rules.
var shopNews = mail.Address{Name: "Shop News", Email: "news@shop.example"}

// ruleDay is a day of March 2026 at nine.
func ruleDay(day int) time.Time { return time.Date(2026, time.March, day, 9, 0, 0, 0, time.UTC) }

// newsletter is a message of the shop's series.
func newsletter(uid mail.UID, n int) mail.Summary {
	return mail.Summary{UID: uid, Subject: fmt.Sprintf("Weekly news %d", n), From: shopNews, Date: ruleDay(n),
		MessageID: fmt.Sprintf("n%d@shop.example", n)}
}

// rulesService is Ann's account, with a folder for newsletters and a
// label: an old newsletter is in her inbox, among other mail.
func rulesService() *fakeService {
	fs := newFakeService()
	fs.clock = func() time.Time { return fakeRuleClock }
	fs.settings.Conversations = false
	folders := []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox},
		{Name: "Newsletters"},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
		{Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
		{Name: "Drafts", SpecialUse: mail.SpecialDrafts},
	}
	msgs := []mail.Summary{
		newsletter(1, 1),
		{UID: 2, Subject: "Lunch on Friday?", From: mail.Address{Name: "Bob Lee", Email: "bob@example.org"},
			Date: ruleDay(1).Add(time.Hour), MessageID: "lunch@example.org", Seen: true},
		{UID: 3, Subject: "Invoice for March", From: mail.Address{Name: "Billing", Email: "billing@example.net"},
			Date: ruleDay(1).Add(2 * time.Hour), MessageID: "invoice@example.net", HasAttachments: true, Seen: true},
	}
	fs.addAccount(testAccount("a1", "Ann", "ann@example.com"), "secret", folders, msgs)
	fs.recount("a1", "INBOX")
	fs.addLabel("a1", "Receipts", mail.LabelOrange)
	return fs
}

// newsRule is the rule that moves the shop's newsletters to their folder.
func newsRule() mail.Rule {
	return mail.Rule{Name: "Newsletters", Conditions: []mail.Condition{{Field: mail.CondSender, Value: "news@shop.example"}},
		Actions: []mail.Action{{Kind: mail.ActionMove, Account: "a1", Folder: "Newsletters"}}}
}

// rulesApp starts the window on a service and opens the rules from the
// mail window's sidebar.
func rulesApp(t *testing.T, fs *fakeService) (*app, *ui.Tester) {
	t.Helper()
	a := openApp(fs)
	tt := ui.NewTester(a.view, 1220, 800)
	clickMore(t, tt, locales.Rules())
	if !a.rules.open || !tt.HasText(locales.MailRules()) {
		t.Fatalf("the rules did not open: %q", tt.Texts())
	}
	return a, tt
}

// choose opens a picker by its name and chooses an option of it.
func choose(t *testing.T, tt *ui.Tester, picker, option string) {
	t.Helper()
	clickRole(t, tt, ui.RolePopUpButton, picker)
	clickRole(t, tt, ui.RoleOption, option)
}

// options are what a picker offers, as assistive technology reads its
// open list.
func options(t *testing.T, tt *ui.Tester, picker string) []string {
	t.Helper()
	clickRole(t, tt, ui.RolePopUpButton, picker)
	var out []string
	for _, info := range tt.AccessAll() {
		if info.Role == ui.RoleOption {
			out = append(out, info.Name)
		}
	}
	tt.Key(0, ui.KeyEscape)
	return out
}

// writeNewsRule writes, in the editor of a new rule, the rule that moves
// the shop's newsletters to their folder, and saves nothing.
func writeNewsRule(t *testing.T, tt *ui.Tester) {
	t.Helper()
	click(t, tt, locales.NewRule())
	if !tt.Focused(locales.RuleName()) {
		t.Fatalf("the name does not take the keyboard: %q", tt.Texts())
	}
	tt.Type("Newsletters")
	typeIn(t, tt, locales.ValueOf(1), "news@shop.example")
	choose(t, tt, locales.ActionFolderOf(1), "Newsletters")
}

// inbox are the subjects of a folder of the fake, newest first.
func subjectsIn(fs *fakeService, account, folder string) []string {
	var out []string
	for _, m := range fs.messages[listKey(account, folder)] {
		out = append(out, m.Subject)
	}
	return out
}

// mailActs are the calls of the fake that changed mail.
func mailActs(fs *fakeService) []string {
	var out []string
	for _, act := range fs.acts {
		if !strings.Contains(act, "FetchMessage") {
			out = append(out, act)
		}
	}
	return out
}

// ruleRows are the rules the list shows, as assistive technology names
// their rows.
func ruleRows(tt *ui.Tester) []string {
	var out []string
	for _, info := range tt.AccessAll() {
		if info.Role == ui.RoleListItem {
			out = append(out, info.Name)
		}
	}
	return out
}

// loggedRules is rulesService with three rules and a log with an entry in
// each state: a newsletter moved by the rule (done), a receipt whose label
// the server refused (failed), a newsletter moved and put back (undone),
// a team note whose star waits for the server (pending), and the old
// newsletter, which the user applied the rule to (done, by hand).
func loggedRules() *fakeService {
	fs := rulesService()
	news := fs.addRule(newsRule())
	receipts := fs.labels["a1"][0]
	fs.addRule(mail.Rule{Name: "Receipts", Conditions: []mail.Condition{{Field: mail.CondSubject, Value: "receipt"}},
		Actions: []mail.Action{{Kind: mail.ActionAddLabel, Account: "a1", Label: receipts.ID}, {Kind: mail.ActionMarkRead}}})
	fs.addRule(mail.Rule{Name: "Team list", Conditions: []mail.Condition{{Field: mail.CondSender, Value: "team@lists.example.org"}},
		Actions: []mail.Action{{Kind: mail.ActionStar}}})
	fs.arrive("a1", "INBOX", newsletter(10, 2))
	fs.applyPending("a1")
	fs.setLabelErr = errFake
	fs.arrive("a1", "INBOX", mail.Summary{UID: 11, Subject: "Your receipt #4471", From: mail.Address{Name: "Northwind", Email: "orders@northwind.example"},
		Date: ruleDay(3), MessageID: "r4471@northwind.example"})
	fs.applyPending("a1")
	fs.setLabelErr = nil
	moved := fs.arrive("a1", "INBOX", newsletter(12, 3))
	fs.applyPending("a1")
	if err := fs.undoEntry(fs.entry(moved[0].ID)); err != nil {
		panic(err)
	}
	fs.runWaits = errors.New("dial tcp: lookup imap.example.com: no such host")
	fs.arrive("a1", "INBOX", mail.Summary{UID: 13, Subject: "Team sync notes", From: mail.Address{Name: "Team", Email: "team@lists.example.org"},
		Date: ruleDay(4), MessageID: "sync@lists.example.org"})
	fs.applyPending("a1")
	fs.runWaits = nil
	preview, err := fs.PreviewRule(context.Background(), news, 0, 0)
	if err != nil {
		panic(err)
	}
	old := slices.DeleteFunc(preview.Targets, func(target mail.RuleTarget) bool { return target.UID != 1 })
	if _, err := fs.RunRule(context.Background(), news.ID, old); err != nil {
		panic(err)
	}
	fs.acts, fs.ruleCalls = nil, nil
	return fs
}

// The rules open from the mail window's title bar in place of the panes:
// in their order, each with what it does in words, a paused one marked
// and its switch off. Done and Escape go back to the mail. With no rule,
// the view says what one can do.
func TestRulesOpenFromTheMailWindow(t *testing.T) {
	fs := loggedRules()
	fs.addRule(mail.Rule{Name: "Old promotions", Paused: true, Match: mail.MatchAny,
		Conditions: []mail.Condition{{Field: mail.CondSubject, Value: "sale"}, {Field: mail.CondHeader, Header: "List-Id", Value: "promo"}},
		Actions:    []mail.Action{{Kind: mail.ActionMove, Account: "a1", Folder: "Trash"}}})
	a, tt := rulesApp(t, fs)
	if got := ruleRows(tt); !slices.Equal(got, []string{"Newsletters", "Receipts", "Team list", "Old promotions"}) {
		t.Fatalf("rows = %q", got)
	}
	for _, text := range []string{
		locales.RuleSummary(locales.CondSenderText("news@shop.example"), locales.ActMoveText("Newsletters")),
		locales.RuleSummary(locales.CondSubjectText("receipt"), locales.ActAddLabelText("Receipts")+locales.ListSeparator()+locales.ActMarkReadText()),
		locales.RuleSummary(locales.CondSubjectText("sale")+locales.RuleOr()+locales.CondHeaderText("List-Id", "promo"), locales.ActMoveText(locales.Trash())),
		locales.RulePaused(), locales.RulesIntro(),
	} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if info, _ := tt.AccessByRole(ui.RoleSwitch, locales.UseRule("Old promotions")); info.Checked {
		t.Error("the paused rule's switch is on")
	}
	if info, ok := tt.AccessByRole(ui.RoleSwitch, locales.UseRule("Newsletters")); !ok || !info.Checked {
		t.Errorf("the rule's switch = %+v", info)
	}
	click(t, tt, locales.Done())
	if a.rules.open || !tt.HasText(locales.Inbox()) {
		t.Fatalf("Done: %q", tt.Texts())
	}
	clickMore(t, tt, locales.Rules())
	tt.Key(0, ui.KeyEscape)
	if a.rules.open {
		t.Error("Escape left the rules open")
	}

	_, et := rulesApp(t, rulesService())
	if !et.HasText(locales.RulesEmpty()) || len(ruleRows(et)) != 0 {
		t.Errorf("no rule: %q", et.Texts())
	}
}

// The first two acceptance criteria in the window: a rule that moves the
// shop's newsletters lists, before it is saved, the newsletter already in
// the inbox, and says that saving changes none of it. Saving moves
// nothing. A newsletter that arrives after is moved to the folder, the
// old one stays, and the window reads its list again as it hears of it.
func TestNewsletterRuleActsOnNewMailOnly(t *testing.T) {
	fs := rulesService()
	a, tt := rulesApp(t, fs)
	writeNewsRule(t, tt)
	if len(fs.ruleCalls) != 0 {
		t.Fatalf("written, not saved: %v", fs.ruleCalls)
	}
	for _, text := range []string{locales.RulePreviewCount(1), "Weekly news 1", locales.RuleNewMailOnly()} {
		if !tt.HasText(text) {
			t.Fatalf("the preview lacks %q: %q", text, tt.Texts())
		}
	}
	if tt.HasText("Lunch on Friday?") || tt.HasText(locales.ApplyRuleNow(1)) {
		t.Errorf("the preview of the rule not saved: %q", tt.Texts())
	}
	click(t, tt, locales.SaveRule())
	if got := fs.ruleCalls; !slices.Equal(got, []string{"SaveRule Newsletters"}) {
		t.Fatalf("calls = %v", got)
	}
	if len(fs.rules) != 1 || fs.rules[0].Name != "Newsletters" || !slices.Equal(fs.rules[0].Conditions, newsRule().Conditions) ||
		!slices.Equal(fs.rules[0].Actions, newsRule().Actions) {
		t.Fatalf("saved %+v", fs.rules)
	}
	if acts := mailActs(fs); len(acts) != 0 {
		t.Fatalf("saving changed mail: %v", acts)
	}
	if got := subjectsIn(fs, "a1", "INBOX"); !slices.Contains(got, "Weekly news 1") {
		t.Fatalf("the old newsletter left the inbox: %v", got)
	}
	for _, text := range []string{locales.RuleSaved(), locales.RuleSavedNewMailOnly(), locales.ApplyRuleNow(1)} {
		if !tt.HasText(text) {
			t.Errorf("saved, the editor lacks %q: %q", text, tt.Texts())
		}
	}

	click(t, tt, locales.CloseRuleEditor())
	if got := ruleRows(tt); !slices.Equal(got, []string{"Newsletters"}) {
		t.Fatalf("the list after saving = %q", got)
	}
	click(t, tt, locales.Done())
	fs.arrive("a1", "INBOX", newsletter(10, 2),
		mail.Summary{UID: 11, Subject: "Minutes", From: mail.Address{Name: "Carol", Email: "carol@example.org"}, Date: ruleDay(2).Add(time.Hour)})
	fs.applyPending("a1")
	if got := subjectsIn(fs, "a1", "Newsletters"); !slices.Equal(got, []string{"Weekly news 2"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if got := subjectsIn(fs, "a1", "INBOX"); !slices.Contains(got, "Weekly news 1") || slices.Contains(got, "Weekly news 2") {
		t.Errorf("the inbox holds %v", got)
	}
	tt.Frame()
	if got := rowSubjects(a); !slices.Contains(got, "Minutes") || !slices.Contains(got, "Weekly news 1") || slices.Contains(got, "Weekly news 2") {
		t.Errorf("the list after the rule moved the new newsletter: %v", got)
	}
	clickMore(t, tt, locales.Rules())
	click(t, tt, locales.ActivityTab())
	if got := ruleRows(tt); !slices.Equal(got, []string{"Weekly news 2"}) || !tt.HasText(locales.StateApplied()) {
		t.Errorf("the log = %q: %q", got, tt.Texts())
	}
}

// The third acceptance criterion: the window offers a rule the engine's
// actions and no others, none of which sends, forwards or deletes; each
// saves as itself. A rule that came to say another, as a database
// written by other means may hold, is refused at that action.
func TestRuleActionsAreAClosedSet(t *testing.T) {
	fs := rulesService()
	receipts := fs.labels["a1"][0]
	a, tt := rulesApp(t, fs)
	var want []string
	for _, k := range mail.ActionKinds {
		want = append(want, actionKindName(k))
	}
	click(t, tt, locales.NewRule())
	got := options(t, tt, locales.ActionOf(1))
	if !slices.Equal(got, want) {
		t.Fatalf("the actions offered = %q, want %q", got, want)
	}
	for _, name := range got {
		lower := strings.ToLower(name)
		for _, word := range []string{"send", "forward", "delete", "reply", "redirect", "trash"} {
			if strings.Contains(lower, word) {
				t.Errorf("an action offered says %q: %q", word, name)
			}
		}
	}
	click(t, tt, locales.CancelRuleEditor())

	for _, k := range mail.ActionKinds {
		click(t, tt, locales.NewRule())
		tt.Type("Rule " + string(k))
		typeIn(t, tt, locales.ValueOf(1), "news@shop.example")
		choose(t, tt, locales.ActionOf(1), actionKindName(k))
		switch k {
		case mail.ActionMove:
			choose(t, tt, locales.ActionFolderOf(1), "Newsletters")
		case mail.ActionAddLabel, mail.ActionRemoveLabel:
			choose(t, tt, locales.ActionLabelOf(1), "Receipts")
		}
		click(t, tt, locales.SaveRule())
		saved := fs.rules[len(fs.rules)-1]
		if saved.Name != "Rule "+string(k) || len(saved.Actions) != 1 || saved.Actions[0].Kind != k {
			t.Fatalf("%s saved as %+v: %q", k, saved, tt.Texts())
		}
		if k == mail.ActionAddLabel && saved.Actions[0].Label != receipts.ID {
			t.Errorf("the label saved = %d", saved.Actions[0].Label)
		}
		click(t, tt, locales.CloseRuleEditor())
	}
	for _, r := range fs.rules {
		for _, act := range r.Actions {
			if !act.Kind.Valid() {
				t.Errorf("%s holds %q", r.Name, act.Kind)
			}
		}
	}

	// A rule that forwards, put in the store by other means.
	fs.rules = append(fs.rules, mail.Rule{ID: 99, Name: "Forward all", Match: mail.MatchAll,
		Conditions: []mail.Condition{{Field: mail.CondAttachment}}, Actions: []mail.Action{{Kind: "forward"}}})
	a.readRules()
	tt.Frame()
	click(t, tt, locales.EditRule("Forward all"))
	click(t, tt, locales.AddCondition())
	click(t, tt, locales.RemoveCondition(2))
	click(t, tt, locales.SaveRule())
	if !tt.HasText(locales.RuleActionNotAllowed()) {
		t.Errorf("a rule that forwards: %q", tt.Texts())
	}
	if info, _ := tt.AccessByRole(ui.RolePopUpButton, locales.ActionOf(1)); !info.Invalid ||
		!slices.Equal(info.ErrorMessage, []string{locales.RuleActionNotAllowed()}) {
		t.Errorf("the action = %+v", info)
	}
	if i, _ := fs.fakeRule(99); fs.rules[i].Actions[0].Kind != "forward" {
		t.Errorf("the rule that forwards was saved over: %+v", fs.rules[i])
	}
}

// A move to the Trash says that it deletes nothing now, and that the
// Trash, once emptied, does; a move elsewhere says nothing.
func TestRuleMoveToTrashWarns(t *testing.T) {
	_, tt := rulesApp(t, rulesService())
	click(t, tt, locales.NewRule())
	if tt.HasText(locales.RuleTrashWarning()) {
		t.Fatal("a warning before any folder")
	}
	choose(t, tt, locales.ActionFolderOf(1), locales.Trash())
	if !tt.HasText(locales.RuleTrashWarning()) {
		t.Fatalf("no warning on a move to the Trash: %q", tt.Texts())
	}
	for _, folder := range []string{locales.Junk(), locales.Archive(), "Newsletters"} {
		choose(t, tt, locales.ActionFolderOf(1), folder)
		if tt.HasText(locales.RuleTrashWarning()) {
			t.Errorf("a warning on a move to %s", folder)
		}
	}
	// Drafts is no folder a rule can name.
	if got := options(t, tt, locales.ActionFolderOf(1)); slices.Contains(got, locales.Drafts()) ||
		!slices.Equal(got, []string{locales.Inbox(), locales.Archive(), locales.Junk(), locales.Trash(), locales.Sent(), "Newsletters"}) {
		t.Errorf("the folders offered = %q", got)
	}
}

// fieldFault is what assistive technology has of a control the editor
// marked: whether it is invalid, and the messages that say why.
func fieldFault(t *testing.T, tt *ui.Tester, role ui.Role, name string) (bool, []string) {
	t.Helper()
	info, ok := tt.AccessByRole(role, name)
	if !ok {
		t.Fatalf("no %v %q: %q", role, name, tt.Texts())
	}
	return info.Invalid, info.ErrorMessage
}

// The engine's word on a rule shows at the item it is about, in the
// user's words, as the rule is written: a header's name that is none at
// once; what is still to fill in once a save was asked for, item after
// item as each is filled in; actions that undo each other at the second.
// The control at fault is marked invalid with its message.
func TestRuleFaultsShowAtTheirItems(t *testing.T) {
	fs := rulesService()
	a, tt := rulesApp(t, fs)
	click(t, tt, locales.NewRule())
	if a.rules.editor.fault.text == "" || tt.HasText(locales.RuleNameInvalid(mail.MaxRuleName)) || tt.HasText(locales.RuleValueMissing()) {
		t.Fatalf("a new rule shows what is still to fill in: %q", tt.Texts())
	}
	click(t, tt, locales.SaveRule())
	if len(fs.rules) != 0 {
		t.Fatalf("an empty rule was saved: %+v", fs.rules)
	}
	invalid, why := fieldFault(t, tt, ui.RoleTextField, locales.RuleName())
	if !invalid || !slices.Equal(why, []string{locales.RuleNameInvalid(mail.MaxRuleName)}) {
		t.Fatalf("the name = %v %q: %q", invalid, why, tt.Texts())
	}
	typeIn(t, tt, locales.RuleName(), "Newsletters")
	invalid, why = fieldFault(t, tt, ui.RoleTextField, locales.ValueOf(1))
	if !invalid || !slices.Equal(why, []string{locales.RuleValueMissing()}) || tt.HasText(locales.RuleNameInvalid(mail.MaxRuleName)) {
		t.Fatalf("the text of condition 1 = %v %q", invalid, why)
	}
	typeIn(t, tt, locales.ValueOf(1), "news@shop.example")
	invalid, why = fieldFault(t, tt, ui.RolePopUpButton, locales.ActionFolderOf(1))
	if !invalid || !slices.Equal(why, []string{locales.RuleChooseFolder()}) {
		t.Fatalf("the folder of action 1 = %v %q", invalid, why)
	}
	choose(t, tt, locales.ActionFolderOf(1), "Newsletters")
	if a.rules.editor.fault.text != "" || !tt.HasText(locales.RulePreviewCount(1)) {
		t.Fatalf("a complete rule: fault %q, %q", a.rules.editor.fault.text, tt.Texts())
	}

	// A header's name with a space is said at once, and holds the preview.
	click(t, tt, locales.AddCondition())
	choose(t, tt, locales.ConditionOf(2), locales.RuleFieldHeader())
	typeIn(t, tt, locales.HeaderOf(2), "List Id")
	typeIn(t, tt, locales.ValueOf(2), "news.shop.example")
	invalid, why = fieldFault(t, tt, ui.RoleTextField, locales.HeaderOf(2))
	if !invalid || !slices.Equal(why, []string{locales.RuleHeaderInvalid()}) || !tt.HasText(locales.RulePreviewBlocked()) {
		t.Fatalf("a header's name with a space = %v %q: %q", invalid, why, tt.Texts())
	}
	click(t, tt, locales.RemoveCondition(2))
	if a.rules.editor.fault.text != "" {
		t.Fatalf("the condition at fault removed, the fault stays: %q", a.rules.editor.fault.text)
	}

	// Read and unread: the second action is at fault.
	click(t, tt, locales.AddAction())
	choose(t, tt, locales.ActionOf(2), locales.KindMarkRead())
	click(t, tt, locales.AddAction())
	choose(t, tt, locales.ActionOf(3), locales.KindMarkUnread())
	invalid, why = fieldFault(t, tt, ui.RolePopUpButton, locales.ActionOf(3))
	if !invalid || !slices.Equal(why, []string{locales.RuleReadAndUnread()}) {
		t.Fatalf("read and unread = %v %q", invalid, why)
	}
	if invalid, _ := fieldFault(t, tt, ui.RolePopUpButton, locales.ActionOf(2)); invalid {
		t.Error("the first of read and unread is marked")
	}
	click(t, tt, locales.RemoveAction(3))
	click(t, tt, locales.AddAction())
	choose(t, tt, locales.ActionOf(3), locales.KindMove())
	choose(t, tt, locales.ActionFolderOf(3), locales.Archive())
	if _, why := fieldFault(t, tt, ui.RolePopUpButton, locales.ActionOf(3)); !slices.Equal(why, []string{locales.RuleOneMove()}) {
		t.Errorf("two moves: %q", why)
	}
	click(t, tt, locales.RemoveAction(3))

	// No condition left.
	click(t, tt, locales.RemoveCondition(1))
	if !tt.HasText(locales.RuleNeedsCondition()) {
		t.Errorf("no condition: %q", tt.Texts())
	}
	if len(fs.rules) != 0 || len(mailActs(fs)) != 0 {
		t.Errorf("checking saved or changed something: %+v %v", fs.rules, mailActs(fs))
	}
}

// twoAccountRules is rulesService with Ann's second account, Home.
func twoAccountRules() *fakeService {
	fs := rulesService()
	fs.addAccount(testAccount("a2", "Home", "ann@home.example"), "secret", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox}, {Name: "Lists"}, {Name: "Trash", SpecialUse: mail.SpecialTrash},
	}, []mail.Summary{{UID: 1, Subject: "Garden club", From: mail.Address{Name: "Club", Email: "club@garden.example"}, Date: ruleDay(2)}})
	return fs
}

// A rule names one account: with two, a row that names a folder or label
// has the account to choose, the rule's by default, and a second account
// is said at the row that names it.
func TestRuleNamesOneAccount(t *testing.T) {
	fs := twoAccountRules()
	_, tt := rulesApp(t, fs)
	click(t, tt, locales.NewRule())
	tt.Type("Club")
	typeIn(t, tt, locales.ValueOf(1), "club@")
	if info, _ := tt.AccessByRole(ui.RolePopUpButton, locales.ActionAccountOf(1)); info.Value != "Ann" {
		t.Fatalf("the account of a new move = %+v", info)
	}
	choose(t, tt, locales.ActionAccountOf(1), "Home")
	if got := options(t, tt, locales.ActionFolderOf(1)); !slices.Equal(got, []string{locales.Inbox(), locales.Trash(), "Lists"}) {
		t.Fatalf("Home's folders = %q", got)
	}
	choose(t, tt, locales.ActionFolderOf(1), "Lists")
	click(t, tt, locales.AddCondition())
	choose(t, tt, locales.ConditionOf(2), locales.RuleFieldFolder())
	if info, _ := tt.AccessByRole(ui.RolePopUpButton, locales.AccountOf(2)); info.Value != "Home" {
		t.Errorf("a new condition names %q, not the rule's account", info.Value)
	}
	choose(t, tt, locales.AccountOf(2), "Ann")
	choose(t, tt, locales.FolderOf(2), locales.Inbox())
	if _, why := fieldFault(t, tt, ui.RolePopUpButton, locales.ActionAccountOf(1)); !slices.Equal(why, []string{locales.RuleOneAccount("Ann")}) {
		t.Errorf("two accounts: %q: %q", why, tt.Texts())
	}
	if !tt.HasText(locales.RuleFolderInAccount("Lists", "Home")) && !tt.HasText("Lists") {
		t.Errorf("the folder of the move: %q", tt.Texts())
	}
}

// After it is saved, the preview offers to apply the rule to the
// messages it lists, once, after a question; the messages change and the
// log has an entry for each, said to be the user's. A paused rule, a
// rule changed since it was saved, and more messages than a run takes
// are not applied.
func TestApplyRuleFromItsPreview(t *testing.T) {
	fs := rulesService()
	fs.messages[listKey("a1", "INBOX")] = append(fs.messages[listKey("a1", "INBOX")], func() mail.Summary {
		m := newsletter(4, 2)
		m.Account, m.Folder = "a1", "INBOX"
		return m
	}())
	sortSummaries(fs.messages[listKey("a1", "INBOX")])
	_, tt := rulesApp(t, fs)
	writeNewsRule(t, tt)
	click(t, tt, locales.SaveRule())
	click(t, tt, locales.ApplyRuleNow(2))
	for _, text := range []string{locales.ApplyRuleTitle("Newsletters", 2), locales.ApplyRuleMessage(locales.ActMoveText("Newsletters"))} {
		if !tt.HasText(text) {
			t.Fatalf("the question lacks %q: %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Cancel())
	if slices.ContainsFunc(fs.ruleCalls, func(call string) bool { return strings.HasPrefix(call, "RunRule") }) || len(mailActs(fs)) != 0 {
		t.Fatalf("cancelled, the rule ran: %v %v", fs.ruleCalls, mailActs(fs))
	}
	click(t, tt, locales.ApplyRuleNow(2))
	// A newsletter cached while the question is open is no message the
	// user was asked about: the run takes the two.
	putMail(fs, "a1", "INBOX", newsletter(6, 4))
	fs.ruleReport("a1")
	tt.Frame()
	if !tt.HasText(locales.ApplyRuleTitle("Newsletters", 2)) {
		t.Fatalf("the question changed: %q", tt.Texts())
	}
	click(t, tt, locales.ApplyRuleButton(2))
	if got := fs.ruleCalls[len(fs.ruleCalls)-1]; got != fmt.Sprintf("RunRule %d 2", fs.rules[0].ID) {
		t.Fatalf("calls = %v", fs.ruleCalls)
	}
	if got := slices.Sorted(slices.Values(subjectsIn(fs, "a1", "Newsletters"))); !slices.Equal(got, []string{"Weekly news 1", "Weekly news 2"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if !tt.HasText(locales.RuleRanDone(2)) || !tt.HasText(locales.RulePreviewCount(1)) || !tt.HasText(locales.ApplyRuleNow(1)) {
		t.Errorf("after the run: %q", tt.Texts())
	}
	fs.messages[listKey("a1", "INBOX")] = slices.DeleteFunc(fs.messages[listKey("a1", "INBOX")], func(m mail.Summary) bool { return m.UID == 6 })
	click(t, tt, locales.ShowRuleActivity())
	if got := ruleRows(tt); !slices.Equal(got, []string{"Weekly news 2", "Weekly news 1"}) ||
		!tt.HasText(locales.EntryManual(locales.ActMoveText("Newsletters"))) {
		t.Fatalf("the log = %q: %q", got, tt.Texts())
	}
	if info, _ := tt.AccessByRole(ui.RolePopUpButton, locales.LogRuleFilter()); info.Value != "Newsletters" {
		t.Errorf("the log shows %q, not the rule's", info.Value)
	}

	// Changed since it was saved, the rule offers no run until saved.
	putMail(fs, "a1", "INBOX", newsletter(5, 3))
	click(t, tt, locales.RulesTab())
	click(t, tt, locales.EditRule("Newsletters"))
	if !tt.HasText(locales.ApplyRuleNow(1)) {
		t.Fatalf("a saved rule's editor: %q", tt.Texts())
	}
	tt.Type(" weekly")
	if tt.HasText(locales.ApplyRuleNow(1)) || !tt.HasText(locales.RuleNewMailOnly()) {
		t.Errorf("changed, the rule offers a run: %q", tt.Texts())
	}
	click(t, tt, locales.SaveRule())
	if !tt.HasText(locales.ApplyRuleNow(1)) || fs.rules[0].Name != "Newsletters weekly" {
		t.Errorf("saved again: %+v: %q", fs.rules[0], tt.Texts())
	}

	// Paused, it is not applied.
	fs.rules[0].Paused = true
	click(t, tt, locales.CloseRuleEditor())
	click(t, tt, locales.EditRule("Newsletters weekly"))
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.ApplyRuleNow(1)); !info.Disabled || !tt.HasText(locales.RuleApplyPaused()) {
		t.Errorf("a paused rule's offer = %+v: %q", info, tt.Texts())
	}
	fs.rules[0].Paused = false
	click(t, tt, locales.CloseRuleEditor())

	// More than a run takes.
	many := make([]mail.Summary, 0, mail.MaxRuleRun+1)
	for i := range mail.MaxRuleRun + 1 {
		m := newsletter(mail.UID(100+i), 4)
		m.Subject = fmt.Sprintf("Daily deal %d", i)
		many = append(many, m)
	}
	putMail(fs, "a1", "INBOX", many...)
	click(t, tt, locales.EditRule("Newsletters weekly"))
	n := mail.MaxRuleRun + 2
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.ApplyRuleNow(n)); !info.Disabled || !tt.HasText(locales.RuleRunTooLarge(mail.MaxRuleRun)) {
		t.Errorf("an offer of %d messages = %+v: %q", n, info, tt.Texts())
	}
}

// openLog opens the rules on their log.
func openLog(t *testing.T, fs *fakeService) (*app, *ui.Tester) {
	t.Helper()
	a, tt := rulesApp(t, fs)
	click(t, tt, locales.ActivityTab())
	if a.rules.tab != rulesTabActivity || !tt.HasText(locales.RuleLogIntro()) {
		t.Fatalf("the log did not open: %q", tt.Texts())
	}
	return a, tt
}

// The fifth acceptance criterion, for the log: newest first, each entry
// says the message, its sender, the rule and what it did, and how that
// stands: done, waiting for the server and why, failed and why, undone;
// an entry of a run the user started says so. An undone entry has nothing
// left to undo.
func TestRuleLogShowsEachState(t *testing.T) {
	_, tt := openLog(t, loggedRules())
	want := []string{"Weekly news 1", "Team sync notes", "Weekly news 3", "Your receipt #4471", "Weekly news 2"}
	if got := ruleRows(tt); !slices.Equal(got, want) {
		t.Fatalf("entries = %q, want %q", got, want)
	}
	for _, text := range []string{
		locales.StateApplied(), locales.StatePending(), "dial tcp: lookup imap.example.com: no such host",
		locales.StateFailed(), errFake.Error(), locales.StateUndone(),
		locales.EntryManual(locales.ActMoveText("Newsletters")),
		locales.ActAddLabelText("Receipts") + locales.ListSeparator() + locales.ActMarkReadText(), locales.ActStarText(),
		locales.EntryLine("Northwind", "Receipts", "Mar 2"),
	} {
		if !tt.HasText(text) {
			t.Errorf("the log lacks %q: %q", text, tt.Texts())
		}
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.UndoEntry("Weekly news 3")); !info.Disabled {
		t.Error("an undone entry can be undone again")
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.UndoEntry("Weekly news 2")); info.Disabled {
		t.Error("a done entry cannot be undone")
	}
}

// The fourth acceptance criterion, for an entry: Undo puts the message
// back where it was, the entry is undone and the list of mail reads it
// again; a message no longer where the rule left it is left alone, and
// the log says so.
func TestUndoARuleEntry(t *testing.T) {
	fs := loggedRules()
	a, tt := openLog(t, fs)
	pages := fs.pageReads
	click(t, tt, locales.UndoEntry("Weekly news 2"))
	if got := subjectsIn(fs, "a1", "INBOX"); !slices.Contains(got, "Weekly news 2") || slices.Contains(subjectsIn(fs, "a1", "Newsletters"), "Weekly news 2") {
		t.Fatalf("undone, the inbox holds %v", got)
	}
	if !tt.HasText(locales.UndoEntryDone("Weekly news 2")) || fs.pageReads == pages {
		t.Errorf("after the undo: %q, list read %v", tt.Texts(), fs.pageReads != pages)
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.UndoEntry("Weekly news 2")); !info.Disabled {
		t.Errorf("the entry undone = %+v", info)
	}
	if e := fs.ruleLog[0]; e.Subject != "Weekly news 2" || e.State != mail.ActivityUndone {
		t.Errorf("the entry = %+v", e)
	}
	if !tt.Focused(locales.EntryActions("Weekly news 2")) {
		t.Error("the keyboard did not stay on the entry")
	}
	// The receipt's star and its read mark went back too, in the store.
	click(t, tt, locales.UndoEntry("Your receipt #4471"))
	if m, _ := fs.find("a1", "INBOX", 11); m.Seen {
		t.Errorf("the receipt is still read: %+v", m)
	}

	// Gone from where the rule left it.
	key := listKey("a1", "Newsletters")
	fs.messages[key] = slices.DeleteFunc(fs.messages[key], func(m mail.Summary) bool { return m.Subject == "Weekly news 1" })
	click(t, tt, locales.UndoEntry("Weekly news 1"))
	if !tt.HasText(locales.UndoEntryFailed("Weekly news 1", locales.RuleMessageGone())) {
		t.Errorf("a message gone: %q", tt.Texts())
	}
	_ = a
}

// The fourth acceptance criterion, for a rule: from the log, a rule is
// paused, and paused with everything it did undone once the user said
// so: its messages go back, its entries are undone, and its switch is
// off. A rule deleted since can still have what it did undone.
func TestPauseAndUndoEverythingARuleDid(t *testing.T) {
	fs := loggedRules()
	news := fs.rules[0]
	a, tt := openLog(t, fs)
	click(t, tt, locales.EntryActions("Weekly news 2"))
	if err := tt.ChooseMenuItem(locales.PauseAndUndoNamed("Newsletters")); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{locales.PauseUndoTitle("Newsletters"), locales.PauseUndoMessage()} {
		if !tt.HasText(text) {
			t.Fatalf("the question lacks %q: %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Cancel())
	if len(fs.ruleCalls) != 0 {
		t.Fatalf("cancelled: %v", fs.ruleCalls)
	}
	click(t, tt, locales.EntryActions("Weekly news 2"))
	if err := tt.ChooseMenuItem(locales.PauseAndUndoNamed("Newsletters")); err != nil {
		t.Fatal(err)
	}
	click(t, tt, locales.PauseUndoButton())
	want := []string{fmt.Sprintf("PauseRule %d true", news.ID), fmt.Sprintf("UndoRule %d", news.ID)}
	if !slices.Equal(fs.ruleCalls, want) {
		t.Fatalf("calls = %v, want %v", fs.ruleCalls, want)
	}
	if !fs.rules[0].Paused {
		t.Error("the rule is not paused")
	}
	inbox := subjectsIn(fs, "a1", "INBOX")
	for _, subject := range []string{"Weekly news 1", "Weekly news 2", "Weekly news 3"} {
		if !slices.Contains(inbox, subject) {
			t.Errorf("%s is not back in the inbox: %v", subject, inbox)
		}
	}
	if got := subjectsIn(fs, "a1", "Newsletters"); len(got) != 0 {
		t.Errorf("Newsletters still holds %v", got)
	}
	if !tt.HasText(locales.PauseUndoDone("Newsletters", 2)) {
		t.Errorf("no word of it: %q", tt.Texts())
	}
	for _, e := range fs.ruleLog {
		if e.Rule == news.ID && e.State != mail.ActivityUndone {
			t.Errorf("%s is %s", e.Subject, e.State)
		}
	}
	// The other rules' entries stay.
	if e := fs.ruleLog[1]; e.Subject != "Your receipt #4471" || e.State != mail.ActivityFailed {
		t.Errorf("another rule's entry = %+v", e)
	}
	click(t, tt, locales.RulesTab())
	if info, _ := tt.AccessByRole(ui.RoleSwitch, locales.UseRule("Newsletters")); info.Checked || !tt.HasText(locales.RulePaused()) {
		t.Errorf("the rule's switch = %+v", info)
	}

	// A rule deleted since: undone, as there is nothing to pause. The
	// receipt's entry failed at its label, but marked it read.
	receipts := fs.rules[1]
	if err := fs.DeleteRule(t.Context(), receipts.ID); err != nil {
		t.Fatal(err)
	}
	fs.ruleCalls = nil
	a.readRules()
	click(t, tt, locales.ActivityTab())
	click(t, tt, locales.EntryActions("Your receipt #4471"))
	if got := tt.Menu(); !slices.Contains(got, locales.UndoAllNamed("Receipts")) || slices.Contains(got, locales.PauseRuleNamed("Receipts")) {
		t.Fatalf("the menu of a deleted rule's entry = %q", got)
	}
	if err := tt.ChooseMenuItem(locales.UndoAllNamed("Receipts")); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText(locales.UndoAllTitle("Receipts")) || !tt.HasText(locales.UndoAllMessage()) {
		t.Fatalf("the question: %q", tt.Texts())
	}
	click(t, tt, locales.UndoAllButton())
	if want := []string{fmt.Sprintf("PauseRule %d true", receipts.ID), fmt.Sprintf("UndoRule %d", receipts.ID)}; !slices.Equal(fs.ruleCalls, want) {
		t.Errorf("calls = %v, want %v", fs.ruleCalls, want)
	}
	if !tt.HasText(locales.UndoAllDone("Receipts", 1)) {
		t.Errorf("no word of it: %q", tt.Texts())
	}
	if m, _ := fs.find("a1", "INBOX", 11); m.Seen {
		t.Errorf("the receipt is still read: %+v", m)
	}
}

// The log narrows to a rule, where that rule can be paused and resumed,
// and to a state, and reads a page at a time.
func TestRuleLogFiltersAndPages(t *testing.T) {
	fs := loggedRules()
	for i := range 2 * ruleLogPage {
		fs.arrive("a1", "INBOX", newsletter(mail.UID(200+i), 20+i))
	}
	fs.applyPending("a1")
	fs.ruleCalls, fs.acts = nil, nil
	news := fs.rules[0]
	a, tt := openLog(t, fs)
	total := 2*ruleLogPage + 5
	if !tt.HasText(locales.PageRange(1, ruleLogPage, total)) || len(a.rules.log.entries) != ruleLogPage {
		t.Fatalf("the first page: %d entries: %q", len(a.rules.log.entries), tt.Texts())
	}
	click(t, tt, locales.NextLogPage())
	click(t, tt, locales.NextLogPage())
	if !tt.HasText(locales.PageRange(2*ruleLogPage+1, total, total)) || len(a.rules.log.entries) != 5 ||
		a.rules.log.entries[4].Subject != "Weekly news 2" {
		t.Fatalf("the last page: %q", tt.Texts())
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.NextLogPage()); !info.Disabled {
		t.Error("a page after the last")
	}
	click(t, tt, locales.PreviousLogPage())
	if !tt.HasText(locales.PageRange(ruleLogPage+1, 2*ruleLogPage, total)) {
		t.Errorf("back a page: %q", tt.Texts())
	}

	choose(t, tt, locales.LogStateFilter(), locales.StateFailed())
	if got := ruleRows(tt); !slices.Equal(got, []string{"Your receipt #4471"}) {
		t.Fatalf("failed entries = %q", got)
	}
	choose(t, tt, locales.LogStateFilter(), locales.AnyState())
	choose(t, tt, locales.LogRuleFilter(), "Team list")
	if got := ruleRows(tt); !slices.Equal(got, []string{"Team sync notes"}) {
		t.Fatalf("Team list's entries = %q", got)
	}
	choose(t, tt, locales.LogRuleFilter(), "Newsletters")
	if len(a.rules.log.entries) != ruleLogPage || !tt.HasText(locales.PageRange(1, ruleLogPage, 2*ruleLogPage+3)) {
		t.Fatalf("Newsletters' entries: %q", tt.Texts())
	}
	click(t, tt, locales.PauseRule())
	if !slices.Equal(fs.ruleCalls, []string{fmt.Sprintf("PauseRule %d true", news.ID)}) || !tt.HasText(locales.RulePausedNote("Newsletters")) {
		t.Fatalf("paused from the log: %v: %q", fs.ruleCalls, tt.Texts())
	}
	click(t, tt, locales.ResumeRule())
	if fs.rules[0].Paused || !tt.HasText(locales.RuleResumedNote("Newsletters")) {
		t.Errorf("resumed from the log: %+v: %q", fs.rules[0], tt.Texts())
	}
	if len(mailActs(fs)) != 0 {
		t.Errorf("pausing changed mail: %v", mailActs(fs))
	}
	// An entry's menu narrows the log to its rule.
	choose(t, tt, locales.LogRuleFilter(), locales.AllRules())
	choose(t, tt, locales.LogStateFilter(), locales.StateFailed())
	click(t, tt, locales.EntryActions("Your receipt #4471"))
	if err := tt.ChooseMenuItem(locales.ShowRuleOnly("Receipts")); err != nil {
		t.Fatal(err)
	}
	if got := ruleRows(tt); !slices.Equal(got, []string{"Your receipt #4471"}) {
		t.Errorf("Receipts' entries = %q", got)
	}
}

// ruleNames are the names of the fake's rules, in their order.
func ruleNames(fs *fakeService) []string {
	var out []string
	for _, r := range fs.rules {
		out = append(out, r.Name)
	}
	return out
}

// dragRow drags a row of a list by its name to below another's middle.
func dragRow(t *testing.T, tt *ui.Tester, from, below string) {
	t.Helper()
	src, ok := tt.Find(from)
	dst, ok2 := tt.Find(below)
	if !ok || !ok2 {
		t.Fatalf("no %q or %q: %q", from, below, tt.Texts())
	}
	x, y := src.X+src.W/2, src.Y+src.H/2
	toY := dst.Y + dst.H + 4
	tt.Press(x, y)
	for k := 1; k <= 4; k++ {
		tt.Move(x, y+(toY-y)*float32(k)/4)
	}
	tt.Release(x, toY)
}

// The rules move up and down with their buttons, which keep the keyboard
// as the rule moves, Enter pressing them, and with the mouse, a row
// dragged to another place; each move is the engine's order at once.
// The switch pauses a rule and has it sort mail again. Deleting a rule
// asks first and says its log stays, which it does.
func TestRuleListOrderPauseAndDelete(t *testing.T) {
	fs := loggedRules()
	news, receipts, team := fs.rules[0], fs.rules[1], fs.rules[2]
	_, tt := rulesApp(t, fs)
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.MoveRuleUp("Newsletters")); !info.Disabled {
		t.Error("the first rule moves up")
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.MoveRuleDown("Team list")); !info.Disabled {
		t.Error("the last rule moves down")
	}
	click(t, tt, locales.MoveRuleUp("Team list"))
	if got := ruleNames(fs); !slices.Equal(got, []string{"Newsletters", "Team list", "Receipts"}) || !slices.Equal(ruleRows(tt), got) {
		t.Fatalf("moved up: %v, the list %q", got, ruleRows(tt))
	}
	if !tt.Focused(locales.MoveRuleUp("Team list")) {
		t.Fatal("the keyboard did not stay on the button")
	}
	tt.Key(0, ui.KeyEnter)
	if got := ruleNames(fs); !slices.Equal(got, []string{"Team list", "Newsletters", "Receipts"}) {
		t.Fatalf("Enter: %v", got)
	}
	if !tt.Focused(locales.MoveRuleDown("Team list")) {
		t.Error("at the top, the keyboard is not on the button that moves it down")
	}
	want := []string{fmt.Sprintf("MoveRule %d 1", team.ID), fmt.Sprintf("MoveRule %d 0", team.ID)}
	if !slices.Equal(fs.ruleCalls, want) {
		t.Errorf("calls = %v, want %v", fs.ruleCalls, want)
	}

	fs.ruleCalls = nil
	dragRow(t, tt, "Team list", "Receipts")
	if got := ruleNames(fs); !slices.Equal(got, []string{"Newsletters", "Receipts", "Team list"}) || !slices.Equal(ruleRows(tt), got) {
		t.Fatalf("dragged: %v, the list %q, calls %v", got, ruleRows(tt), fs.ruleCalls)
	}
	if !slices.Equal(fs.ruleCalls, []string{fmt.Sprintf("MoveRule %d 2", team.ID)}) {
		t.Errorf("calls = %v", fs.ruleCalls)
	}

	fs.ruleCalls = nil
	clickRole(t, tt, ui.RoleSwitch, locales.UseRule("Receipts"))
	if i, _ := fs.fakeRule(receipts.ID); !fs.rules[i].Paused || !tt.HasText(locales.RulePaused()) {
		t.Fatalf("switched off: %+v: %q", fs.rules[i], tt.Texts())
	}
	clickRole(t, tt, ui.RoleSwitch, locales.UseRule("Receipts"))
	if i, _ := fs.fakeRule(receipts.ID); fs.rules[i].Paused || tt.HasText(locales.RulePaused()) {
		t.Fatalf("switched on: %+v", fs.rules[i])
	}
	if want := []string{fmt.Sprintf("PauseRule %d true", receipts.ID), fmt.Sprintf("PauseRule %d false", receipts.ID)}; !slices.Equal(fs.ruleCalls, want) {
		t.Errorf("calls = %v", fs.ruleCalls)
	}

	click(t, tt, locales.DeleteRule("Newsletters"))
	for _, text := range []string{locales.DeleteRuleTitle("Newsletters"), locales.DeleteRuleMessage()} {
		if !tt.HasText(text) {
			t.Fatalf("the question lacks %q: %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Cancel())
	if len(fs.rules) != 3 {
		t.Fatalf("cancelled, the rule is gone")
	}
	click(t, tt, locales.DeleteRule("Newsletters"))
	click(t, tt, locales.DeleteRuleButton())
	if got := ruleRows(tt); !slices.Equal(got, []string{"Receipts", "Team list"}) {
		t.Fatalf("deleted: %q", got)
	}
	if !tt.Focused(locales.DeleteRule("Receipts")) {
		t.Error("the keyboard is not on the next rule")
	}
	if len(mailActs(fs)) != 0 {
		t.Errorf("the list changed mail: %v", mailActs(fs))
	}
	// Its log stays, to be undone.
	click(t, tt, locales.ActivityTab())
	if !tt.HasText("Weekly news 2") || !tt.HasText(locales.EntryLine(shopNews.Name, "Newsletters", "Mar 2")) {
		t.Errorf("the deleted rule's log: %q", tt.Texts())
	}
	click(t, tt, locales.UndoEntry("Weekly news 2"))
	if got := subjectsIn(fs, "a1", "INBOX"); !slices.Contains(got, "Weekly news 2") {
		t.Errorf("the deleted rule's entry was not undone: %v", got)
	}
	_ = news
}

// What the rules did in the background reaches the window as the engine
// reports it: the log on screen reads it, the preview of the rule being
// written too, and so does the list of mail behind them.
func TestRuleActivityReadsTheWindowAgain(t *testing.T) {
	fs := rulesService()
	fs.addRule(newsRule())
	a, tt := openLog(t, fs)
	if len(ruleRows(tt)) != 0 || !tt.HasText(locales.RuleLogEmpty()) {
		t.Fatalf("an empty log: %q", tt.Texts())
	}
	pages := fs.pageReads
	fs.arrive("a1", "INBOX", newsletter(10, 2))
	fs.applyPending("a1")
	tt.Frame()
	if got := ruleRows(tt); !slices.Equal(got, []string{"Weekly news 2"}) || !tt.HasText(locales.StateApplied()) {
		t.Fatalf("the log after the report: %q", tt.Texts())
	}
	if fs.pageReads == pages || slices.Contains(rowSubjects(a), "Weekly news 2") {
		t.Errorf("the list was not read again: %v", rowSubjects(a))
	}

	// The preview of the rule being written follows the mail.
	click(t, tt, locales.RulesTab())
	click(t, tt, locales.NewRule())
	tt.Type("Shop")
	typeIn(t, tt, locales.ValueOf(1), "news@shop.example")
	choose(t, tt, locales.ActionOf(1), locales.KindStar())
	if !tt.HasText(locales.RulePreviewCount(1)) {
		t.Fatalf("the preview: %q", tt.Texts())
	}
	putMail(fs, "a1", "INBOX", newsletter(11, 3))
	fs.ruleReport("a1")
	tt.Frame()
	if !tt.HasText(locales.RulePreviewCount(2)) {
		t.Errorf("the preview after the report: %q", tt.Texts())
	}
	// A report of an account the app does not have changes nothing.
	fs.ruleReport("gone")
}

// A rule is made from a message, from the list's menu or from a message
// of a conversation: for its mailing list where the cache has its
// List-Id, else for its sender, moving its mail to a folder still to
// choose. Closing the editor goes back to the mail.
func TestMakeARuleFromAMessage(t *testing.T) {
	fs := rulesService()
	m, _ := fs.find("a1", "INBOX", 1)
	fs.bodies[bodyKey("a1", "INBOX", 1)] = mail.Message{Summary: m, BodyText: "This week.", BodyCached: true,
		RawHeaders: "Delivered-To: ann@example.com\r\nList-Id: Shop News <news.shop.example>\r\n"}
	a := openApp(fs)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.RightClick("Lunch on Friday?"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.MakeRuleEllipsis()); err != nil {
		t.Fatal(err)
	}
	ed := a.rules.editor
	if !a.rules.open || ed == nil || ed.name != "Bob Lee" || len(ed.conds) != 1 || ed.conds[0].field != mail.CondSender ||
		ed.conds[0].value != "bob@example.org" || len(ed.acts) != 1 || ed.acts[0].kind != mail.ActionMove || ed.acts[0].account != "a1" {
		t.Fatalf("the rule made = %+v", ed)
	}
	if !tt.HasText(locales.RulePreviewCount(1)) || !tt.HasText(locales.RuleChooseFolderPlaceholder()) {
		t.Errorf("the editor: %q", tt.Texts())
	}
	click(t, tt, locales.CancelRuleEditor())
	if a.rules.open || !tt.HasText(locales.Inbox()) {
		t.Fatalf("closed, the editor did not go back to the mail: %q", tt.Texts())
	}
	if len(fs.ruleCalls) != 0 || len(mailActs(fs)) != 0 {
		t.Errorf("making a rule from a message did something: %v %v", fs.ruleCalls, mailActs(fs))
	}

	// A message of a mailing list, as the cache keeps its List-Id.
	if err := tt.RightClick("Weekly news 1"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.MakeRuleEllipsis()); err != nil {
		t.Fatal(err)
	}
	if c := a.rules.editor.conds[0]; c.field != mail.CondHeader || c.header != "List-Id" || c.value != "news.shop.example" {
		t.Fatalf("the condition of a list's message = %+v", c)
	}
	click(t, tt, locales.CancelRuleEditor())

	// A message of a conversation, from its own menu.
	fs.settings.Conversations = true
	c := openApp(fs)
	ct := ui.NewTester(c.view, 1220, 800)
	click(t, ct, "Invoice for March")
	click(t, ct, locales.ForMessage(locales.MoreActions(), 1, 1))
	if err := ct.ChooseMenuItem(locales.MakeRuleEllipsis()); err != nil {
		t.Fatal(err)
	}
	if ed := c.rules.editor; ed == nil || ed.name != "Billing" || ed.conds[0].value != "billing@example.net" {
		t.Fatalf("the rule made from a conversation's message = %+v", ed)
	}
}

// A rule of the list opens in the editor as it was saved, and saves as
// changed, in its place; its preview, a page at a time.
func TestEditARuleAndPageItsPreview(t *testing.T) {
	fs := rulesService()
	for i := range 2*rulePreviewPage + 5 {
		putMail(fs, "a1", "INBOX", newsletter(mail.UID(100+i), 2+i%20))
	}
	news := fs.addRule(newsRule())
	a, tt := rulesApp(t, fs)
	click(t, tt, locales.EditRule("Newsletters"))
	ed := a.rules.editor
	if ed.id != news.ID || ed.name != "Newsletters" || ed.conds[0].value != "news@shop.example" || ed.acts[0].folder != "Newsletters" {
		t.Fatalf("the editor = %+v", ed)
	}
	if info, _ := tt.AccessByRole(ui.RoleButton, locales.SaveRule()); !info.Disabled {
		t.Error("an unchanged rule can be saved")
	}
	total := 2*rulePreviewPage + 6
	if !tt.HasText(locales.RulePreviewCount(total)) || !tt.HasText(locales.PageRange(1, rulePreviewPage, total)) ||
		len(ed.preview.Messages) != rulePreviewPage {
		t.Fatalf("the preview's first page: %q", tt.Texts())
	}
	click(t, tt, locales.NextPreviewPage())
	click(t, tt, locales.NextPreviewPage())
	if !tt.HasText(locales.PageRange(2*rulePreviewPage+1, total, total)) || len(ed.preview.Messages) != 6 ||
		ed.preview.Messages[5].Subject != "Weekly news 1" {
		t.Fatalf("the last page: %q", tt.Texts())
	}
	click(t, tt, locales.PreviousPreviewPage())
	if ed.offset != rulePreviewPage {
		t.Errorf("back a page: offset %d", ed.offset)
	}

	click(t, tt, "any of these")
	click(t, tt, locales.AddCondition())
	choose(t, tt, locales.ConditionOf(2), locales.RuleFieldAttachment())
	clickRole(t, tt, ui.RoleCheckBox, locales.RuleStop())
	click(t, tt, locales.SaveRule())
	if len(fs.rules) != 1 {
		t.Fatalf("a rule edited is another: %+v", fs.rules)
	}
	r := fs.rules[0]
	if r.ID != news.ID || r.Match != mail.MatchAny || !r.Stop || len(r.Conditions) != 2 || r.Conditions[1].Field != mail.CondAttachment {
		t.Errorf("saved as %+v", r)
	}
	// Any of the two finds the invoice, which has attachments, too.
	if !tt.HasText(locales.RulePreviewCount(total + 1)) {
		t.Errorf("the preview of any: %q", tt.Texts())
	}
	if len(mailActs(fs)) != 0 {
		t.Errorf("editing changed mail: %v", mailActs(fs))
	}
}

// The live checks of the rule being written take turns, and only the
// last one asked for answers the editor: one a later change overtook
// does not run, so a slow check of what was typed before never shows over
// the newer one.
func TestRuleChecksTakeTurns(t *testing.T) {
	fs := rulesService()
	a, tt := rulesApp(t, fs)
	click(t, tt, locales.NewRule())
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	tt.Type("Shop")
	typeIn(t, tt, locales.ValueOf(1), "news")
	choose(t, tt, locales.ActionOf(1), locales.KindStar())
	if len(jobs) < 3 {
		t.Fatalf("%d jobs for the changes", len(jobs))
	}
	// The last change's job runs first; the ones before, late, do not.
	for i := len(jobs) - 1; i >= 0; i-- {
		jobs[i]()
	}
	tt.Frame()
	if !tt.HasText(locales.RulePreviewCount(1)) || a.rules.editor.fault.text != "" {
		t.Errorf("after the late jobs: fault %q: %q", a.rules.editor.fault.text, tt.Texts())
	}
	// A check that answers after the editor closed changes nothing.
	jobs = nil
	typeIn(t, tt, locales.ValueOf(1), "@shop.example")
	click(t, tt, locales.CancelRuleEditor())
	for _, job := range jobs {
		job()
	}
	if a.rules.editor != nil {
		t.Error("a late check opened the editor again")
	}
}

// What the store or the engine fails at is said where it happened: the
// rules not read, the log not read, a rule not checked or saved, a run
// or an undo that failed.
func TestRuleFailures(t *testing.T) {
	fs := loggedRules()
	fs.ruleErr = errFake
	a, tt := rulesApp(t, fs)
	if !tt.HasText(locales.RulesReadFailed(errFake.Error())) || tt.HasText(locales.RulesEmpty()) {
		t.Fatalf("rules not read: %q", tt.Texts())
	}
	click(t, tt, locales.ActivityTab())
	if !tt.HasText(locales.RuleLogReadFailed(errFake.Error())) || tt.HasText(locales.RuleLogEmpty()) {
		t.Fatalf("the log not read: %q", tt.Texts())
	}
	click(t, tt, locales.RulesTab())
	click(t, tt, locales.NewRule())
	tt.Type("Shop")
	if !tt.HasText(locales.RuleCheckFailed(errFake.Error())) {
		t.Errorf("a rule not checked: %q", tt.Texts())
	}
	click(t, tt, locales.SaveRule())
	if !tt.HasText(locales.RuleSaveFailed(errFake.Error())) {
		t.Errorf("a rule not saved: %q", tt.Texts())
	}
	click(t, tt, locales.CancelRuleEditor())

	fs.ruleErr = nil
	a.readRules()
	a.readLog()
	tt.Frame()
	click(t, tt, locales.EditRule("Newsletters"))
	putMail(fs, "a1", "INBOX", newsletter(30, 9))
	a.checkRuleEditor(0)
	tt.Frame()
	fs.ruleErr = errFake
	click(t, tt, locales.ApplyRuleNow(2))
	click(t, tt, locales.ApplyRuleButton(2))
	if !tt.HasText(locales.RuleRunFailed(errFake.Error())) {
		t.Errorf("a run that failed: %q", tt.Texts())
	}
	fs.ruleErr = nil
	click(t, tt, locales.CloseRuleEditor())
	click(t, tt, locales.ActivityTab())
	fs.undoErr = mail.ErrRuleMessageGone
	click(t, tt, locales.UndoEntry("Weekly news 2"))
	if !tt.HasText(locales.UndoEntryFailed("Weekly news 2", locales.RuleMessageGone())) {
		t.Errorf("an undo that failed: %q", tt.Texts())
	}
	click(t, tt, locales.EntryActions("Weekly news 2"))
	if err := tt.ChooseMenuItem(locales.PauseAndUndoNamed("Newsletters")); err != nil {
		t.Fatal(err)
	}
	click(t, tt, locales.PauseUndoButton())
	if !tt.HasText(locales.UndoAllFailed("Newsletters", 0, locales.RuleMessageGone())) || !fs.rules[0].Paused {
		t.Errorf("a pause whose undo failed: %q", tt.Texts())
	}
}

// The words of the rules are in Simplified Chinese too, and the view
// says them so.
func TestRulesStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"rules": zh.Rules(), "mailRules": zh.MailRules(), "rulesIntro": zh.RulesIntro(), "newRule": zh.NewRule(),
		"rulesEmpty": zh.RulesEmpty(), "useRule": zh.UseRule("x"), "moveRuleUp": zh.MoveRuleUp("x"), "deleteRuleTitle": zh.DeleteRuleTitle("x"),
		"deleteRuleMessage": zh.DeleteRuleMessage(), "ruleWhen": zh.RuleWhen(), "matchAll": zh.MatchAll(), "matchAny": zh.MatchAny(),
		"ruleThen": zh.RuleThen(), "ruleStop": zh.RuleStop(), "ruleNeverSends": zh.RuleNeverSends(), "ruleTrashWarning": zh.RuleTrashWarning(),
		"rulePreviewCount": zh.RulePreviewCount(3), "ruleNewMailOnly": zh.RuleNewMailOnly(), "applyRuleNow": zh.ApplyRuleNow(3),
		"applyRuleTitle": zh.ApplyRuleTitle("x", 3), "ruleLogIntro": zh.RuleLogIntro(), "statePending": zh.StatePending(),
		"stateApplied": zh.StateApplied(), "stateFailed": zh.StateFailed(), "stateUndone": zh.StateUndone(),
		"pauseUndoTitle": zh.PauseUndoTitle("x"), "pauseUndoMessage": zh.PauseUndoMessage(), "pauseUndoDone": zh.PauseUndoDone("x", 2),
		"makeRuleEllipsis": zh.MakeRuleEllipsis(), "ruleOneAccount": zh.RuleOneAccount("x"), "ruleHeaderInvalid": zh.RuleHeaderInvalid(),
		"kindMove": zh.KindMove(), "kindAddLabel": zh.KindAddLabel(), "kindRemoveLabel": zh.KindRemoveLabel(), "kindStar": zh.KindStar(),
		"kindMarkRead": zh.KindMarkRead(), "kindMarkUnread": zh.KindMarkUnread(), "kindSkipNotification": zh.KindSkipNotification(),
		"ruleFieldRecipients": zh.RuleFieldRecipients(), "ruleFieldHeader": zh.RuleFieldHeader(),
	} {
		if !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}

	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a := openApp(loggedRules())
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	clickMore(t, tt, zh.Rules())
	for _, text := range []string{zh.MailRules(), zh.RulesIntro(), zh.RuleSummary(zh.CondSenderText("news@shop.example"), zh.ActMoveText("Newsletters"))} {
		if !tt.HasText(text) {
			t.Errorf("the rules in zh-Hans lack %q: %q", text, tt.Texts())
		}
	}
	click(t, tt, zh.ActivityTab())
	for _, text := range []string{zh.StateApplied(), zh.StatePending(), zh.StateFailed(), zh.StateUndone(), zh.RuleLogIntro()} {
		if !tt.HasText(text) {
			t.Errorf("the log in zh-Hans lacks %q: %q", text, tt.Texts())
		}
	}
}

// within reports whether a control is wholly inside a window of a size.
func within(tt *ui.Tester, name string, w, h float32) bool {
	info, ok := tt.Access(name)
	b := info.Bounds
	return ok && b.W > 0 && b.X >= 0 && b.Y >= 0 && b.X+b.W <= w && b.Y+b.H <= h
}

// At the main window's smallest size, in both appearances, the sidebar's
// menu is in the title bar and opens the rules; the controls of the list,
// the editor with its preview and its offer, and the log are inside the
// window.
func TestRulesAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		fs := loggedRules()
		putMail(fs, "a1", "INBOX", newsletter(40, 8))
		a := openApp(fs)
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		if !within(tt, locales.More(), 1000, 640) {
			t.Fatalf("dark=%v: the sidebar's menu is not in the title bar", dark)
		}
		clickMore(t, tt, locales.Rules())
		for _, label := range []string{locales.NewRule(), locales.Done(), locales.UseRule("Team list"), locales.MoveRuleUp("Team list"),
			locales.MoveRuleDown("Newsletters"), locales.EditRule("Team list"), locales.DeleteRule("Team list"), locales.ActivityTab()} {
			if !within(tt, label, 1000, 640) {
				t.Errorf("dark=%v: %q is not in the window", dark, label)
			}
		}
		click(t, tt, locales.EditRule("Newsletters"))
		if !within(tt, locales.ApplyRuleNow(2), 1000, 640) || !within(tt, locales.CloseRuleEditor(), 1000, 640) {
			t.Errorf("dark=%v: the offer to apply the rule is not in the window: %q", dark, tt.Texts())
		}
		click(t, tt, locales.AddCondition())
		choose(t, tt, locales.ConditionOf(2), locales.RuleFieldHeader())
		for _, label := range []string{locales.SaveRule(), locales.CancelRuleEditor(), locales.RuleName(), locales.HeaderOf(2),
			locales.ValueOf(2), locales.RemoveCondition(2), locales.ActionFolderOf(1), locales.AddAction()} {
			if !within(tt, label, 1000, 640) {
				t.Errorf("dark=%v: %q of the editor is not in the window", dark, label)
			}
		}
		click(t, tt, locales.CancelRuleEditor())
		click(t, tt, locales.ActivityTab())
		for _, label := range []string{locales.UndoEntry("Weekly news 2"), locales.EntryActions("Weekly news 2"), locales.LogStateFilter()} {
			if !within(tt, label, 1000, 640) {
				t.Errorf("dark=%v: %q of the log is not in the window", dark, label)
			}
		}
	}
}

// Mail that arrives in the folder the window shows under the rules is
// notified: the rules cover the list, so nobody sees it come.
func TestNotifiedWhileTheRulesShow(t *testing.T) {
	w := startWatchApp(t)
	w.focused = true
	w.arrive(t, "a1", newMail(20, "Sam", "Seen as it came"))
	if len(w.notes) != 0 {
		t.Fatalf("notified of mail that came before the user's eyes: %+v", w.notes)
	}
	w.a.openRules(rulesTabList)
	w.arrive(t, "a1", newMail(21, "Kim", "Under the rules"))
	if len(w.notes) != 1 {
		t.Errorf("mail that came under the rules was not notified: %+v", w.notes)
	}
}

// putMail puts messages in a folder of the fake, as a sync that sorts
// nothing, the first of a folder say, caches them.
func putMail(fs *fakeService, account, folder string, msgs ...mail.Summary) {
	key := listKey(account, folder)
	for _, m := range msgs {
		m.Account, m.Folder = account, folder
		fs.messages[key] = append(fs.messages[key], m)
	}
	sortSummaries(fs.messages[key])
	fs.recount(account, folder)
}

// The rules open in a window of their own, with their title and room for
// the editor and its preview; closing it goes back to the mail.
func TestRulesOpenTheirOwnWindow(t *testing.T) {
	a := openApp(rulesService())
	var got mygo.WindowOptions
	calls := 0
	a.newWindow = func(o mygo.WindowOptions) *mygo.Window {
		calls++
		got = o
		return nil
	}
	a.openRules(rulesTabList)
	if calls != 1 {
		t.Fatalf("windows = %d", calls)
	}
	if got.Title != locales.MailRules() || got.Width < 900 || got.Height < 560 || got.Content == nil {
		t.Errorf("window = %+v", got)
	}
	if !a.rules.open {
		t.Error("the rules did not open")
	}
	a.closeRules()
	if a.rules.open {
		t.Error("closeRules left the rules open")
	}
}

// Without a window, the rules show in the main view, which is how the
// tests without a window read them.
func TestRulesWithoutAWindow(t *testing.T) {
	a := openApp(rulesService())
	a.newWindow = nil
	a.openRules(rulesTabList)
	if !a.rules.open {
		t.Fatal("the rules did not open")
	}
	if a.rulesWin != nil {
		t.Error("a window appeared without newWindow")
	}
}
