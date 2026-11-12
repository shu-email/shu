package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

// bookNow is the time of the address book tests.
var bookNow = time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)

// me is the account the address book tests write as.
var me = mail.Address{Name: "Ann", Email: "a1@example.com"}

// bookStore is a store with the account a1, its folders of every use, and
// its clock at bookNow.
func bookStore(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	s.now = func() time.Time { return bookNow }
	addAccount(t, s, "a1")
	if err := s.SaveFolders(context.Background(), "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox}, {Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive}, {Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Drafts", SpecialUse: mail.SpecialDrafts},
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

// note is a message of the address book tests, dated days before bookNow.
func note(uid mail.UID, id string, from mail.Address, days float64, to ...mail.Address) mail.Message {
	return mail.Message{Summary: mail.Summary{
		UID: uid, MessageID: id, From: from, To: to, Subject: "Note " + id,
		Date: bookNow.Add(-time.Duration(days * float64(24*time.Hour))),
	}}
}

func addr(name, email string) mail.Address { return mail.Address{Name: name, Email: email} }

// emails is the addresses a read of the address book returns, in order.
func emails(t *testing.T, s *Store, q mail.ContactQuery) []string {
	t.Helper()
	list, err := s.Contacts(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range list {
		out = append(out, c.Email)
	}
	return out
}

// typed is the read of the address book of a word typed in a field.
func typed(text string, exclude ...string) mail.ContactQuery {
	return mail.ContactQuery{Words: mail.ContactWords(text), Exclude: exclude, Limit: mail.DefaultSuggestions}
}

// stored is the row of an address as it is, a deleted one's too.
func stored(t *testing.T, s *Store, email string) (mail.ContactState, bool) {
	t.Helper()
	rows, err := s.query(context.Background(), `SELECT `+contactColumns+` FROM contacts c WHERE c.key = ?`, mail.ContactKey(email))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) == 0 {
		return mail.ContactState{}, false
	}
	return contactRow(rows.Values[0]), true
}

// counts is how many messages a contact counts, sent and received.
func counts(t *testing.T, s *Store, email string) [2]int {
	t.Helper()
	c, _ := stored(t, s, email)
	return [2]int{c.Sent, c.Received}
}

// Mail entering the cache makes contacts: the sender of what reaches the
// user, the recipients of what the user wrote, found in any folder, and
// of an alias's mail too; never the user's own addresses, a sender that
// takes no replies, a name that is an address, or the mail of the Junk
// and Drafts folders. Mail fetched later counts, a body fetched for a row
// cached before does not count it again, and older mail keeps the newest
// date.
func TestContactsFromTheMailCached(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i1", AccountID: "a1", Name: "Sales", Email: "Sales@Example.com"}); err != nil {
		t.Fatal(err)
	}
	alice, bob, carol := addr("Alice Smith", "alice@example.org"), addr("bob@example.org", "bob@example.org"), addr("Carol", "carol@example.org")
	syncMessages(t, s, "a1", "INBOX",
		note(1, "m1@x", alice, 2, me),
		note(2, "m2@x", addr("Shop", "no-reply@shop.example"), 1, me),
		note(3, "m3@x", bob, 3, me),
		note(4, "m4@x", me, 4, carol, me),
		note(5, "m5@x", addr("Sales", "sales@example.com"), 4, me))
	syncMessages(t, s, "a1", "Sent",
		note(1, "s1@x", me, 5, addr("Dan D.", "dan@example.org"), addr("", "sales@example.com")),
		note(2, "s2@x", addr("Sales", "sales@example.com"), 6, addr("Erin", "erin@example.org")))
	syncMessages(t, s, "a1", "Junk", note(1, "j1@x", addr("Prize", "winner@spam.example"), 1, me))
	syncMessages(t, s, "a1", "Drafts", note(1, "d1@x", me, 1, addr("Frank", "frank@example.org")))

	for email, want := range map[string][2]int{
		"alice@example.org": {0, 1}, "bob@example.org": {0, 1}, "carol@example.org": {1, 0},
		"dan@example.org": {1, 0}, "erin@example.org": {1, 0},
	} {
		if got := counts(t, s, email); got != want {
			t.Errorf("%s counts %v, want %v", email, got, want)
		}
	}
	for _, email := range []string{"no-reply@shop.example", "winner@spam.example", "frank@example.org", "a1@example.com", "sales@example.com"} {
		if _, ok := stored(t, s, email); ok {
			t.Errorf("%s is a contact", email)
		}
	}
	if c, _ := stored(t, s, "bob@example.org"); c.Name != "" {
		t.Errorf("bob is named %q, his address", c.Name)
	}
	if c, _ := stored(t, s, "dan@example.org"); c.Name != "Dan D." || c.NameFrom != mail.NameWritten || !c.LastSent.Equal(bookNow.Add(-5*24*time.Hour)) {
		t.Errorf("dan = %+v", c)
	}

	// Older mail, fetched on demand, counts and keeps the newest date.
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 1,
		Messages: []mail.Message{headerOnly(note(1000, "old@x", alice, 40, me))}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := stored(t, s, "alice@example.org"); c.Received != 2 || !c.LastReceived.Equal(bookNow.Add(-2*24*time.Hour)) {
		t.Errorf("alice after older mail = %+v", c)
	}
	// A body fetched for a cached row counts nothing again; a message
	// first cached with its body counts.
	body := note(1, "m1@x", alice, 2, me)
	body.BodyText = "Hello"
	if err := s.SaveMessage(ctx, "a1", "INBOX", body); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", note(60, "m60@x", addr("Grace", "grace@example.org"), 1, me)); err != nil {
		t.Fatal(err)
	}
	if got := counts(t, s, "alice@example.org"); got != [2]int{0, 2} {
		t.Errorf("alice after her body was fetched counts %v", got)
	}
	if got := counts(t, s, "grace@example.org"); got != [2]int{0, 1} {
		t.Errorf("grace counts %v", got)
	}
}

