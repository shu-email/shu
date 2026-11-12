package mail

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// Fetch records the header fields a fetch asks for.
func (s *fakeSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	for _, section := range options.BodySection {
		if len(section.HeaderFields) > 0 {
			s.f.mu.Lock()
			s.f.headerFetches = append(s.f.headerFetches, slices.Clone(section.HeaderFields))
			s.f.mu.Unlock()
		}
	}
	return s.SessionIMAP4rev2.Fetch(w, numSet, options)
}

// fetchesOf counts the fetches that asked for a header field.
func (f *fakeIMAP) fetchesOf(field string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fields := range f.headerFetches {
		if slices.Contains(fields, field) {
			n++
		}
	}
	return n
}

// rulesSettled waits until no actions of rules are applied in the
// background.
func (s *Service) rulesSettled() {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	for len(s.ruleRunning) > 0 {
		s.ruleIdle.Wait()
	}
}

// ruleNow is the time of the tests of rules.
var ruleNow = time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)

// ruleMail is a message for the tests of rules, minutes after nine; one
// without an id has no Message-ID, and extra is more header lines.
func ruleMail(subject, from, id, extra string, minutes int) []byte {
	date := time.Date(2026, 3, 2, 9, minutes, 0, 0, time.UTC).Format(time.RFC1123Z)
	head := "From: " + from + "\r\nTo: Ann <ann@example.com>\r\nSubject: " + subject + "\r\nDate: " + date + "\r\n"
	if id != "" {
		head += "Message-ID: <" + id + ">\r\n"
	}
	return []byte(head + extra + "\r\nHello.\r\n")
}

// news is a newsletter of the tests, n of a series.
func news(subject, id string, minutes int) []byte {
	return ruleMail(subject, "Shop News <news@shop.example>", id, "", minutes)
}

// ruleRig is a service with an account on a fake server whose folders are
// INBOX, Newsletters, Lists, Archive, Trash and Drafts.
type ruleRig struct {
	t       *testing.T
	ctx     context.Context
	f       *fakeIMAP
	secrets *memSecrets
	reopen  func() Store
	svc     *Service
	a       Account
}

func newRuleRig(t *testing.T, caps imap.CapSet) *ruleRig {
	t.Helper()
	store := newMemStore()
	return newRuleRigOn(t, caps, func() Store { return store })
}

