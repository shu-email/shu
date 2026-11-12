package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

func TestMain(m *testing.M) {
	i18n.SetLocaleFunc(func() string { return "en" })
	m.Run()
}

func testAccount(id, name, email string) mail.Account {
	return mail.Account{
		ID: id, Name: name, Email: email, Username: email,
		IMAP: mail.Server{Host: "imap.example.com", Port: 993, TLS: mail.TLSImplicit},
		SMTP: mail.Server{Host: "smtp.example.com", Port: 465, TLS: mail.TLSImplicit},
	}
}

func testFolders() []mail.Folder {
	return []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox, Unread: 2, Total: 3},
		{Name: "Sent", SpecialUse: mail.SpecialSent},
		{Name: "Archive", SpecialUse: mail.SpecialArchive},
		{Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash},
	}
}

func testMessages(accountID string) []mail.Summary {
	now := time.Now()
	return []mail.Summary{
		{UID: 1, Folder: "INBOX", Subject: "Design review — Thursday at 10", Preview: "I pulled the latest frames into Figma.",
			From: mail.Address{Name: "Maya Chen", Email: "maya@example.com"}, Date: now.Add(-time.Hour), Seen: false, HasAttachments: true},
		{UID: 2, Folder: "INBOX", Subject: "[shu-email/shu] Pull request #128", Preview: "requested your review",
			From: mail.Address{Name: "GitHub", Email: "notifications@github.com"}, Date: now.Add(-2 * time.Hour)},
		{UID: 3, Folder: "INBOX", Subject: "Re: Contract for Q4 campaign", Preview: "Attached is the signed copy.",
			From: mail.Address{Name: "Lena Ortiz", Email: "lena@example.com"}, Date: now.Add(-3 * time.Hour), Seen: true},
		{UID: 4, Folder: "Sent", Subject: "Hello Bob", Preview: "Body text",
			From: mail.Address{Name: "Me", Email: "me@example.com"}, To: []mail.Address{{Email: "bob@example.com"}},
			Date: now.Add(-4 * time.Hour), Seen: true},
	}
}

func sampleApp(t *testing.T) (*app, *fakeService) {
	t.Helper()
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", testFolders(), testMessages("a1"))
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a, fs
}

func TestView(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	for _, text := range []string{"Shu", "Inbox", "Starred", "Compose", "Design review — Thursday at 10", "Search mail"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
}

// Choosing another mailbox changes the list.
func TestMailboxChoice(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Sent"); err != nil {
		t.Fatal(err)
	}
	if a.folder != "Sent" {
		t.Errorf("folder %q after a click", a.folder)
	}
	if !tt.HasText("Hello Bob") {
		t.Errorf("sent list did not load: %q", tt.Texts())
	}
	if tt.HasText("Design review — Thursday at 10") {
		t.Error("inbox message still in the sent list")
	}
}

// Selecting a message loads its body and marks it read in step.
func TestSelectingMessageLoadsBodyAndMarksRead(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if a.selected < 0 {
		t.Fatal("no message chosen")
	}
	if !tt.HasText("Body of Design review — Thursday at 10") {
		t.Errorf("body missing from %q", tt.Texts())
	}
	if !a.all[0].Seen {
		t.Error("row is still unread")
	}
	if got := a.folderUnread("INBOX"); got != 1 {
		t.Errorf("inbox unread = %d, want 1", got)
	}
	if len(fs.flagCalls) != 0 {
		// FetchMessage marks it read; no separate flag call is needed.
		t.Logf("flag calls = %v", fs.flagCalls)
	}
}

// Typing in the search field searches the cache and hides other rows.
func TestSearch(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Search mail"); err != nil {
		t.Fatal(err)
	}
	tt.Type("GitHub")
	if !tt.HasText("Search results") {
		t.Errorf("search title missing: %q", tt.Texts())
	}
	if !tt.HasText("Pull request #128") {
		t.Errorf("search result missing: %q", tt.Texts())
	}
	if tt.HasText("Design review — Thursday at 10") {
		t.Error("search still shows a non-matching message")
	}
}

