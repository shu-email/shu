package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/emersion/go-imap/v2"

	"shu/ai"
	"shu/internal/sqlitetest"
	"shu/locales"
	"shu/mail"
	"shu/store"
)

// egressRecorder keeps whole every request that leaves the AI layer: its
// address, its headers and its body as sent, and the strings of the body
// as they read once the JSON is decoded.
type egressRecorder struct {
	next     http.RoundTripper
	requests []string
}

func (r *egressRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", req.Method, req.URL)
	for name, values := range req.Header {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(values, ", "))
	}
	b.Write(body)
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			b.WriteString("\n" + x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				b.WriteString("\n" + k)
				walk(e)
			}
		}
	}
	walk(decoded)
	r.requests = append(r.requests, b.String())
	req.Body = io.NopCloser(bytes.NewReader(body))
	return r.next.RoundTrip(req)
}

// No contact data leaves the device: a triage of the inbox, with the
// window on the real engine and a database on disk, sends the model what
// it always sent of the messages listed, their senders as the messages
// name them, their subjects and previews, and nothing of the address
// book: not the name the user gave a sender, not an address added by
// hand, not the people the user wrote to, a blind copy among them. The
// AI layer cannot reach the book in any other way: its store is its
// settings and its cache, and it imports neither the engine nor the
// database.
func TestTriageCarriesNoContactData(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Trash")
	smtpServer := startLocalSMTP(t)
	date := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	for _, m := range []struct{ from, subject, id string }{
		{"Maya Chen <maya@example.com>", "Design review moved", "review"},
		{"Lena Park <lena@example.com>", "Contract signed", "contract"},
	} {
		raw := "From: " + m.from + "\r\nTo: Ann <ann@example.com>\r\nSubject: " + m.subject + "\r\nDate: " + date.Format(time.RFC1123Z) +
			"\r\nMessage-ID: <" + m.id + "@example.com>\r\n\r\nPlease have a look.\r\n"
		if _, err := imapServer.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: date}); err != nil {
			t.Fatal(err)
		}
	}
	imapHost, imapPort := hostPort(t, imapServer.addr)
	smtpHost, smtpPort := hostPort(t, smtpServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: imapHost, Port: imapPort, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: smtpHost, Port: smtpPort, TLS: mail.TLSStartTLS},
	}
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" || len(a.all) != 2 {
		t.Fatalf("start: %q, %d rows", a.err, len(a.all))
	}

	// The address book: whom the user wrote to, and the user's edits.
	if _, err := svc.Send(ctx, "a1", mail.Outgoing{
		To:      []mail.Address{{Name: "Victor Quill", Email: "victor.quill@example.net"}},
		Bcc:     []mail.Address{{Name: "Bea Hidden", Email: "bea.hidden@example.net"}},
		Subject: "Hello", Text: "Hi.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RenameContact(ctx, "maya@example.com", "Maya Zephyrine"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddContact(ctx, "Hand Added", "hand.added@example.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FavoriteContact(ctx, "lena@example.com", true); err != nil {
		t.Fatal(err)
	}
	book, err := svc.Contacts(ctx, "", 0, 0)
	if err != nil || len(book.Contacts) != 5 {
		t.Fatalf("address book = %+v, %v", book, err)
	}

	provider, secrets := &aiProvider{}, aiSecrets{}
	recorder := &egressRecorder{next: provider}
	aiSvc := ai.NewService(&aiStore{settings: map[string]string{}, cache: map[string]ai.CacheEntry{}}, secrets)
	aiSvc.SetHTTPClient(&http.Client{Transport: recorder})
	cfg := ai.DefaultConfig()
	cfg.Enabled = true
	if err := aiSvc.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := aiSvc.SetKey(ai.ProviderOpenAI, testAIKey); err != nil {
		t.Fatal(err)
	}
	a.ai = aiSvc
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.TriageInbox())
	shown := a.review.payload.Text
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(recorder.requests) != 1 || !tt.HasText(locales.AiSuggestions()) {
		t.Fatalf("triage made %d requests: %q", len(recorder.requests), tt.Texts())
	}
	sent := recorder.requests[0]
	// The request carries the messages listed, as they name their
	// senders, so the test would see a name of the address book there.
	for _, want := range []string{"Maya Chen <maya@example.com>", "Design review moved", "Lena Park <lena@example.com>"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the triage did not carry %q:\n%s", want, sent)
		}
	}
	// Of the address book, only what the listed messages say themselves.
	listed := map[string]bool{}
	for _, m := range a.all {
		listed[m.From.Name], listed[m.From.Email] = true, true
	}
	for _, c := range book.Contacts {
		for _, part := range []string{c.Name, c.Email} {
			if part != "" && !listed[part] && (strings.Contains(sent, part) || strings.Contains(shown, part)) {
				t.Errorf("the triage carried %q of the address book:\n%s", part, sent)
			}
		}
	}
	for _, secret := range []string{"Zephyrine", "victor", "Victor", "Quill", "bea.hidden", "Bea Hidden", "hand.added", "Hand Added"} {
		if strings.Contains(sent, secret) {
			t.Errorf("the triage carried %q:\n%s", secret, sent)
		}
	}
}

