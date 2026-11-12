package mail

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"slices"
	"strings"
	"testing"
	"time"
)

// The words typed to find a contact, and the terms a contact is found by.
func TestContactWordsAndTerms(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"", nil},
		{"  ,; ", nil},
		{"Al", []string{"al"}},
		{"alice SM", []string{"alice", "sm"}},
		{`"Smith, Alice" <alice.sm`, []string{"smith", "alice", "alice.sm"}},
		{"'o'brien", []string{"o'brien"}},
		{"_bob@ex", []string{"bob@ex"}},
		{"王小", []string{"王小"}},
		{"АЛИСА", []string{"алиса"}},
		{"al al", []string{"al"}},
		{"a b c d e f g h i j", []string{"a", "b", "c", "d", "e", "f", "g", "h"}},
	} {
		if got := ContactWords(c.text); !slices.Equal(got, c.want) {
			t.Errorf("ContactWords(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	for _, c := range []struct {
		name, email string
		want        []string
	}{
		{"Alice Smith", "Alice.Smith@Example.com", []string{"alice.smith@example.com", "smith@example.com", "alice", "smith"}},
		{"", "bob@example.com", []string{"bob@example.com"}},
		{"Jean-Luc O'Brien", "jl@example.com", []string{"jl@example.com", "jean-luc", "luc", "o'brien", "brien"}},
		{"王小明", "wang@example.cn", []string{"wang@example.cn", "王小明", "小明", "明"}},
		{"山田 太郎", "taro@example.jp", []string{"taro@example.jp", "山田", "田", "太郎", "郎"}},
		{"李Lee", "li@example.cn", []string{"li@example.cn", "李lee", "lee"}},
		{"Алиса Петрова", "alisa@example.ru", []string{"alisa@example.ru", "алиса", "петрова"}},
		{"(Work) Ann", "z_ann+news@example.com", []string{"z_ann+news@example.com", "ann+news@example.com", "news@example.com", "work)", "ann"}},
	} {
		if got := ContactTerms(c.name, c.email); !slices.Equal(got, c.want) {
			t.Errorf("ContactTerms(%q, %q) = %q, want %q", c.name, c.email, got, c.want)
		}
	}
	if got := ContactTerms(strings.Repeat("word ", 100)+"last", "a@example.com"); len(got) != 3 {
		t.Errorf("a name of the same word is %d terms", len(got))
	}
	var name []string
	for i := range 100 {
		name = append(name, fmt.Sprintf("w%03d", i))
	}
	if got := ContactTerms(strings.Join(name, " "), "a@example.com"); len(got) != maxContactTerms {
		t.Errorf("a long name is %d terms, not at most %d", len(got), maxContactTerms)
	}
}

// Addresses that say they take no replies.
func TestNoReply(t *testing.T) {
	for email, want := range map[string]bool{
		"noreply@github.com": true, "no-reply@accounts.google.com": true, "No_Reply@shop.example": true,
		"donotreply@bank.example": true, "do-not-reply@x.example": true, "noreply+abc@x.example": true,
		"noreply.alerts@x.example": true, "MAILER-DAEMON@mx.example": true, "123+bot@users.noreply.github.com": true,
		"alice@example.com": false, "reply@x.example": false, "notifications@github.com": false,
		"norepl@x.example": false, "noreply": false, "replies@noreplying.example": false,
	} {
		if got := NoReply(email); got != want {
			t.Errorf("NoReply(%q) = %v", email, got)
		}
	}
}

// What a message cached says of the people on it.
func TestMessageContacts(t *testing.T) {
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	own := func(email string) bool {
		return ContactKey(email) == "ann@example.com" || ContactKey(email) == "lab@example.com"
	}
	at := now.Add(-time.Hour)
	for _, c := range []struct {
		name string
		m    Message
		want []ContactEvent
	}{
		{"a sender", Message{Summary: Summary{From: Address{"Alice", "alice@example.org"}, To: []Address{{Email: "ann@example.com"}}, Date: at}},
			[]ContactEvent{{Email: "alice@example.org", Name: "Alice", At: at}}},
		{"the user's own, to others", Message{Summary: Summary{From: Address{"Ann", "Ann@Example.com"}, Date: at,
			To: []Address{{"Bob", "bob@example.org"}, {"Me", "ann@example.com"}, {"Lab", "LAB@example.com"}},
			Cc: []Address{{"Robert", "BOB@example.org"}, {"", "carol@example.org"}, {"", ""}, {"Bad", "bad address@x"}}}},
			[]ContactEvent{{Email: "bob@example.org", Name: "Bob", Sent: true, At: at}, {Email: "carol@example.org", Sent: true, At: at}}},
		{"an alias's", Message{Summary: Summary{From: Address{"Lab", "lab@example.com"}, To: []Address{{"Gus", "gus@example.org"}}, Date: at}},
			[]ContactEvent{{Email: "gus@example.org", Name: "Gus", Sent: true, At: at}}},
		{"no reply", Message{Summary: Summary{From: Address{"Shop", "no-reply@shop.example"}, Date: at}}, nil},
		{"no sender", Message{Summary: Summary{To: []Address{{Email: "bob@example.org"}}, Date: at}}, nil},
		{"a broken sender", Message{Summary: Summary{From: Address{"X", "x@@example.org"}, Date: at}}, nil},
		{"a name that is the address", Message{Summary: Summary{From: Address{"'ALICE@example.org'", "alice@example.org"}, Date: at}},
			[]ContactEvent{{Email: "alice@example.org", At: at}}},
		{"a name that is another address", Message{Summary: Summary{From: Address{"al@old.example", "alice@example.org"}, Date: at}},
			[]ContactEvent{{Email: "alice@example.org", At: at}}},
		{"a name with control characters", Message{Summary: Summary{From: Address{"Alice\r\n\tSmith\x00", "alice@example.org"}, Date: at}},
			[]ContactEvent{{Email: "alice@example.org", Name: "Alice Smith", At: at}}},
		{"a date in the future", Message{Summary: Summary{From: Address{"Alice", "alice@example.org"}, Date: now.AddDate(3, 0, 0)}},
			[]ContactEvent{{Email: "alice@example.org", Name: "Alice", At: now}}},
		{"no date", Message{Summary: Summary{From: Address{"Alice", "alice@example.org"}}},
			[]ContactEvent{{Email: "alice@example.org", Name: "Alice", At: time.Unix(0, 0).UTC()}}},
	} {
		got := MessageContacts(c.m, own, now)
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i].Email != c.want[i].Email || got[i].Name != c.want[i].Name || got[i].Sent != c.want[i].Sent || !got[i].At.Equal(c.want[i].At) {
				t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			}
		}
	}
	out := Outgoing{
		To:  []Address{{"Bob", "bob@example.org"}, {"Me", "ann@example.com"}},
		Cc:  []Address{{"", "carol@example.org"}, {"Bobby", "Bob@example.org"}},
		Bcc: []Address{{"Erin", "erin@example.org"}},
	}
	got := SentContacts(out, at.Add(time.Second/2), own)
	var names []string
	for _, e := range got {
		names = append(names, e.Email+"/"+e.Name)
		if !e.Sent || !e.At.Equal(at) {
			t.Errorf("sent event = %+v", e)
		}
	}
	if !slices.Equal(names, []string{"bob@example.org/Bob", "carol@example.org/", "erin@example.org/Erin"}) {
		t.Errorf("sent to %v", names)
	}
}

