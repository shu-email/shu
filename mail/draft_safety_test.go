package mail

import (
	"context"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// flag sets a flag on a message of the fake server without expunging, as
// a mail client that marks mail deleted and removes it later does.
func (f *fakeIMAP) flag(t *testing.T, mailbox string, uid UID, flag imap.Flag) {
	t.Helper()
	c := f.session(t)
	defer c.close()
	if _, err := c.client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	if err := c.client.Store(set, &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{flag},
	}, nil).Close(); err != nil {
		t.Fatalf("flag %s/%d: %v", mailbox, uid, err)
	}
}

// subjects lists the subjects of a mailbox of the fake server, sorted.
func (f *fakeIMAP) subjects(t *testing.T, mailbox string) []string {
	t.Helper()
	var out []string
	for _, m := range f.messages(t, mailbox) {
		out = append(out, m.Subject)
	}
	slices.Sort(out)
	return out
}

// openTestDrafts selects the Drafts folder on a connection of the service.
func openTestDrafts(t *testing.T, svc *Service, a Account) *draftBox {
	t.Helper()
	ctx := context.Background()
	acc, password, err := svc.account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.dialIMAP(ctx, acc, password)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	box, err := svc.openDrafts(ctx, c, a.ID)
	if err != nil || box == nil {
		t.Fatalf("open drafts: %v, %v", box, err)
	}
	return box
}

// What a search for a draft's copies returns is deleted for good. An IMAP
// HEADER search matches substrings, and an empty value matches every
// message that has the header at all, so the search alone must never
// decide: only a message carrying exactly the draft's ID and Message-ID
// is a copy of it.
func TestDraftCopiesAreMatchedExactly(t *testing.T) {
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	// 1: the draft's real copy.
	f.append(t, "Drafts", foreignDraft("1@example.com", "Mine", "mine", draftIDHeader+": abcd"))
	// 2 and 3: other messages whose headers contain the draft's values.
	f.append(t, "Drafts", foreignDraft("21@example.com", "Longer ID", "theirs", draftIDHeader+": xabcdx"))
	f.append(t, "Drafts", foreignDraft("1@example.com.au", "Longer domain", "theirs", draftIDHeader+": abcd"))
	// 4: the right Message-ID under another draft's ID.
	f.append(t, "Drafts", foreignDraft("1@example.com", "Other draft", "theirs", draftIDHeader+": abcde"))
	// 5: a draft another mail client wrote, with no ID of this app.
	f.append(t, "Drafts", foreignDraft("phone@example.com", "From the phone", "theirs"))
	box := openTestDrafts(t, svc, a)

	for _, tc := range []struct {
		name string
		d    Draft
		want []UID
	}{
		{"exact", Draft{ID: "abcd", MessageID: "1@example.com"}, []UID{1}},
		{"another draft with the same Message-ID", Draft{ID: "abcde", MessageID: "1@example.com"}, []UID{4}},
		{"substring of the ID", Draft{ID: "bcd", MessageID: "1@example.com"}, nil},
		{"substring of the Message-ID", Draft{ID: "abcd", MessageID: "@example.com"}, nil},
		{"no ID", Draft{ID: "", MessageID: "1@example.com"}, nil},
		{"no Message-ID", Draft{ID: "abcd", MessageID: ""}, nil},
		{"neither", Draft{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := box.copies(tc.d)
			if err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("copies = %v, want %v", got, tc.want)
			}
		})
	}
}

// A draft taken over from another mail client is found again by its
// Message-ID when its recorded place stops meaning anything. A message
// whose Message-ID merely contains that one is somebody else's draft: it
// must not be taken for the copy, or the next edit would delete it.
func TestLookalikeDraftIsNotTakenOver(t *testing.T) {
	ctx := context.Background()
	svc, _, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	f.append(t, "Drafts", foreignDraft("1@example.com", "From the phone", "typed on the phone"))
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	d, err := svc.AdoptDraft(ctx, a.ID, "Drafts", 1)
	if err != nil {
		t.Fatal(err)
	}
	// The folder is made anew, so the recorded UID means nothing, and it
	// now holds only a draft whose Message-ID contains the adopted one's.
	if err := f.user.Delete("Drafts"); err != nil {
		t.Fatal(err)
	}
	f.create(t, "Drafts")
	f.append(t, "Drafts", foreignDraft("21@example.com", "Lookalike", "someone else's"))

	mustSyncDrafts(t, svc, a.ID)
	if got := f.subjects(t, "Drafts"); !slices.Contains(got, "Lookalike") {
		t.Fatalf("server drafts after the sync = %v", got)
	}
	// Editing the adopted draft replaces its own copy and nothing else.
	edit := mustDraft(t, svc, d.ID)
	edit.Body = "typed on the phone, finished here"
	mustSaveDraft(t, svc, edit)
	mustSyncDrafts(t, svc, a.ID)

	want := []string{"From the phone", "Lookalike"}
	if got := f.subjects(t, "Drafts"); !slices.Equal(got, want) {
		t.Fatalf("server drafts after the edit = %v, want %v: another client's draft was deleted", got, want)
	}
	for _, m := range f.messages(t, "Drafts") {
		if m.Subject == "Lookalike" && m.BodyText != "someone else's" {
			t.Errorf("the other client's draft was rewritten: %q", m.BodyText)
		}
	}
}

// Without UIDPLUS the only way to remove a copy is EXPUNGE, which removes
// every message flagged \Deleted. A message another mail client flagged
// and has not removed yet can still be undeleted there, so syncing a
// draft must leave it in place, flag and all.
func TestDraftSyncSparesMailMarkedDeletedElsewhere(t *testing.T) {
	caps := allCaps()
	delete(caps, imap.CapUIDPlus)
	svc, _, f, a := draftService(t, caps, "INBOX", "Drafts")
	f.append(t, "Drafts", foreignDraft("phone@example.com", "Marked deleted on the phone", "still undeletable there"))
	f.flag(t, "Drafts", 1, imap.FlagDeleted)

	d := mustSaveDraft(t, svc, Draft{AccountID: a.ID, To: "bob@example.com", Subject: "Plan", Body: "one"})
	mustSyncDrafts(t, svc, a.ID)
	d.Body = "one, two"
	mustSaveDraft(t, svc, d)
	// This sync appends the new copy and expunges the old one.
	if result := mustSyncDrafts(t, svc, a.ID); result.Uploaded != 1 {
		t.Fatalf("result = %+v", result)
	}

	msgs := f.messages(t, "Drafts")
	var spared *serverMessage
	plans := 0
	for i := range msgs {
		switch msgs[i].Subject {
		case "Marked deleted on the phone":
			spared = &msgs[i]
		case "Plan":
			plans++
		}
	}
	if plans != 1 {
		t.Errorf("copies of the draft = %d, want 1", plans)
	}
	if spared == nil {
		t.Fatal("the sync expunged a message another mail client had only marked deleted")
	}
	if !spared.hasFlag(imap.FlagDeleted) {
		t.Errorf("the other client's message lost its \\Deleted flag: %v", spared.Flags)
	}

	// Discarding the draft removes its copy the same careful way.
	if err := svc.DiscardDraft(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.subjects(t, "Drafts"); !slices.Equal(got, []string{"Marked deleted on the phone"}) {
		t.Errorf("server drafts after the discard = %v", got)
	}
}