// The address book end to end: the window on the real engine, a database
// on disk and local IMAP and SMTP servers. Once the inbox is synced and a
// message sent, two letters typed in a new message offer the person
// written to above a sender who wrote once, and later; Enter takes her,
// and the message goes to her address. Renamed in the address book, she
// is offered under the new name; deleted there, she is not offered.
func TestAddressBookEndToEnd(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	imapServer := startLocalIMAP(t, "INBOX", "Sent", "Trash")
	smtpServer := startLocalSMTP(t)
	date := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
	for _, m := range []struct {
		from, subject, id string
		at                time.Time
	}{
		{"Mark Stone <mark.stone@example.net>", "Quarterly newsletter", "news", date},
		{"Lena Park <lena@example.com>", "Contract signed", "contract", date.Add(-time.Hour)},
	} {
		raw := "From: " + m.from + "\r\nTo: Ann <ann@example.com>\r\nSubject: " + m.subject + "\r\nDate: " + m.at.Format(time.RFC1123Z) +
			"\r\nMessage-ID: <" + m.id + "@example.com>\r\n\r\nHello.\r\n"
		if _, err := imapServer.user.Append("INBOX", appendBody{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: m.at}); err != nil {
			t.Fatal(err)
		}
	}
	imapHost, imapPort := hostPort(t, imapServer.addr)
	smtpHost, smtpPort := hostPort(t, smtpServer.addr)
	account := mail.Account{
		ID: "a1", Name: "Ann", Email: "ann@example.com", Username: "ann",
		IMAP: mail.Server{Host: imapHost, Port: imapPort, TLS: mail.TLSStartTLS},
		SMTP: mail.Server{Host: smtpHost, Port: smtpPort, TLS: mail.TLSStartTLS},
	}
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "shu.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := mail.NewService(db, memKeychain{}, t.TempDir())
	svc.SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- local test servers
	if err := svc.AddAccount(ctx, account, "secret"); err != nil {
		t.Fatal(err)
	}
	sendAtOnce(t, svc)
	a := newTestApp(svc)
	a.setLocale = func(string) {}
	a.start()
	if a.err != "" || len(a.all) != 2 {
		t.Fatalf("start: %q, %d rows", a.err, len(a.all))
	}

	// One message sent, to Maya, written as an address.
	cs := a.openCompose(nil)
	ct := ui.NewTester(cs.view, 720, 620)
	typeIn(t, ct, locales.To(), "Maya Chen <maya@example.com>")
	typeIn(t, ct, locales.Subject(), "Design review")
	typeIn(t, ct, locales.Body(), "See you Thursday.")
	click(t, ct, locales.Send())
	if !cs.closed || len(smtpServer.received()) != 1 {
		t.Fatalf("the first send: closed %v, err %q, %d delivered", cs.closed, cs.err, len(smtpServer.received()))
	}

	// Two letters: Maya, written to, above Mark, who wrote once, later.
	next := a.openCompose(nil)
	nt := ui.NewTester(next.view, 720, 620)
	typeIn(t, nt, locales.To(), "ma")
	names, on := offers(nt)
	if want := []string{"Maya Chen maya@example.com", "Mark Stone mark.stone@example.net"}; !slices.Equal(names, want) || on != want[0] {
		t.Fatalf("“ma” offers %q, the arrows on %q", names, on)
	}
	nt.Key(0, ui.KeyEnter)
	if next.to != "Maya Chen <maya@example.com>, " {
		t.Fatalf("Enter wrote %q", next.to)
	}
	typeIn(t, nt, locales.Subject(), "Agenda")
	typeIn(t, nt, locales.Body(), "Attached below.")
	click(t, nt, locales.Send())
	envelopes := smtpServer.envelopesGot()
	if !next.closed || len(envelopes) != 2 || !slices.Equal(envelopes[1].to, []string{"maya@example.com"}) {
		t.Fatalf("the second send: closed %v, err %q, envelopes %+v", next.closed, next.err, envelopes)
	}
	if to := addressesOf(t, readMessage(t, smtpServer.received()[1]), "To"); len(to) != 1 || to[0].Name != "Maya Chen" || to[0].Address != "maya@example.com" {
		t.Errorf("delivered to %+v", to)
	}

	// Renamed in the address book. Nothing of the book reaches a server:
	// the suggestions and the edits below open no connection.
	connections := smtpServer.connections()
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Contacts())
	click(t, main, locales.RenameContact("Maya Chen"))
	main.Key(ui.Cmd, ui.KeyA)
	main.Type("Maya Q. Chen")
	main.Key(0, ui.KeyEnter)
	if got := suggestedFirst(t, a, "ma"); len(got) != 2 || got[0] != "Maya Q. Chen maya@example.com" {
		t.Fatalf("renamed, “ma” offers %q", got)
	}

	// Deleted there, after the question.
	click(t, main, locales.DeleteContact("Maya Q. Chen"))
	click(t, main, locales.DeleteContactButton())
	if got := suggestedFirst(t, a, "ma"); !slices.Equal(got, []string{"Mark Stone mark.stone@example.net"}) {
		t.Fatalf("deleted, “ma” offers %q", got)
	}
	if page, err := svc.Contacts(ctx, "maya", 0, 0); err != nil || len(page.Contacts) != 0 {
		t.Errorf("the book still finds Maya: %+v, %v", page, err)
	}
	if n := smtpServer.connections(); n != connections {
		t.Errorf("the address book opened %d connections to the mail server", n-connections)
	}
}