// A message counts once, whatever folders it is in and however often a
// sync writes it: a copy in Archive, a folder reset, a move, and a
// message with no Message-ID written again.
func TestContactsCountEachMessageOnce(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	alice, bob := addr("Alice", "alice@example.org"), addr("Bob", "bob@example.org")
	syncMessages(t, s, "a1", "INBOX", note(1, "<m1@X.org>", alice, 2, me), note(2, "", bob, 3, me))
	syncMessages(t, s, "a1", "Archive", note(1, "m1@x.org", alice, 2, me))
	// The server reset the inbox: its rows go and come back.
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 2}, Reset: true,
		Messages: []mail.Message{note(1, "m1@x.org", alice, 2, me), note(2, "", bob, 3, me)}}); err != nil {
		t.Fatal(err)
	}
	// A move: the row goes from the inbox, and a sync of the folder it
	// went to writes it again.
	if err := s.DeleteMessage(ctx, "a1", "INBOX", 2); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "Archive", note(7, "", bob, 3, me))
	if got := counts(t, s, "alice@example.org"); got != [2]int{0, 1} {
		t.Errorf("alice counts %v", got)
	}
	if got := counts(t, s, "bob@example.org"); got != [2]int{0, 1} {
		t.Errorf("bob counts %v", got)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM contact_messages`); got != 2 {
		t.Errorf("%d messages counted, want 2", got)
	}
}

// Recording a message sent counts its recipients, To, Cc and Bcc, under
// the names the user wrote, at the time it was due, and once: not again
// when the state is recorded twice, nor when its copy in Sent is synced.
// An entry that failed counts nothing.
func TestContactsFromSending(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	created := bookNow.Add(-time.Hour)
	out := mail.Outgoing{
		To:      []mail.Address{addr("Dan D.", "dan@example.org"), me},
		Cc:      []mail.Address{addr("", "erin@example.org")},
		Bcc:     []mail.Address{addr("Frank", "Frank@Example.org")},
		Subject: "Plans", MessageID: "out1@example.com", From: me,
	}
	for _, p := range []mail.PendingSend{
		{ID: "o1", AccountID: "a1", Raw: []byte("raw"), Envelope: out, State: mail.StateScheduled, CreatedAt: created, DueAt: created.Add(10 * time.Second)},
		{ID: "o2", AccountID: "a1", Raw: []byte("raw"), State: mail.StatePending, CreatedAt: created,
			Envelope: mail.Outgoing{To: []mail.Address{addr("Gus", "gus@example.org")}, MessageID: "out2@example.com"}},
	} {
		if err := s.EnqueueOutbox(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := stored(t, s, "dan@example.org"); ok {
		t.Fatal("a message counted before it was sent")
	}
	for _, state := range []string{mail.StateSending, mail.StateSent, mail.StateSent} {
		if err := s.SetOutboxState(ctx, "o1", state, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOutboxState(ctx, "o2", mail.StateFailed, "refused"); err != nil {
		t.Fatal(err)
	}
	due := created.Add(10 * time.Second)
	for email, name := range map[string]string{"dan@example.org": "Dan D.", "erin@example.org": "", "frank@example.org": "Frank"} {
		c, ok := stored(t, s, email)
		if !ok || c.Sent != 1 || c.Received != 0 || c.Name != name || !c.LastSent.Equal(due) {
			t.Errorf("%s = %+v, %v", email, c, ok)
		}
	}
	if c, _ := stored(t, s, "frank@example.org"); c.Email != "Frank@Example.org" {
		t.Errorf("frank is written %q, not as the user wrote him", c.Email)
	}
	for _, email := range []string{"a1@example.com", "gus@example.org"} {
		if _, ok := stored(t, s, email); ok {
			t.Errorf("%s is a contact", email)
		}
	}
	// Its copy in Sent, which the next sync brings.
	syncMessages(t, s, "a1", "Sent", note(1, "<out1@example.com>", me, 0, addr("Dan D.", "dan@example.org")))
	if got := counts(t, s, "dan@example.org"); got != [2]int{1, 0} {
		t.Errorf("after the Sent copy dan counts %v", got)
	}
}

// The address book is written in the transaction of the mail that makes
// it: a sync, or the record of a send, that fails leaves no contact, and
// one that succeeds has its contacts. The address book is worth less than
// the mail: when its part fails, the mail is written alone.
func TestContactsWriteWithTheirMail(t *testing.T) {
	ctx := context.Background()
	refuse := func(t *testing.T, s *Store, name, on string) {
		t.Helper()
		if _, err := s.exec(ctx, `CREATE TRIGGER `+name+` `+on+` BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
	}
	enqueue := func(t *testing.T, s *Store, id string) {
		t.Helper()
		if err := s.EnqueueOutbox(ctx, mail.PendingSend{ID: id, AccountID: "a1", Raw: []byte("raw"), State: mail.StateSending,
			CreatedAt: bookNow, Envelope: mail.Outgoing{To: []mail.Address{addr("Dan", "dan@example.org")}, MessageID: id + "@x"}}); err != nil {
			t.Fatal(err)
		}
	}
	batch := mail.SyncBatch{Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1},
		Messages: []mail.Message{note(1, "m1@x", addr("Alice", "alice@example.org"), 1, me)}}

	t.Run("the mail fails", func(t *testing.T) {
		s := bookStore(t)
		refuse(t, s, "no_messages", `BEFORE INSERT ON messages`)
		if err := s.ApplySync(ctx, "a1", batch); err == nil {
			t.Fatal("the sync was written")
		}
		refuse(t, s, "no_sent", `BEFORE UPDATE OF state ON outbox WHEN new.state = 'sent'`)
		enqueue(t, s, "o1")
		if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err == nil {
			t.Fatal("the send was recorded")
		}
		for _, table := range []string{"contacts", "contact_terms", "contact_messages"} {
			if n := countRows(t, s, `SELECT COUNT(*) FROM `+table); n != 0 {
				t.Errorf("%s has %d rows of mail that was not written", table, n)
			}
		}
		if f, _, _ := s.Folder(ctx, "a1", "INBOX"); f.LastUID != 0 {
			t.Errorf("the cursor moved to %d", f.LastUID)
		}
	})
	t.Run("the address book fails", func(t *testing.T) {
		s := bookStore(t)
		refuse(t, s, "no_contacts", `BEFORE INSERT ON contacts`)
		if err := s.ApplySync(ctx, "a1", batch); err != nil {
			t.Fatal(err)
		}
		enqueue(t, s, "o1")
		if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err != nil {
			t.Fatal(err)
		}
		if msgs, _ := s.ListMessages(ctx, "a1", "INBOX", "", 10); len(msgs) != 1 {
			t.Errorf("the inbox has %d messages", len(msgs))
		}
		if p, _ := s.Outbox(ctx, "o1"); p.State != mail.StateSent {
			t.Errorf("the entry is %s", p.State)
		}
		if n := countRows(t, s, `SELECT COUNT(*) FROM contact_messages`); n != 0 {
			t.Errorf("%d messages counted for no contact", n)
		}
	})
	t.Run("both are written", func(t *testing.T) {
		s := bookStore(t)
		if err := s.ApplySync(ctx, "a1", batch); err != nil {
			t.Fatal(err)
		}
		enqueue(t, s, "o1")
		if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err != nil {
			t.Fatal(err)
		}
		if got := emails(t, s, mail.ContactQuery{}); !slices.Equal(got, []string{"dan@example.org", "alice@example.org"}) {
			t.Errorf("address book = %v", got)
		}
	})
}

