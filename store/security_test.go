package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"shu/mail"
)

// securityHeaders is a message's header lines that say of its sender and
// its list, as a sync keeps them.
const securityHeaders = "Authentication-Results: mx.example; spf=pass; dkim=pass; dmarc=pass header.from=example.org\r\n" +
	"List-Unsubscribe: <https://example.org/unsub?id=1>, <mailto:leave@example.org>\r\n" +
	"List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"

// What a message's headers said of it survives a read of the cache, and
// a message whose headers said nothing of it keeps none.
func TestSaveMessageKeepsSecurity(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")

	fetched := mail.Message{Summary: mail.Summary{UID: 1, MessageID: "m1@example.com",
		Subject: "Weekly", Date: pageBase}}
	fetched.SetRawHeaders(securityHeaders)
	if err := s.SaveMessage(ctx, "a1", "INBOX", fetched); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveMessage(ctx, "a1", "INBOX", mail.Message{Summary: mail.Summary{
		UID: 2, MessageID: "m2@example.com", Subject: "Lunch", Date: pageBase.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.GetMessage(ctx, "a1", "INBOX", 1)
	if err != nil || !ok {
		t.Fatalf("the message = %v, %v", ok, err)
	}
	if got.Security == nil {
		t.Fatalf("no security kept; the headers kept were %q", got.RawHeaders)
	}
	if got.Trust().Verdict != mail.AuthPassed {
		t.Errorf("the verdict read back as %q", got.Trust().Verdict)
	}
	list, post := got.Unsubscribe()
	if !strings.Contains(list, "https://example.org/unsub?id=1") || !strings.Contains(list, "mailto:leave@example.org") {
		t.Errorf("List-Unsubscribe read back as %q", list)
	}
	if post != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post read back as %q", post)
	}

	bare, ok, err := s.GetMessage(ctx, "a1", "INBOX", 2)
	if err != nil || !ok {
		t.Fatalf("the second message = %v, %v", ok, err)
	}
	if bare.Security != nil {
		t.Errorf("a message without results kept %+v", bare.Security)
	}
	if got := bare.Trust().Verdict; got != mail.AuthAbsent {
		t.Errorf("a message without results read as %q, want %q", got, mail.AuthAbsent)
	}
}

// A sync keeps the security of the headers it read, without opening the
// message, and the cache read has it.
func TestASyncKeepsSecurity(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolder(t, s, "a1", "INBOX")

	m := mail.Message{Summary: mail.Summary{UID: 5, MessageID: "m5@example.com",
		Subject: "Weekly", Date: pageBase}}
	m.SetRawHeaders(securityHeaders)
	if m.Security == nil {
		t.Fatal("the test's headers say nothing")
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 7, LastUID: 5},
		Messages: []mail.Message{m},
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.GetMessage(ctx, "a1", "INBOX", 5)
	if err != nil || !ok {
		t.Fatalf("the message = %v, %v", ok, err)
	}
	if got.BodyCached {
		t.Error("the sync opened the message")
	}
	if got.Security == nil || got.Security.Trust.Verdict != mail.AuthPassed {
		t.Errorf("the sync kept %+v", got.Security)
	}
	if list, _ := got.Unsubscribe(); list == "" {
		t.Error("the sync kept no unsubscribe")
	}
}
