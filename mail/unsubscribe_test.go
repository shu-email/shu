package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// keptUnsubscribe is what the store saved of leaving one sender's list,
// read through the list the engine reads.
func keptUnsubscribe(t *testing.T, store Store, accountID, email string) (UnsubscribeRecord, bool) {
	t.Helper()
	list, err := store.Unsubscribes(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Email == email {
			return r, true
		}
	}
	return UnsubscribeRecord{}, false
}

// What List-Unsubscribe says of a list: its links, its addresses, and
// whether it takes a one-click request.
func TestParseUnsubscribeOffer(t *testing.T) {
	for _, c := range []struct {
		name     string
		list     string
		post     string
		links    []string
		emails   []string
		oneClick bool
		method   UnsubscribeMethod
		dest     string
	}{
		{
			name:     "one-click wins over the address",
			list:     "<https://shop.example/unsub?id=1>, <mailto:leave@shop.example>",
			post:     "List-Unsubscribe=One-Click",
			links:    []string{"https://shop.example/unsub?id=1"},
			emails:   []string{"leave@shop.example"},
			oneClick: true,
			method:   UnsubscribeOneClick,
			dest:     "https://shop.example/unsub?id=1",
		},
		{
			name:   "an address without a one-click request",
			list:   "<https://shop.example/unsub?id=1>, <mailto:leave@shop.example>",
			links:  []string{"https://shop.example/unsub?id=1"},
			emails: []string{"leave@shop.example"},
			method: UnsubscribeEmail,
			dest:   "leave@shop.example",
		},
		{
			name:   "a page alone",
			list:   "<https://shop.example/unsubscribe>",
			links:  []string{"https://shop.example/unsubscribe"},
			method: UnsubscribePage,
			dest:   "https://shop.example/unsubscribe",
		},
		{
			// RFC 8058 asks a one-click endpoint for HTTPS, so that the
			// list's token does not travel in cleartext.
			name:     "an HTTP link is a page, not a one-click request",
			list:     "<http://shop.example/unsub>",
			post:     "List-Unsubscribe=One-Click",
			links:    []string{"http://shop.example/unsub"},
			oneClick: true,
			method:   UnsubscribePage,
			dest:     "http://shop.example/unsub",
		},
		{
			name:     "the one-click link is the HTTPS one",
			list:     "<http://shop.example/unsub>, <https://shop.example/unsub>",
			post:     "List-Unsubscribe=One-Click",
			links:    []string{"http://shop.example/unsub", "https://shop.example/unsub"},
			oneClick: true,
			method:   UnsubscribeOneClick,
			dest:     "https://shop.example/unsub",
		},
		{
			name:   "the address's parameters are not its name",
			list:   "<mailto:leave@shop.example?subject=unsubscribe>",
			emails: []string{"leave@shop.example"},
			method: UnsubscribeEmail,
			dest:   "leave@shop.example",
		},
		{
			name:   "several values, one list",
			list:   "<mailto:leave@shop.example>\n<https://shop.example/unsub>",
			emails: []string{"leave@shop.example"},
			links:  []string{"https://shop.example/unsub"},
			method: UnsubscribeEmail,
			dest:   "leave@shop.example",
		},
		{
			name:   "a link the app will not ask is no link",
			list:   "<http://>, <mailto:leave@shop.example>",
			emails: []string{"leave@shop.example"},
			method: UnsubscribeEmail,
			dest:   "leave@shop.example",
		},
		{
			name: "nothing",
			list: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			offer := ParseUnsubscribeOffer(c.list, c.post)
			if !slices.Equal(offer.Links, c.links) || !slices.Equal(offer.Emails, c.emails) || offer.OneClick != c.oneClick {
				t.Fatalf("offer = %+v", offer)
			}
			if got := offer.Empty(); got != (c.method == "") {
				t.Errorf("Empty = %v", got)
			}
			method, dest, ok := offer.Preferred()
			if c.method == "" {
				if ok {
					t.Fatalf("preferred = %q %q", method, dest)
				}
				return
			}
			if !ok || method != c.method || dest != c.dest {
				t.Errorf("preferred = %q %q %v, want %q %q", method, dest, ok, c.method, c.dest)
			}
			if got, ok := offer.Destination(c.method); !ok || got != c.dest {
				t.Errorf("Destination(%q) = %q %v", c.method, got, ok)
			}
		})
	}
}