// The order of the address book: the favorites first, then those the
// user wrote to, by how much and how lately, then those the user added,
// then those that only wrote to the user, by how much and how lately; so
// two letters of a name offer the person the user writes to before
// frequent and one-off senders. The user's own addresses, an alias added
// after its mail came in among them, are never offered.
func TestContactRanking(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	var inbox, sent []mail.Message
	uid := mail.UID(0)
	receive := func(from mail.Address, days ...float64) {
		for _, d := range days {
			uid++
			inbox = append(inbox, note(uid, fmt.Sprintf("r%d@x", uid), from, d, me))
		}
	}
	write := func(to mail.Address, days ...float64) {
		for _, d := range days {
			uid++
			sent = append(sent, note(uid, fmt.Sprintf("s%d@x", uid), me, d, to))
		}
	}
	john, joan, jordan, josh := addr("John Written", "john@example.org"), addr("Joan Frequent", "joan@example.org"),
		addr("Jordan Oneoff", "jordan@example.org"), addr("Josh News", "news@josh.example")
	jody, jolene, joe := addr("Jody Lately", "jody@example.org"), addr("Jolene Long Ago", "jolene@example.org"), addr("Joe Team", "team@example.org")
	write(john, 200)                       // once, long ago
	write(jody, 1, 2, 3)                   // often and lately
	write(jolene, 300, 301, 302, 303, 304) // often, long ago
	receive(joan, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	receive(jordan, 90)
	receive(josh, 400, 401, 402, 403, 404, 405, 406, 407, 408, 409, 410, 411)
	receive(joe, 1)
	syncMessages(t, s, "a1", "INBOX", inbox...)
	syncMessages(t, s, "a1", "Sent", sent...)
	if _, err := s.AddContact(ctx, "Joy Added", "joy@example.org"); err != nil {
		t.Fatal(err)
	}

	want := []string{"jody@example.org", "john@example.org", "jolene@example.org", "joy@example.org",
		"joan@example.org", "team@example.org", "jordan@example.org", "news@josh.example"}
	if got := emails(t, s, mail.ContactQuery{Words: []string{"jo"}}); !slices.Equal(got, want) {
		t.Errorf("jo offers %v, want %v", got, want)
	}
	if got := emails(t, s, typed("jo")); len(got) != mail.DefaultSuggestions || got[0] != "jody@example.org" {
		t.Errorf("suggestions = %v", got)
	}
	// A favorite comes first.
	if _, err := s.FavoriteContact(ctx, "News@Josh.example", true); err != nil {
		t.Fatal(err)
	}
	if got := emails(t, s, typed("jo")); got[0] != "news@josh.example" || got[1] != "jody@example.org" {
		t.Errorf("with a favorite = %v", got)
	}
	// An alias added since is the user's own.
	if err := s.SaveIdentity(ctx, mail.Identity{ID: "i1", AccountID: "a1", Email: "TEAM@example.org"}); err != nil {
		t.Fatal(err)
	}
	if got := emails(t, s, typed("joe")); len(got) != 0 {
		t.Errorf("the user's own address is offered: %v", got)
	}
	if got := emails(t, s, mail.ContactQuery{}); slices.Contains(got, "team@example.org") || len(got) != 7 {
		t.Errorf("address book = %v", got)
	}
}

// The order of a large address book is the order its rule gives, which
// the index of the order and the statement agree on.
func TestContactOrderFollowsItsRule(t *testing.T) {
	s := bookStore(t)
	rng := rand.New(rand.NewPCG(7, 11))
	b := newContactBook()
	var keys []string
	for i := range 400 {
		key := fmt.Sprintf("c%03d@example.org", i)
		c := b.contact(key)
		c.Email, c.Favorite, c.Added = key, rng.IntN(10) == 0, rng.IntN(8) == 0
		if rng.IntN(3) > 0 {
			c.Sent = rng.IntN(3)
			c.SentScore = float64(c.Sent) * rng.Float64()
		}
		c.Received = rng.IntN(4)
		c.ReceivedScore = float64(c.Received) * rng.Float64()
		if rng.IntN(20) == 0 {
			// Ties of every score.
			c.SentScore, c.ReceivedScore = 0, 0
		}
		b.changed[key] = true
		keys = append(keys, key)
	}
	if err := s.transaction(context.Background(), b.statements()); err != nil {
		t.Fatal(err)
	}
	tier := func(c *mail.ContactState) bool { return c.Sent > 0 || c.Added }
	score := func(c *mail.ContactState) float64 {
		if tier(c) {
			return c.SentScore
		}
		return c.ReceivedScore
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := b.contacts[keys[i]], b.contacts[keys[j]]
		switch {
		case a.Favorite != b.Favorite:
			return a.Favorite
		case tier(a) != tier(b):
			return tier(a)
		case score(a) != score(b):
			return score(a) > score(b)
		case a.ReceivedScore != b.ReceivedScore:
			return a.ReceivedScore > b.ReceivedScore
		}
		return keys[i] < keys[j]
	})
	if got := emails(t, s, mail.ContactQuery{}); !slices.Equal(got, keys) {
		t.Errorf("the address book is out of order:\n got %v\nwant %v", got[:20], keys[:20])
	}
	// The pages of it, read one after another, are it.
	var paged []string
	for offset := 0; ; offset += 50 {
		page := emails(t, s, mail.ContactQuery{Offset: offset, Limit: 50})
		if len(page) == 0 {
			break
		}
		paged = append(paged, page...)
	}
	if !slices.Equal(paged, keys) {
		t.Error("the pages of the address book are not the address book")
	}
}

