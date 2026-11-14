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
	// A sent entry keeps its row, and the bytes it held for delivery go:
	// the Sent copy is made from the bytes the send held.
	sent, err := s.Outbox(ctx, "o1")
	if err != nil || len(sent.Raw) != 0 || sent.State != mail.StateSent || sent.Attempts != 1 {
		t.Errorf("sent entry = %+v, %v", sent, err)
	}
	if _, err := s.Outbox(ctx, "missing"); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("missing outbox error = %v", err)
	}
}

// An entry that reaches a state no send follows drops the bytes it kept
// for delivery; one a send may still make keeps them. The row stays with
// what it says of the message either way.
func TestOutboxDropsTheBytesOfAnEntryNoSendFollows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	addAccount(t, s, "a1")
	now := time.Unix(1_700_000_000, 0)
	for _, p := range []mail.PendingSend{
		{ID: "sent", AccountID: "a1", Raw: []byte("sent"), CreatedAt: now},
		{ID: "cancelled", AccountID: "a1", Raw: []byte("cancelled"), CreatedAt: now},
		{ID: "taken-back", AccountID: "a1", Raw: []byte("taken back"), CreatedAt: now, State: mail.StateScheduled, DueAt: now.Add(time.Hour)},
		{ID: "due", AccountID: "a1", Raw: []byte("due"), CreatedAt: now, State: mail.StateScheduled, DueAt: now.Add(time.Hour)},
		{ID: "failed", AccountID: "a1", Raw: []byte("failed"), CreatedAt: now},
		{ID: "waiting", AccountID: "a1", Raw: []byte("waiting"), CreatedAt: now, State: mail.StateScheduled, DueAt: now.Add(time.Hour)},
	} {
		p.Envelope = mail.Outgoing{To: []mail.Address{{Email: "a@example.com"}}, Subject: p.ID}
		if err := s.EnqueueOutbox(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for id, state := range map[string]string{
		"sent": mail.StateSent, "cancelled": mail.StateCancelled, "failed": mail.StateFailed,
	} {
		if err := s.SetOutboxState(ctx, id, state, ""); err != nil {
			t.Fatal(err)
		}
	}
	if won, err := s.ClaimOutbox(ctx, "taken-back", mail.StateScheduled, mail.StateCancelled); err != nil || !won {
		t.Fatalf("taking back = %v, %v", won, err)
	}
	if won, err := s.ClaimOutbox(ctx, "due", mail.StateScheduled, mail.StateSending); err != nil || !won {
		t.Fatalf("claiming due = %v, %v", won, err)
	}
	for id, emptied := range map[string]bool{
		"sent": true, "cancelled": true, "taken-back": true,
		"due": false, "failed": false, "waiting": false,
	} {
		p, err := s.Outbox(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(p.Raw) == 0; got != emptied {
			t.Errorf("%s emptied = %v, want %v", id, got, emptied)
		}
		if p.Envelope.Subject != id {
			t.Errorf("%s kept its envelope? %+v", id, p.Envelope)
		}
	}
}

// The outbox keeps what a message says, not which draft it was: the
// draft's ID is no part of the stored envelope.
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
