package mail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// serverMessage is a message of the fake server as another mail client
// sees it.
type serverMessage struct {
	UID   UID
	Flags []imap.Flag
	Raw   string
	Message
}

func (m serverMessage) hasFlag(flag imap.Flag) bool {
	return slices.ContainsFunc(m.Flags, func(f imap.Flag) bool {
		return strings.EqualFold(string(f), string(flag))
	})
}

// session opens a connection of its own to the fake server, as another
// mail client has.
func (f *fakeIMAP) session(t *testing.T) *imapSession {
	t.Helper()
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	c, err := svc.dialIMAP(context.Background(), fakeAccount(t, f.addr), "secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// messages reads a mailbox of the fake server.
func (f *fakeIMAP) messages(t *testing.T, mailbox string) []serverMessage {
	t.Helper()
	c := f.session(t)
	defer c.close()
	selected, err := c.client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	if selected.NumMessages == 0 {
		return nil
	}
	var set imap.SeqSet
	set.AddRange(1, 0)
	section := &imap.FetchItemBodySection{Peek: true}
	fetched, err := c.client.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil {
		t.Fatalf("fetch %s: %v", mailbox, err)
	}
	out := make([]serverMessage, 0, len(fetched))
	for _, buf := range fetched {
		raw := buf.FindBodySection(section)
		parsed, err := ParseMessage(raw)
		if err != nil {
			t.Fatalf("parse %s/%d: %v", mailbox, buf.UID, err)
		}
		out = append(out, serverMessage{UID: UID(buf.UID), Flags: buf.Flags, Raw: string(raw), Message: parsed})
	}
	return out
}

// remove deletes a message of the fake server for good, as another mail
// client replacing or discarding a draft does.
func (f *fakeIMAP) remove(t *testing.T, mailbox string, uid UID) {
	t.Helper()
	c := f.session(t)
	defer c.close()
	if _, err := c.client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	if err := c.client.Store(set, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	if err := c.client.Expunge().Close(); err != nil {
		t.Fatalf("expunge: %v", err)
	}
}

// foreignDraft is a draft as another mail client would save it.
func foreignDraft(messageID, subject, body string, headers ...string) []byte {
	var b strings.Builder
	b.WriteString("From: Ann <ann@example.com>\r\n")
	b.WriteString("To: Bob <bob@example.com>\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Message-ID: <" + messageID + ">\r\n")
	b.WriteString("Date: Tue, 03 Jan 2023 10:20:00 +0000\r\n")
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body + "\r\n")
	return []byte(b.String())
}

// draftService is a service on a fake server with the given mailboxes,
// its folder list synced.
func draftService(t *testing.T, caps imap.CapSet, mailboxes ...string) (*Service, *memStore, *fakeIMAP, Account) {
	t.Helper()
	f := startFakeIMAP(t, caps)
	f.create(t, mailboxes...)
	svc, store, a := newIMAPService(t, f, "secret")
	if _, err := svc.SyncAccount(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	return svc, store, f, a
}

// closedPort is a port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// setIMAPPort points the account's IMAP at a port: a closed one is a
// network that is down, the fake's a network that is back.
func setIMAPPort(t *testing.T, svc *Service, a Account, port int) {
	t.Helper()
	a.IMAP.Port = port
	if err := svc.UpdateAccount(context.Background(), a, ""); err != nil {
		t.Fatal(err)
	}
}

func mustSaveDraft(t *testing.T, svc *Service, d Draft) Draft {
	t.Helper()
	saved, err := svc.SaveDraft(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func mustSyncDrafts(t *testing.T, svc *Service, accountID string) DraftSyncResult {
	t.Helper()
	result, err := svc.SyncDrafts(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mustDraft(t *testing.T, svc *Service, id string) Draft {
	t.Helper()
	d, ok, err := svc.Draft(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("draft %s: found %v, err %v", id, ok, err)
	}
	return d
}

// The first upload lands in the server's Drafts folder flagged \Draft,
// with the recipients that parse, and the local draft keeps the text
// exactly as typed.
func TestDraftFirstUpload(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	typed := Draft{
		AccountID:  a.ID,
		To:         "Bob <bob@example.com>, carol@",
		Cc:         "dave@example.com",
		Bcc:        "Erin <erin@example.com>",
		Subject:    "Quarterly plan",
		Body:       "First line\n\nSecond paragraph, unfinished",
		InReplyTo:  "previous@example.com",
		References: []string{"root@example.com", "previous@example.com"},
	}
	d := mustSaveDraft(t, svc, typed)
	if d.ID == "" || d.MessageID == "" || d.Revision != 1 || !d.Dirty() || d.ServerUID != 0 {
		t.Fatalf("saved draft = %+v", d)
	}
	if got := f.messages(t, "Drafts"); len(got) != 0 {
		t.Fatalf("saving uploaded: %+v", got)
	}

	result := mustSyncDrafts(t, svc, a.ID)
	if result.Folder != "Drafts" || result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	m := msgs[0]
	if !m.hasFlag(imap.FlagDraft) || !m.hasFlag(imap.FlagSeen) {
		t.Errorf("flags = %v", m.Flags)
	}
	if m.Subject != "Quarterly plan" || m.MessageID != d.MessageID || m.InReplyTo != "previous@example.com" ||
		!slices.Equal(m.References, typed.References) {
		t.Errorf("server copy headers = %+v", m.Summary)
	}
	if len(m.To) != 1 || m.To[0] != (Address{Name: "Bob", Email: "bob@example.com"}) {
		t.Errorf("server copy To = %+v, want only the address that parses", m.To)
	}
	if len(m.Cc) != 1 || m.Cc[0].Email != "dave@example.com" {
		t.Errorf("server copy Cc = %+v", m.Cc)
	}
	if !strings.Contains(m.BodyText, "First line") || !strings.Contains(m.BodyText, "Second paragraph, unfinished") {
		t.Errorf("server copy body = %q", m.BodyText)
	}
	for _, want := range []string{draftIDHeader + ": " + d.ID, "erin@example.com", "From: \"Ann\" <ann@example.com>"} {
		if !strings.Contains(m.Raw, want) {
			t.Errorf("server copy lacks %q:\n%s", want, m.Raw)
		}
	}

	got := mustDraft(t, svc, d.ID)
	if got.Dirty() || got.SyncedRevision != 1 || got.ServerFolder != "Drafts" || got.ServerUID != m.UID ||
		got.ServerUIDValidity == 0 {
		t.Errorf("sync state = %+v", got)
	}
	if got.To != typed.To || got.Cc != typed.Cc || got.Bcc != typed.Bcc || got.Body != typed.Body {
		t.Errorf("local draft changed by the upload: %+v", got)
	}

	// Nothing changed, so a second sync uploads nothing.
	if again := mustSyncDrafts(t, svc, a.ID); again.Uploaded != 0 {
		t.Errorf("second sync = %+v", again)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].UID != m.UID {
		t.Errorf("second sync changed the server: %+v", msgs)
	}

	// The folder shows the draft once, although the server copy is cached.
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, err := svc.DraftList(ctx, a.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].DraftID != d.ID || list[0].Subject != "Quarterly plan" ||
		list[0].From.Email != "ann@example.com" || !list[0].Seen {
		t.Errorf("draft list = %+v", list)
	}
}

// An edit replaces the server copy: IMAP cannot edit, so exactly one
// message must remain, the new one.
func TestDraftEditReplacesServerCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps func() imap.CapSet
	}{
		{"uidplus", allCaps},
		{"no uidplus", func() imap.CapSet {
			caps := allCaps()
			delete(caps, imap.CapUIDPlus)
			return caps
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, store, f, a := draftService(t, tc.caps(), "INBOX", "Drafts")
			d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Plan", Body: "one"})
			mustSyncDrafts(t, svc, a.ID)
			first := f.messages(t, "Drafts")
			if len(first) != 1 {
				t.Fatalf("server drafts = %d", len(first))
			}
			if got := mustDraft(t, svc, d.ID); got.ServerUID != first[0].UID {
				t.Fatalf("recorded UID %d, server has %d", got.ServerUID, first[0].UID)
			}
			// The first copy is cached, as after a visit to the folder.
			if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
				t.Fatal(err)
			}

			d.Body = "one, two"
			edited := mustSaveDraft(t, svc, d)
			if edited.Revision != 2 || !edited.Dirty() || edited.MessageID != d.MessageID {
				t.Fatalf("edited draft = %+v", edited)
			}
			if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 1 {
				t.Fatalf("result = %+v", result)
			}
			msgs := f.messages(t, "Drafts")
			if len(msgs) != 1 {
				t.Fatalf("server drafts after the edit = %d, want exactly one", len(msgs))
			}
			if msgs[0].UID == first[0].UID || !strings.Contains(msgs[0].BodyText, "one, two") ||
				msgs[0].MessageID != d.MessageID || !msgs[0].hasFlag(imap.FlagDraft) {
				t.Errorf("server copy = uid %d %+v", msgs[0].UID, msgs[0].Message)
			}
			got := mustDraft(t, svc, d.ID)
			if got.Dirty() || got.ServerUID != msgs[0].UID {
				t.Errorf("sync state = %+v, server uid %d", got, msgs[0].UID)
			}
			// The superseded copy left the cache with the server.
			if _, ok, _ := store.GetMessage(ctx, a.ID, "Drafts", first[0].UID); ok {
				t.Error("the superseded copy is still cached")
			}
			if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
				t.Fatal(err)
			}
			list, _ := svc.DraftList(ctx, a.ID, "", 50)
			if len(list) != 1 || list[0].DraftID != d.ID || list[0].Preview != "one, two" {
				t.Errorf("draft list = %+v", list)
			}
		})
	}
}

// Discarding removes the draft here and on the server.
func TestDiscardDraftRemovesServerCopy(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Never mind", Body: "x"})
	keep := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Keep me", Body: "y"})
	mustSyncDrafts(t, svc, a.ID)
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 2 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || msgs[0].Subject != "Keep me" {
		t.Fatalf("server drafts after discard = %+v", msgs)
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the discarded draft's row remains")
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != keep.ID {
		t.Errorf("draft list = %+v", list)
	}
	// Discarding twice, or a draft that never was, is not an error.
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Errorf("second discard: %v", err)
	}
	// A save that arrives late does not bring the draft back.
	if _, err := svc.SaveDraft(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if err := svc.DiscardDraft(ctx, keep.ID); err != nil {
		t.Fatal(err)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("server drafts = %+v", msgs)
	}
}

// Sending a draft delivers it under the draft's Message-ID and removes
// the draft here and on the server; a failed send keeps it.
func TestSendDraftRemovesLocalAndServerCopies(t *testing.T) {
	ctx := context.Background()
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent", "Drafts")
	smtpServer := startFakeSMTP(t)
	svc, store, secrets := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Hello", Body: "Body text"})
	mustSyncDrafts(t, svc, a.ID)
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	if msgs := imapServer.messages(t, "Drafts"); len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	out := Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Hello", Text: "Body text", DraftID: d.ID}

	// The server refuses: the draft stays, here and there.
	if err := secrets.Set(PasswordKey(a.ID), []byte("wrong")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, a.ID, out); err == nil {
		t.Fatal("send with a wrong password succeeded")
	}
	if err := secrets.Set(PasswordKey(a.ID), []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.Draft(ctx, d.ID); !ok {
		t.Fatal("a failed send removed the draft")
	}
	if msgs := imapServer.messages(t, "Drafts"); len(msgs) != 1 {
		t.Fatalf("a failed send changed the server drafts: %d", len(msgs))
	}

	result, err := svc.Send(ctx, a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Copied {
		t.Errorf("sent copy warning = %v", result.Warning)
	}
	sent := smtpServer.received()
	if len(sent) != 1 {
		t.Fatalf("smtp messages = %d", len(sent))
	}
	if !strings.Contains(sent[0], "Message-ID: <"+d.MessageID+">") {
		t.Errorf("sent under another Message-ID than the draft's %q:\n%s", d.MessageID, sent[0])
	}
	if strings.Contains(sent[0], draftIDHeader) {
		t.Errorf("the draft header left the device:\n%s", sent[0])
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the sent draft's row remains")
	}
	if msgs := imapServer.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("server drafts after sending = %+v", msgs)
	}
	if msgs := imapServer.messages(t, "Sent"); len(msgs) != 1 {
		t.Errorf("sent copies = %d", len(msgs))
	}
	// No row is left in the folder, cached copy included.
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 0 {
		t.Errorf("draft list after sending = %+v", list)
	}
	// Sending the same draft again finds no draft and sends as a plain
	// message would.
	if _, err := svc.Send(ctx, a.ID, out); err != nil {
		t.Errorf("send after the draft is gone: %v", err)
	}
}

// A message resent from the outbox retires its draft too, unless the
// draft was edited after the failed send.
func TestRetryPendingRetiresTheDraft(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%v", edited), func(t *testing.T) {
			ctx := context.Background()
			imapServer := startFakeIMAP(t, allCaps())
			imapServer.create(t, "INBOX", "Sent", "Drafts")
			smtpServer := startFakeSMTP(t)
			svc, store, secrets := newTestService()
			svc.SetTLSConfig(insecureTLS())
			a := combinedAccount(t, imapServer, smtpServer)
			if err := svc.AddAccount(ctx, a, "secret"); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
				t.Fatal(err)
			}
			d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Hello", Body: "Body text"})
			mustSyncDrafts(t, svc, a.ID)
			if err := secrets.Set(PasswordKey(a.ID), []byte("wrong")); err != nil {
				t.Fatal(err)
			}
			out := Outgoing{To: []Address{{Email: "bob@example.com"}}, Subject: "Hello", Text: "Body text", DraftID: d.ID}
			if _, err := svc.Send(ctx, a.ID, out); err == nil {
				t.Fatal("send with a wrong password succeeded")
			}
			if err := secrets.Set(PasswordKey(a.ID), []byte("secret")); err != nil {
				t.Fatal(err)
			}
			if edited {
				d.Body = "Body text, with an afterthought"
				mustSaveDraft(t, svc, d)
			}
			// Another draft, written since, has nothing to do with it.
			other := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Unrelated", Body: "x"})
			pending, _ := store.PendingSends(ctx, a.ID)
			if len(pending) != 1 {
				t.Fatalf("pending = %+v", pending)
			}
			if _, err := svc.RetryPending(ctx, pending[0].ID); err != nil {
				t.Fatal(err)
			}
			if len(smtpServer.received()) != 1 {
				t.Fatal("the retry did not send")
			}
			_, found, _ := svc.Draft(ctx, d.ID)
			if found != edited {
				t.Errorf("draft found = %v after the retry, want %v", found, edited)
			}
			if msgs := imapServer.messages(t, "Drafts"); (len(msgs) == 1) != edited {
				t.Errorf("server drafts = %d", len(msgs))
			}
			if _, ok, _ := svc.Draft(ctx, other.ID); !ok {
				t.Error("the retry removed an unrelated draft")
			}
		})
	}
}

// Saving needs no network. A draft saved while the server is unreachable
// stays to upload, and the next sync that connects uploads it.
func TestOfflineDraftSyncsWhenConnectivityReturns(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	_, online := f.hostPort(t)
	setIMAPPort(t, svc, a, closedPort(t))

	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Offline", Body: "written on a train"})
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("sync succeeded without a server")
	}
	d.Body = "written on a train, then some more"
	edited := mustSaveDraft(t, svc, d)
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("sync succeeded without a server")
	}
	got := mustDraft(t, svc, d.ID)
	if !got.Dirty() || got.Revision != edited.Revision || got.ServerUID != 0 || got.Body != d.Body {
		t.Fatalf("offline draft = %+v", got)
	}
	// The draft is listed although the server never saw it.
	if list, _ := svc.DraftList(ctx, a.ID, "", 50); len(list) != 1 || list[0].DraftID != d.ID {
		t.Fatalf("draft list offline = %+v", list)
	}

	setIMAPPort(t, svc, a, online)
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "then some more") {
		t.Fatalf("server drafts = %+v", msgs)
	}
	if got := mustDraft(t, svc, d.ID); got.Dirty() || got.ServerUID != msgs[0].UID {
		t.Errorf("sync state = %+v", got)
	}
}