// First run shows the account form; saving it opens the mailbox.
func TestFirstRunOnboarding(t *testing.T) {
	fs := newFakeService()
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 1200)
	if !tt.HasText(locales.AccountsIntro()) {
		t.Fatalf("no onboarding: %q", tt.Texts())
	}
	a.openAccountForm(nil)
	a.accountForm.Name = "Ann"
	a.accountForm.Email = "ann@example.com"
	a.accountForm.Password = "secret"
	a.accountForm.Host = "imap.example.com"
	a.accountForm.Port = "993"
	a.accountForm.SMTPHost = "smtp.example.com"
	a.accountForm.SMTPPort = "465"
	tt.Frame()
	if err := tt.Click("Test and save"); err != nil {
		t.Fatal(err)
	}
	if len(fs.accounts) != 1 {
		t.Fatalf("accounts = %+v", fs.accounts)
	}
	if a.showAccounts {
		t.Error("account view stays after saving")
	}
	if !tt.HasText("Inbox") {
		t.Errorf("mailbox did not open: %q", tt.Texts())
	}
}

// A failed test leaves the form open with the error.
func TestOnboardingTestFailure(t *testing.T) {
	fs := newFakeService()
	fs.testErr = errFake
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 1200)
	a.openAccountForm(nil)
	a.accountForm.Name = "Ann"
	a.accountForm.Email = "ann@example.com"
	a.accountForm.Password = "secret"
	a.accountForm.Host = "imap.example.com"
	a.accountForm.Port = "993"
	a.accountForm.SMTPHost = "smtp.example.com"
	a.accountForm.SMTPPort = "465"
	tt.Frame()
	if err := tt.Click("Test and save"); err != nil {
		t.Fatal(err)
	}
	if len(fs.accounts) != 0 {
		t.Error("account saved despite a failed connection")
	}
	if !tt.HasText(locales.TestFailed(errFake.Error())) {
		t.Errorf("error missing: %q", tt.Texts())
	}
}

// The account menu switches accounts and loads their mail.
func TestAccountSwitching(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "First", "first@example.com"), "secret", testFolders(), testMessages("a1"))
	fs.addAccount(testAccount("a2", "Second", "second@example.com"), "secret", testFolders(), []mail.Summary{{
		UID: 9, Folder: "INBOX", Subject: "Second inbox", From: mail.Address{Name: "Other", Email: "other@example.com"},
	}})
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Accounts"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Second · second@example.com"); err != nil {
		t.Fatalf("menu = %v: %v", tt.Menu(), err)
	}
	if a.active != "a2" {
		t.Errorf("active = %q", a.active)
	}
	if !tt.HasText("Second inbox") {
		t.Errorf("second account's mail missing: %q", tt.Texts())
	}
}

// Starring is optimistic and rolls back when the server refuses.
func TestStarRollsBackOnError(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Star message"); err != nil {
		t.Fatal(err)
	}
	if !a.all[0].Flagged {
		t.Fatal("star did not show")
	}
	fs.flagErr = errFake
	tt.Frame()
	if err := tt.Click("Unstar message"); err != nil {
		t.Fatal(err)
	}
	if !a.all[0].Flagged {
		t.Error("star was not rolled back")
	}
	if a.err == "" {
		t.Error("failure was not reported")
	}
}

// Moving removes the row, and a failed move brings it back.
func TestMoveAndRollback(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Archive message"); err != nil {
		t.Fatal(err)
	}
	if len(fs.moved) != 1 || fs.moved[0] != "INBOX->Archive" {
		t.Errorf("moves = %v", fs.moved)
	}
	if tt.HasText("Design review — Thursday at 10") {
		t.Error("archived message remains")
	}
	// Now a failed delete: the row must come back.
	if err := tt.Click("[shu-email/shu] Pull request #128"); err != nil {
		t.Fatal(err)
	}
	fs.moveErr = errFake
	tt.Frame()
	if err := tt.Click("Delete message"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("[shu-email/shu] Pull request #128") {
		t.Error("failed move removed the message")
	}
	if a.err == "" {
		t.Error("move failure was not reported")
	}
}

// Reply opens a compose state with the recipient and quotation filled.
func TestReplyPrefill(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Reply"); err != nil {
		t.Fatal(err)
	}
	if len(a.composes) != 1 {
		t.Fatalf("composes = %d", len(a.composes))
	}
	cs := a.composes[0]
	if !strings.Contains(cs.to, "maya@example.com") {
		t.Errorf("to = %q", cs.to)
	}
	if !strings.HasPrefix(cs.subject, "Re: ") {
		t.Errorf("subject = %q", cs.subject)
	}
	if !strings.Contains(cs.body, "> Body of Design review") {
		t.Errorf("quoted body = %q", cs.body)
	}
	if cs.inReplyTo != "" {
		// The fake summary has no Message-ID; a real one would.
		t.Logf("in reply to %q", cs.inReplyTo)
	}
}

