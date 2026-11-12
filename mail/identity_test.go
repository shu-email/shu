package mail

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// An account sends from its own address, first, and from the identities
// added, in the order they were. An identity is one address, which the
// account does not send from already, whatever its case; its name keeps
// no character that could end a header.
func TestIdentitiesAreKeptInOrder(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	a := testAccount()
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	list, err := svc.Identities(ctx, a.ID)
	if err != nil || len(list) != 1 || list[0] != (Identity{AccountID: a.ID, Name: "Ann", Email: "ann@example.com"}) {
		t.Fatalf("identities of a new account = %+v, %v", list, err)
	}
	sales, err := svc.AddIdentity(ctx, a.ID, "Ann at Sales", " sales@example.com ")
	if err != nil {
		t.Fatal(err)
	}
	home, err := svc.AddIdentity(ctx, a.ID, "Ann\r\nBcc: evil@example.com", "ann@home.example")
	if err != nil {
		t.Fatal(err)
	}
	if sales.ID == "" || home.ID == "" || sales.ID == home.ID || sales.Email != "sales@example.com" ||
		home.Name != "AnnBcc: evil@example.com" {
		t.Errorf("added %+v and %+v", sales, home)
	}
	list, _ = svc.Identities(ctx, a.ID)
	if len(list) != 3 || list[0].ID != "" || list[1] != sales || list[2] != home {
		t.Fatalf("identities = %+v", list)
	}

	for _, c := range []struct {
		name, email string
		want        error
	}{
		{"", "", ErrIdentityAddress},
		{"", "not an address", ErrIdentityAddress},
		{"", "Ann <other@example.com>", ErrIdentityAddress},
		{"", "a@example.com, b@example.com", ErrIdentityAddress},
		{"", "x@example.com\r\nBcc: evil@example.com", ErrIdentityAddress},
		{"Me", "ANN@example.com", ErrIdentityExists},
		{"Me", "Sales@Example.com", ErrIdentityExists},
	} {
		if _, err := svc.AddIdentity(ctx, a.ID, c.name, c.email); !errors.Is(err, c.want) {
			t.Errorf("adding %q = %v, want %v", c.email, err, c.want)
		}
	}
	if _, err := svc.AddIdentity(ctx, "nobody", "", "x@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an identity for no account = %v", err)
	}

	// A change keeps the identity's place, and the same checks.
	sales.Name, sales.Email = "Sales", "deals@example.com"
	if _, err := svc.UpdateIdentity(ctx, sales); err != nil {
		t.Fatal(err)
	}
	home.Email = "DEALS@example.com"
	if _, err := svc.UpdateIdentity(ctx, home); !errors.Is(err, ErrIdentityExists) {
		t.Errorf("changing to another identity's address = %v", err)
	}
	if _, err := svc.UpdateIdentity(ctx, Identity{AccountID: a.ID, Email: "x@example.com"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("changing the account's own = %v", err)
	}
	list, _ = svc.Identities(ctx, a.ID)
	if len(list) != 3 || list[1].Name != "Sales" || list[1].Email != "deals@example.com" || list[2].Email != "ann@home.example" {
		t.Errorf("identities after a change = %+v", list)
	}

	if err := svc.DeleteIdentity(ctx, a.ID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting no identity = %v", err)
	}
	if err := svc.DeleteIdentity(ctx, a.ID, sales.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.Identities(ctx, a.ID); len(list) != 2 || list[1].ID != home.ID || list[1].Email != "ann@home.example" {
		t.Errorf("identities after a deletion = %+v", list)
	}
	if err := svc.RemoveAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.identities) != 0 {
		t.Errorf("identities outlived their account: %+v", store.identities)
	}
}