// An edit made offline to a draft already uploaded replaces the copy once
// the server is back.
func TestOfflineEditOfUploadedDraft(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	_, online := f.hostPort(t)
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Notes", Body: "v1"})
	mustSyncDrafts(t, svc, a.ID)

	setIMAPPort(t, svc, a, closedPort(t))
	d.Body = "v2"
	mustSaveDraft(t, svc, d)
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("sync succeeded without a server")
	}
	if got := mustDraft(t, svc, d.ID); !got.Dirty() || got.Body != "v2" {
		t.Fatalf("offline edit = %+v", got)
	}
	setIMAPPort(t, svc, a, online)
	mustSyncDrafts(t, svc, a.ID)
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "v2") {
		t.Fatalf("server drafts = %+v", msgs)
	}
}

// A draft discarded while the server is unreachable is gone here at once,
// and its copy is removed by the next sync rather than coming back.
func TestDiscardOfflineIsRemembered(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	_, online := f.hostPort(t)
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Doomed", Body: "x"})
	local := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Never uploaded", Body: "y"})
	mustSyncDrafts(t, svc, a.ID)
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	// A third draft the server never sees.
	setIMAPPort(t, svc, a, closedPort(t))
	unsent := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Only here", Body: "z"})

	for _, id := range []string{d.ID, unsent.ID} {
		if err := svc.DiscardDraft(ctx, id); err != nil {
			t.Fatalf("discard offline: %v", err)
		}
		if _, ok, _ := svc.Draft(ctx, id); ok {
			t.Errorf("draft %s is still readable after its discard", id)
		}
	}
	// The one never uploaded needs no server to go.
	if _, ok, _ := store.Draft(ctx, unsent.ID); ok {
		t.Error("a draft the server never had waits for the server")
	}
	row, ok, _ := store.Draft(ctx, d.ID)
	if !ok || !row.PendingDelete {
		t.Fatalf("the deletion was not remembered: %+v, %v", row, ok)
	}
	// Neither the draft nor its cached server copy shows.
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != local.ID {
		t.Fatalf("draft list offline = %+v", list)
	}
	if _, err := svc.SaveDraft(ctx, d); !errors.Is(err, ErrDraftDiscarded) {
		t.Errorf("save after discard = %v", err)
	}

	setIMAPPort(t, svc, a, online)
	if msgs := f.messages(t, "Drafts"); len(msgs) != 2 {
		t.Fatalf("server drafts before the sync = %d", len(msgs))
	}
	result := mustSyncDrafts(t, svc, a.ID)
	if result.Removed != 1 {
		t.Errorf("result = %+v", result)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || msgs[0].Subject != "Never uploaded" {
		t.Fatalf("server drafts after the sync = %+v", msgs)
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the row outlived the server copy")
	}
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	if list, _ := svc.DraftList(ctx, a.ID, "", 50); len(list) != 1 {
		t.Errorf("the discarded draft came back: %+v", list)
	}
}

// Rule (a): another client replaced the uploaded copy and the draft has
// no edits here. The local draft gives way; the replacement shows as a
// draft of the server.
func TestDraftReplacedElsewhereWithoutLocalEdits(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Plan", Body: "written here"})
	mustSyncDrafts(t, svc, a.ID)
	uploaded := mustDraft(t, svc, d.ID)

	f.remove(t, "Drafts", uploaded.ServerUID)
	f.append(t, "Drafts", foreignDraft("elsewhere-1@example.com", "Plan", "edited on the phone"))

	result := mustSyncDrafts(t, svc, a.ID)
	if result.Replaced != 1 || result.Conflicts != 0 || result.Uploaded != 0 {
		t.Fatalf("result = %+v", result)
	}
	if _, ok, _ := svc.Draft(ctx, d.ID); ok {
		t.Fatal("the local draft outlived its replacement")
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "edited on the phone") {
		t.Fatalf("server drafts = %+v", msgs)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != "" || list[0].UID != msgs[0].UID || list[0].Folder != "Drafts" {
		t.Fatalf("draft list = %+v", list)
	}

	// A window that still shows the old text saves it again. It is kept
	// as a draft of its own, marked, rather than lost or overwriting.
	back := mustSaveDraft(t, svc, d)
	if !back.Conflict || back.MessageID == d.MessageID || back.Body != "written here" {
		t.Fatalf("draft saved after its replacement = %+v", back)
	}
	mustSyncDrafts(t, svc, a.ID)
	if msgs := f.messages(t, "Drafts"); len(msgs) != 2 {
		t.Errorf("server drafts = %d, want the replacement and the kept draft", len(msgs))
	}
}

// Rule (a) again, the other client having deleted the draft.
func TestDraftDeletedElsewhere(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	mustSyncDrafts(t, svc, a.ID)
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	f.remove(t, "Drafts", mustDraft(t, svc, d.ID).ServerUID)
	if result := mustSyncDrafts(t, svc, a.ID); result.Replaced != 1 {
		t.Fatalf("result = %+v", result)
	}
	if list, _ := svc.DraftList(ctx, a.ID, "", 50); len(list) != 0 {
		t.Errorf("draft list = %+v", list)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("the deleted draft was uploaded again: %+v", msgs)
	}
}

// Rule (b): another client replaced the uploaded copy while the draft had
// edits here. Both versions stay, and the local one is marked.
func TestDraftConflictKeepsBothVersions(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Plan", Body: "written here"})
	mustSyncDrafts(t, svc, a.ID)
	uploaded := mustDraft(t, svc, d.ID)

	d.Body = "written here, edited here"
	mustSaveDraft(t, svc, d)
	// The other client keeps the headers it does not know, the draft's
	// ID among them, as some do. Its message is still not ours to delete.
	f.remove(t, "Drafts", uploaded.ServerUID)
	f.append(t, "Drafts", foreignDraft(uploaded.MessageID, "Plan", "edited on the phone",
		draftIDHeader+": "+d.ID))

	result := mustSyncDrafts(t, svc, a.ID)
	if result.Conflicts != 1 || result.Replaced != 0 || result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	got := mustDraft(t, svc, d.ID)
	if !got.Conflict || got.Body != "written here, edited here" || got.Dirty() {
		t.Fatalf("local draft = %+v", got)
	}
	if got.MessageID == uploaded.MessageID {
		t.Error("the kept draft shares its Message-ID with the other client's version")
	}
	bodies := func() []string {
		var out []string
		for _, m := range f.messages(t, "Drafts") {
			out = append(out, m.BodyText)
		}
		slices.Sort(out)
		return out
	}
	want := []string{"edited on the phone", "written here, edited here"}
	if got := bodies(); !slices.Equal(got, want) {
		t.Fatalf("server drafts = %q, want %q", got, want)
	}
	// The folder shows both: the local draft and the other client's.
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 2 || list[0].DraftID != d.ID || list[1].DraftID != "" ||
		list[1].Preview != "edited on the phone" {
		t.Fatalf("draft list = %+v", list)
	}

	// Editing on does not touch the other client's version.
	d.Body = "written here, edited here twice"
	mustSaveDraft(t, svc, d)
	mustSyncDrafts(t, svc, a.ID)
	want = []string{"edited on the phone", "written here, edited here twice"}
	if got := bodies(); !slices.Equal(got, want) {
		t.Fatalf("server drafts after another edit = %q, want %q", got, want)
	}
	// Nor does discarding the local draft.
	if err := svc.DismissDraftConflict(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if mustDraft(t, svc, d.ID).Conflict {
		t.Error("the conflict marker stays after its dismissal")
	}
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if got := bodies(); !slices.Equal(got, []string{"edited on the phone"}) {
		t.Fatalf("server drafts after the discard = %q", got)
	}
}

// Rule (c): under a new UIDVALIDITY the recorded UID means nothing. The
// draft is found by its ID if the server still has it, and uploaded once
// if it does not; it is never dropped for it.
func TestDraftSurvivesUIDValidityChange(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored=%v", restored), func(t *testing.T) {
			svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
			d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "kept"})
			mustSyncDrafts(t, svc, a.ID)
			before := mustDraft(t, svc, d.ID)
			raw := f.messages(t, "Drafts")[0].Raw

			if err := f.user.Delete("Drafts"); err != nil {
				t.Fatal(err)
			}
			f.create(t, "Drafts")
			// Push the UIDs on, so that the old one names another message.
			f.append(t, "Drafts", foreignDraft("unrelated@example.com", "Unrelated", "someone else's"))
			if restored {
				f.append(t, "Drafts", []byte(raw))
			}

			result := mustSyncDrafts(t, svc, a.ID)
			if result.Replaced != 0 || result.Conflicts != 0 {
				t.Fatalf("result = %+v", result)
			}
			if restored && result.Uploaded != 0 {
				t.Errorf("a copy the server still had was uploaded again: %+v", result)
			}
			msgs := f.messages(t, "Drafts")
			if len(msgs) != 2 {
				t.Fatalf("server drafts = %d, want the unrelated one and the draft once", len(msgs))
			}
			after := mustDraft(t, svc, d.ID)
			if after.Dirty() || after.ServerUIDValidity == before.ServerUIDValidity || after.Body != "kept" {
				t.Errorf("draft = %+v", after)
			}
			var linked serverMessage
			for _, m := range msgs {
				if m.UID == after.ServerUID {
					linked = m
				}
			}
			if linked.Subject != "Plan" {
				t.Errorf("the draft is linked to %+v", linked.Summary)
			}
		})
	}
}