// The name of a contact: the user's beats the name written on mail to it,
// which beats the one it gives itself, the newest of which counts; an
// older message does not take it back, a name that is an address is none,
// and an empty name the user gives leaves the naming to the mail again.
func TestContactNames(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	name := func() string {
		t.Helper()
		c, _ := stored(t, s, "alice@example.org")
		return c.Name
	}
	syncMessages(t, s, "a1", "INBOX", note(1, "m1@x", addr("Alice S.", "alice@example.org"), 10, me))
	syncMessages(t, s, "a1", "INBOX", note(2, "m2@x", addr("Alice Smith", "alice@example.org"), 2, me))
	if got := name(); got != "Alice Smith" {
		t.Errorf("the newest name is not kept: %q", got)
	}
	older := []mail.Message{headerOnly(note(1000, "m0@x", addr("Ally", "alice@example.org"), 50, me))}
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 1, Messages: older}); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "INBOX", note(3, "m3@x", addr(`"alice@example.org"`, "alice@example.org"), 1, me),
		note(4, "m4@x", addr("al@old.example", "alice@example.org"), 1, me))
	if got := name(); got != "Alice Smith" {
		t.Errorf("an older name or an address replaced the name: %q", got)
	}
	// The user writes to her, by an older name.
	syncMessages(t, s, "a1", "Sent", note(1, "s1@x", me, 20, addr("Alice", "alice@example.org")))
	syncMessages(t, s, "a1", "INBOX", note(5, "m5@x", addr("ALICE SMITH (Work)", "alice@example.org"), 0, me))
	if got := name(); got != "Alice" {
		t.Errorf("the name the user wrote is not kept: %q", got)
	}
	if _, err := s.RenameContact(ctx, "Alice@Example.org", "Ali"); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "Sent", note(2, "s2@x", me, 0, addr("Alice Smith", "alice@example.org")))
	if c, _ := stored(t, s, "alice@example.org"); c.Name != "Ali" || c.NameFrom != mail.NameEdited || c.Sent != 2 {
		t.Errorf("the user's name was replaced by mail: %+v", c)
	}
	if c, err := s.RenameContact(ctx, "alice@example.org", ""); err != nil || c.Edited || c.Name != "" {
		t.Fatalf("emptied = %+v, %v", c, err)
	}
	syncMessages(t, s, "a1", "INBOX", note(6, "m6@x", addr("Alice from Accounts", "alice@example.org"), 0, me))
	if got := name(); got != "Alice from Accounts" {
		t.Errorf("after the user emptied the name, mail named her %q", got)
	}
}