// newRuleRigOn makes a rig on the stores reopen gives: the same one, or
// the database opened anew, as the app finds it as it starts.
func newRuleRigOn(t *testing.T, caps imap.CapSet, reopen func() Store) *ruleRig {
	t.Helper()
	f := startFakeIMAP(t, caps)
	f.create(t, "INBOX", "Newsletters", "Lists", "Archive", "Trash", "Drafts")
	r := &ruleRig{t: t, ctx: context.Background(), f: f, secrets: newMemSecrets(), reopen: reopen, a: fakeAccount(t, f.addr)}
	r.restart()
	if err := r.svc.AddAccount(r.ctx, r.a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SyncAccount(r.ctx, r.a.ID); err != nil {
		t.Fatal(err)
	}
	return r
}

// restart starts a service anew on the rig's store, as the app does.
func (r *ruleRig) restart() {
	if r.svc != nil {
		r.svc.StopWatching()
	}
	r.svc = NewService(r.reopen(), r.secrets, "")
	r.svc.SetTLSConfig(insecureTLS())
	r.svc.SetClock(func() time.Time { return ruleNow })
	r.t.Cleanup(r.svc.StopWatching)
}

func (r *ruleRig) deliver(mailbox string, raw []byte) { r.f.append(r.t, mailbox, raw) }

// sync syncs folders, and waits for the actions their mail brought.
func (r *ruleRig) sync(folders ...string) {
	r.t.Helper()
	for _, folder := range folders {
		if err := r.svc.SyncFolder(r.ctx, r.a.ID, folder, 50); err != nil {
			r.t.Fatalf("sync %s: %v", folder, err)
		}
	}
	r.svc.rulesSettled()
}

func (r *ruleRig) save(rule Rule) Rule {
	r.t.Helper()
	saved, err := r.svc.SaveRule(r.ctx, rule)
	if err != nil {
		r.t.Fatalf("save %q: %v", rule.Name, err)
	}
	return saved
}

// newsRule is a rule of the rig's account that moves the shop's mail to
// a folder.
func (r *ruleRig) newsRule(folder string) Rule {
	return Rule{
		Name:       "Newsletters",
		Conditions: []Condition{{Field: CondSender, Value: "news@shop.example"}},
		Actions:    []Action{{Kind: ActionMove, Account: r.a.ID, Folder: folder}},
	}
}

// onServer lists the subjects of a mailbox of the server, sorted.
func (r *ruleRig) onServer(mailbox string) []string {
	r.t.Helper()
	out := []string{}
	for _, m := range r.f.messages(r.t, mailbox) {
		out = append(out, m.Subject)
	}
	slices.Sort(out)
	return out
}

// serverMessage returns the message of a subject in a mailbox of the server.
func (r *ruleRig) serverMessage(mailbox, subject string) serverMessage {
	r.t.Helper()
	for _, m := range r.f.messages(r.t, mailbox) {
		if m.Subject == subject {
			return m
		}
	}
	r.t.Fatalf("no %q on the server in %s", subject, mailbox)
	return serverMessage{}
}

// cached lists the subjects of a folder of the cache, sorted.
func (r *ruleRig) cached(folder string) []string {
	r.t.Helper()
	msgs, err := r.svc.store.MessageHeaders(r.ctx, r.a.ID, folder, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	out := []string{}
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	slices.Sort(out)
	return out
}

// message returns the cached message of a subject in a folder.
func (r *ruleRig) message(folder, subject string) Message {
	r.t.Helper()
	msgs, err := r.svc.store.MessageHeaders(r.ctx, r.a.ID, folder, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Subject == subject {
			return m
		}
	}
	r.t.Fatalf("no %q cached in %s", subject, folder)
	return Message{}
}

// validity is the UIDVALIDITY the cache has of a folder.
func (r *ruleRig) validity(folder string) uint32 {
	r.t.Helper()
	f, ok, err := r.svc.store.Folder(r.ctx, r.a.ID, folder)
	if err != nil || !ok {
		r.t.Fatalf("folder %s: %v, %v", folder, ok, err)
	}
	return f.UIDValidity
}

func (r *ruleRig) log(q ActivityQuery) []RuleActivity {
	r.t.Helper()
	page, err := r.svc.RuleActivity(r.ctx, q)
	if err != nil {
		r.t.Fatal(err)
	}
	return page.Entries
}

// states lists the subjects and states of the log's entries, oldest first.
func (r *ruleRig) states(q ActivityQuery) []string {
	r.t.Helper()
	var out []string
	for _, e := range slices.Backward(r.log(q)) {
		out = append(out, e.Subject+": "+string(e.State))
	}
	return out
}

// A rule saved sorts the mail that arrives after, and none of the mail
// that was there: a newsletter that arrives goes to its folder, the one
// cached before stays, and the match is logged with what it changed.
func TestRulesSortTheMailThatArrives(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.deliver("INBOX", news("Weekly news 1", "n1@shop.example", 1))
	r.deliver("INBOX", ruleMail("Lunch?", "Bob <bob@example.org>", "l1@example.org", "", 2))
	r.sync("INBOX")
	rule := r.save(r.newsRule("Newsletters"))
	r.sync("INBOX")
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Lunch?", "Weekly news 1"}) || len(r.log(ActivityQuery{})) != 0 {
		t.Fatalf("a sync with nothing new: INBOX holds %v, the log %+v", got, r.log(ActivityQuery{}))
	}

	heard := make(chan string, 8)
	r.svc.OnRuleActivity(func(accountID string) { heard <- accountID })
	r.deliver("INBOX", news("Weekly news 2", "n2@shop.example", 3))
	r.deliver("INBOX", ruleMail("Minutes", "Carol <carol@example.org>", "m1@example.org", "", 4))
	r.sync("INBOX")
	select {
	case id := <-heard:
		if id != r.a.ID {
			t.Errorf("the window heard of %q", id)
		}
	default:
		t.Error("the window did not hear the rule's actions were applied")
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Lunch?", "Minutes", "Weekly news 1"}) {
		t.Errorf("INBOX holds %v", got)
	}
	if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"Weekly news 2"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if got := r.cached("INBOX"); !slices.Equal(got, []string{"Lunch?", "Minutes", "Weekly news 1"}) {
		t.Errorf("the cache's INBOX holds %v", got)
	}
	entries := r.log(ActivityQuery{})
	if len(entries) != 1 {
		t.Fatalf("log = %+v", entries)
	}
	e := entries[0]
	want := RuleActivity{
		ID: e.ID, At: ruleNow, Rule: rule.ID, RuleName: "Newsletters", Account: r.a.ID, Folder: "INBOX",
		UIDValidity: e.UIDValidity, UID: 3, MessageID: "n2@shop.example", Key: "n2@shop.example", Subject: "Weekly news 2",
		From: Address{Name: "Shop News", Email: "news@shop.example"}, Actions: rule.Actions,
		Changes: RuleChanges{MovedTo: "Newsletters", DestUID: 1}, Quiet: true, State: ActivityApplied,
	}
	if e.UIDValidity == 0 || !e.At.Equal(ruleNow) || !reflect.DeepEqual(e, want) {
		t.Errorf("entry =\n%+v\nwant\n%+v", e, want)
	}
}

// What a first sync, FetchOlder and a sync after a UIDVALIDITY change
// cache was there before, and no rule touches it; the mail that arrives
// after each is sorted.
func TestRulesLeaveTheMailThatWasThere(t *testing.T) {
	t.Run("first sync", func(t *testing.T) {
		r := newRuleRig(t, allCaps())
		r.save(r.newsRule("Newsletters"))
		r.save(Rule{Name: "Lists", Conditions: []Condition{{Field: CondFolder, Account: r.a.ID, Folder: "Lists"}},
			Actions: []Action{{Kind: ActionStar}}})
		r.deliver("INBOX", news("Old news", "o1@shop.example", 1))
		r.deliver("Lists", ruleMail("Old post", "List <list@example.org>", "p1@example.org", "", 2))
		r.sync("INBOX", "Lists")
		if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Old news"}) || len(r.log(ActivityQuery{})) != 0 ||
			r.serverMessage("Lists", "Old post").hasFlag(imap.FlagFlagged) {
			t.Fatalf("the first sync sorted: INBOX %v, log %+v", got, r.log(ActivityQuery{}))
		}
		r.deliver("INBOX", news("New news", "o2@shop.example", 3))
		r.deliver("Lists", ruleMail("New post", "List <list@example.org>", "p2@example.org", "", 4))
		r.sync("INBOX", "Lists")
		if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"New news"}) || !r.serverMessage("Lists", "New post").hasFlag(imap.FlagFlagged) {
			t.Errorf("what arrived after: Newsletters %v; the post is starred: %v", got, r.serverMessage("Lists", "New post").hasFlag(imap.FlagFlagged))
		}
	})

	t.Run("older mail", func(t *testing.T) {
		r := newRuleRig(t, allCaps())
		r.save(Rule{Name: "Lists", Conditions: []Condition{{Field: CondFolder, Account: r.a.ID, Folder: "Lists"}},
			Actions: []Action{{Kind: ActionStar}}})
		for i, subject := range []string{"Post 1", "Post 2", "Post 3", "Post 4"} {
			r.deliver("Lists", ruleMail(subject, "List <list@example.org>", "", "", i))
		}
		if err := r.svc.SyncFolder(r.ctx, r.a.ID, "Lists", 2); err != nil {
			t.Fatal(err)
		}
		older, err := r.svc.FetchOlder(r.ctx, r.a.ID, "Lists", 10)
		if err != nil || older.Fetched != 2 {
			t.Fatalf("older mail = %+v, %v", older, err)
		}
		r.svc.rulesSettled()
		if got := r.cached("Lists"); len(got) != 4 || len(r.log(ActivityQuery{})) != 0 {
			t.Fatalf("Lists cached %v, log %+v", got, r.log(ActivityQuery{}))
		}
		for _, m := range r.f.messages(t, "Lists") {
			if m.hasFlag(imap.FlagFlagged) {
				t.Errorf("%s was starred", m.Subject)
			}
		}
		r.deliver("Lists", ruleMail("Post 5", "List <list@example.org>", "", "", 5))
		r.sync("Lists")
		if !r.serverMessage("Lists", "Post 5").hasFlag(imap.FlagFlagged) || len(r.log(ActivityQuery{})) != 1 {
			t.Errorf("the post that arrived after: log %+v", r.log(ActivityQuery{}))
		}
	})

	t.Run("older mail of a folder synced empty", func(t *testing.T) {
		r := newRuleRig(t, allCaps())
		r.save(r.newsRule("Newsletters"))
		r.sync("INBOX")
		r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
		if _, err := r.svc.FetchOlder(r.ctx, r.a.ID, "INBOX", 10); err != nil {
			t.Fatal(err)
		}
		r.svc.rulesSettled()
		if got := r.cached("INBOX"); !slices.Equal(got, []string{"Sale"}) || len(r.log(ActivityQuery{})) != 0 {
			t.Errorf("FetchOlder cached %v and logged %v", got, r.states(ActivityQuery{}))
		}
	})

	t.Run("a folder made anew", func(t *testing.T) {
		r := newRuleRig(t, allCaps())
		r.deliver("INBOX", ruleMail("Hello", "Bob <bob@example.org>", "h1@example.org", "", 1))
		r.sync("INBOX")
		r.save(r.newsRule("Newsletters"))
		before, _, _ := r.svc.store.Folder(r.ctx, r.a.ID, "INBOX")
		if err := r.f.user.Delete("INBOX"); err != nil {
			t.Fatal(err)
		}
		r.f.create(t, "INBOX")
		r.deliver("INBOX", news("News 1", "r1@shop.example", 2))
		r.deliver("INBOX", news("News 2", "r2@shop.example", 3))
		r.sync("INBOX")
		after, _, _ := r.svc.store.Folder(r.ctx, r.a.ID, "INBOX")
		if after.UIDValidity == before.UIDValidity {
			t.Fatal("the folder kept its UIDVALIDITY")
		}
		if got := r.onServer("INBOX"); !slices.Equal(got, []string{"News 1", "News 2"}) || len(r.log(ActivityQuery{})) != 0 {
			t.Fatalf("after the reset INBOX holds %v, log %+v", got, r.log(ActivityQuery{}))
		}
		r.deliver("INBOX", news("News 3", "r3@shop.example", 4))
		r.sync("INBOX")
		if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"News 3"}) {
			t.Errorf("Newsletters holds %v", got)
		}
	})
}