// Without a Drafts folder on the server the drafts stay on this device.
// That is a result, not an error to retry.
func TestDraftsWithoutServerFolder(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Local only", Body: "x"})
	for i := 0; i < 2; i++ {
		result, err := svc.SyncDrafts(ctx, a.ID)
		if err != nil || result.Folder != "" || result.Uploaded != 0 {
			t.Fatalf("sync %d = %+v, %v", i, result, err)
		}
	}
	if got := mustDraft(t, svc, d.ID); !got.Dirty() || got.ServerUID != 0 {
		t.Errorf("draft = %+v", got)
	}
	if list, _ := svc.DraftList(ctx, a.ID, "", 50); len(list) != 1 || list[0].DraftID != d.ID {
		t.Errorf("draft list = %+v", list)
	}
	// A discard has nothing to remove and nothing to wait for.
	gone := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Discarded", Body: "y"})
	if err := svc.DiscardDraft(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Draft(ctx, gone.ID); ok {
		t.Error("a discarded local draft keeps its row")
	}

	// Once the server has the folder, the draft goes up.
	f.create(t, "Drafts")
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Folder != "Drafts" || result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].Subject != "Local only" {
		t.Errorf("server drafts = %+v", msgs)
	}

	// A folder the cache names but the server lost is no folder either.
	if err := f.user.Delete("Drafts"); err != nil {
		t.Fatal(err)
	}
	d.Body = "x, edited"
	mustSaveDraft(t, svc, d)
	result, err := svc.SyncDrafts(ctx, a.ID)
	if err != nil || result.Folder != "" {
		t.Fatalf("sync after the folder went = %+v, %v", result, err)
	}
	if got := mustDraft(t, svc, d.ID); got.Body != "x, edited" {
		t.Errorf("draft = %+v", got)
	}
}