// Editing a contact shows in the suggestions at once: a new name finds
// it and the old one no more, a favorite comes first, and a deleted one
// is gone.
func TestContactEditsShowAtOnce(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	syncMessages(t, s, "a1", "INBOX", note(1, "m1@x", addr("Robert Brown", "rb@example.org"), 1, me),
		note(2, "m2@x", addr("Rose", "rose@example.org"), 1, me), note(3, "m3@x", addr("Rose", "rose@example.org"), 2, me))
	if got := emails(t, s, typed("ro")); !slices.Equal(got, []string{"rose@example.org", "rb@example.org"}) {
		t.Fatalf("ro offers %v", got)
	}
	if c, err := s.RenameContact(ctx, "rb@example.org", "Bobby Brown"); err != nil || !c.Edited || c.Name != "Bobby Brown" {
		t.Fatalf("renamed = %+v, %v", c, err)
	}
	if got := emails(t, s, typed("bo")); !slices.Equal(got, []string{"rb@example.org"}) {
		t.Errorf("the new name does not find him: %v", got)
	}
	if got := emails(t, s, typed("robert")); len(got) != 0 {
		t.Errorf("the old name still finds %v", got)
	}
	if _, err := s.FavoriteContact(ctx, "rb@example.org", true); err != nil {
		t.Fatal(err)
	}
	if got := emails(t, s, typed("r")); !slices.Equal(got, []string{"rb@example.org", "rose@example.org"}) {
		t.Errorf("the favorite is not first: %v", got)
	}
	if err := s.DeleteContact(ctx, "rose@example.org", bookNow); err != nil {
		t.Fatal(err)
	}
	if got := emails(t, s, typed("ro")); len(got) != 0 {
		t.Errorf("a deleted contact is offered: %v", got)
	}
}

