package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

// An icon is kept once per key, with what the lookup found, and replaced
// by a later one; the sweep reads the hashes of the icons there are.
func TestSenderIconRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, ok, err := s.SenderIcon(ctx, "domain:github.com"); err != nil || ok {
		t.Fatalf("a key never looked up: %v, %v", ok, err)
	}
	checked := time.Unix(1700000000, 0).UTC()
	found := mail.StoredIcon{Key: "domain:github.com", ContentHash: "abc123", Found: true, CheckedAt: checked}
	if err := s.SaveSenderIcon(ctx, found); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.SenderIcon(ctx, "domain:github.com")
	if err != nil || !ok {
		t.Fatalf("read back: %v, %v", ok, err)
	}
	if got != found {
		t.Errorf("read %+v, saved %+v", got, found)
	}
	refs, err := s.SenderIconRefs(ctx)
	if err != nil || len(refs) != 1 || refs[0] != "abc123" {
		t.Errorf("refs = %v, %v", refs, err)
	}

	// A later lookup that found nothing replaces the row and names no
	// file, so the icon's file is a sweep's to take.
	missing := mail.StoredIcon{Key: "domain:github.com", CheckedAt: checked.Add(time.Hour)}
	if err := s.SaveSenderIcon(ctx, missing); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.SenderIcon(ctx, "domain:github.com"); !ok || got.Found || got.ContentHash != "" {
		t.Errorf("read %+v, want a lookup that found none", got)
	}
	if refs, err := s.SenderIconRefs(ctx); err != nil || len(refs) != 0 {
		t.Errorf("refs after a lookup that found none = %v, %v", refs, err)
	}
}