// An account whose folders were never listed finds the Drafts folder on
// its first draft sync.
func TestSyncDraftsListsFoldersItself(t *testing.T) {
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Drafts")
	svc, _, a := newIMAPService(t, f, "secret")
	mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Early", Body: "x"})
	if result := mustSyncDrafts(t, svc, a.ID); result.Folder != "Drafts" || result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 {
		t.Errorf("server drafts = %d", len(msgs))
	}
}

// A sync interrupted after the server took the copy leaves the draft to
// upload again, and the next sync leaves one copy, not two.
func TestInterruptedUploadLeavesOneCopy(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "v1"})

	store.failDraftSync = errors.New("disk full")
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("a sync that could not record its upload succeeded")
	}
	if got := mustDraft(t, svc, d.ID); !got.Dirty() || got.ServerUID != 0 {
		t.Fatalf("draft after the failure = %+v", got)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	store.failDraftSync = nil

	d.Body = "v2"
	mustSaveDraft(t, svc, d)
	mustSyncDrafts(t, svc, a.ID)
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "v2") {
		t.Fatalf("server drafts after the retry = %+v", msgs)
	}
	if got := mustDraft(t, svc, d.ID); got.Dirty() || got.ServerUID != msgs[0].UID {
		t.Errorf("draft = %+v", got)
	}
}