// Deleting a contact forgets it at once: it leaves the address book and
// the suggestions, and keeps its address alone. Mail it sends later does
// not bring it back or count, nor does mail dated before the deletion
// that arrives later, even mail to it; a message the user writes to it
// afterwards brings it back with that message alone, and so does adding
// it by hand.
func TestDeletedContacts(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	bob := addr("Bob", "bob@example.org")
	syncMessages(t, s, "a1", "INBOX", note(1, "m1@x", bob, 3, me), note(2, "m2@x", bob, 2, me))
	if _, err := s.FavoriteContact(ctx, "bob@example.org", true); err != nil {
		t.Fatal(err)
	}
	deleted := bookNow.Add(-24 * time.Hour)
	if err := s.DeleteContact(ctx, "BOB@example.org", deleted); err != nil {
		t.Fatal(err)
	}
	c, ok := stored(t, s, "bob@example.org")
	if !ok || !c.Deleted || c.Name != "" || c.Favorite || c.Sent+c.Received != 0 || c.SentScore+c.ReceivedScore != 0 || !c.ForgotAt.Equal(deleted) {
		t.Fatalf("deleted row = %+v, %v", c, ok)
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM contact_terms WHERE key = 'bob@example.org'`); got != 0 {
		t.Errorf("a deleted contact keeps %d terms", got)
	}
	if got := emails(t, s, mail.ContactQuery{}); len(got) != 0 {
		t.Errorf("address book = %v", got)
	}
	// His mail, new and old, and mail to him from before.
	syncMessages(t, s, "a1", "INBOX", note(3, "m3@x", bob, 0, me))
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 1,
		Messages: []mail.Message{headerOnly(note(1000, "m0@x", bob, 30, me))}}); err != nil {
		t.Fatal(err)
	}
	syncMessages(t, s, "a1", "Sent", note(1, "s1@x", me, 5, addr("Robert", "bob@example.org")))
	if c, _ := stored(t, s, "bob@example.org"); !c.Deleted || c.Sent+c.Received != 0 {
		t.Fatalf("mail brought him back: %+v", c)
	}
	for _, err := range []error{
		func() error { _, err := s.RenameContact(ctx, "bob@example.org", "B"); return err }(),
		func() error { _, err := s.FavoriteContact(ctx, "bob@example.org", true); return err }(),
		s.DeleteContact(ctx, "bob@example.org", bookNow),
		s.DeleteContact(ctx, "nobody@example.org", bookNow),
	} {
		if !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("an edit of a contact not in the book: %v", err)
		}
	}
	// The user writes to him.
	if err := s.EnqueueOutbox(ctx, mail.PendingSend{ID: "o1", AccountID: "a1", Raw: []byte("raw"), State: mail.StateSending,
		CreatedAt: bookNow, Envelope: mail.Outgoing{To: []mail.Address{addr("Bobby", "bob@example.org")}, MessageID: "o1@x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err != nil {
		t.Fatal(err)
	}
	c, _ = stored(t, s, "bob@example.org")
	if c.Deleted || c.Sent != 1 || c.Received != 0 || c.Name != "Bobby" || c.Favorite {
		t.Fatalf("written to again = %+v", c)
	}
	if got := emails(t, s, typed("bob")); !slices.Equal(got, []string{"bob@example.org"}) {
		t.Errorf("bob offers %v", got)
	}
	// Mail he sent after he was written to counts; mail from before the
	// deletion never does.
	syncMessages(t, s, "a1", "INBOX", note(4, "m4@x", bob, 0, me))
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 1,
		Messages: []mail.Message{headerOnly(note(1001, "m00@x", bob, 60, me))}}); err != nil {
		t.Fatal(err)
	}
	if got := counts(t, s, "bob@example.org"); got != [2]int{1, 1} {
		t.Errorf("bob counts %v", got)
	}

	// Added by hand after a deletion, under the user's name.
	if err := s.DeleteContact(ctx, "bob@example.org", bookNow); err != nil {
		t.Fatal(err)
	}
	if c, err := s.AddContact(ctx, "Robert B.", "Bob@Example.org"); err != nil || !c.Added || !c.Edited || c.Name != "Robert B." || c.Sent+c.Received != 0 {
		t.Fatalf("added again = %+v, %v", c, err)
	}
	if _, err := s.AddContact(ctx, "", "bob@example.org"); !errors.Is(err, mail.ErrContactExists) {
		t.Errorf("added twice: %v", err)
	}
	if _, err := s.AddContact(ctx, "Me", "A1@example.com"); !errors.Is(err, mail.ErrContactOwn) {
		t.Errorf("added the user's own address: %v", err)
	}
}

// A suggestion is a contact a word typed begins a term of: any word of
// its name, its address or a part of the address before the @, in any
// case and any script, the names of Chinese, Japanese and Korean by any
// of their characters. Every word typed must find it; the addresses in
// the field are not offered, each contact is offered once, and no more
// than asked for, from where asked.
func TestContactSuggestions(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	for _, c := range [][2]string{
		{"Alice Smith", "alice.smith@example.com"}, {"Alan Turing", "alan@bletchley.example"},
		{"Bob Alison", "bob@example.com"}, {"王小明", "wang@example.cn"}, {"山田太郎", "taro@example.jp"},
		{"Алиса Петрова", "petrova@example.ru"}, {"Jean-Luc Picard", "picard@example.com"}, {"", "Zed.Last_Name+tag@example.com"},
	} {
		if _, err := s.AddContact(ctx, c[0], c[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		typed   string
		exclude []string
		want    []string
	}{
		{"al", nil, []string{"alan@bletchley.example", "alice.smith@example.com", "bob@example.com"}},
		{"AL", nil, []string{"alan@bletchley.example", "alice.smith@example.com", "bob@example.com"}},
		{"sm", nil, []string{"alice.smith@example.com"}},
		{"alice.sm", nil, []string{"alice.smith@example.com"}},
		{"smith@exa", nil, []string{"alice.smith@example.com"}},
		{"alice sm", nil, []string{"alice.smith@example.com"}},
		{"Smith, Al", nil, []string{"alice.smith@example.com"}},
		{`"Alice Smith" <alice.smith@ex`, nil, []string{"alice.smith@example.com"}},
		{"example", nil, nil},
		{"al", []string{"ALAN@bletchley.example", "bob@example.com"}, []string{"alice.smith@example.com"}},
		{"小明", nil, []string{"wang@example.cn"}},
		{"王", nil, []string{"wang@example.cn"}},
		{"明", nil, []string{"wang@example.cn"}},
		{"太郎", nil, []string{"taro@example.jp"}},
		{"ал", nil, []string{"petrova@example.ru"}},
		{"ПЕТ", nil, []string{"petrova@example.ru"}},
		{"luc", nil, []string{"picard@example.com"}},
		{"jean-l", nil, []string{"picard@example.com"}},
		{"last", nil, []string{"Zed.Last_Name+tag@example.com"}},
		{"name+t", nil, []string{"Zed.Last_Name+tag@example.com"}},
		{"alx", nil, nil},
	} {
		got := emails(t, s, typed(c.typed, c.exclude...))
		want := c.want
		if want == nil {
			want = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%q (excluding %v) offers %v, want %v", c.typed, c.exclude, got, want)
		}
	}
	// Each once, though two of its terms begin with the word, and no more
	// than asked, from where asked.
	all := []string{"alan@bletchley.example", "alice.smith@example.com", "bob@example.com"}
	if got := emails(t, s, mail.ContactQuery{Words: []string{"a"}}); !slices.Equal(got, all) {
		t.Errorf("a offers %v", got)
	}
	if got := emails(t, s, mail.ContactQuery{Words: []string{"a"}, Limit: 2}); !slices.Equal(got, all[:2]) {
		t.Errorf("a with a limit of 2 offers %v", got)
	}
	if got := emails(t, s, mail.ContactQuery{Words: []string{"a"}, Offset: 2}); !slices.Equal(got, all[2:]) {
		t.Errorf("a from the third offers %v", got)
	}
}

// A database of the previous release, v10, gains the address book of the
// mail it holds: the senders and the people the user wrote to, from any
// folder, an alias's mail too, and the blind copies of what the outbox
// sent, each message once, and none of Junk, Drafts or a sender that
// takes no replies. A sync of a message it counted counts nothing again.
func TestMigrationBuildsTheAddressBook(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 10; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	day := func(n int) int64 { return bookNow.Add(-time.Duration(n) * 24 * time.Hour).Unix() }
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`, nil},
		{`INSERT INTO identities (id, account_id, position, name, email) VALUES ('i1', 'a1', 1, 'Lab', 'lab@example.com')`, nil},
		{`INSERT INTO folders (id, account_id, name, special_use) VALUES (1, 'a1', 'INBOX', '\Inbox'), (2, 'a1', 'Sent', '\Sent'),
			(3, 'a1', 'Archive', '\Archive'), (4, 'a1', 'Junk', '\Junk'), (5, 'a1', 'Drafts', '\Drafts')`, nil},
		{`INSERT INTO threads (id, account_id) VALUES (1, 'a1')`, nil},
		{`INSERT INTO messages (folder_id, server_uid, message_id, from_name, from_email, to_json, cc_json, subject, date, thread_id) VALUES
			(1, 1, 'm1@x', 'Alice Smith', 'alice@example.org', '[{"email":"ann@example.com"}]', NULL, 'Hi', ?, 1),
			(3, 1, 'm1@x', 'Alice Smith', 'alice@example.org', '[{"email":"ann@example.com"}]', NULL, 'Hi', ?, 1),
			(1, 2, NULL, 'Bob', 'bob@example.org', NULL, NULL, 'No id', ?, 1),
			(1, 3, 'm3@x', 'Shop', 'noreply@shop.example', NULL, NULL, 'Sale', ?, 1),
			(2, 1, 'out1@example.com', 'Ann', 'ann@example.com', '[{"name":"Dan D.","email":"dan@example.org"}]',
				'[{"email":"erin@example.org"}]', 'Plans', ?, 1),
			(2, 2, 's2@x', 'Lab', 'LAB@example.com', '[{"name":"Gus","email":"gus@example.org"}]', NULL, 'Lab', ?, 1),
			(4, 1, 'j1@x', 'Prize', 'winner@spam.example', NULL, NULL, 'Won', ?, 1),
			(5, 1, 'd1@x', 'Ann', 'ann@example.com', '[{"email":"frank@example.org"}]', NULL, 'Draft', ?, 1)`,
			[]any{day(2), day(2), day(3), day(1), day(5), day(6), day(1), day(1)}},
		{`INSERT INTO outbox (id, account_id, raw, envelope_json, state, created_at) VALUES
			('o1', 'a1', 'raw', '{"to":[{"name":"Dan D.","email":"dan@example.org"}],"cc":[{"email":"erin@example.org"}],"bcc":[{"name":"Hal","email":"hal@example.org"}],"subject":"Plans","text":"x","messageID":"out1@example.com"}', 'sent', ?),
			('o2', 'a1', 'raw', '{"to":[{"email":"ivy@example.org"}],"subject":"Failed","text":"x","messageID":"o2@example.com"}', 'failed', ?)`,
			[]any{day(5), day(4)}},
		{"PRAGMA user_version=10", nil},
	} {
		if _, err := db.Execute(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) || SchemaVersion < 11 {
		t.Fatalf("user_version = %d, schema v%d", got, SchemaVersion)
	}
	for email, want := range map[string][2]int{
		"alice@example.org": {0, 1}, "bob@example.org": {0, 1}, "dan@example.org": {1, 0},
		"erin@example.org": {1, 0}, "hal@example.org": {1, 0}, "gus@example.org": {1, 0},
	} {
		if got := counts(t, s, email); got != want {
			t.Errorf("%s counts %v, want %v", email, got, want)
		}
	}
	for _, email := range []string{"noreply@shop.example", "winner@spam.example", "frank@example.org", "ivy@example.org", "ann@example.com", "lab@example.com"} {
		if _, ok := stored(t, s, email); ok {
			t.Errorf("%s is a contact", email)
		}
	}
	if c, _ := stored(t, s, "dan@example.org"); c.Name != "Dan D." || !c.LastSent.Equal(time.Unix(day(5), 0)) {
		t.Errorf("dan = %+v", c)
	}
	if got := emails(t, s, typed("al")); !slices.Equal(got, []string{"alice@example.org"}) {
		t.Errorf("al offers %v", got)
	}
	if got := emails(t, s, mail.ContactQuery{}); len(got) != 6 || got[0] != "dan@example.org" {
		t.Errorf("address book = %v", got)
	}
	syncMessages(t, s, "a1", "INBOX", note(9, "<m1@x>", addr("Alice Smith", "alice@example.org"), 2, mail.Address{Email: "ann@example.com"}))
	if got := counts(t, s, "alice@example.org"); got != [2]int{0, 1} {
		t.Errorf("a message counted again after the migration: %v", got)
	}
}