// Sending closes the window on success and keeps it open on failure.
func TestComposeSend(t *testing.T) {
	a, fs := sampleApp(t)
	out := &mail.Outgoing{To: []mail.Address{{Email: "bob@example.com"}}, Subject: "Hello", Text: "Body"}
	a.openCompose(out)
	cs := a.composes[len(a.composes)-1]
	cs.send()
	if len(fs.sent) != 1 || fs.sent[0].Subject != "Hello" {
		t.Fatalf("sent = %+v", fs.sent)
	}
	if !cs.closed {
		t.Error("compose window stays open after sending")
	}

	fs.sendErr = errFake
	a.openCompose(out)
	failed := a.composes[len(a.composes)-1]
	failed.send()
	if failed.closed {
		t.Error("compose closed after a failed send")
	}
	if !strings.Contains(failed.err, errFake.Error()) {
		t.Errorf("error = %q", failed.err)
	}

	fs.sendErr = nil
	fs.sendWarn = errFake
	a.openCompose(out)
	warned := a.composes[len(a.composes)-1]
	warned.send()
	if !warned.closed || warned.note == "" {
		t.Errorf("missing-copy warning: closed=%v note=%q", warned.closed, warned.note)
	}
}

// The HTML toggle switches modes and shows the body either way.
func TestHTMLToggle(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Show HTML"); err != nil {
		t.Fatal(err)
	}
	if !a.htmlOn {
		t.Fatal("HTML mode did not turn on")
	}
	if !tt.HasText("Show plain text") {
		t.Errorf("toggle label did not change: %q", tt.Texts())
	}
	if err := tt.Click("Show plain text"); err != nil {
		t.Fatal(err)
	}
	if a.htmlOn {
		t.Error("HTML mode did not turn off")
	}
}

// The language choice calls the locale setter.
func TestSettingsLanguage(t *testing.T) {
	a, _ := sampleApp(t)
	chosen := ""
	a.setLocale = func(tag string) { chosen = tag }
	a.showSettings = true
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("English"); err != nil {
		t.Fatal(err)
	}
	if chosen != "en" || a.lang != 1 {
		t.Errorf("chosen %q lang %d", chosen, a.lang)
	}
	if err := tt.Click("简体中文"); err != nil {
		t.Fatal(err)
	}
	if chosen != "zh-Hans" || a.lang != 2 {
		t.Errorf("chosen %q lang %d", chosen, a.lang)
	}
}

// The layout holds at the minimum size, in both appearances.
func TestMinimumSizeAndDark(t *testing.T) {
	a, _ := sampleApp(t)
	for _, dark := range []bool{false, true} {
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		for _, text := range []string{"Shu", "Inbox", "Search mail", "Compose"} {
			if !tt.HasText(text) {
				t.Errorf("dark=%v missing %q", dark, text)
			}
		}
	}
}

// The outbox banner surfaces an unknown delivery and retries by hand.
func TestPendingSendBanner(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", testFolders(), testMessages("a1"))
	fs.pending = []mail.PendingSend{{ID: "o1", AccountID: "a1", State: mail.StateUnknown}}
	a := newTestApp(fs)
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	if !tt.HasText(locales.DeliveryUnknown(1)) {
		t.Errorf("banner missing: %q", tt.Texts())
	}
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	if len(fs.pending) != 0 {
		t.Error("retry did not clear the outbox")
	}
}

// The compose view sends with its button and closes.
func TestComposeViewSends(t *testing.T) {
	a, fs := sampleApp(t)
	a.openCompose(&mail.Outgoing{To: []mail.Address{{Email: "bob@example.com"}}, Subject: "Hello", Text: "Body"})
	cs := a.composes[len(a.composes)-1]
	tt := ui.NewTester(cs.view, 720, 620)
	if !tt.HasText("Send") {
		t.Errorf("compose view missing its actions: %q", tt.Texts())
	}
	if err := tt.Click("Send"); err != nil {
		t.Fatal(err)
	}
	if len(fs.sent) != 1 || fs.sent[0].Subject != "Hello" {
		t.Fatalf("sent = %+v", fs.sent)
	}
	if !cs.closed {
		t.Error("compose did not close after sending")
	}
}