// A cancelled sync changes nothing and leaves the draft to upload.
func TestSyncDraftsCancelled(t *testing.T) {
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("a cancelled sync succeeded")
	}
	if got := mustDraft(t, svc, d.ID); !got.Dirty() {
		t.Errorf("draft = %+v", got)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("server drafts = %d", len(msgs))
	}
	// Saving an unknown account's draft is refused, not stored.
	if _, err := svc.SaveDraft(context.Background(), Draft{AccountID: "nobody", Body: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("save for an unknown account = %v", err)
	}
}

// A save with the same content is not an edit: it uploads nothing.
func TestSaveDraftWithoutChange(t *testing.T) {
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	clock := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return clock })
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	mustSyncDrafts(t, svc, a.ID)
	uid := f.messages(t, "Drafts")[0].UID
	clock = clock.Add(time.Hour)
	same := mustSaveDraft(t, svc, d)
	if same.Revision != 1 || same.Dirty() || !same.UpdatedAt.Equal(d.UpdatedAt) {
		t.Fatalf("unchanged save = %+v", same)
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 0 {
		t.Errorf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].UID != uid {
		t.Errorf("server drafts = %+v", msgs)
	}
	// The newest draft leads the list.
	newer := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Newer", Body: "y"})
	list, _ := svc.DraftList(context.Background(), a.ID, "", 50)
	if len(list) != 2 || list[0].DraftID != newer.ID || !list[0].Date.Equal(clock) {
		t.Errorf("draft list = %+v", list)
	}
	if found, _ := svc.DraftList(context.Background(), a.ID, "newer", 50); len(found) != 1 || found[0].DraftID != newer.ID {
		t.Errorf("search = %+v", found)
	}
}

