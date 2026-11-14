package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"shu/mail"
)

func TestOutboxTransitionsAndCrashRecovery(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	now := time.Unix(1_700_000_000, 0)
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
		Envelope:  mail.Outgoing{To: []mail.Address{{Email: "a@example.com"}}, Subject: "hi"},
		CreatedAt: now,
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Outbox(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != mail.StatePending || string(got.Raw) != string(p.Raw) || got.Envelope.Subject != "hi" {
		t.Errorf("outbox = %+v", got)
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateSending, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingSends(ctx, "a1")
	if err != nil || len(pending) != 1 || pending[0].State != mail.StateUnknown {
		t.Fatalf("crash recovery = %+v, %v", pending, err)
	}
	if err := s.SetOutboxState(ctx, "o1", mail.StateSent, ""); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingSends(ctx, "a1")
	if len(pending) != 0 {
		t.Errorf("sent entry still pending: %+v", pending)
	}
	if _, err := s.Outbox(ctx, "missing"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing outbox error = %v", err)
	}
}

// A draft comes back exactly as written: the recipient text as typed,
// with its linkage, at revision 1 and with nothing on the server.

func TestOutboxEnvelopeLeavesOutTheDraft(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	p := mail.PendingSend{
		ID: "o1", AccountID: "a1", Raw: []byte("Subject: hi\r\n\r\nbody"),
		Envelope: mail.Outgoing{
			To: []mail.Address{{Email: "a@example.com"}}, Subject: "hi",
			MessageID: "m1@example.com", DraftID: "d1",
		},
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	if err := s.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Outbox(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Envelope.DraftID != "" || got.Envelope.MessageID != "m1@example.com" || got.Envelope.Subject != "hi" {
		t.Errorf("stored envelope = %+v", got.Envelope)
	}
	rows, err := s.query(ctx, `SELECT envelope_json FROM outbox WHERE id = 'o1'`)
	if err != nil {
		t.Fatal(err)
	}
	if raw := text(rows.Values[0][0]); strings.Contains(raw, "d1") || strings.Contains(strings.ToLower(raw), "draft") {
		t.Errorf("envelope JSON names the draft: %s", raw)
	}
}

// A database from before drafts gains the table and keeps its data.