// A message counts by its Message-ID, however it is written, and one
// without by its sender, date and subject.
func TestContactMessageKey(t *testing.T) {
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	if a, b := ContactMessageKey("<m1@Example.ORG>", "x@example.org", at, "A"), ContactMessageKey(" m1@example.org ", "", time.Time{}, ""); a != b || a != "m1@example.org" {
		t.Errorf("keys of one Message-ID: %q, %q", a, b)
	}
	base := ContactMessageKey("", "Bob@example.org", at, "Hi")
	if again := ContactMessageKey("not an id", "bob@EXAMPLE.org", at, "Hi"); again != base {
		t.Errorf("a message without a Message-ID counts as %q, then %q", base, again)
	}
	for _, other := range []string{
		ContactMessageKey("", "carol@example.org", at, "Hi"),
		ContactMessageKey("", "bob@example.org", at.Add(time.Second), "Hi"),
		ContactMessageKey("", "bob@example.org", at, "Hi again"),
	} {
		if other == base || strings.Contains(other, "@") {
			t.Errorf("key %q of another message", other)
		}
	}
}

// The rules of a contact: what names it, what counts for it, and what
// deleting it does.
func TestContactStateRules(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC).AddDate(0, 0, n) }
	var c ContactState
	apply := func(e ContactEvent) bool {
		t.Helper()
		return c.Apply(e)
	}
	apply(ContactEvent{Email: "Alice@example.org", Name: "Alice S.", At: day(0)})
	apply(ContactEvent{Email: "alice@example.org", Name: "Ally", At: day(-5)})
	if c.Name != "Alice S." || c.Email != "Alice@example.org" || c.Received != 2 || !c.LastReceived.Equal(day(0)) {
		t.Errorf("after two received = %+v", c)
	}
	apply(ContactEvent{Email: "alice@example.org", Name: "Alice", Sent: true, At: day(-10)})
	apply(ContactEvent{Email: "alice@example.org", Name: "ALICE", At: day(1)})
	if c.Name != "Alice" || c.NameFrom != NameWritten || c.Email != "alice@example.org" || c.Sent != 1 {
		t.Errorf("the name the user wrote is not kept: %+v", c)
	}
	c.Rename("Ali")
	apply(ContactEvent{Email: "alice@example.org", Name: "Alice Smith", Sent: true, At: day(2)})
	if contact := c.Contact(); contact.Name != "Ali" || !contact.Edited || contact.Sent != 2 {
		t.Errorf("the user's name is not kept: %+v", contact)
	}
	c.Rename("")
	apply(ContactEvent{Email: "alice@example.org", Name: "Alice Smith", At: day(3)})
	if contact := c.Contact(); contact.Name != "Alice Smith" || contact.Edited {
		t.Errorf("an emptied name is not the mail's again: %+v", contact)
	}

	// Deleted, it keeps its address alone.
	c.Favorite = true
	c.Forget(day(4).Add(time.Second / 3))
	if c != (ContactState{Email: "alice@example.org", Deleted: true, ForgotAt: day(4)}) {
		t.Fatalf("forgotten = %+v", c)
	}
	if apply(ContactEvent{Email: "alice@example.org", Name: "Alice", At: day(5)}) || !c.Deleted {
		t.Error("mail from a deleted contact brought it back")
	}
	if apply(ContactEvent{Email: "alice@example.org", Name: "Alice", Sent: true, At: day(3)}) || !c.Deleted {
		t.Error("mail to a deleted contact from before the deletion brought it back")
	}
	if !apply(ContactEvent{Email: "alice@example.org", Name: "Al", Sent: true, At: day(4)}) || c.Deleted || c.Sent != 1 || c.Received != 0 || c.Name != "Al" {
		t.Fatalf("written to again = %+v", c)
	}
	if apply(ContactEvent{Email: "alice@example.org", At: day(1)}) || c.Received != 0 {
		t.Error("mail from before the deletion counts after the contact came back")
	}
	// A later deletion does not move the time back.
	c.Forget(day(2))
	if !c.ForgotAt.Equal(day(4)) {
		t.Errorf("forgot at %v", c.ForgotAt)
	}
	c.Add("Alice Added", "alice@example.org")
	if contact := c.Contact(); !contact.Added || !contact.Edited || c.Deleted || contact.Sent+contact.Received != 0 {
		t.Errorf("added = %+v", contact)
	}

	// The weight of a message halves every 30 days.
	if r := contactWeight(day(30)) / contactWeight(day(0)); math.Abs(r-2) > 1e-9 {
		t.Errorf("a month later a message weighs %v times as much", r)
	}
	if w := contactWeight(time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)); math.IsInf(w, 0) || math.IsNaN(w) {
		t.Errorf("a far date weighs %v", w)
	}
	if w := contactWeight(time.Unix(0, 0)); w <= 0 {
		t.Errorf("a message of 1970 weighs %v", w)
	}
}

