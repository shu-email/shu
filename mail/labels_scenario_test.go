package mail

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// labelMail is a message for the tests of labels, minutes after a time;
// one without an id has no Message-ID.
func labelMail(subject, from, id string, minutes int) []byte {
	date := time.Date(2026, 3, 2, 9, minutes, 0, 0, time.UTC).Format(time.RFC1123Z)
	head := "From: " + from + "\r\nTo: Ann <ann@example.com>\r\nSubject: " + subject + "\r\nDate: " + date + "\r\n"
	if id != "" {
		head += "Message-ID: <" + id + ">\r\n"
	}
	return []byte(head + "\r\nHello.\r\n")
}

// labelRig is a service on a fake server, with what the tests of labels
// read of both.
type labelRig struct {
	t   *testing.T
	ctx context.Context
	svc *Service
	f   *fakeIMAP
	a   Account
}

func (r *labelRig) sync(folders ...string) {
	r.t.Helper()
	if _, err := r.svc.SyncAccount(r.ctx, r.a.ID); err != nil {
		r.t.Fatal(err)
	}
	for _, folder := range folders {
		if err := r.svc.SyncFolder(r.ctx, r.a.ID, folder, 50); err != nil {
			r.t.Fatalf("sync %s: %v", folder, err)
		}
	}
}