// offerMessage caches a message of a fake store whose headers offer a
// list.
func offerMessage(t *testing.T, store *memStore, list, post string) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveFolders(ctx, "a1", []Folder{{Name: "INBOX"}}); err != nil {
		t.Fatal(err)
	}
	m := Message{Summary: Summary{
		UID: 1, MessageID: "n1@shop.example", Subject: "Weekly",
		From: Address{Name: "Shop News", Email: "news@shop.example"},
	}}
	raw := ""
	if list != "" {
		raw += "List-Unsubscribe: " + list + "\r\n"
	}
	if post != "" {
		raw += "List-Unsubscribe-Post: " + post + "\r\n"
	}
	m.SetRawHeaders(raw)
	if err := store.SaveMessage(ctx, "a1", "INBOX", m); err != nil {
		t.Fatal(err)
	}
}

// A one-click request is one POST of one form field, and the choice is
// kept for the sender.
func TestUnsubscribeOneClick(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	var mu sync.Mutex
	var got []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.String()+" "+r.Header.Get("Content-Type")+"="+r.FormValue("List-Unsubscribe"))
		mu.Unlock()
	}))
	defer server.Close()
	svc.SetTLSConfig(insecureTLS())
	offerMessage(t, store, "<"+server.URL+"/unsub?id=1>", "List-Unsubscribe=One-Click")

	offer, err := svc.UnsubscribeOffer(ctx, "a1", "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if method, dest, ok := offer.Preferred(); !ok || method != UnsubscribeOneClick || dest != server.URL+"/unsub?id=1" {
		t.Fatalf("preferred = %q %q %v", method, dest, ok)
	}
	rec, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribeOneClick, server.URL+"/unsub?id=1")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	requests := slices.Clone(got)
	mu.Unlock()
	if len(requests) != 1 || requests[0] != "POST /unsub?id=1 application/x-www-form-urlencoded=One-Click" {
		t.Errorf("the list heard %v", requests)
	}
	if rec.Email != "news@shop.example" || rec.Method != UnsubscribeOneClick || rec.Destination != server.URL+"/unsub?id=1" || rec.At.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	saved, ok := keptUnsubscribe(t, store, "a1", "news@shop.example")
	if !ok || saved != rec {
		t.Errorf("the store kept %+v, %v", saved, ok)
	}
}

// A list that says no is no failure of the window's: the error says so,
// and nothing is kept.
func TestUnsubscribeOneClickFailure(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer server.Close()
	svc.SetTLSConfig(insecureTLS())
	offerMessage(t, store, "<"+server.URL+"/unsub>", "List-Unsubscribe=One-Click")
	if _, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribeOneClick, server.URL+"/unsub"); err == nil {
		t.Fatal("a refused request was taken for made")
	}
	if _, ok := keptUnsubscribe(t, store, "a1", "news@shop.example"); ok {
		t.Error("a refused request was kept")
	}
}

// A list that only names a page sends nothing: the window opens it, and
// only the choice is kept.
func TestUnsubscribePageSendsNothing(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	offerMessage(t, store, "<"+server.URL+"/unsubscribe>", "")
	offer, err := svc.UnsubscribeOffer(ctx, "a1", "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if method, _, ok := offer.Preferred(); !ok || method != UnsubscribePage {
		t.Fatalf("preferred = %q %v", method, ok)
	}
	rec, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribePage, server.URL+"/unsubscribe")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("the list was asked %d times", calls)
	}
	if rec.Method != UnsubscribePage || rec.Destination != server.URL+"/unsubscribe" {
		t.Errorf("record = %+v", rec)
	}
}