// A recipient field splits at the commas between its entries, not at
// those in quotes, comments, angle brackets or groups, and says where each
// entry is.
func TestRecipientEntries(t *testing.T) {
	field := ` "Smith, Alice" <alice@example.org>, bob (Robert, B.) <bob@example.org> ,, Team: a@x.org, b@x.org;, <"odd, one"@x.org>, al, bob@ `
	var got []string
	for _, e := range RecipientEntries(field) {
		got = append(got, e.Text)
		if strings.TrimSpace(field[e.Start:e.End]) != e.Text {
			t.Errorf("entry %q is at %q", e.Text, field[e.Start:e.End])
		}
	}
	want := []string{`"Smith, Alice" <alice@example.org>`, `bob (Robert, B.) <bob@example.org>`, `Team: a@x.org, b@x.org;`,
		`<"odd, one"@x.org>`, `al`, `bob@`}
	if !slices.Equal(got, want) {
		t.Fatalf("entries = %q\nwant %q", got, want)
	}
	entries := RecipientEntries(field)
	if !entries[0].Valid || entries[0].Addresses[0] != (Address{"Smith, Alice", "alice@example.org"}) {
		t.Errorf("first = %+v", entries[0])
	}
	if !entries[2].Valid || len(entries[2].Addresses) != 2 {
		t.Errorf("group = %+v", entries[2])
	}
	if entries[4].Valid || entries[5].Valid || len(entries[4].Addresses) != 0 {
		t.Errorf("a name and a broken address read as addresses: %+v, %+v", entries[4], entries[5])
	}
	if len(RecipientEntries("  ")) != 0 || len(RecipientEntries("a@x.org")) != 1 {
		t.Error("an empty field has entries, or one address does not")
	}
}