// label returns the account's label of a name.
func (r *labelRig) label(name string) Label {
	r.t.Helper()
	labels, err := r.svc.Labels(r.ctx, r.a.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, l := range labels {
		if LabelNameKey(l.Name) == LabelNameKey(name) {
			return l
		}
	}
	r.t.Fatalf("no label %q among %+v", name, labels)
	return Label{}
}

// labelNames lists the account's labels' names in their order.
func (r *labelRig) labelNames() []string {
	r.t.Helper()
	labels, err := r.svc.Labels(r.ctx, r.a.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	out := []string{}
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

// message returns the cached message of a subject in a folder.
func (r *labelRig) message(folder, subject string) Summary {
	r.t.Helper()
	for _, m := range r.page(AccountScope(r.a.ID, folder), Search{}) {
		if m.Subject == subject {
			return m
		}
	}
	r.t.Fatalf("no %q cached in %s", subject, folder)
	return Summary{}
}

func (r *labelRig) page(scope Scope, search Search) []Summary {
	r.t.Helper()
	page, err := r.svc.MessagePage(r.ctx, ListQuery{Scope: scope, Search: search})
	if err != nil {
		r.t.Fatal(err)
	}
	return page.Messages
}

// subjectsIn lists the subjects of a list's messages.
func (r *labelRig) subjectsIn(scope Scope, search Search) []string {
	r.t.Helper()
	out := []string{}
	for _, m := range r.page(scope, search) {
		out = append(out, m.Subject)
	}
	return out
}

// set puts a label on cached messages, or takes it off.
func (r *labelRig) set(id LabelID, on bool, msgs ...Summary) {
	r.t.Helper()
	if err := r.svc.SetLabel(r.ctx, r.a.ID, refsOf(msgs), id, on); err != nil {
		r.t.Fatal(err)
	}
}

// labelsOf writes a message's labels by name, with "(here)" after those
// kept on this device only.
func (r *labelRig) labelsOf(m Summary) []string {
	r.t.Helper()
	labels, err := r.svc.Labels(r.ctx, r.a.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	names := map[LabelID]string{}
	for _, l := range labels {
		names[l.ID] = l.Name
	}
	out := []string{}
	for _, l := range m.Labels {
		name := names[l.ID]
		if l.Local {
			name += " (here)"
		}
		out = append(out, name)
	}
	return out
}

// serverMail is every subject of every mailbox of the fake server with
// its system flags, what deleting a label must leave as it was.
func serverMail(t *testing.T, f *fakeIMAP, mailboxes ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, box := range mailboxes {
		for _, m := range f.messages(t, box) {
			line := m.Subject
			for _, flag := range []imap.Flag{imap.FlagSeen, imap.FlagFlagged, imap.FlagDeleted} {
				if m.hasFlag(flag) {
					line += " " + string(flag)
				}
			}
			out[box] = append(out[box], line)
		}
		slices.Sort(out[box])
	}
	return out
}

// LabelsScenario lives the labels of an account on store, as the app
// does, on a fake server whose INBOX and Archive keep any keyword and
// whose two Notes folders keep none. A keyword another mail client set
// is a label as the account is first synced, Thunderbird's with its name,
// and the keywords of junk and forwarding are none. Two labels made and
// put on one message are on the server and come back from it, and the
// message is in each label's list. A label put on messages of Notes, one
// of them without a Message-ID, is kept on this device and says so, and
// stays on them through a move, a resync of a folder the server made anew
// and a restart: reopen gives the store as the app finds it as it starts,
// the same one or the database opened anew. A search finds messages by
// label with other filters, and deleting the labels takes their keywords
// off the server and leaves every message as it was. It is for the tests
// on the in-memory and the SQLite stores (labels_sqlite_test.go).
func LabelsScenario(t *testing.T, reopen func() Store) {
	t.Helper()
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive", "Notes", "Old Notes")
	f.keepKeywords("Notes", keepsNone)
	f.keepKeywords("Old Notes", keepsNone)
	f.append(t, "INBOX", labelMail("Budget", "Maya Lin <maya@example.org>", "budget@example.org", 1))
	f.appendFlagged(t, "INBOX", labelMail("Lunch", "Bob <bob@example.org>", "lunch@example.org", 2), imap.FlagSeen)
	f.appendFlagged(t, "INBOX", labelMail("Report", "Carol <carol@example.org>", "report@example.org", 3), "Clients", imap.FlagForwarded)
	f.appendFlagged(t, "Archive", labelMail("Contract", "Dan <dan@example.org>", "contract@example.org", 4), "$label2", "Junk")
	idea, scribble := labelMail("Idea", "Ann <ann@example.com>", "idea@example.com", 5), labelMail("Scribble", "Ann <ann@example.com>", "", 6)
	f.append(t, "Notes", idea)
	f.append(t, "Notes", scribble)
	secrets := newMemSecrets()
	start := func() *labelRig {
		svc := NewService(reopen(), secrets, "")
		svc.SetTLSConfig(insecureTLS())
		return &labelRig{t: t, ctx: ctx, svc: svc, f: f, a: fakeAccount(t, f.addr)}
	}
	r := start()
	if err := r.svc.AddAccount(ctx, r.a, "secret"); err != nil {
		t.Fatal(err)
	}
	r.sync("INBOX", "Archive", "Notes")
	everything := AccountScope(r.a.ID, "")

	// The server's keywords, as it keeps them, in lower case.
	if got := r.labelNames(); !slices.Equal(got, []string{"clients", "Work"}) {
		t.Fatalf("labels from the server = %v", got)
	}
	work := r.label("Work")
	if work.Keyword != "$label2" || work.Color != LabelOrange || work.Local || work.Unread != 1 {
		t.Errorf("Thunderbird's tag = %+v", work)
	}
	if got := r.labelsOf(r.message("INBOX", "Report")); !slices.Equal(got, []string{"clients"}) {
		t.Errorf("the message labelled elsewhere has %v", got)
	}
	if got := r.labelsOf(r.message("Archive", "Contract")); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("Thunderbird's message has %v", got)
	}

	// Two labels on one message: on the server, back from it, and the
	// message in each label's list.
	urgent, err := r.svc.CreateLabel(ctx, r.a.ID, "Urgent", LabelRed)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := r.svc.CreateLabel(ctx, r.a.ID, " To Do ", "")
	if err != nil {
		t.Fatal(err)
	}
	if todo.Name != "To Do" || todo.Keyword != "To_Do" || todo.Color != LabelColorAt(3) || urgent.Keyword != "Urgent" {
		t.Errorf("made %+v and %+v", urgent, todo)
	}
	budget := r.message("INBOX", "Budget")
	r.set(urgent.ID, true, budget)
	r.set(todo.ID, true, budget)
	if got := keywordsOn(t, f, "INBOX")["Budget"]; !slices.Equal(got, []string{"to_do", "urgent"}) {
		t.Fatalf("the server's Budget has %v", got)
	}
	r.sync("INBOX")
	if got := r.labelsOf(r.message("INBOX", "Budget")); !slices.Equal(got, []string{"Urgent", "To Do"}) {
		t.Errorf("synced again, Budget has %v", got)
	}
	for _, l := range []Label{urgent, todo} {
		if got := r.subjectsIn(LabelScope(r.a.ID, l.ID), Search{}); !slices.Equal(got, []string{"Budget"}) {
			t.Errorf("%s lists %v", l.Name, got)
		}
		page, err := r.svc.ConversationPage(ctx, ListQuery{Scope: LabelScope(r.a.ID, l.ID)})
		if err != nil || len(page.Conversations) != 1 || !slices.Equal(page.Conversations[0].Labels, []LabelID{urgent.ID, todo.ID}) {
			t.Errorf("%s's conversations = %+v, %v", l.Name, page.Conversations, err)
		}
		if got := r.label(l.Name); got.Unread != 1 || got.Local {
			t.Errorf("%s = %+v", l.Name, got)
		}
	}

	// Notes keeps no keyword: the label is this device's, and no STORE
	// reaches the server.
	stores := f.storeCount()
	notes := []Summary{r.message("Notes", "Idea"), r.message("Notes", "Scribble")}
	r.set(urgent.ID, true, notes...)
	if got := f.storesSince(stores); len(got) != 0 {
		t.Errorf("a label kept here reached the server: %+v", got)
	}
	for _, m := range []Summary{r.message("Notes", "Idea"), r.message("Notes", "Scribble")} {
		if got := r.labelsOf(m); !slices.Equal(got, []string{"Urgent (here)"}) || !m.LabelsLocal {
			t.Errorf("%s has %v, local %v", m.Subject, got, m.LabelsLocal)
		}
	}
	if m := r.message("INBOX", "Budget"); m.LabelsLocal {
		t.Error("the inbox says it keeps no keyword")
	}
	if got := r.label("Urgent"); got.Local || got.Unread != 3 {
		t.Errorf("Urgent = %+v", got)
	}

	// A move, and a resync of the folder the server made anew.
	if _, err := r.svc.MoveMessages(ctx, r.a.ID, refsOf(notes), "Old Notes"); err != nil {
		t.Fatal(err)
	}
	r.sync("Notes", "Old Notes")
	checkHere := func(when string) {
		t.Helper()
		for _, subject := range []string{"Idea", "Scribble"} {
			if got := r.labelsOf(r.message("Old Notes", subject)); !slices.Equal(got, []string{"Urgent (here)"}) {
				t.Errorf("%s, %s has %v", when, subject, got)
			}
		}
		if got := r.subjectsIn(LabelScope(r.a.ID, urgent.ID), Search{}); !slices.Equal(got, []string{"Scribble", "Idea", "Budget"}) {
			t.Errorf("%s, Urgent lists %v", when, got)
		}
	}
	checkHere("moved")
	if err := f.user.Delete("Old Notes"); err != nil {
		t.Fatal(err)
	}
	f.create(t, "Old Notes")
	f.append(t, "Old Notes", idea)
	f.append(t, "Old Notes", scribble)
	r.sync("Old Notes")
	checkHere("resynced")

	// A restart.
	r = start()
	r.sync()
	checkHere("restarted")
	if got := r.labelNames(); !slices.Equal(got, []string{"clients", "Work", "Urgent", "To Do"}) {
		t.Errorf("labels after a restart = %v", got)
	}
	if got := r.labelsOf(r.message("INBOX", "Budget")); !slices.Equal(got, []string{"Urgent", "To Do"}) {
		t.Errorf("after a restart, Budget has %v", got)
	}
	opened, err := r.svc.FetchMessage(ctx, r.a.ID, "Old Notes", r.message("Old Notes", "Idea").UID, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.labelsOf(opened.Message.Summary); !slices.Equal(got, []string{"Urgent (here)"}) || !opened.Message.LabelsLocal {
		t.Errorf("opened, Idea has %v, local %v", got, opened.Message.LabelsLocal)
	}

	// Search, by label with other filters.
	for _, tc := range []struct {
		typed string
		want  []string
	}{
		{"label:URGENT from:maya", []string{"Budget"}},
		{`label:"to do" is:unread`, []string{"Budget"}},
		{`label：urgent in:"Old Notes"`, []string{"Scribble", "Idea"}},
		{"label:urgent label:work", []string{}},
		{"label:work", []string{"Contract"}},
		{"label:nothing", []string{}},
	} {
		search := ParseSearch(tc.typed, time.UTC)
		got := r.page(everything, search)
		subjects := []string{}
		for _, m := range got {
			subjects = append(subjects, m.Subject)
			if !m.Matched.Has(FieldLabel) {
				t.Errorf("%s: %s does not say it matched by its label", tc.typed, m.Subject)
			}
		}
		if !slices.Equal(subjects, tc.want) {
			t.Errorf("%s finds %v, want %v", tc.typed, subjects, tc.want)
		}
	}

	// Deleting labels keeps every message as it was.
	boxes := []string{"INBOX", "Archive", "Notes", "Old Notes"}
	before := serverMail(t, f, boxes...)
	if err := r.svc.DeleteLabel(ctx, r.a.ID, todo.ID); err != nil {
		t.Fatal(err)
	}
	if got := keywordsOn(t, f, "INBOX")["Budget"]; !slices.Equal(got, []string{"urgent"}) {
		t.Errorf("after To Do went, the server's Budget has %v", got)
	}
	if err := r.svc.DeleteLabel(ctx, r.a.ID, urgent.ID); err != nil {
		t.Fatal(err)
	}
	for _, box := range boxes {
		for subject, kws := range keywordsOn(t, f, box) {
			if slices.Contains(kws, "urgent") || slices.Contains(kws, "to_do") {
				t.Errorf("%s/%s keeps %v", box, subject, kws)
			}
		}
	}
	if after := serverMail(t, f, boxes...); !reflect.DeepEqual(after, before) {
		t.Errorf("deleting labels changed the mail:\n was %v\n now %v", before, after)
	}
	if got := r.labelNames(); !slices.Equal(got, []string{"clients", "Work"}) {
		t.Errorf("labels after the deletions = %v", got)
	}
	for _, box := range boxes {
		for _, m := range r.page(AccountScope(r.a.ID, box), Search{}) {
			if got := r.labelsOf(m); slices.Contains(got, "Urgent") || slices.Contains(got, "Urgent (here)") || slices.Contains(got, "To Do") {
				t.Errorf("%s/%s keeps %v", box, m.Subject, got)
			}
		}
	}
	if got := fmt.Sprint(r.subjectsIn(everything, Search{})); got != "[Scribble Idea Contract Report Lunch Budget]" {
		t.Errorf("the cache holds %s", got)
	}
}

func TestLabelsScenario(t *testing.T) {
	store := newMemStore()
	LabelsScenario(t, func() Store { return store })
}