// An attachment's Save as writes the fetched bytes to the chosen path.
func TestAttachmentSaveAs(t *testing.T) {
	a, fs := sampleApp(t)
	m := a.all[0]
	key := bodyKey("a1", m.Folder, m.UID)
	fs.bodies[key] = mail.Message{
		Summary: m, BodyText: "Body", BodyCached: true,
		Attachments: []mail.Attachment{{Part: "2", Filename: "report.txt", ContentType: "text/plain", Size: 4}},
	}
	fs.attachment = mail.Attachment{Part: "2", Filename: "report.txt"}
	delete(a.bodies, summaryKey(m))
	a.reloadBody()
	path := filepath.Join(t.TempDir(), "report.txt")
	a.saveFile = func(parent *mygo.Window, name string) (string, error) { return path, nil }
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("report.txt") {
		t.Fatalf("attachment card missing: %q", tt.Texts())
	}
	if err := tt.Click("Save as"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data" {
		t.Errorf("saved bytes = %q", data)
	}
}

// The first message opens with the folder, and J/K move the choice.
func TestKeyboardNavigation(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if a.selected != 0 {
		t.Fatalf("selected = %d after opening the folder", a.selected)
	}
	tt.Key(0, ui.KeyJ)
	if a.selected != 1 {
		t.Fatalf("selected = %d after J", a.selected)
	}
	if !tt.HasText("Body of [shu-email/shu] Pull request #128") {
		t.Errorf("J did not open the next body: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyK)
	if a.selected != 0 {
		t.Errorf("selected = %d after K", a.selected)
	}
	// U marks the chosen message unread again.
	tt.Key(0, ui.KeyU)
	if a.all[0].Seen {
		t.Error("U did not mark the message unread")
	}
	if got := a.folderUnread("INBOX"); got != 1 {
		t.Errorf("inbox unread = %d after U", got)
	}
}

// Plain shortcuts do not fire while the search field has the focus.
func TestShortcutsDoNotStealTyping(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	selected := a.selected
	if err := tt.Click("Search mail"); err != nil {
		t.Fatal(err)
	}
	tt.Type("de")
	if a.query != "de" {
		t.Errorf("query = %q", a.query)
	}
	if a.selected != selected {
		t.Errorf("typing moved the selection from %d to %d", selected, a.selected)
	}
	if len(a.all) == 0 || !strings.Contains(a.all[0].Subject, "Design review") {
		t.Errorf("typing E archived the message: %+v", a.all)
	}
}

// Cmd+K focuses the search field from the mail panes.
func TestCmdKFocusesSearch(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("GitHub")
	if a.query != "GitHub" {
		t.Errorf("query = %q after Cmd+K and typing", a.query)
	}
}

// Archiving offers an undo that moves the message back and restores its
// row.
func TestUndoMove(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Archive message"); err != nil {
		t.Fatal(err)
	}
	if a.undo == nil {
		t.Fatal("no undo offered")
	}
	if tt.HasText("Design review — Thursday at 10") {
		t.Error("archived message remains")
	}
	if err := tt.Click("Undo"); err != nil {
		t.Fatal(err)
	}
	if len(fs.moveBacks) != 1 || fs.moveBacks[0] != "Archive->INBOX" {
		t.Fatalf("move backs = %v", fs.moveBacks)
	}
	if a.undo != nil {
		t.Error("undo stays after use")
	}
	if !tt.HasText("Design review — Thursday at 10") {
		t.Errorf("undo did not restore the row: %q", tt.Texts())
	}
}

// The Dock badge follows the inbox's unread count.
func TestUnreadDockBadge(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", testFolders(), testMessages("a1"))
	a := newTestApp(fs)
	badge := -1
	a.setBadge = func(n int) { badge = n }
	a.start()
	if badge != 1 {
		t.Fatalf("badge = %d after opening the folder", badge)
	}
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Key(0, ui.KeyU)
	if badge != 2 {
		t.Errorf("badge = %d after marking unread", badge)
	}
}

// Cmd-click chooses several messages, and a toolbar action applies to all
// of them.
func TestMultiSelectArchive(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "[shu-email/shu] Pull request #128"); err != nil {
		t.Fatal(err)
	}
	if a.sel.Len() != 2 {
		t.Fatalf("selection = %d", a.sel.Len())
	}
	if !tt.HasText(locales.SelectedCount(2)) {
		t.Errorf("selection count missing: %q", tt.Texts())
	}
	if err := tt.Click("Archive message"); err != nil {
		t.Fatal(err)
	}
	if len(fs.moved) != 2 {
		t.Fatalf("moves = %v", fs.moved)
	}
	if len(a.all) != 1 {
		t.Errorf("rows left = %d, want 1", len(a.all))
	}
	if a.sel.Len() != 1 {
		t.Errorf("selection = %d after the move, want the remaining row chosen", a.sel.Len())
	}
}