// Two syncs of a folder may overlap, the one that began first writing
// last. Each message that arrived is sorted once all the same: the
// slower write leaves the cursor where the faster put it, and what it
// fetched below it arrived for the other.
func TestRulesSortEachMessageOnce(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.deliver("INBOX", ruleMail("Msg 1", "Bob <bob@example.org>", "m1@example.org", "", 1))
	r.sync("INBOX")
	r.save(Rule{Name: "Quiet", Conditions: []Condition{{Field: CondSubject, Value: "msg"}}, Actions: []Action{{Kind: ActionSkipNotification}}})
	r.deliver("INBOX", ruleMail("Msg 2", "Bob <bob@example.org>", "m2@example.org", "", 2))
	// The slower sync has fetched Msg 2 and asks for the counts again;
	// before it hears them, Msg 3 arrives and a faster sync caches both.
	r.f.afterStatus(func() {
		r.f.afterStatus(func() {
			raw := ruleMail("Msg 3", "Bob <bob@example.org>", "m3@example.org", "", 3)
			if _, err := r.f.user.Append("INBOX", appendLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{Time: ruleNow}); err != nil {
				t.Errorf("append: %v", err)
			}
			if err := r.svc.SyncFolder(r.ctx, r.a.ID, "INBOX", 50); err != nil {
				t.Errorf("the faster sync: %v", err)
			}
		})
	})
	r.sync("INBOX", "INBOX")
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Msg 2: applied", "Msg 3: applied"}) {
		t.Errorf("log = %v", got)
	}
	if f, _, _ := r.svc.store.Folder(r.ctx, r.a.ID, "INBOX"); f.LastUID != 3 {
		t.Errorf("the cursor is at %d", f.LastUID)
	}
}