// A draft written in another mail client is taken over on demand: linked
// to its server message, so an edit here replaces that message.
func TestAdoptDraft(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	f.append(t, "Drafts", foreignDraft("phone-1@example.com", "From the phone", "typed on the phone",
		"Cc: carol@example.com", "Bcc: Erin <erin@example.com>", "In-Reply-To: <previous@example.com>",
		"References: <root@example.com> <previous@example.com>"))
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != "" {
		t.Fatalf("draft list = %+v", list)
	}
	d, err := svc.AdoptDraft(ctx, a.ID, "Drafts", list[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if d.To != "Bob <bob@example.com>" || d.Cc != "carol@example.com" || d.Bcc != "Erin <erin@example.com>" ||
		d.Subject != "From the phone" || d.Body != "typed on the phone" || d.MessageID != "phone-1@example.com" ||
		d.InReplyTo != "previous@example.com" || len(d.References) != 2 {
		t.Fatalf("adopted draft = %+v", d)
	}
	if d.Dirty() || d.ServerUID != list[0].UID || d.ServerFolder != "Drafts" || d.ServerUIDValidity == 0 {
		t.Fatalf("adopted draft sync state = %+v", d)
	}
	// Adopting again opens the same draft.
	again, err := svc.AdoptDraft(ctx, a.ID, "Drafts", list[0].UID)
	if err != nil || again.ID != d.ID {
		t.Fatalf("second adoption = %+v, %v", again, err)
	}
	// The folder shows it once, as a local draft now.
	list, _ = svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 || list[0].DraftID != d.ID {
		t.Fatalf("draft list after adoption = %+v", list)
	}
	// Nothing was edited, so the server keeps the other client's message.
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 0 {
		t.Errorf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 || msgs[0].UID != d.ServerUID {
		t.Fatalf("server drafts = %+v", msgs)
	}

	d.Body = "typed on the phone, finished on the desktop"
	mustSaveDraft(t, svc, d)
	mustSyncDrafts(t, svc, a.ID)
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 {
		t.Fatalf("server drafts after the edit = %d, want the replacement alone", len(msgs))
	}
	if msgs[0].MessageID != "phone-1@example.com" || !strings.Contains(msgs[0].BodyText, "finished on the desktop") ||
		!strings.Contains(msgs[0].Raw, "erin@example.com") || !strings.Contains(msgs[0].Raw, draftIDHeader+": "+d.ID) {
		t.Errorf("replacement = %s", msgs[0].Raw)
	}
}

// An adopted draft whose folder got a new UIDVALIDITY is found again by
// its Message-ID, as its message carries no ID of this app, and is
// uploaded once if the server lost it.
func TestAdoptedDraftSurvivesUIDValidityChange(t *testing.T) {
	ctx := context.Background()
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("restored=%v", restored), func(t *testing.T) {
			svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
			raw := foreignDraft("phone-2@example.com", "From the phone", "typed on the phone")
			f.append(t, "Drafts", raw)
			if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
				t.Fatal(err)
			}
			d, err := svc.AdoptDraft(ctx, a.ID, "Drafts", 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.user.Delete("Drafts"); err != nil {
				t.Fatal(err)
			}
			f.create(t, "Drafts")
			f.append(t, "Drafts", foreignDraft("unrelated@example.com", "Unrelated", "someone else's"))
			if restored {
				f.append(t, "Drafts", raw)
			}
			result := mustSyncDrafts(t, svc, a.ID)
			if result.Replaced != 0 || result.Conflicts != 0 || (restored && result.Uploaded != 0) {
				t.Fatalf("result = %+v", result)
			}
			msgs := f.messages(t, "Drafts")
			if len(msgs) != 2 {
				t.Fatalf("server drafts = %d", len(msgs))
			}
			after := mustDraft(t, svc, d.ID)
			if after.Dirty() || after.ServerUID != 2 || after.ServerUIDValidity == d.ServerUIDValidity ||
				after.Body != "typed on the phone" {
				t.Errorf("draft = %+v", after)
			}
		})
	}
}

// What is typed outside ASCII reaches the server copy as typed.
func TestDraftUploadKeepsUnicode(t *testing.T) {
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	body := "你好，\n\n季度计划见下，行尾有空格 \n— 安"
	d := mustSaveDraft(t, svc, Draft{
		AccountID: a.ID, To: "张伟 <zhang@example.com>", Bcc: "李娜 <li@example.com>, audit@example.com",
		Subject: "季度计划：第一版", Body: body,
	})
	mustSyncDrafts(t, svc, a.ID)
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 {
		t.Fatalf("server drafts = %d", len(msgs))
	}
	m := msgs[0]
	if m.Subject != "季度计划：第一版" || len(m.To) != 1 || m.To[0] != (Address{Name: "张伟", Email: "zhang@example.com"}) {
		t.Errorf("server copy = %+v", m.Summary)
	}
	for _, want := range []string{"你好，", "季度计划见下", "— 安"} {
		if !strings.Contains(m.BodyText, want) {
			t.Errorf("server copy body lacks %q: %q", want, m.BodyText)
		}
	}
	bcc, id := draftHeaders([]byte(m.Raw))
	if len(bcc) != 2 || bcc[0] != (Address{Name: "李娜", Email: "li@example.com"}) ||
		bcc[1].Email != "audit@example.com" || id != d.ID {
		t.Errorf("server copy Bcc = %+v, draft ID %q", bcc, id)
	}
	// The draft itself is untouched by its way through MIME.
	if got := mustDraft(t, svc, d.ID); got.Body != body || got.To != "张伟 <zhang@example.com>" {
		t.Errorf("local draft = %+v", got)
	}
}

// A draft with attachments is not taken over: an edit would replace it
// with a message without them.
func TestAdoptDraftRefusesAttachments(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	f.append(t, "Drafts", readFixture(t, "attachment.eml"))
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 1 {
		t.Fatalf("draft list = %+v", list)
	}
	if _, err := svc.AdoptDraft(ctx, a.ID, "Drafts", list[0].UID); !errors.Is(err, ErrDraftHasAttachments) {
		t.Fatalf("adoption = %v", err)
	}
	if drafts, _ := store.Drafts(ctx, a.ID); len(drafts) != 0 {
		t.Errorf("a refused adoption left %+v", drafts)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 1 {
		t.Errorf("server drafts = %d", len(msgs))
	}
	if _, err := svc.AdoptDraft(ctx, a.ID, "Drafts", 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("adoption of a missing message = %v", err)
	}
}

// An upload this device lost track of shows as a draft of the server.
// Opening it opens the draft it is a copy of, not a second one.
func TestAdoptOwnStaleUpload(t *testing.T) {
	ctx := context.Background()
	svc, store, _, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	store.failDraftSync = errors.New("disk full")
	if _, err := svc.SyncDrafts(ctx, a.ID); err == nil {
		t.Fatal("a sync that could not record its upload succeeded")
	}
	store.failDraftSync = nil
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	list, _ := svc.DraftList(ctx, a.ID, "", 50)
	if len(list) != 2 || list[0].DraftID != d.ID || list[1].DraftID != "" {
		t.Fatalf("draft list = %+v", list)
	}
	got, err := svc.AdoptDraft(ctx, a.ID, "Drafts", list[1].UID)
	if err != nil || got.ID != d.ID {
		t.Fatalf("adoption = %+v, %v", got, err)
	}
	if drafts, _ := store.Drafts(ctx, a.ID); len(drafts) != 1 {
		t.Errorf("drafts = %+v", drafts)
	}
}

// An edit saved after the sync read its drafts, but before the draft's
// turn to upload, goes up with that sync: the newest text is uploaded.
func TestEditBeforeUpload(t *testing.T) {
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Typing", Body: "first"})
	store.afterDrafts = func() {
		edit := d
		edit.Body = "first, second"
		mustSaveDraft(t, svc, edit)
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "first, second") {
		t.Fatalf("server drafts = %+v", msgs)
	}
	if got := mustDraft(t, svc, d.ID); got.Dirty() || got.Body != "first, second" {
		t.Errorf("draft = %+v", got)
	}
}

// An edit saved while the draft is on its way up is not lost and not
// taken for uploaded: the draft stays to upload, and the next sync
// publishes it.
func TestEditDuringUpload(t *testing.T) {
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Typing", Body: "first"})
	store.afterDraft = func() {
		edit := d
		edit.Body = "first, second"
		mustSaveDraft(t, svc, edit)
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}
	// The server has the text the upload read; the edit waits.
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "first") || strings.Contains(msgs[0].BodyText, "second") {
		t.Fatalf("server drafts after the overlapping save = %+v", msgs)
	}
	got := mustDraft(t, svc, d.ID)
	if !got.Dirty() || got.Body != "first, second" || got.SyncedRevision != 1 || got.ServerUID != msgs[0].UID {
		t.Fatalf("draft after the overlapping save = %+v", got)
	}
	mustSyncDrafts(t, svc, a.ID)
	msgs = f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "first, second") {
		t.Fatalf("server drafts = %+v", msgs)
	}
	if got := mustDraft(t, svc, d.ID); got.Dirty() {
		t.Errorf("draft = %+v", got)
	}
}