// Adding an account when one exists floats the form over the mail window.
func TestAddAccountOverlay(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.openAccountForm(nil)
	tt.Frame()
	if !a.accountOpen || a.showAccounts {
		t.Fatalf("accountOpen=%v showAccounts=%v", a.accountOpen, a.showAccounts)
	}
	if !tt.HasText(locales.AddAccount()) {
		t.Errorf("overlay title missing: %q", tt.Texts())
	}
	a.accountForm.Name = "Second"
	a.accountForm.Email = "second@example.com"
	a.accountForm.Password = "secret"
	a.accountForm.Host = "imap.example.com"
	a.accountForm.Port = "993"
	a.accountForm.SMTPHost = "smtp.example.com"
	a.accountForm.SMTPPort = "465"
	tt.Frame()
	if err := tt.Click("Test and save"); err != nil {
		t.Fatal(err)
	}
	if len(fs.accounts) != 2 {
		t.Fatalf("accounts = %+v", fs.accounts)
	}
	if a.accountOpen {
		t.Error("overlay stays open after saving")
	}
}

// Editing the active account uses the same overlay, and Cancel dismisses
// it without saving.
func TestEditAccountOverlayCancel(t *testing.T) {
	a, _ := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.openAccountForm(a.activeAccount())
	tt.Frame()
	if !tt.HasText(locales.EditAccount()) {
		t.Errorf("edit title missing: %q", tt.Texts())
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if a.accountOpen {
		t.Error("Cancel did not close the overlay")
	}
}

// Starring several messages flags them all, and the toolbar offers the
// opposite action afterwards.
func TestMultiSelectStar(t *testing.T) {
	a, fs := sampleApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "[shu-email/shu] Pull request #128"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Star message"); err != nil {
		t.Fatal(err)
	}
	if len(fs.flagCalls) != 2 {
		t.Fatalf("flag calls = %v", fs.flagCalls)
	}
	for _, m := range a.all {
		if m.UID == 1 || m.UID == 2 {
			if !m.Flagged {
				t.Errorf("message %d is not flagged", m.UID)
			}
		}
	}
	if !tt.HasText("Unstar message") {
		t.Errorf("toolbar does not offer Unstar: %q", tt.Texts())
	}
}

// The undo offer retires after its delay.
func TestUndoExpires(t *testing.T) {
	a, _ := sampleApp(t)
	fired := false
	a.after = func(d time.Duration, fn func()) *time.Timer {
		fired = true
		fn()
		return nil
	}
	a.setUndo(&undoMove{label: "Archived"})
	if !fired {
		t.Fatal("no timer was armed")
	}
	if a.undo != nil {
		t.Error("the undo offer outlived its timer")
	}
}

// The first message is chosen as one selection, so the row is
// highlighted, and clearing the selection empties the reading pane.
func TestAutoSelectionAndClear(t *testing.T) {
	a, _ := sampleApp(t)
	if a.selected != 0 || a.sel.Len() != 1 || !a.sel.Has(summaryKey(a.all[0])) {
		t.Fatalf("selected=%d sel=%d", a.selected, a.sel.Len())
	}
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Design review — Thursday at 10"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "[shu-email/shu] Pull request #128"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Clear selection"); err != nil {
		t.Fatal(err)
	}
	if a.selected != -1 || a.sel.Len() != 0 {
		t.Errorf("selected=%d sel=%d after clearing", a.selected, a.sel.Len())
	}
	if !tt.HasText(locales.NoMessageSelected()) {
		t.Errorf("reading pane is not empty: %q", tt.Texts())
	}
}