// contactService is a service on the in-memory store with the account
// ann@example.com and the contacts given, added by hand.
func contactService(t *testing.T, contacts ...[2]string) (*Service, *memStore) {
	t.Helper()
	svc, store, _ := newTestService()
	ctx := context.Background()
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	for _, c := range contacts {
		if _, err := svc.AddContact(ctx, c[0], c[1]); err != nil {
			t.Fatalf("add %v: %v", c, err)
		}
	}
	return svc, store
}

// A recipient field is checked before a message goes: a field net/mail
// reads is its addresses, as before, and nothing else is; a broken
// address, a bare name of nobody, one of several contacts and one of a
// single contact are told apart, the last with the contact to offer.
func TestCheckRecipients(t *testing.T) {
	ctx := context.Background()
	svc, _ := contactService(t, [2]string{"Alice Smith", "alice@example.org"}, [2]string{"Alan Turing", "alan@example.org"},
		[2]string{"王小明", "wang@example.cn"})
	check := func(field string) RecipientCheck {
		t.Helper()
		c, err := svc.CheckRecipients(ctx, field)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, field := range []string{"", "  ", "alice@example.org", `"Smith, Alice" <alice@example.org>, bob@example.org,`,
		"Team: a@x.org, b@x.org;", "王小明 <wang@example.cn>"} {
		c := check(field)
		want, _ := ParseAddressList([]string{field})
		if !c.OK() || !slices.Equal(c.Addresses, want) {
			t.Errorf("%q = %+v, want the addresses %+v", field, c, want)
		}
	}
	c := check("bob@example.org, bob@, Alice, al, Zed, 小明, alice@example.org;carol@example.org")
	if c.OK() || !slices.Equal(c.Addresses, []Address{{Email: "bob@example.org"}}) {
		t.Fatalf("check = %+v", c)
	}
	type problem struct {
		text    string
		kind    RecipientKind
		matches int
	}
	var got []problem
	for _, p := range c.Problems {
		got = append(got, problem{p.Text, p.Kind, len(p.Matches)})
	}
	want := []problem{{"bob@", RecipientInvalid, 0}, {"Alice", RecipientMatch, 1}, {"al", RecipientAmbiguous, 2},
		{"Zed", RecipientUnknown, 0}, {"小明", RecipientMatch, 1}, {"alice@example.org;carol@example.org", RecipientInvalid, 0}}
	if !slices.Equal(got, want) {
		t.Errorf("problems = %+v\nwant %+v", got, want)
	}
	if m := c.Problems[1].Matches[0]; m.Email != "alice@example.org" || m.Address().String() != "Alice Smith <alice@example.org>" {
		t.Errorf("the match offered = %+v", m)
	}
	field := "bob@example.org, bob@, Alice"
	for _, p := range check(field).Problems {
		if strings.TrimSpace(field[p.Start:p.End]) != p.Text {
			t.Errorf("problem %q is at %q", p.Text, field[p.Start:p.End])
		}
	}
	// What net/mail refuses whole is never sent, though each part reads
	// alone.
	for _, field := range []string{"a@x.org; b@x.org", "a@x.org b@x.org", "<a@x.org", "Alice Smith"} {
		if c := check(field); c.OK() {
			t.Errorf("%q passed as %+v", field, c.Addresses)
		}
	}
	if c := check("Team: a@x.org"); c.OK() || len(c.Problems) != 1 || c.Problems[0].Kind != RecipientInvalid {
		t.Errorf("an unclosed group = %+v", c)
	}
}

// What a suggestion puts in a field reads back as its address, whatever
// its name holds.
func TestSuggestionsWriteValidAddresses(t *testing.T) {
	ctx := context.Background()
	names := []string{"Smith, John", `Quote "Q" Person`, "Ann (Lab)", "Back\\slash", "王小明", "Émilie Ünal", "Emoji 😀",
		"x@y.example", "Dr. Who", "<angle>", "semi;colon", "a:b"}
	var contacts [][2]string
	for i, name := range names {
		contacts = append(contacts, [2]string{name, fmt.Sprintf("zq%c@example.org", 'a'+i)})
	}
	svc, _ := contactService(t, contacts...)
	var field []string
	for i := range names {
		got, err := svc.Suggest(ctx, fmt.Sprintf("zq%c", 'a'+i), nil, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("suggest %d = %+v, %v", i, got, err)
		}
		text := got[0].Address().String()
		list, err := mail.ParseAddressList(text)
		if err != nil || len(list) != 1 || list[0].Address != got[0].Email {
			t.Errorf("%q is written %q, which reads as %+v, %v", got[0].Name, text, list, err)
		}
		field = append(field, text)
	}
	c, err := svc.CheckRecipients(ctx, strings.Join(field, ", ")+", ")
	if err != nil || !c.OK() || len(c.Addresses) != len(names) {
		t.Errorf("a field of every suggestion = %+v, %v", c, err)
	}
}

// The calls of the address book: what can be added, what is offered and
// in which pages, and edits of what is not there.
func TestContactCalls(t *testing.T) {
	ctx := context.Background()
	svc, store := contactService(t)
	if _, err := svc.AddIdentity(ctx, "a1", "Lab", "lab@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"", "bob", "bob@", "Bob <bob@example.org>", "bob@example.org, carol@example.org", "bo b@example.org"} {
		if _, err := svc.AddContact(ctx, "Bob", email); !errors.Is(err, ErrContactAddress) {
			t.Errorf("added %q: %v", email, err)
		}
	}
	for _, email := range []string{"ANN@example.com", "lab@example.com"} {
		if _, err := svc.AddContact(ctx, "Me", email); !errors.Is(err, ErrContactOwn) {
			t.Errorf("added the user's own %q: %v", email, err)
		}
	}
	c, err := svc.AddContact(ctx, "  Bob\r\nBuilder\x07 ", " bob@example.org ")
	if err != nil || c.Name != "Bob Builder" || c.Email != "bob@example.org" || !c.Added || !c.Edited {
		t.Fatalf("added = %+v, %v", c, err)
	}
	if _, err := svc.AddContact(ctx, "", "BOB@example.org"); !errors.Is(err, ErrContactExists) {
		t.Errorf("added twice: %v", err)
	}
	for _, err := range []error{
		func() error { _, err := svc.RenameContact(ctx, "nobody@example.org", "N"); return err }(),
		func() error { _, err := svc.FavoriteContact(ctx, "nobody@example.org", true); return err }(),
		svc.DeleteContact(ctx, "nobody@example.org"),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("an edit of nobody: %v", err)
		}
	}
	if c, err := svc.RenameContact(ctx, "bob@example.org", "\tRobert\n"); err != nil || c.Name != "Robert" {
		t.Errorf("renamed = %+v, %v", c, err)
	}
	for i := range 7 {
		if _, err := svc.AddContact(ctx, "Robin", "robin"+string(rune('a'+i))+"@example.org"); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := svc.Suggest(ctx, "  ", nil, 0); len(got) != 0 {
		t.Errorf("nothing typed offers %+v", got)
	}
	if got, _ := svc.Suggest(ctx, "ro", []string{"ROBINA@example.org"}, 3); len(got) != 3 || got[0].Email != "bob@example.org" {
		t.Errorf("ro offers %+v", got)
	}
	// A contact that is an identity added since is the user's own.
	store.mu.Lock()
	store.contacts[ContactKey("team@example.com")] = &ContactState{Email: "team@example.com", Name: "Robin Team", Added: true}
	store.mu.Unlock()
	if _, err := svc.AddIdentity(ctx, "a1", "Team", "team@example.com"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Suggest(ctx, "robin team", nil, 0); len(got) != 0 {
		t.Errorf("the user's own address is offered: %+v", got)
	}
	page, err := svc.Contacts(ctx, "", 0, 5)
	if err != nil || len(page.Contacts) != 5 || !page.More {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := svc.Contacts(ctx, "", 5, 5)
	if err != nil || len(next.Contacts) != 3 || next.More || next.Contacts[0].Email == page.Contacts[4].Email {
		t.Fatalf("second page = %+v, %v", next, err)
	}
	if found, _ := svc.Contacts(ctx, "rob", 0, 0); len(found.Contacts) != 8 || found.More {
		t.Errorf("a search of the book = %+v", found)
	}
	if none, _ := svc.Contacts(ctx, "zz", 0, 0); none.Contacts == nil || len(none.Contacts) != 0 {
		t.Errorf("an empty search = %#v", none)
	}
}

// The address book on the in-memory store, as the SQLite store keeps it
// (contacts_sqlite_test.go).
func TestContactsScenarioInMemory(t *testing.T) {
	ContactsScenario(t, newMemStore())
}
