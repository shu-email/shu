package mail

import (
	"context"
	"strings"
	"testing"
)

// A message read whole keeps what its headers say of its sender and its
// list, so that the reading pane reads them from the cache.
func TestParseMessageKeepsSecurity(t *testing.T) {
	raw := strings.Join([]string{
		"From: Shop <news@shop.example>",
		"To: ann@example.com",
		"Subject: Weekly",
		"Message-ID: <weekly@shop.example>",
		"Authentication-Results: mx.example; spf=pass; dkim=pass; dmarc=pass header.from=shop.example",
		"List-Unsubscribe: <https://shop.example/unsub?id=1>, <mailto:leave@shop.example>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hello",
	}, "\r\n")
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Security == nil {
		t.Fatalf("no security kept: %q", m.RawHeaders)
	}
	if got := m.Trust(); got.Verdict != AuthPassed || len(got.Checks) < 2 {
		t.Errorf("trust = %+v, want a pass with its checks", got)
	}
	if got := m.Security.ListUnsubscribe; !strings.Contains(got, "https://shop.example/unsub?id=1") ||
		!strings.Contains(got, "mailto:leave@shop.example") {
		t.Errorf("List-Unsubscribe kept as %q", got)
	}
	if got := m.Security.ListUnsubscribePost; got != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post kept as %q", got)
	}
	if list, post := m.Unsubscribe(); list != m.Security.ListUnsubscribe || post != m.Security.ListUnsubscribePost {
		t.Errorf("unsubscribe = %q, %q", list, post)
	}
}

// A message whose server said nothing of it keeps no security, and stays
// unverified: absent is not a pass.
func TestSecurityOfAMessageWithoutResults(t *testing.T) {
	raw := "From: Ann <ann@example.com>\r\nSubject: Lunch\r\n\r\nSee you"
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Security != nil {
		t.Errorf("security of a message without results = %+v", m.Security)
	}
	if got := m.Trust().Verdict; got != AuthAbsent {
		t.Errorf("verdict = %q, want %q", got, AuthAbsent)
	}
	if list, post := m.Unsubscribe(); list != "" || post != "" {
		t.Errorf("unsubscribe = %q, %q", list, post)
	}
	if ParseSecurity("") != nil {
		t.Error("an empty header block kept a security")
	}
}

// A header that does not parse, or names none of the methods, leaves the
// message unverified rather than failing the read.
func TestSecurityOfMalformedHeaders(t *testing.T) {
	for _, raw := range []string{
		"Authentication-Results: this has no results at all\r\n",
		"Authentication-Results: ;;;;===;\r\nDKIM-Signature:\r\n",
		"Received-SPF: \r\nARC-Authentication-Results: i=1\r\n",
		"Authentication-Results: mx.example; spf=bogus; dkim=bogus\r\n",
	} {
		sec := ParseSecurity(raw)
		if sec != nil && sec.Trust.Verdict != AuthAbsent {
			t.Errorf("%q read as %q", raw, sec.Trust.Verdict)
		}
	}
}

// A sync keeps what a message's headers say of it without opening the
// message, so the reading pane has it from the cache.
func TestSyncKeepsWhatTheHeadersSay(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, nil)
	f.create(t, "INBOX")
	f.append(t, "INBOX", []byte(strings.Join([]string{
		"From: Shop <news@shop.example>",
		"To: ann@example.com",
		"Subject: Weekly",
		"Message-ID: <weekly@shop.example>",
		"Authentication-Results: mx.example; spf=pass; dkim=pass; dmarc=pass header.from=shop.example",
		"List-Unsubscribe: <https://shop.example/unsub?id=1>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hello",
	}, "\r\n")))
	svc, store, a := newIMAPService(t, f, "secret")
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	msgs := store.messages[msgKey(a.ID, "INBOX")]
	if len(msgs) != 1 {
		t.Fatalf("%d messages cached, want 1", len(msgs))
	}
	for _, m := range msgs {
		if m.BodyCached {
			t.Fatal("the message was opened: the sync is what keeps this")
		}
		if m.Security == nil {
			t.Fatalf("no security kept; the headers kept were %q", m.RawHeaders)
		}
		if got := m.Security.Trust.Verdict; got != AuthPassed {
			t.Errorf("verdict = %q, want %q", got, AuthPassed)
		}
		if got := m.Security.ListUnsubscribe; !strings.Contains(got, "https://shop.example/unsub?id=1") {
			t.Errorf("List-Unsubscribe kept as %q", got)
		}
		if got := m.Security.ListUnsubscribePost; got != "List-Unsubscribe=One-Click" {
			t.Errorf("List-Unsubscribe-Post kept as %q", got)
		}
	}
}