// The watcher announces none of the mail a rule keeps quiet, moves out of
// the inbox or marks read; it announces the rest. The rules ran before it
// took the mail to announce, whichever sync fetched it.
func TestRulesKeepTheWatcherQuiet(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Newsletters")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", false)
	ctx := context.Background()
	if _, err := r.svc.SyncAccount(ctx, r.account.ID); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []Rule{
		{Name: "Digests", Conditions: []Condition{{Field: CondSubject, Value: "digest"}}, Actions: []Action{{Kind: ActionSkipNotification}}},
		{Name: "News", Conditions: []Condition{{Field: CondSender, Value: "news@shop"}}, Actions: []Action{{Kind: ActionMove, Account: r.account.ID, Folder: "Newsletters"}}},
		{Name: "Receipts", Conditions: []Condition{{Field: CondSubject, Value: "receipt"}}, Actions: []Action{{Kind: ActionMarkRead}}},
	} {
		if _, err := r.svc.SaveRule(ctx, rule); err != nil {
			t.Fatal(err)
		}
	}
	r.watch()
	r.checked()
	r.timer()
	deliver(t, f, "INBOX", ruleMail("Daily digest", "Forum <forum@example.org>", "d1@example.org", "", 1))
	deliver(t, f, "INBOX", news("Sale", "s1@shop.example", 2))
	deliver(t, f, "INBOX", ruleMail("Your receipt", "Shop <orders@shop.example>", "r1@shop.example", "", 3))
	deliver(t, f, "INBOX", ruleMail("Dinner", "Bob <bob@example.org>", "b1@example.org", "", 4))
	// The mail delivered before the dinner was checked by the time it is
	// announced, and the reports come in order.
	var announced []string
	r.event("the dinner", func(ev WatchEvent) bool {
		announced = append(announced, subjects(ev.New)...)
		return slices.Contains(subjects(ev.New), "Dinner")
	})
	if !slices.Equal(announced, []string{"Dinner"}) {
		t.Fatalf("announced %v, want the dinner alone", announced)
	}
	r.svc.rulesSettled()
	rr := &ruleRig{t: t, ctx: ctx, f: f, svc: r.svc, a: r.account}
	if got := rr.onServer("Newsletters"); !slices.Equal(got, []string{"Sale"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if !rr.serverMessage("INBOX", "Your receipt").hasFlag(imap.FlagSeen) || rr.serverMessage("INBOX", "Daily digest").hasFlag(imap.FlagSeen) {
		t.Error("the receipt is unread, or the digest read")
	}
	if got := rr.states(ActivityQuery{}); !slices.Equal(got, []string{"Daily digest: applied", "Sale: applied", "Your receipt: applied"}) {
		t.Errorf("log = %v", got)
	}
}

// A sync of the window can fetch new mail before the watcher does; the
// watcher, which polls here, then finds it cached and announces none of
// what the rules kept quiet.
func TestRulesKeepQuietWhatTheWindowSynced(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Newsletters")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	r := newWatchRig(t, f, "secret", true)
	ctx := context.Background()
	if _, err := r.svc.SyncAccount(ctx, r.account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.SaveRule(ctx, Rule{Name: "News", Conditions: []Condition{{Field: CondSender, Value: "news@shop"}},
		Actions: []Action{{Kind: ActionSkipNotification}}}); err != nil {
		t.Fatal(err)
	}
	r.watch()
	r.checked()
	wait := r.timer()
	deliver(t, f, "INBOX", news("Sale", "s1@shop.example", 1))
	deliver(t, f, "INBOX", ruleMail("Dinner", "Bob <bob@example.org>", "b1@example.org", "", 2))
	if err := r.svc.SyncFolder(ctx, r.account.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	wait.fire()
	if ev := r.checked(); !slices.Equal(subjects(ev.New), []string{"Dinner"}) {
		t.Fatalf("announced %v, want the dinner alone", subjects(ev.New))
	}
}

// Rules run from the cache: a server out of reach after the sync leaves
// their actions pending, and the mail is received at once all the same;
// a later sync applies them. A preview works without the server.
func TestRulesWaitForTheServer(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	rule := r.newsRule("Newsletters")
	rule.Actions = append(rule.Actions, Action{Kind: ActionStar})
	r.save(rule)
	// The actions are held back while the server goes away.
	lock := r.svc.ruleLock(r.a.ID)
	lock.Lock()
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	if err := r.svc.SyncFolder(r.ctx, r.a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	if got := r.cached("INBOX"); !slices.Equal(got, []string{"Sale"}) {
		t.Fatalf("the mail was not received at once: INBOX holds %v", got)
	}
	r.f.stop()
	lock.Unlock()
	r.svc.rulesSettled()
	entries := r.log(ActivityQuery{})
	if len(entries) != 1 || entries[0].State != ActivityPending || entries[0].Error == "" || !entries[0].Changes.empty() {
		t.Fatalf("with the server away the log is %+v", entries)
	}
	if m := r.message("INBOX", "Sale"); m.Flagged {
		t.Error("the cache shows a star the server never got")
	}
	preview, err := r.svc.PreviewRule(r.ctx, rule, 0, 10)
	if err != nil || preview.Total != 1 || preview.Messages[0].Subject != "Sale" {
		t.Errorf("offline preview = %+v, %v", preview, err)
	}

	r.f.restart(t)
	r.sync("INBOX")
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale: applied"}) {
		t.Fatalf("after the server came back the log is %v", got)
	}
	if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"Sale"}) || !r.serverMessage("Newsletters", "Sale").hasFlag(imap.FlagFlagged) {
		t.Errorf("Newsletters holds %v", got)
	}
	if got := r.cached("INBOX"); len(got) != 0 {
		t.Errorf("INBOX still holds %v", got)
	}
}

// A pending entry undone is dropped at once, without the server, and is
// not applied when the server is back; the other is.
func TestUndoAPendingEntry(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	r.save(r.newsRule("Newsletters"))
	lock := r.svc.ruleLock(r.a.ID)
	lock.Lock()
	r.deliver("INBOX", news("Sale 1", "s1@shop.example", 1))
	r.deliver("INBOX", news("Sale 2", "s2@shop.example", 2))
	if err := r.svc.SyncFolder(r.ctx, r.a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	r.f.stop()
	lock.Unlock()
	r.svc.rulesSettled()
	entries := r.log(ActivityQuery{})
	if len(entries) != 2 || entries[1].Subject != "Sale 1" || entries[1].State != ActivityPending {
		t.Fatalf("log = %v", r.states(ActivityQuery{}))
	}
	if err := r.svc.UndoActivity(r.ctx, entries[1].ID); err != nil {
		t.Fatalf("undo with the server away: %v", err)
	}
	r.f.restart(t)
	r.sync("INBOX")
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale 1: undone", "Sale 2: applied"}) {
		t.Errorf("log = %v", got)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Sale 1"}) || !slices.Equal(r.onServer("Newsletters"), []string{"Sale 2"}) {
		t.Errorf("INBOX holds %v", got)
	}
}

// An action the server refuses fails its entry, and the sync that
// brought the mail does not fail.
func TestRuleActionsTheServerRefuses(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	r.save(r.newsRule("Newsletters"))
	if err := r.f.user.Delete("Newsletters"); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	r.sync("INBOX")
	entries := r.log(ActivityQuery{})
	if len(entries) != 1 || entries[0].State != ActivityFailed || entries[0].Error == "" {
		t.Fatalf("log = %+v", entries)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Sale"}) || !slices.Equal(r.cached("INBOX"), got) {
		t.Errorf("INBOX holds %v, cached %v", got, r.cached("INBOX"))
	}
	// A failed entry is tried no more.
	r.f.create(t, "Newsletters")
	r.sync("INBOX")
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale: failed"}) || len(r.onServer("Newsletters")) != 0 {
		t.Errorf("log %v, Newsletters %v", got, r.onServer("Newsletters"))
	}
}

