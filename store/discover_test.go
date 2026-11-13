package store

import (
	"context"
	"testing"

	"shu/mail"
)

// A domain's servers are kept once and replaced by a later finding, a
// manual entry among them.
func TestDiscoveryRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok, err := s.Discovery(ctx, "example.com"); err != nil || ok {
		t.Fatalf("a domain never looked up: %v, %v", ok, err)
	}
	found := mail.Discovery{
		Domain: "example.com",
		IMAP:   mail.Server{Host: "imap.example.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP:   mail.Server{Host: "smtp.example.com", Port: 587, TLS: mail.TLSStartTLS},
		Source: mail.DiscoveryAutoconfig,
	}
	if err := s.SaveDiscovery(ctx, found); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Discovery(ctx, "example.com")
	if err != nil || !ok {
		t.Fatalf("read back: %v, %v", ok, err)
	}
	if got != found {
		t.Errorf("read %+v, saved %+v", got, found)
	}
	// The user's own entry replaces it, and the domain is unchanged.
	manual := found
	manual.IMAP.Host, manual.IMAP.Port = "mail.example.org", 143
	manual.SMTP.Host, manual.SMTP.Port = "send.example.org", 25
	manual.Source = mail.DiscoveryManual
	if err := s.SaveDiscovery(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.Discovery(ctx, "example.com"); err != nil || got != manual {
		t.Errorf("after the manual entry: %+v, %v", got, err)
	}
	// Another domain is another row.
	if _, ok, _ := s.Discovery(ctx, "other.example"); ok {
		t.Error("another domain has the first one's servers")
	}
}