// Writes of the address book from several jobs at once lose none of
// their counts, nor the user's edit made meanwhile.
func TestContactsUnderConcurrentWrites(t *testing.T) {
	s := bookStore(t)
	ctx := context.Background()
	alice := addr("Alice", "alice@example.org")
	syncMessages(t, s, "a1", "INBOX", note(1, "first@x", alice, 1, me))
	folders := []string{"INBOX", "Archive", "Sent"}
	const each = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, folder := range folders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := range each {
				uid := mail.UID(100 + n)
				m := note(uid, fmt.Sprintf("%s-%d@x", folder, n), alice, float64(n), me)
				if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
					Folder: mail.Folder{Name: folder, UIDValidity: 1, LastUID: uid}, Messages: []mail.Message{m},
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := range each {
				id := fmt.Sprintf("o%d-%d", i, n)
				if err := s.EnqueueOutbox(ctx, mail.PendingSend{ID: id, AccountID: "a1", Raw: []byte("raw"), State: mail.StateSending,
					CreatedAt: bookNow, Envelope: mail.Outgoing{To: []mail.Address{alice}, MessageID: id + "@x"}}); err != nil {
					t.Error(err)
					return
				}
				if err := s.SetOutboxState(ctx, id, mail.StateSent, ""); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if _, err := s.RenameContact(ctx, "alice@example.org", "Alice (book)"); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	c, _ := stored(t, s, "alice@example.org")
	if c.Received != 1+3*each || c.Sent != 3*each || c.Name != "Alice (book)" {
		t.Errorf("alice = %d sent, %d received, named %q; want %d, %d, the user's name", c.Sent, c.Received, c.Name, 3*each, 1+3*each)
	}
}

// manyContacts writes n contacts straight into the address book: names
// of a few hundred first and last names in Latin, Cyrillic and Chinese,
// with addresses at a hundred domains, and the scores of years of mail.
func manyContacts(t testing.TB, s *Store, n int) {
	t.Helper()
	first := []string{"Alice", "Alan", "Albert", "Alex", "Amy", "Anna", "Bob", "Carol", "Dan", "Erin", "Frank", "Grace",
		"Hal", "Ivy", "John", "Joan", "Kate", "Liam", "Mia", "Noah", "Olga", "Pavel", "Quinn", "Rosa", "Sam", "Tara",
		"Uma", "Vera", "Will", "Xena", "Yuri", "Zoe", "Алиса", "Борис", "王", "李", "张", "刘"}
	last := []string{"Smith", "Jones", "Brown", "Taylor", "Wilson", "Davies", "Evans", "Thomas", "Roberts", "Walker",
		"Петрова", "Иванов", "小明", "伟", "芳", "娜"}
	rng := rand.New(rand.NewPCG(1, 2))
	for start := 0; start < n; start += 10000 {
		b := newContactBook()
		for i := start; i < min(start+10000, n); i++ {
			f, l := first[rng.IntN(len(first))], last[rng.IntN(len(last))]
			email := fmt.Sprintf("%s.%d@domain%d.example", strings.ToLower(l), i, i%100)
			c := b.contact(mail.ContactKey(email))
			c.Email, c.Name = email, f+" "+l
			if rng.IntN(4) == 0 {
				c.Sent, c.SentScore = 1+rng.IntN(20), rng.Float64()*100
			}
			c.Received, c.ReceivedScore = rng.IntN(50), rng.Float64()*100
			c.Favorite = rng.IntN(500) == 0
			b.changed[mail.ContactKey(email)] = true
		}
		if err := s.transaction(context.Background(), b.statements()); err != nil {
			t.Fatal(err)
		}
	}
}

// With 50,000 contacts, a suggestion for one letter, two, a whole word,
// two words, a word nobody's begins and a name in Chinese, and a page of
// the address book near its start and deep in it, each take a small part
// of what a keystroke can wait. The suggestion reads the range of the
// term index its word begins and the page the index of the order. The
// times are logged; the bound is generous, for slow and busy machines and
// the race detector.
func TestManyContactsStayResponsive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an address book of 50,000 contacts")
	}
	const n = 50000
	const budget = 150 * time.Millisecond
	s := bookStore(t)
	ctx := context.Background()
	begin := time.Now()
	manyContacts(t, s, n)
	t.Logf("wrote %d contacts in %v", n, time.Since(begin))
	if got := countRows(t, s, `SELECT COUNT(*) FROM contacts`); got != n {
		t.Fatalf("%d contacts, want %d", got, n)
	}
	for _, r := range []struct {
		name string
		q    mail.ContactQuery
		some bool
	}{
		{"one letter", typed("a"), true},
		{"two letters", typed("al"), true},
		{"a word", typed("alice"), true},
		{"two words", typed("alice sm"), true},
		{"an address", typed("4242@domain42"), true},
		{"a word of nobody's", typed("zq"), false},
		{"a name in Chinese", typed("小明"), true},
		{"the first page", mail.ContactQuery{Limit: 50}, true},
		{"a page deep in it", mail.ContactQuery{Offset: n - 100, Limit: 50}, true},
	} {
		var times []time.Duration
		var got []string
		for range 3 {
			begin := time.Now()
			got = emails(t, s, r.q)
			times = append(times, time.Since(begin))
		}
		slices.Sort(times)
		t.Logf("%-20s %3d contacts  %v", r.name, len(got), times[1])
		if times[1] > budget {
			t.Errorf("%s took %v, more than %v", r.name, times[1], budget)
		}
		if (len(got) > 0) != r.some {
			t.Errorf("%s found %v", r.name, got)
		}
	}
	plan := func(q mail.ContactQuery) string {
		query, args := contactQuery(q, map[string]bool{"a1@example.com": true})
		rows, err := s.query(ctx, `EXPLAIN QUERY PLAN `+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var steps []string
		for _, r := range rows.Values {
			steps = append(steps, text(r[len(r)-1]))
		}
		return strings.Join(steps, "\n")
	}
	p := plan(typed("al"))
	t.Logf("plan of a suggestion:\n%s", p)
	if !strings.Contains(p, "contact_terms USING PRIMARY KEY (term>? AND term<?)") || strings.Contains(p, "SCAN c") {
		t.Errorf("a suggestion does not read the range of the term index:\n%s", p)
	}
	p = plan(mail.ContactQuery{Limit: 50})
	t.Logf("plan of a page:\n%s", p)
	if !strings.Contains(p, "contacts_rank") || strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("a page of the address book does not read the index of the order:\n%s", p)
	}
}

// BenchmarkSuggest measures a suggestion of two letters in an address
// book of 50,000 contacts.
func BenchmarkSuggest(b *testing.B) {
	sqlitetest.Require(b)
	s, err := InMemory(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	manyContacts(b, s, 50000)
	q := typed("al")
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Contacts(context.Background(), q); err != nil {
			b.Fatal(err)
		}
	}
}