// A one-click endpoint that redirects is refused: the request goes to the
// host the confirmation named, and to no other.
func TestUnsubscribeOneClickRefusesARedirect(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	var target int
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { target++ }))
	defer other.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/unsub", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	svc.SetTLSConfig(insecureTLS())
	offerMessage(t, store, "<"+server.URL+"/unsub>", "List-Unsubscribe=One-Click")
	if _, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribeOneClick, server.URL+"/unsub"); err == nil {
		t.Fatal("a redirect was followed")
	}
	if target != 0 {
		t.Errorf("the redirect's host was asked %d times", target)
	}
	if _, ok := keptUnsubscribe(t, store, "a1", "news@shop.example"); ok {
		t.Error("a refused request was kept")
	}
}

// The request goes to the destination the user confirmed, never to the
// one a message the folder gave another UID to meanwhile offers.
func TestUnsubscribeSendsOnlyToTheConfirmedDestination(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	svc.SetTLSConfig(insecureTLS())
	offerMessage(t, store, "<"+server.URL+"/unsub>", "List-Unsubscribe=One-Click")
	if _, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribeOneClick, "https://other.example/unsub"); !errors.Is(err, ErrNoUnsubscribe) {
		t.Errorf("a destination the message does not offer = %v", err)
	}
	if calls != 0 {
		t.Errorf("the list was asked %d times", calls)
	}
}

// A message that offers nothing offers nothing, whichever read is made.
func TestUnsubscribeWithoutHeaders(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	offerMessage(t, store, "", "")
	if _, err := svc.UnsubscribeOffer(ctx, "a1", "INBOX", 1); !errors.Is(err, ErrNoUnsubscribe) {
		t.Errorf("offer = %v", err)
	}
	if _, err := svc.Unsubscribe(ctx, "a1", "INBOX", 1, UnsubscribeEmail, "leave@shop.example"); !errors.Is(err, ErrNoUnsubscribe) {
		t.Errorf("unsubscribe = %v", err)
	}
	if _, err := svc.UnsubscribeOffer(ctx, "a1", "INBOX", 9); !errors.Is(err, ErrNotFound) {
		t.Errorf("a message kept not here = %v", err)
	}
}

// An unsubscribe email leaves from the account as any message does, to
// the address the list named, and nothing else is sent.
func TestUnsubscribeByEmail(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	env.imap.append(t, "INBOX", ruleMail("Weekly", "Shop News <news@shop.example>", "n1@shop.example",
		"List-Unsubscribe: <mailto:leave@shop.example?subject=unsubscribe%20weekly>\r\n", 1))
	if err := env.svc.SyncFolder(ctx, env.a.ID, "INBOX", 50); err != nil {
		t.Fatal(err)
	}
	server := env.imap.messages(t, "INBOX")
	if len(server) != 1 {
		t.Fatalf("INBOX holds %d messages", len(server))
	}
	uid := server[0].UID
	offer, err := env.svc.UnsubscribeOffer(ctx, env.a.ID, "INBOX", uid)
	if err != nil {
		t.Fatal(err)
	}
	if method, dest, ok := offer.Preferred(); !ok || method != UnsubscribeEmail || dest != "leave@shop.example" {
		t.Fatalf("preferred = %q %q %v", method, dest, ok)
	}
	rec, err := env.svc.Unsubscribe(ctx, env.a.ID, "INBOX", uid, UnsubscribeEmail, "leave@shop.example")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Email != "news@shop.example" || rec.Destination != "leave@shop.example" {
		t.Errorf("record = %+v", rec)
	}
	if got := env.smtp.recipients(); !slices.Equal(got, []string{"leave@shop.example"}) {
		t.Errorf("the server was told to deliver to %v", got)
	}
	received := env.smtp.received()
	// The list asked for its own subject; the email says what it asked.
	if len(received) != 1 || !strings.Contains(received[0], "Subject: unsubscribe weekly") {
		t.Errorf("the server received %v", received)
	}
	if from := env.smtp.senders(); len(from) != 1 || from[0] != env.a.Email {
		t.Errorf("the envelope's sender = %v, want the account's address", from)
	}
}
