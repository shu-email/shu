package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestSpecialUseAttributes checks what a server's attributes and names
// say of a folder, Gmail's among them.
func TestSpecialUseAttributes(t *testing.T) {
	tests := []struct {
		name  string
		attrs []string
		want  string
	}{
		{"gmail's all mail", []string{`\All`, `\HasNoChildren`}, SpecialArchive},
		{"an archive named as one", []string{`\Archive`}, SpecialArchive},
		{"all mail beside an archive, either order", []string{`\All`, `\Archive`}, SpecialArchive},
		{"all mail beside an archive, the other order", []string{`\Archive`, `\All`}, SpecialArchive},
		{"gmail's important", []string{`\Important`}, SpecialImportant},
		{"gmail's sent", []string{`\Sent`}, SpecialSent},
		{"gmail's spam", []string{`\Junk`}, SpecialJunk},
		{"gmail's starred", []string{`\Flagged`}, SpecialFlagged},
		{"a plain folder", []string{`\HasNoChildren`}, ""},
	}
	for _, test := range tests {
		if got := specialUse(test.attrs); got != test.want {
			t.Errorf("%s: specialUse(%v) = %q, want %q", test.name, test.attrs, got, test.want)
		}
	}

	// The names are a fallback only: Gmail localizes them, so a name
	// alone is not enough to tell an archive.
	if got := folderSpecialUse("[Gmail]/所有邮件", nil); got != "" {
		t.Errorf("a localized name without attributes = %q, want none", got)
	}
	if got := folderSpecialUse("[Gmail]/All Mail", nil); got != SpecialArchive {
		t.Errorf("All Mail by its English name = %q, want %q", got, SpecialArchive)
	}
	if got := specialUse(nil); got != "" {
		t.Errorf("no attributes = %q", got)
	}
}

// TestGmailFindsArchiveAndImportant checks that Gmail's attributes reach
// the folder list, and that Important, a label Gmail keeps, cannot be
// renamed.
func TestGmailFindsArchiveAndImportant(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	const (
		allMail   = "[Gmail]/所有邮件"
		important = "[Gmail]/重要"
		sent      = "[Gmail]/已发邮件"
	)
	f.setListAttrs(map[string][]imap.MailboxAttr{
		"[Gmail]": {imap.MailboxAttrNoSelect, imap.MailboxAttrHasChildren},
		allMail:   {imap.MailboxAttrAll, imap.MailboxAttrHasNoChildren},
		important: {imap.MailboxAttrImportant, imap.MailboxAttrHasNoChildren},
		sent:      {imap.MailboxAttrSent, imap.MailboxAttrHasNoChildren},
		"INBOX":   {imap.MailboxAttrHasNoChildren},
	})
	for _, name := range []string{"[Gmail]", allMail, important, sent, "INBOX"} {
		if err := f.user.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	svc, _, _ := newTestService()
	svc.SetTLSConfig(insecureTLS())
	a := fakeAccount(t, f.addr)
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	folders, err := svc.SyncAccount(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(folders))
	for _, folder := range folders {
		got[folder.Name] = folder.SpecialUse
	}
	for name, want := range map[string]string{
		"INBOX":   SpecialInbox,
		allMail:   SpecialArchive,
		important: SpecialImportant,
		sent:      SpecialSent,
	} {
		if got[name] != want {
			t.Errorf("%s: special use = %q, want %q", name, got[name], want)
		}
	}
	if _, listed := got["[Gmail]"]; listed {
		t.Error("the noselect parent became a folder")
	}

	if err := svc.RenameFolder(ctx, a.ID, important, important+" 2"); !errors.Is(err, ErrFolderSystem) {
		t.Errorf("renaming Important = %v, want ErrFolderSystem", err)
	}
}