// A draft discarded while the sync that would upload it is running is
// not uploaded, and is gone once the discard has its turn.
func TestDiscardDuringSync(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	store.afterDrafts = func() {
		// A discard marks the row at once, then waits its turn on the
		// server behind the running sync. This is the mark.
		row, _, _ := store.Draft(ctx, d.ID)
		row.PendingDelete = true
		if err := store.SetDraftSync(ctx, row); err != nil {
			t.Error(err)
		}
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 0 {
		t.Fatalf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 0 {
		t.Fatalf("a discarded draft was uploaded: %+v", msgs)
	}
	// And this is its turn.
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the discarded draft's row remains")
	}
}

// An edit saved just as a sync finds the copy replaced elsewhere makes it
// a conflict: the draft is not dropped with the edit in it.
func TestEditWhileReplacedElsewhere(t *testing.T) {
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "here"})
	mustSyncDrafts(t, svc, a.ID)
	f.remove(t, "Drafts", mustDraft(t, svc, d.ID).ServerUID)
	f.append(t, "Drafts", foreignDraft("elsewhere-2@example.com", "Plan", "there"))
	store.afterDrafts = func() {
		edit := d
		edit.Body = "here, just now"
		mustSaveDraft(t, svc, edit)
	}
	result := mustSyncDrafts(t, svc, a.ID)
	if result.Conflicts != 1 || result.Replaced != 0 {
		t.Fatalf("result = %+v", result)
	}
	got := mustDraft(t, svc, d.ID)
	if !got.Conflict || got.Body != "here, just now" || got.Dirty() {
		t.Fatalf("draft = %+v", got)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 2 {
		t.Errorf("server drafts = %d, want both versions", len(msgs))
	}
}