// A header condition has its header fetched as the mail arrives, only
// while a rule reads it, and kept with the message, also when it is
// opened; a message cached before has it once opened.
func TestRuleHeadersAreFetchedForThem(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.deliver("INBOX", ruleMail("Before", "Lists <lists@example.org>", "b@example.org", "List-Id: =?utf-8?q?Caf=C3=A9_list?= <cafe.example.org>\r\n", 1))
	r.sync("INBOX")
	lists, err := r.svc.CreateLabel(r.ctx, r.a.ID, "Lists", "")
	if err != nil {
		t.Fatal(err)
	}
	rule := r.save(Rule{Name: "Café", Conditions: []Condition{{Field: CondHeader, Header: "list-id", Value: "CAFÉ"}},
		Actions: []Action{{Kind: ActionAddLabel, Account: r.a.ID, Label: lists.ID}}})
	if r.f.fetchesOf("List-Id") != 0 {
		t.Fatal("List-Id was fetched before a rule read it")
	}
	r.deliver("INBOX", ruleMail("Encoded", "Lists <lists@example.org>", "e@example.org", "List-Id: =?utf-8?q?Caf=C3=A9?= <cafe.example.org>\r\n", 2))
	r.deliver("INBOX", ruleMail("Plain", "Lists <lists@example.org>", "p@example.org", "List-Id: Other <other.example.org>\r\n", 3))
	r.deliver("INBOX", ruleMail("None", "Bob <bob@example.org>", "n@example.org", "", 4))
	r.sync("INBOX")
	if n := r.f.fetchesOf("List-Id"); n != 1 {
		t.Errorf("List-Id was fetched %d times, want once", n)
	}
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Encoded: applied"}) {
		t.Fatalf("log = %v", got)
	}
	if got := keywordsOn(t, r.f, "INBOX"); !slices.Equal(got["Encoded"], []string{"lists"}) || len(got["Plain"]) != 0 {
		t.Errorf("the server's keywords = %v", got)
	}
	if got := r.message("INBOX", "Plain").RawHeaders; got != "List-Id: Other <other.example.org>\r\n" {
		t.Errorf("Plain keeps %q", got)
	}
	if got := r.message("INBOX", "None").RawHeaders; got != "" {
		t.Errorf("None keeps %q", got)
	}
	// Opening a message keeps what its arrival fetched, and gives one
	// cached before what the rule reads, which the preview then finds.
	for _, subject := range []string{"Plain", "Before"} {
		if _, err := r.svc.FetchMessage(r.ctx, r.a.ID, "INBOX", r.message("INBOX", subject).UID, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := headerValues(r.message("INBOX", "Plain").RawHeaders, "List-Id"); !slices.Equal(got, []string{"Other <other.example.org>"}) {
		t.Errorf("opened, Plain keeps %v", got)
	}
	preview, err := r.svc.PreviewRule(r.ctx, rule, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, m := range preview.Messages {
		found = append(found, m.Subject)
	}
	if !slices.Equal(found, []string{"Encoded", "Before"}) {
		t.Errorf("the preview finds %v", found)
	}
	// With no rule that reads it, no header is fetched.
	if _, err := r.svc.PauseRule(r.ctx, rule.ID, true); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", ruleMail("After", "Lists <lists@example.org>", "a@example.org", "List-Id: Cafe <cafe.example.org>\r\n", 5))
	r.sync("INBOX")
	if n := r.f.fetchesOf("List-Id"); n != 1 || r.message("INBOX", "After").RawHeaders != "" {
		t.Errorf("with the rule paused List-Id was fetched %d times in all", n)
	}
}

// An undo puts back what a rule did: the message goes back where it was,
// unread and unstarred as it was, with the label it lost and without the
// one it got; and the inbox, synced again, keeps it.
func TestUndoPutsBackWhatARuleDid(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	r.deliver("INBOX", news("Old sale", "o1@shop.example", 1))
	r.sync("INBOX")
	keep, err := r.svc.CreateLabel(r.ctx, r.a.ID, "Shops", "")
	if err != nil {
		t.Fatal(err)
	}
	read, err := r.svc.CreateLabel(r.ctx, r.a.ID, "To read", "")
	if err != nil {
		t.Fatal(err)
	}
	r.save(Rule{Name: "Sales", Conditions: []Condition{{Field: CondSubject, Value: "sale"}},
		Actions: []Action{{Kind: ActionAddLabel, Account: r.a.ID, Label: read.ID}}})
	r.deliver("INBOX", news("Sale", "s1@shop.example", 2))
	r.sync("INBOX")
	if err := r.svc.SetLabel(r.ctx, r.a.ID, []MessageRef{{"INBOX", r.message("INBOX", "Sale").UID}}, keep.ID, true); err != nil {
		t.Fatal(err)
	}
	// The rule after the one that labelled it moves it, stars it, marks
	// it read and takes off a label it had.
	rule := r.save(Rule{Name: "Shop", Conditions: []Condition{{Field: CondLabel, Account: r.a.ID, Label: keep.ID}},
		Actions: []Action{
			{Kind: ActionMove, Account: r.a.ID, Folder: "Newsletters"}, {Kind: ActionStar}, {Kind: ActionMarkRead},
			{Kind: ActionRemoveLabel, Account: r.a.ID, Label: keep.ID}, {Kind: ActionSkipNotification},
		}})
	target := RuleTarget{Account: r.a.ID, Folder: "INBOX", UIDValidity: r.validity("INBOX"), UID: r.message("INBOX", "Sale").UID}
	if _, err := r.svc.RunRule(r.ctx, rule.ID, []RuleTarget{target}); err != nil {
		t.Fatal(err)
	}
	moved := r.serverMessage("Newsletters", "Sale")
	if !moved.hasFlag(imap.FlagSeen) || !moved.hasFlag(imap.FlagFlagged) || !slices.Equal(keywordsOn(t, r.f, "Newsletters")["Sale"], []string{"to_read"}) {
		t.Fatalf("the rule left %v", moved.Flags)
	}
	entry := r.log(ActivityQuery{Rule: rule.ID})[0]
	if c := entry.Changes; c.MovedTo != "Newsletters" || c.DestUID == 0 || len(c.Flags) != 2 || !slices.Equal(c.LabelsRemoved, []LabelID{keep.ID}) {
		t.Fatalf("changes = %+v", c)
	}

	if err := r.svc.UndoActivity(r.ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if got := r.onServer("Newsletters"); len(got) != 0 {
		t.Errorf("Newsletters still holds %v", got)
	}
	back := r.serverMessage("INBOX", "Sale")
	if back.hasFlag(imap.FlagSeen) || back.hasFlag(imap.FlagFlagged) {
		t.Errorf("back in INBOX with %v", back.Flags)
	}
	if got := keywordsOn(t, r.f, "INBOX")["Sale"]; !slices.Equal(got, []string{"shops", "to_read"}) {
		t.Errorf("back in INBOX with the keywords %v", got)
	}
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"Sale: applied", "Sale: undone"}) {
		t.Errorf("log = %v", got)
	}
	// Undone it stays undone, and the rules leave it where it went back.
	if err := r.svc.UndoActivity(r.ctx, entry.ID); err != nil {
		t.Errorf("undone again: %v", err)
	}
	r.sync("INBOX")
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Old sale", "Sale"}) || len(r.log(ActivityQuery{})) != 2 {
		t.Errorf("synced again, INBOX holds %v and the log %v", got, r.states(ActivityQuery{}))
	}
	if err := r.svc.UndoActivity(r.ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("undo of no entry: %v", err)
	}
}