// A draft keeps the identity it is written from. When that identity is
// deleted the draft goes from the account's own address, and says so: an
// edit, which its server copy follows. A message naming an identity that
// is gone goes from the account's own address too.
func TestDeletedIdentityFallsBackToTheAccount(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	alias, err := env.svc.AddIdentity(ctx, env.a.ID, "Sales", "ann@sales.example.net")
	if err != nil {
		t.Fatal(err)
	}
	d := mustSaveDraft(t, env.svc, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Offer", Identity: alias.ID})
	if d.Identity != alias.ID || !strings.HasSuffix(d.MessageID, "@sales.example.net") {
		t.Fatalf("draft = %+v", d)
	}
	mustSyncDrafts(t, env.svc, env.a.ID)
	if copies := env.imap.messages(t, "Drafts"); len(copies) != 1 || copies[0].From != alias.Address() {
		t.Fatalf("server copy from %+v", copies)
	}
	list, _ := env.svc.DraftList(ctx, env.a.ID, "", 10)
	if len(list) != 1 || list[0].From != alias.Address() {
		t.Errorf("the Drafts list shows %+v", list)
	}

	if err := env.svc.DeleteIdentity(ctx, env.a.ID, alias.ID); err != nil {
		t.Fatal(err)
	}
	after := mustDraft(t, env.svc, d.ID)
	if after.Identity != "" || after.Revision <= d.Revision || !after.Dirty() {
		t.Fatalf("draft after its identity went = %+v", after)
	}
	mustSyncDrafts(t, env.svc, env.a.ID)
	own := Address{Name: env.a.Name, Email: env.a.Email}
	if copies := env.imap.messages(t, "Drafts"); len(copies) != 1 || copies[0].From != own {
		t.Errorf("server copy after the identity went is from %+v", copies)
	}
	if list, _ := env.svc.DraftList(ctx, env.a.ID, "", 10); len(list) != 1 || list[0].From != own {
		t.Errorf("the Drafts list shows %+v", list)
	}

	out := after.outgoing()
	out.Identity, out.DraftID = alias.ID, after.ID
	result, err := env.svc.Send(ctx, env.a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outbox.Envelope.Identity != "" || result.Outbox.Envelope.From != own {
		t.Errorf("envelope = %+v", result.Outbox.Envelope)
	}
	if got := env.smtp.senders(); !slices.Equal(got, []string{env.a.Email}) {
		t.Errorf("sent from %v", got)
	}
}

// A reply starts from the identity the message was addressed to: by its
// To, then its Cc, then the Delivered-To its servers added. Addresses
// compare without case; the account's own is the answer otherwise.
func TestReplyIdentity(t *testing.T) {
	own := Identity{AccountID: "a1", Name: "Ann", Email: "ann@example.com"}
	sales := Identity{ID: "i1", AccountID: "a1", Name: "Sales", Email: "sales@example.com"}
	support := Identity{ID: "i2", AccountID: "a1", Name: "Support", Email: "Support@Example.com"}
	identities := []Identity{own, sales, support}
	forwarded, err := ParseMessage([]byte("Delivered-To: ann@gmail.example\r\nDelivered-To: Sales Team <SALES@example.com>\r\n" +
		"From: Bob <bob@example.com>\r\nTo: team@example.com\r\nSubject: Hi\r\n\r\nHello\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := forwarded.DeliveredTo(); len(got) != 2 || got[0].Email != "ann@gmail.example" || got[1].Email != "SALES@example.com" {
		t.Fatalf("delivered to %+v, kept as %q", got, forwarded.RawHeaders)
	}
	at := func(to, cc []Address) Message { return Message{Summary: Summary{To: to, Cc: cc}} }
	bob := Address{Email: "bob@example.com"}
	for _, c := range []struct {
		name string
		m    Message
		want Identity
	}{
		{"to an alias", at([]Address{bob, {Email: "SALES@example.com"}}, nil), sales},
		{"cc to an alias", at([]Address{bob}, []Address{{Email: "support@example.com"}}), support},
		{"to before cc", at([]Address{{Email: "sales@example.com"}}, []Address{{Email: "support@example.com"}}), sales},
		{"own first in to", at([]Address{{Email: "ann@example.com"}, {Email: "sales@example.com"}}, nil), own},
		{"delivered to an alias", forwarded, sales},
		{"to nobody of the account's", at([]Address{bob}, nil), own},
		{"no addresses", Message{}, own},
	} {
		if got := ReplyIdentity(identities, c.m); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := ReplyIdentity(nil, at([]Address{bob}, nil)); got != (Identity{}) {
		t.Errorf("without identities: %+v", got)
	}
	// A header line that is no address names nothing.
	if got := (Message{RawHeaders: "Delivered-To: not an address\r\nX: y\r\n"}).DeliveredTo(); len(got) != 0 {
		t.Errorf("delivered to %+v", got)
	}
}

// A message sent from an alias names it in its From, under its name, and
// goes from it in the envelope too (MAIL FROM), as Thunderbird sends. Its
// Message-ID is of the alias's domain, the copy in Sent is from it, and
// the outbox keeps the address, so that it is sent again from it.
func TestAliasSendsAndSavesFromItself(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	alias, err := env.svc.AddIdentity(ctx, env.a.ID, "Ann (Lab)", "ann@lab.example.org")
	if err != nil {
		t.Fatal(err)
	}
	out := Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "From the lab", Text: "Hello", Identity: alias.ID}
	result, err := env.svc.Send(ctx, env.a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if got := env.smtp.senders(); !slices.Equal(got, []string{"ann@lab.example.org"}) {
		t.Errorf("envelope sender = %v", got)
	}
	received := env.smtp.received()
	if len(received) != 1 {
		t.Fatalf("delivered %d messages", len(received))
	}
	sent := env.imap.messages(t, "Sent")
	if len(sent) != 1 {
		t.Fatalf("Sent holds %d messages", len(sent))
	}
	for what, raw := range map[string]string{"delivered": received[0], "sent copy": sent[0].Raw} {
		m, err := ParseMessage([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if m.From != alias.Address() || !strings.HasSuffix(m.MessageID, "@lab.example.org") {
			t.Errorf("%s: from %+v, Message-ID %q", what, m.From, m.MessageID)
		}
		if !strings.Contains(raw, `From: "Ann (Lab)" <ann@lab.example.org>`) {
			t.Errorf("%s: From header:\n%.400s", what, raw)
		}
		if strings.Contains(raw, env.a.Email) {
			t.Errorf("%s names the account's own address:\n%.600s", what, raw)
		}
	}
	if e := result.Outbox.Envelope; e.Identity != alias.ID || e.From != alias.Address() {
		t.Errorf("outbox envelope = %+v", e)
	}

	// Refused once, the message is sent again from the alias, though the
	// alias was deleted meanwhile: it is the message the user sent.
	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Send(ctx, env.a.ID, out); err == nil {
		t.Fatal("a send with a wrong password succeeded")
	}
	if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.DeleteIdentity(ctx, env.a.ID, alias.ID); err != nil {
		t.Fatal(err)
	}
	pending, _ := env.svc.PendingSends(ctx, env.a.ID)
	if len(pending) != 1 {
		t.Fatalf("outbox = %+v", pending)
	}
	if _, err := env.restart().RetryPending(ctx, pending[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := env.smtp.senders(); !slices.Equal(got, []string{"ann@lab.example.org", "ann@lab.example.org"}) {
		t.Errorf("envelope senders = %v", got)
	}
	if m, err := ParseMessage([]byte(env.smtp.received()[1])); err != nil || m.From != alias.Address() {
		t.Errorf("the retry went from %+v, %v", m.From, err)
	}
}

// A draft another mail client wrote from one of the account's aliases is
// taken over as written from that identity.
func TestAdoptedDraftKeepsItsIdentity(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	alias, err := svc.AddIdentity(ctx, a.ID, "Sales", "sales@example.com")
	if err != nil {
		t.Fatal(err)
	}
	f.append(t, "Drafts", []byte("From: Sales <SALES@example.com>\r\nTo: bob@example.com\r\n"+
		"Bcc: audit@example.com\r\nSubject: Quote\r\nMessage-ID: <quote-1@example.com>\r\n\r\nPrices.\r\n"))
	f.append(t, "Drafts", foreignDraft("plain-1@example.com", "Plain", "Hi"))
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	d, err := svc.AdoptDraft(ctx, a.ID, "Drafts", 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Identity != alias.ID || d.Bcc != "audit@example.com" {
		t.Errorf("draft from the alias = %+v", d)
	}
	other, err := svc.AdoptDraft(ctx, a.ID, "Drafts", 2)
	if err != nil {
		t.Fatal(err)
	}
	if other.Identity != "" {
		t.Errorf("draft from the account's own address has identity %q", other.Identity)
	}
}
