package mail

import (
	"context"
	"errors"
	"testing"
)

func testAccount() Account {
	return Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit},
		SMTP: Server{Host: "smtp.example.com", Port: 465, TLS: TLSImplicit},
	}
}

func newTestService() (*Service, *memStore, *memSecrets) {
	store := newMemStore()
	secrets := newMemSecrets()
	return NewService(store, secrets, ""), store, secrets
}

func TestAccountLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, store, secrets := newTestService()
	a := testAccount()
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	accounts, active, err := svc.Accounts(ctx)
	if err != nil || len(accounts) != 1 || active != "a1" {
		t.Fatalf("accounts = %+v active %q err %v", accounts, active, err)
	}
	if got, _ := secrets.Get(PasswordKey("a1")); string(got) != "secret" {
		t.Errorf("password = %q", got)
	}
	a.Name = "Ann Updated"
	if err := svc.UpdateAccount(ctx, a, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(PasswordKey("a1")); string(got) != "secret" {
		t.Errorf("password changed by an empty update: %q", got)
	}
	if err := svc.UpdateAccount(ctx, a, "new-secret"); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(PasswordKey("a1")); string(got) != "new-secret" {
		t.Errorf("password after update = %q", got)
	}
	if err := svc.RemoveAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if accounts, _ := store.Accounts(ctx); len(accounts) != 0 {
		t.Errorf("account remains: %+v", accounts)
	}
	if _, err := secrets.Get(PasswordKey("a1")); err == nil {
		t.Error("keychain entry remains after delete")
	}
}

func TestAddAccountValidatesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	svc, store, secrets := newTestService()
	bad := testAccount()
	bad.Email = "not-an-address"
	if err := svc.AddAccount(ctx, bad, "secret"); err == nil {
		t.Error("invalid account accepted")
	}
	if a, _ := store.Accounts(ctx); len(a) != 0 {
		t.Errorf("invalid account stored: %+v", a)
	}
	secrets.fail = true
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err == nil {
		t.Error("keychain failure ignored")
	}
	if a, _ := store.Accounts(ctx); len(a) != 0 {
		t.Errorf("account stored without a password: %+v", a)
	}
	secrets.fail = false
	if err := svc.AddAccount(ctx, testAccount(), ""); err == nil {
		t.Error("empty password accepted")
	}
}

func TestUpdateAndRemoveMissingAccount(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService()
	if err := svc.UpdateAccount(ctx, testAccount(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	if err := svc.RemoveAccount(ctx, "missing"); err != nil {
		t.Errorf("remove missing = %v", err)
	}
}

func TestAccountValidation(t *testing.T) {
	valid := testAccount()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid account rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Account)
	}{
		{"email", func(a *Account) { a.Email = "" }},
		{"name", func(a *Account) { a.Name = " " }},
		{"username", func(a *Account) { a.Username = "" }},
		{"imap host", func(a *Account) { a.IMAP.Host = "" }},
		{"imap port", func(a *Account) { a.IMAP.Port = 0 }},
		{"imap tls", func(a *Account) { a.IMAP.TLS = "none" }},
		{"smtp host", func(a *Account) { a.SMTP.Host = "" }},
		{"smtp port", func(a *Account) { a.SMTP.Port = 70000 }},
		{"smtp tls", func(a *Account) { a.SMTP.TLS = "" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a := testAccount()
			tt.edit(&a)
			if err := a.Validate(); err == nil {
				t.Error("invalid account accepted")
			}
		})
	}
}

func TestProviderPresets(t *testing.T) {
	for _, p := range Providers {
		if p.IMAP.Host == "" || p.SMTP.Host == "" {
			t.Errorf("provider %q has no servers", p.ID)
		}
		if got, ok := ProviderByID(p.ID); !ok || got != p {
			t.Errorf("ProviderByID(%q) = %+v, %v", p.ID, got, ok)
		}
	}
	for email, want := range map[string]string{
		"a@gmail.com":    "gmail",
		"a@icloud.com":   "icloud",
		"a@outlook.com":  "outlook",
		"a@fastmail.com": "fastmail",
		"a@yahoo.com":    "yahoo",
	} {
		if got, ok := ProviderFor(email); !ok || got.ID != want {
			t.Errorf("ProviderFor(%q) = %q, %v", email, got.ID, ok)
		}
	}
	if _, ok := ProviderFor("a@example.com"); ok {
		t.Error("custom domain matched a preset")
	}
}

func TestPendingSendsReportsCrashAsUnknown(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	if err := svc.AddAccount(ctx, testAccount(), "secret"); err != nil {
		t.Fatal(err)
	}
	p := PendingSend{ID: "o1", AccountID: "a1", State: StateSending, Raw: []byte("raw")}
	if err := store.EnqueueOutbox(ctx, p); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.PendingSends(ctx, "a1")
	if err != nil || len(pending) != 1 || pending[0].State != StateUnknown {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
}
