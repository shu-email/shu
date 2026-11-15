package mail

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// sendOnce sends a message from a fresh account on the given servers and
// returns the result and the one copy the server's Sent folder holds.
func sendOnce(t *testing.T, imapServer *fakeIMAP, smtpServer *fakeSMTP, out Outgoing) (SendResult, Account) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := combinedAccount(t, imapServer, smtpServer)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	out.To = []Address{{Email: "bob@example.com"}}
	out.Subject = "Hello"
	out.Text = "Body text"
	result, err := svc.Send(ctx, a.ID, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Sent", 50); err != nil {
		t.Fatal(err)
	}
	sent, err := svc.CachedMessages(ctx, a.ID, "Sent", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("Sent holds %d copies, want 1", len(sent))
	}
	return result, a
}

// TestSendKeepsTheServersOwnSentCopy checks that a server which saves what
// SMTP delivers — Gmail does — is not given a second copy, and that the
// result names the copy the server made.
func TestSendKeepsTheServersOwnSentCopy(t *testing.T) {
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent")
	smtpServer := startFakeSMTP(t)
	smtpServer.savesSent(imapServer, "Sent")

	result, _ := sendOnce(t, imapServer, smtpServer, Outgoing{})
	if !result.Copied || result.Warning != nil {
		t.Fatalf("copy = %v, warning = %v", result.Copied, result.Warning)
	}
	if result.SentUID == 0 {
		t.Fatal("the server's copy has no UID")
	}
	if got := copiesOf(t, imapServer, result.Outbox.Envelope.MessageID); len(got) != 1 || got[0] != result.SentUID {
		t.Errorf("copies of the message = %v, reported %d", got, result.SentUID)
	}
}

// TestSendAppendsTheCopyTheServerDoesNotSave checks that a server which
// keeps no copy of its own is given one, as before.
func TestSendAppendsTheCopyTheServerDoesNotSave(t *testing.T) {
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent")
	smtpServer := startFakeSMTP(t)

	result, _ := sendOnce(t, imapServer, smtpServer, Outgoing{})
	if !result.Copied || result.Warning != nil {
		t.Fatalf("copy = %v, warning = %v", result.Copied, result.Warning)
	}
	if result.SentUID == 0 {
		t.Error("the appended copy has no UID")
	}
}

// TestSendKeepsBccInTheOutbox checks that blind copies stay in the outbox
// row when the copy in Sent is the server's own, which has no Bcc header:
// the row is where the user's own record of them remains.
func TestSendKeepsBccInTheOutbox(t *testing.T) {
	imapServer := startFakeIMAP(t, allCaps())
	imapServer.create(t, "INBOX", "Sent")
	smtpServer := startFakeSMTP(t)
	smtpServer.savesSent(imapServer, "Sent")

	result, _ := sendOnce(t, imapServer, smtpServer, Outgoing{
		Bcc: []Address{{Name: "Bob", Email: "bob@example.com"}},
	})
	if len(result.Outbox.Envelope.Bcc) != 1 || result.Outbox.Envelope.Bcc[0].Email != "bob@example.com" {
		t.Errorf("outbox Bcc = %+v", result.Outbox.Envelope.Bcc)
	}
}

// copiesOf returns the UIDs of the messages of a folder whose Message-ID
// is messageID, as another client would find them.
func copiesOf(t *testing.T, f *fakeIMAP, messageID string) []UID {
	t.Helper()
	c := f.session(t)
	defer c.close()
	if _, err := c.client.Select("Sent", &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := c.client.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}},
	}, &imap.SearchOptions{ReturnAll: true}).Wait()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]UID, 0, len(data.AllUIDs()))
	for _, uid := range data.AllUIDs() {
		out = append(out, UID(uid))
	}
	return out
}