// A move whose destination UID the server did not give, or whose UID
// names another message since, is found by its Message-ID, compared
// exactly. A message without a Message-ID is found by its UID alone, and
// is left where it is when that is no longer it.
func TestUndoFindsTheMessageWhereItWent(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	r.deliver("Newsletters", news("Already there", "s1@shop.example", 1))
	r.save(r.newsRule("Newsletters"))
	r.deliver("INBOX", news("No UID", "s1@shop.example", 2))
	r.deliver("INBOX", news("Wrong UID", "s2@shop.example", 3))
	r.deliver("INBOX", news("No id", "", 4))
	r.sync("INBOX")
	// Newer than what the rule moved, a message whose Message-ID a search
	// for the moved one's finds too.
	r.deliver("Newsletters", news("Look-alike", "x-s2@shop.example", 5))
	entries := map[string]RuleActivity{}
	for _, e := range r.log(ActivityQuery{}) {
		entries[e.Subject] = e
	}
	if len(entries) != 3 {
		t.Fatalf("log = %v", r.states(ActivityQuery{}))
	}
	lookAlike := r.serverMessage("Newsletters", "Look-alike").UID
	for subject, uid := range map[string]UID{"No UID": 0, "Wrong UID": lookAlike, "No id": lookAlike} {
		e := entries[subject]
		e.Changes.DestUID = uid
		if err := r.svc.store.UpdateRuleActivity(r.ctx, []RuleActivity{e}); err != nil {
			t.Fatal(err)
		}
	}
	for _, subject := range []string{"No UID", "Wrong UID"} {
		if err := r.svc.UndoActivity(r.ctx, entries[subject].ID); err != nil {
			t.Errorf("undo %s: %v", subject, err)
		}
	}
	if err := r.svc.UndoActivity(r.ctx, entries["No id"].ID); !errors.Is(err, ErrRuleMessageGone) {
		t.Errorf("undo of a message without a Message-ID or its UID: %v", err)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"No UID", "Wrong UID"}) {
		t.Errorf("INBOX holds %v", got)
	}
	if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"Already there", "Look-alike", "No id"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	if got := r.states(ActivityQuery{}); !slices.Equal(got, []string{"No UID: undone", "Wrong UID: undone", "No id: applied"}) {
		t.Errorf("log = %v", got)
	}
}