// A draft whose row goes while it uploads, as when its account is
// removed, leaves no copy behind.
func TestDraftRemovedDuringUpload(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	store.afterDrafts = func() {
		if err := store.DeleteDraft(ctx, d.ID); err != nil {
			t.Error(err)
		}
	}
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 0 {
		t.Fatalf("result = %+v", result)
	}
	if msgs := f.messages(t, "Drafts"); len(msgs) != 0 {
		t.Errorf("server drafts = %+v", msgs)
	}
}

// Discarding a draft another client replaced removes the local draft and
// leaves the other client's message alone, even one carrying the draft's
// ID.
func TestDiscardLeavesTheOtherClientsVersion(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "here"})
	mustSyncDrafts(t, svc, a.ID)
	uploaded := mustDraft(t, svc, d.ID)
	f.remove(t, "Drafts", uploaded.ServerUID)
	f.append(t, "Drafts", foreignDraft(uploaded.MessageID, "Plan", "there", draftIDHeader+": "+d.ID))
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the discarded draft's row remains")
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, "there") {
		t.Fatalf("server drafts = %+v", msgs)
	}
}

// A deletion remembered for a server that then lost its Drafts folder
// has nothing left to remove.
func TestPendingDeletionWithoutFolder(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	_, online := f.hostPort(t)
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Plan", Body: "x"})
	mustSyncDrafts(t, svc, a.ID)
	setIMAPPort(t, svc, a, closedPort(t))
	if err := svc.DiscardDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.user.Delete("Drafts"); err != nil {
		t.Fatal(err)
	}
	setIMAPPort(t, svc, a, online)
	if result, err := svc.SyncDrafts(ctx, a.ID); err != nil || result.Folder != "" {
		t.Fatalf("sync = %+v, %v", result, err)
	}
	if _, ok, _ := store.Draft(ctx, d.ID); ok {
		t.Error("the row waits for a folder that is gone")
	}
}

// Saves and syncs running together lose no edit and leave one copy.
func TestSaveDuringSync(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, Subject: "Typing", Body: "0"})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			if _, err := svc.SyncDrafts(ctx, a.ID); err != nil {
				t.Errorf("sync %d: %v", i, err)
			}
		}
	}()
	last := ""
	for i := 1; i <= 30; i++ {
		edit := d
		edit.Body = fmt.Sprintf("keystroke %d", i)
		last = edit.Body
		if _, err := svc.SaveDraft(ctx, edit); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	mustSyncDrafts(t, svc, a.ID)
	got := mustDraft(t, svc, d.ID)
	if got.Body != last || got.Dirty() || got.Conflict {
		t.Fatalf("draft = %+v", got)
	}
	msgs := f.messages(t, "Drafts")
	if len(msgs) != 1 || !strings.Contains(msgs[0].BodyText, last) || msgs[0].UID != got.ServerUID {
		t.Fatalf("server drafts = %+v", msgs)
	}
}

func TestDraftAddresses(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []Address
	}{
		{"", nil},
		{"   ", nil},
		{"bob@example.com", []Address{{Email: "bob@example.com"}}},
		{"Bob <bob@example.com>, carol@example.com", []Address{{Name: "Bob", Email: "bob@example.com"}, {Email: "carol@example.com"}}},
		{`"Doe, John" <john@example.com>`, []Address{{Name: "Doe, John", Email: "john@example.com"}}},
		// Half-typed addresses are left out; the rest stays.
		{"bob@example.com, carol@", []Address{{Email: "bob@example.com"}}},
		{"bob@example.com, ", []Address{{Email: "bob@example.com"}}},
		{"Bob <bob@exa", nil},
		{"just a name", nil},
	} {
		if got := draftAddresses(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("draftAddresses(%q) = %+v, want %+v", tc.text, got, tc.want)
		}
	}
}

func TestDraftMatches(t *testing.T) {
	d := Draft{To: "Bob <bob@example.com>", Subject: "Quarterly Plan", Body: "numbers attached"}
	for query, want := range map[string]bool{
		"": true, "  ": true, "quarterly": true, "BOB@": true, "numbers": true, "invoice": false,
	} {
		if got := d.matches(query); got != want {
			t.Errorf("matches(%q) = %v, want %v", query, got, want)
		}
	}
}