// Disabling a rule stops it, and drops what it had pending; undoing a
// rule takes back all it did, in one call, and leaves the other rules'
// work alone.
func TestDisableAndUndoARule(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	rule := r.save(r.newsRule("Newsletters"))
	stars := r.save(Rule{Name: "Stars", Conditions: []Condition{{Field: CondSender, Value: "bob@"}}, Actions: []Action{{Kind: ActionStar}}})
	r.deliver("INBOX", news("Sale 1", "s1@shop.example", 1))
	r.deliver("INBOX", ruleMail("Hi", "Bob <bob@example.org>", "h1@example.org", "", 2))
	r.sync("INBOX")
	r.deliver("INBOX", news("Sale 2", "s2@shop.example", 3))
	r.sync("INBOX")
	// A third waits for a server that went away as the rule is disabled.
	lock := r.svc.ruleLock(r.a.ID)
	lock.Lock()
	r.deliver("INBOX", news("Sale 3", "s3@shop.example", 4))
	if err := r.svc.SyncFolder(r.ctx, r.a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	r.f.stop()
	lock.Unlock()
	r.svc.rulesSettled()
	if got := r.states(ActivityQuery{Rule: rule.ID}); !slices.Equal(got, []string{"Sale 1: applied", "Sale 2: applied", "Sale 3: pending"}) {
		t.Fatalf("log of the rule = %v", got)
	}
	if _, err := r.svc.PauseRule(r.ctx, rule.ID, true); err != nil {
		t.Fatal(err)
	}
	r.f.restart(t)
	if got := r.states(ActivityQuery{Rule: rule.ID}); !slices.Equal(got, []string{"Sale 1: applied", "Sale 2: applied", "Sale 3: undone"}) {
		t.Fatalf("log of the rule = %v", got)
	}
	r.deliver("INBOX", news("Sale 4", "s4@shop.example", 5))
	r.sync("INBOX")
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Hi", "Sale 3", "Sale 4"}) {
		t.Errorf("a disabled rule sorted: INBOX holds %v", got)
	}

	n, err := r.svc.UndoRule(r.ctx, rule.ID)
	if err != nil || n != 2 {
		t.Fatalf("undid %d, %v", n, err)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Hi", "Sale 1", "Sale 2", "Sale 3", "Sale 4"}) || len(r.onServer("Newsletters")) != 0 {
		t.Errorf("after the undo INBOX holds %v, Newsletters %v", got, r.onServer("Newsletters"))
	}
	if !r.serverMessage("INBOX", "Hi").hasFlag(imap.FlagFlagged) || !slices.Equal(r.states(ActivityQuery{Rule: stars.ID}), []string{"Hi: applied"}) {
		t.Error("the undo of one rule touched another's work")
	}
	if n, err := r.svc.UndoRule(r.ctx, rule.ID); err != nil || n != 0 {
		t.Errorf("undone again: %d, %v", n, err)
	}
	// Enabled again, it sorts what arrives, not what it was undone of.
	if _, err := r.svc.PauseRule(r.ctx, rule.ID, false); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Sale 5", "s5@shop.example", 6))
	r.sync("INBOX")
	if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"Sale 5"}) {
		t.Errorf("enabled again, Newsletters holds %v", got)
	}
}

// A preview lists exactly the cached messages a rule would match, a page
// at a time with how many in all, and changes nothing; it takes a rule not
// saved, and refuses one that cannot be.
func TestPreviewListsWhatARuleWouldMatch(t *testing.T) {
	r := newRuleRig(t, allCaps())
	for i, subject := range []string{"Sale 1", "Sale 2", "Sale 3"} {
		r.deliver("INBOX", news(subject, "", i))
	}
	r.deliver("INBOX", ruleMail("Sale talk", "Bob <bob@example.org>", "", "", 5))
	r.deliver("Newsletters", news("Sale 0", "", 0))
	r.sync("INBOX", "Newsletters")
	serverBefore := map[string][]string{"INBOX": r.onServer("INBOX"), "Newsletters": r.onServer("Newsletters")}
	flagsBefore := serverFlags(t, r.f, "INBOX")
	rule := r.newsRule("Archive")
	first, err := r.svc.PreviewRule(r.ctx, rule, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.svc.PreviewRule(r.ctx, rule, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	var pages []string
	for _, m := range append(first.Messages, second.Messages...) {
		pages = append(pages, m.Subject)
		if m.Account != r.a.ID || m.Folder != "INBOX" {
			t.Errorf("%s is in %s/%s", m.Subject, m.Account, m.Folder)
		}
	}
	if !slices.Equal(pages, []string{"Sale 3", "Sale 2", "Sale 1"}) || first.Total != 3 || second.Total != 3 || len(first.Targets) != 3 {
		t.Errorf("pages %v, totals %d and %d, %d targets", pages, first.Total, second.Total, len(first.Targets))
	}
	if serverBefore["INBOX"] == nil || !reflect.DeepEqual(serverBefore, map[string][]string{"INBOX": r.onServer("INBOX"), "Newsletters": r.onServer("Newsletters")}) ||
		!reflect.DeepEqual(flagsBefore, serverFlags(t, r.f, "INBOX")) || len(r.log(ActivityQuery{})) != 0 {
		t.Error("a preview changed something")
	}
	if rules, _ := r.svc.Rules(r.ctx); len(rules) != 0 {
		t.Errorf("a preview saved %+v", rules)
	}
	bad := rule
	bad.Actions = []Action{{Kind: ActionMove, Account: r.a.ID, Folder: "Nowhere"}}
	if _, err := r.svc.PreviewRule(r.ctx, bad, 0, 10); !errors.Is(err, ErrRuleFolder) {
		t.Errorf("preview of a rule that cannot be saved: %v", err)
	}
}

// A run of a rule over its preview acts on exactly the messages shown
// that are still there and still match, logs them as matches the user
// ran, and is undone as any.
func TestRunARuleOverItsPreview(t *testing.T) {
	r := newRuleRig(t, allCaps())
	for i, subject := range []string{"Sale 1", "Sale 2", "Sale 3"} {
		r.deliver("INBOX", news(subject, "", i))
	}
	r.sync("INBOX")
	rule := r.save(r.newsRule("Newsletters"))
	preview, err := r.svc.PreviewRule(r.ctx, rule, 0, 1)
	if err != nil || len(preview.Targets) != 3 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	// Meanwhile one went elsewhere and one more arrived, which the
	// preview did not show.
	if _, err := r.svc.MoveMessages(r.ctx, r.a.ID, []MessageRef{{"INBOX", r.message("INBOX", "Sale 2").UID}}, "Archive"); err != nil {
		t.Fatal(err)
	}
	r.deliver("INBOX", news("Sale 4", "", 4))
	r.sync("INBOX")
	ran, err := r.svc.RunRule(r.ctx, rule.ID, preview.Targets)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, e := range ran {
		subjects = append(subjects, e.Subject)
		if !e.Manual || e.State != ActivityApplied || e.Changes.MovedTo != "Newsletters" {
			t.Errorf("ran %+v", e)
		}
	}
	slices.Sort(subjects)
	if !slices.Equal(subjects, []string{"Sale 1", "Sale 3"}) {
		t.Errorf("ran over %v", subjects)
	}
	if got := r.onServer("Newsletters"); !slices.Equal(got, []string{"Sale 1", "Sale 3", "Sale 4"}) {
		t.Errorf("Newsletters holds %v", got)
	}
	n, err := r.svc.UndoRule(r.ctx, rule.ID)
	if err != nil || n != 3 {
		t.Fatalf("undid %d, %v", n, err)
	}
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Sale 1", "Sale 3", "Sale 4"}) || !slices.Equal(r.onServer("Archive"), []string{"Sale 2"}) {
		t.Errorf("after the undo INBOX holds %v", got)
	}
	if _, err := r.svc.RunRule(r.ctx, rule.ID, make([]RuleTarget, MaxRuleRun+1)); !errors.Is(err, ErrRuleRunTooLarge) {
		t.Errorf("a run too large: %v", err)
	}
	if _, err := r.svc.PauseRule(r.ctx, rule.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RunRule(r.ctx, rule.ID, preview.Targets); !errors.Is(err, ErrRulePaused) {
		t.Errorf("a run of a paused rule: %v", err)
	}
}

// Mail this app moves into the inbox is no mail that arrived: the user's
// move back of a sorted newsletter is not sorted again, nor announced.
func TestMailMovedBackIsNotSortedAgain(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Newsletters")
	f.append(t, "INBOX", readFixture(t, "plain.eml"))
	w := newWatchRig(t, f, "secret", true)
	ctx := context.Background()
	if _, err := w.svc.SyncAccount(ctx, w.account.ID); err != nil {
		t.Fatal(err)
	}
	r := &ruleRig{t: t, ctx: ctx, f: f, svc: w.svc, a: w.account}
	r.save(r.newsRule("Newsletters"))
	w.watch()
	w.checked()
	wait := w.timer()
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	wait.fire()
	if ev := w.checked(); len(ev.New) != 0 {
		t.Fatalf("announced %v", subjects(ev.New))
	}
	wait = w.timer()
	w.svc.rulesSettled()
	r.sync("Newsletters")
	if _, err := w.svc.MoveMessages(ctx, w.account.ID, []MessageRef{{"Newsletters", r.message("Newsletters", "Sale").UID}}, "INBOX"); err != nil {
		t.Fatal(err)
	}
	wait.fire()
	if ev := w.checked(); len(ev.New) != 0 {
		t.Errorf("the message moved back was announced: %v", subjects(ev.New))
	}
	w.svc.rulesSettled()
	if got := r.onServer("INBOX"); !slices.Equal(got, []string{"Plain message", "Sale"}) || len(r.log(ActivityQuery{})) != 1 {
		t.Errorf("INBOX holds %v, log %v", got, r.states(ActivityQuery{}))
	}
}

// The log keeps the newest MaxRuleActivity entries, and every pending one
// however old.
func TestRuleLogIsBounded(t *testing.T) {
	RuleLogBound(t, newMemStore())
}

// Rules and their log go with the account they name; the rules that name
// none stay.
func TestRulesGoWithTheirAccount(t *testing.T) {
	r := newRuleRig(t, allCaps())
	r.sync("INBOX")
	named := r.save(r.newsRule("Newsletters"))
	global := r.save(Rule{Name: "Stars", Conditions: []Condition{{Field: CondSubject, Value: "urgent"}}, Actions: []Action{{Kind: ActionStar}}})
	r.deliver("INBOX", news("Sale", "s1@shop.example", 1))
	r.sync("INBOX")
	if len(r.log(ActivityQuery{})) != 1 {
		t.Fatalf("log = %v", r.states(ActivityQuery{}))
	}
	if err := r.svc.RemoveAccount(r.ctx, r.a.ID); err != nil {
		t.Fatal(err)
	}
	rules, err := r.svc.Rules(r.ctx)
	if err != nil || len(rules) != 1 || rules[0].ID != global.ID {
		t.Errorf("rules after the account went = %+v, %v", rules, err)
	}
	if len(r.log(ActivityQuery{})) != 0 || len(r.log(ActivityQuery{Rule: named.ID})) != 0 {
		t.Errorf("the log kept %v", r.states(ActivityQuery{}))
	}
}
