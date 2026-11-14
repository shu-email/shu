package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// lab is the address Ann's account sends from besides its own: an alias
// at her lab.
var lab = mail.Identity{ID: "lab", AccountID: "a1", Name: "Ann Lee (Lab)", Email: "ann@lab.example.org"}

// annSignature is what Ann signs with, in every kind of message.
var annSignature = mail.Signature{Text: "Ann Lee\nResearch lead, Example Lab", New: true, Reply: true, Forward: true}

// designBody and designFiles are the text and the files of Maya's
// message about the design review.
const designBody = "Hi Ann,\n\nI pulled the latest frames into Figma. The onboarding flow is on page 3.\n\nMaya"

var designFiles = []mail.Attachment{
	{Part: "2", Filename: "frames.pdf", ContentType: "application/pdf", Size: 240 << 10},
	{Part: "3", Filename: "onboarding-flow.png", ContentType: "image/png", Size: 80 << 10},
	{Part: "4", Filename: "notes.txt", ContentType: "text/plain", Size: 2 << 10},
}

// composeService is the fake of Ann's account, which sends from its own
// address and the lab's and signs with annSignature. Maya's message, the
// first of the inbox, went to Ann and Lena, with copies to the lab's
// address and to Raj, and carries three files; the compose settings are
// the engine's, but for the send delay, which the tests of the delay set.
func composeService() *fakeService {
	fs := newFakeService()
	folders := append(testFolders(), mail.Folder{Name: "Drafts", SpecialUse: mail.SpecialDrafts})
	msgs := testMessages("a1")
	msgs[0].MessageID = "design-1@example.com"
	msgs[0].To = []mail.Address{{Name: "Ann Lee", Email: "ann@example.com"}, {Name: "Lena Ortiz", Email: "lena@example.com"}}
	msgs[0].Cc = []mail.Address{{Email: "ANN@lab.example.org"}, {Name: "Raj Patel", Email: "raj@example.com"}}
	fs.addAccount(testAccount("a1", "Ann Lee", "ann@example.com"), "secret", folders, msgs)
	fs.identities["a1"] = []mail.Identity{lab}
	fs.signatures["a1"] = annSignature
	m, _ := fs.find("a1", "INBOX", 1)
	fs.bodies[bodyKey("a1", "INBOX", 1)] = mail.Message{Summary: m, BodyText: designBody, BodyCached: true, Attachments: designFiles}
	return fs
}

// composeApp starts the window on composeService, showing messages one
// by one.
func composeApp(t *testing.T) (*app, *fakeService) {
	t.Helper()
	fs := inMessageView(composeService())
	return openApp(fs), fs
}

// lastCompose is the compose window opened last, with a tester of it.
func lastCompose(t *testing.T, a *app) (*composeState, *ui.Tester) {
	t.Helper()
	if len(a.composes) == 0 {
		t.Fatal("no compose window opened")
	}
	cs := a.composes[len(a.composes)-1]
	return cs, ui.NewTester(cs.view, 720, 620)
}

// openDesign chooses Maya's message in the mail window.
func openDesign(t *testing.T, main *ui.Tester) {
	t.Helper()
	click(t, main, "Design review — Thursday at 10")
	if !main.HasText(locales.Attachments()) {
		t.Fatalf("Maya's message did not open with its files: %q", main.Texts())
	}
}

// The labels the From picker names Ann's addresses by.
const (
	ownLabel = "Ann Lee <ann@example.com>"
	labLabel = "Ann Lee (Lab) <ann@lab.example.org>"
	// signed is annSignature as a body carries it.
	signed = "-- \nAnn Lee\nResearch lead, Example Lab"
)

// The Bcc field is folded away until the user asks for it, with a button
// beside Cc that the keyboard reaches. Asked for, it takes the keyboard
// and stays, also emptied; its addresses are kept in the draft, a draft
// with a Bcc opens with the field shown, and the message goes to them.
func TestBccFoldedAwayUntilAsked(t *testing.T) {
	a, fs := draftsApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if _, ok := tt.AccessByRole(ui.RoleComboBox, locales.Bcc()); ok || cs.showBcc {
		t.Fatalf("a new message shows the Bcc field: %q", tt.Texts())
	}
	typeIn(t, tt, locales.To(), "Bob <bob@example.com>")
	click(t, tt, locales.Cc())
	tt.Key(0, ui.KeyTab)
	if !tt.Focused(locales.ShowBcc()) {
		t.Fatal("Tab from Cc does not reach the Bcc button")
	}
	tt.Key(0, ui.KeyEnter)
	if !cs.showBcc || !tt.Focused(locales.Bcc()) {
		t.Fatalf("Enter on the Bcc button: shown %v, the field focused %v", cs.showBcc, tt.Focused(locales.Bcc()))
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, locales.ShowBcc()); ok {
		t.Error("the Bcc button stays beside an open Bcc field")
	}
	tt.Type("Erin <erin@example.com>")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	if d := theDraft(t, fs); d.Bcc != "Erin <erin@example.com>" || d.To != "Bob <bob@example.com>" {
		t.Fatalf("draft = %+v", d)
	}
	// Emptied, the field stays where the address was.
	cs.bcc = ""
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleComboBox, locales.Bcc()); !ok {
		t.Fatal("the Bcc field folded away as it was emptied")
	}
	typeIn(t, tt, locales.Bcc(), "erin@example.com")

	// Closed, and opened again from Drafts: the field shows, with its
	// address, and the message goes to it.
	cs.close()
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, locales.EditDraft())
	open := openComposes(a)
	if len(open) != 1 || !open[0].showBcc || open[0].bcc != "erin@example.com" {
		t.Fatalf("the draft opened again = %+v", open)
	}
	rt := ui.NewTester(open[0].view, 720, 620)
	if !slices.Contains(fieldValues(rt), "erin@example.com") {
		t.Errorf("the Bcc field does not show its address: %q", fieldValues(rt))
	}
	click(t, rt, locales.Send())
	if len(fs.sent) != 1 || !slices.Equal(fs.sent[0].Bcc, []mail.Address{{Email: "erin@example.com"}}) ||
		!slices.Equal(fs.sent[0].To, []mail.Address{{Name: "Bob", Email: "bob@example.com"}}) {
		t.Fatalf("sent = %+v", fs.sent)
	}
}

// An account that sends from more than one address offers them in a
// From picker, its own address first and chosen. The choice is kept in
// the draft, opens again with it, and the message goes from it. An
// account with its own address alone has no picker.
func TestFromPickerKeptInDraft(t *testing.T) {
	a, fs := composeApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if from, ok := tt.AccessByRole(ui.RolePopUpButton, locales.From()); !ok || from.Value != ownLabel {
		t.Fatalf("the From picker = %+v, found %v: %q", from, ok, tt.Texts())
	}
	typeIn(t, tt, locales.To(), "maya@example.com")
	click(t, tt, ownLabel)
	click(t, tt, labLabel)
	if cs.identity != "lab" {
		t.Fatalf("chose the lab's address, identity %q", cs.identity)
	}
	if d := theDraft(t, fs); d.Identity != "lab" {
		t.Fatalf("draft = %+v", d)
	}
	cs.close()
	reopened := a.resumeDraft(theDraft(t, fs))
	rt := ui.NewTester(reopened.view, 720, 620)
	if from, _ := rt.AccessByRole(ui.RolePopUpButton, locales.From()); from.Value != labLabel || reopened.identity != "lab" {
		t.Fatalf("the draft opened again from %q, identity %q", from.Value, reopened.identity)
	}
	click(t, rt, locales.Send())
	if len(fs.sent) != 1 || fs.sent[0].Identity != "lab" {
		t.Fatalf("sent = %+v", fs.sent)
	}

	// Back to the account's own address.
	cs = a.openCompose(nil)
	tt = ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "maya@example.com")
	click(t, tt, ownLabel)
	click(t, tt, labLabel)
	click(t, tt, labLabel)
	click(t, tt, ownLabel)
	if cs.identity != "" || theDraftOf(t, fs, cs.draftID).Identity != "" {
		t.Fatalf("back to the own address: identity %q", cs.identity)
	}

	// Its own address alone is still shown, as the sender.
	a.aliases["a1"] = nil
	single := a.openCompose(nil)
	st := ui.NewTester(single.view, 720, 620)
	if from, ok := st.AccessByRole(ui.RolePopUpButton, locales.From()); !ok || from.Value != ownLabel {
		t.Errorf("an account with one address shows %+v, found %v: %q", from, ok, st.Texts())
	}
}

// theDraftOf is the draft the fake holds under an ID.
func theDraftOf(t *testing.T, fs *fakeService, id string) mail.Draft {
	t.Helper()
	d, ok := fs.drafts[id]
	if !ok {
		t.Fatalf("no draft %q among %+v", id, fs.drafts)
	}
	return d
}

// A reply goes from the address the message reached: the one in its To,
// else in its Cc, else the one its servers delivered it to, and the
// account's own when it reached none of them.
func TestReplyStartsFromItsIdentity(t *testing.T) {
	fs := inMessageView(composeService())
	inbox := fs.messages[listKey("a1", "INBOX")]
	for i := range inbox {
		switch inbox[i].UID {
		case 2:
			// A list's mail, delivered to the lab's address.
			inbox[i].To = []mail.Address{{Email: "shu-dev@lists.example.org"}}
			fs.bodies[bodyKey("a1", "INBOX", 2)] = mail.Message{Summary: inbox[i], BodyText: "A pull request.", BodyCached: true,
				RawHeaders: "Delivered-To: ann@lab.example.org\r\n"}
		case 3:
			inbox[i].To = []mail.Address{{Email: "contracts@example.com"}}
			inbox[i].Cc = []mail.Address{{Name: "Lab", Email: "Ann@Lab.example.org"}}
		}
	}
	a := openApp(fs)
	main := ui.NewTester(a.view, 1220, 800)
	for _, c := range []struct {
		row, want, label string
	}{
		// Maya's went to the account's address, and copied the lab's.
		{"Design review — Thursday at 10", "", ownLabel},
		{"[shu-email/shu] Pull request #128", "lab", labLabel},
		{"Re: Contract for Q4 campaign", "lab", labLabel},
	} {
		click(t, main, c.row)
		click(t, main, locales.Reply())
		cs, ct := lastCompose(t, a)
		if from, _ := ct.AccessByRole(ui.RolePopUpButton, locales.From()); cs.identity != c.want || from.Value != c.label {
			t.Errorf("a reply to %q goes from %q, the picker says %q", c.row, cs.identity, from.Value)
		}
		cs.close()
	}
	// The address of the account's own: a message that reached none of
	// them.
	inbox[2].Cc = nil
	a.refresh()
	main.Frame()
	click(t, main, "Re: Contract for Q4 campaign")
	click(t, main, locales.Reply())
	if cs, _ := lastCompose(t, a); cs.identity != "" {
		t.Errorf("a reply to mail of none of the addresses goes from %q", cs.identity)
	}
}

// Reply all goes to everyone the message went to but the user, whose
// addresses are all of the account's: its own and those added, in any
// case. Answering one's own message, from an alias, follows it up.
func TestReplyAllLeavesOutEveryIdentity(t *testing.T) {
	a, fs := composeApp(t)
	main := ui.NewTester(a.view, 1220, 800)
	openDesign(t, main)
	click(t, main, locales.ReplyAll())
	cs, _ := lastCompose(t, a)
	if want := "Maya Chen <maya@example.com>, Lena Ortiz <lena@example.com>, Raj Patel <raj@example.com>"; cs.to != want || cs.cc != "" {
		t.Errorf("reply all to %q, cc %q, want %q", cs.to, cs.cc, want)
	}
	click(t, main, locales.Reply())
	if cs, _ := lastCompose(t, a); cs.to != "Maya Chen <maya@example.com>" {
		t.Errorf("reply to %q", cs.to)
	}

	// A message Ann sent from the lab's address, to Bob and herself.
	fs.messages[listKey("a1", "Sent")] = []mail.Summary{{
		UID: 9, Folder: "Sent", Account: "a1", Subject: "Lab results", Seen: true, Date: time.Now().Add(-5 * time.Hour),
		From: lab.Address(), To: []mail.Address{{Name: "Bob", Email: "bob@example.com"}, {Email: "ann@example.com"}},
		Cc: []mail.Address{{Email: "ann@lab.example.org"}, {Email: "carol@example.com"}},
	}}
	fs.bodies[bodyKey("a1", "Sent", 9)] = mail.Message{Summary: fs.messages[listKey("a1", "Sent")][0], BodyText: "Attached.", BodyCached: true}
	click(t, main, "Sent")
	click(t, main, "Lab results")
	click(t, main, locales.Reply())
	if cs, _ := lastCompose(t, a); cs.to != "Bob <bob@example.com>" || cs.identity != "lab" {
		t.Errorf("a reply to her own message goes to %q from %q", cs.to, cs.identity)
	}
	click(t, main, locales.ReplyAll())
	if cs, _ := lastCompose(t, a); cs.to != "Bob <bob@example.com>, carol@example.com" {
		t.Errorf("reply all to her own message goes to %q", cs.to)
	}
}

// designQuote is Maya's message quoted line by line, as a reply quotes
// it by default, in the window's words.
func designQuote(t *testing.T, fs *fakeService) string {
	t.Helper()
	m, ok := fs.find("a1", "INBOX", 1)
	if !ok {
		t.Fatal("no message from Maya")
	}
	return locales.OnWrote(quoteWords().FormatDate(m.Date), "Maya Chen") +
		"\n> Hi Ann,\n>\n> I pulled the latest frames into Figma. The onboarding flow is on page 3.\n>\n> Maya"
}

// The signature goes where the account's switches and the compose
// settings put it: below the room to write in a new message, between the
// room to write and the quote of a reply written above it, at the end of
// one written below it, and between the note and the message of a
// forward. Each switch left off leaves it out of its kind of message.
// The caret is where the user writes: what they type goes there.
func TestSignaturePlacement(t *testing.T) {
	a, fs := composeApp(t)
	main := ui.NewTester(a.view, 1220, 800)
	quote := designQuote(t, fs)

	// A new message. The keyboard starts in To; Tab takes it on to the
	// text, where the caret is above the signature.
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if cs.body != "\n\n"+signed || !tt.Focused(locales.To()) {
		t.Fatalf("a new message = %q, To focused %v", cs.body, tt.Focused(locales.To()))
	}
	click(t, tt, locales.Subject())
	tt.Key(0, ui.KeyTab)
	tt.Type("Hello")
	if cs.body != "Hello\n\n"+signed {
		t.Errorf("typed into a new message: %q", cs.body)
	}

	// A reply written above the quote: the keyboard is in the text, above
	// the signature, which is above the quote.
	openDesign(t, main)
	click(t, main, locales.Reply())
	cs, tt = lastCompose(t, a)
	if want := "\n\n" + signed + "\n\n" + quote; cs.body != want || !tt.Focused(locales.Body()) {
		t.Fatalf("a reply =\n%q\nwant\n%q, the text focused %v", cs.body, want, tt.Focused(locales.Body()))
	}
	tt.Type("Thanks, Maya.")
	if want := "Thanks, Maya.\n\n" + signed + "\n\n" + quote; cs.body != want {
		t.Errorf("typed into a reply =\n%q\nwant\n%q", cs.body, want)
	}

	// Written below the quote, the signature ends the reply and the caret
	// is between them.
	set := a.composeSet
	set.Position = mail.ReplyBelow
	a.setComposeSettings(set)
	click(t, main, locales.Reply())
	cs, tt = lastCompose(t, a)
	if want := quote + "\n\n\n\n" + signed; cs.body != want {
		t.Fatalf("a reply below the quote =\n%q\nwant\n%q", cs.body, want)
	}
	tt.Type("Thanks, Maya.")
	if want := quote + "\n\nThanks, Maya.\n\n" + signed; cs.body != want {
		t.Errorf("typed into a reply below the quote =\n%q\nwant\n%q", cs.body, want)
	}

	// The message as it was, under its headers.
	set.Quote, set.Position = mail.QuoteHeader, mail.ReplyAbove
	a.setComposeSettings(set)
	click(t, main, locales.Reply())
	cs, _ = lastCompose(t, a)
	block := locales.QuoteOriginal() + "\n" + locales.From() + ": Maya Chen <maya@example.com>\n" + locales.QuoteDate() + ": "
	if !strings.HasPrefix(cs.body, "\n\n"+signed+"\n\n"+block) || !strings.HasSuffix(cs.body, "\n\n"+designBody) {
		t.Errorf("a reply quoting the original message = %q", cs.body)
	}

	// A forward: the note above the signature, then the message.
	click(t, main, locales.Forward())
	cs, tt = lastCompose(t, a)
	forwarded := locales.ForwardedHeader() + "\n" + locales.From() + ": Maya Chen <maya@example.com>\n" + locales.QuoteDate() + ": "
	if !strings.HasPrefix(cs.body, "\n\n"+signed+"\n\n"+forwarded) || !strings.HasSuffix(cs.body, "\n\n"+designBody) {
		t.Fatalf("a forward = %q", cs.body)
	}
	click(t, tt, locales.Subject())
	tt.Key(0, ui.KeyTab)
	tt.Type("FYI")
	if !strings.HasPrefix(cs.body, "FYI\n\n"+signed+"\n\n"+forwarded) {
		t.Errorf("typed into a forward: %q", cs.body)
	}

	// Each switch off leaves the signature out of its kind of message.
	for _, c := range []struct {
		name string
		sig  mail.Signature
		open func() *composeState
	}{
		{"new", mail.Signature{Text: annSignature.Text, Reply: true, Forward: true}, func() *composeState { return a.openCompose(nil) }},
		{"reply", mail.Signature{Text: annSignature.Text, New: true, Forward: true}, func() *composeState {
			click(t, main, locales.Reply())
			cs, _ := lastCompose(t, a)
			return cs
		}},
		{"forward", mail.Signature{Text: annSignature.Text, New: true, Reply: true}, func() *composeState {
			click(t, main, locales.Forward())
			cs, _ := lastCompose(t, a)
			return cs
		}},
	} {
		a.signatures["a1"] = c.sig
		if cs := c.open(); strings.Contains(cs.body, "-- \n") || strings.Contains(cs.body, "Research lead") {
			t.Errorf("%s with its switch off = %q", c.name, cs.body)
		}
	}
	a.signatures["a1"] = mail.Signature{Text: annSignature.Text, Reply: true, Forward: true}
	if cs := a.openCompose(nil); cs.body != "" {
		t.Errorf("a new message without its signature = %q", cs.body)
	}
}

// A forward carries the files of the message it passes on, copied by the
// engine, when the settings say so. Send waits while they are copied.
// Each that could not be copied says why, and the others stay attached
// and go with the message. A reply carries none, nor does a forward with
// the setting off.
func TestForwardCarriesTheOriginalsFiles(t *testing.T) {
	a, fs := composeApp(t)
	fs.forwardFail["notes.txt"] = mail.ErrNotFound
	fs.forwardFail["onboarding-flow.png"] = errors.New("connection reset by peer")
	main := ui.NewTester(a.view, 1220, 800)
	openDesign(t, main)
	q := queueJobs(a)
	click(t, main, locales.Forward())
	cs, ct := lastCompose(t, a)
	if !ct.HasText(locales.Attaching(3)) {
		t.Fatalf("the files on their way do not show: %q", ct.Texts())
	}
	typeIn(t, ct, locales.To(), "raj@example.com")
	click(t, ct, locales.Send())
	if cs.err != locales.AttachBusy() || len(fs.sent) != 0 {
		t.Fatalf("sent while the files were copied: err %q, sent %+v", cs.err, fs.sent)
	}
	q.drain(a)
	ct.Frame()
	if got := attachedNames(cs.attachments); got != "frames.pdf" {
		t.Fatalf("attached %q", got)
	}
	for _, text := range []string{"frames.pdf", locales.ForwardAttachMissing("notes.txt"),
		locales.ForwardAttachFailed("onboarding-flow.png", "connection reset by peer")} {
		if !ct.HasText(text) {
			t.Errorf("the forward lacks %q: %q", text, ct.Texts())
		}
	}
	if d := theDraftOf(t, fs, cs.draftID); attachedNames(d.Attachments) != "frames.pdf" || d.Subject != "Fwd: Design review — Thursday at 10" {
		t.Errorf("draft = %+v", d)
	}
	if !slices.Equal(fs.forwards, []string{"a1/INBOX/1"}) {
		t.Errorf("copied from %v", fs.forwards)
	}
	click(t, ct, locales.Send())
	if len(fs.sent) != 1 || attachedNames(fs.sent[0].Attachments) != "frames.pdf" {
		t.Fatalf("sent = %+v", fs.sent)
	}

	// Every file copied.
	clear(fs.forwardFail)
	click(t, main, locales.Forward())
	if cs, ct := lastCompose(t, a); attachedNames(cs.attachments) != "frames.pdf, onboarding-flow.png, notes.txt" || cs.err != "" {
		t.Errorf("attached %q, err %q: %q", attachedNames(cs.attachments), cs.err, ct.Texts())
	}

	// The message cannot be read at all: the forward is there without
	// its files, and says so.
	fs.forwardErr = errFake
	click(t, main, locales.Forward())
	if cs, ct := lastCompose(t, a); len(cs.attachments) != 0 || cs.attaching != 0 ||
		!ct.HasText(locales.ForwardAttachmentsFailed(errFake.Error())) || !strings.Contains(cs.body, designBody) {
		t.Errorf("a forward whose files cannot be read: %+v: %q", cs.attachments, ct.Texts())
	}
	fs.forwardErr = nil

	// A reply carries none, nor a forward with the setting off.
	copies := len(fs.forwards)
	click(t, main, locales.Reply())
	set := a.composeSet
	set.ForwardAttachments = false
	a.setComposeSettings(set)
	click(t, main, locales.Forward())
	if cs, _ := lastCompose(t, a); len(fs.forwards) != copies || len(cs.attachments) != 0 || cs.attaching != 0 {
		t.Errorf("copied %v, the forward has %+v", fs.forwards, cs.attachments)
	}
}

// At the windows' smallest sizes, in both appearances, compose has the
// From picker, the Bcc field and its actions inside the window, and the
// mail window the message waiting and its Undo beside the list.
func TestSendingAtMinimumSizeAndDark(t *testing.T) {
	inside := func(t *testing.T, name string, tt *ui.Tester, text string, w, h float32) {
		t.Helper()
		box, ok := tt.Find(text)
		if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > w || box.Y+box.H > h {
			t.Errorf("%s: %q is at %+v (found %v): %q", name, text, box, ok, tt.Texts())
		}
	}
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		a, fs, _ := delayApp(t)
		cs := a.openCompose(nil)
		tt := ui.NewTesterWith(cs.view, 520, 420, ui.TesterOptions{Dark: dark})
		typeIn(t, tt, locales.To(), "maya@example.com")
		click(t, tt, locales.ShowBcc())
		for _, text := range []string{ownLabel, locales.To(), locales.Bcc(), locales.Subject(), locales.Send(), locales.DiscardDraft()} {
			inside(t, name+" compose", tt, text, 520, 420)
		}
		main := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		fs.clock = main.Now
		undo := locales.UndoSendOf(locales.NoSubject())
		fs.onScheduled = func(context.Context, string) {
			main.Frame()
			// The notice is the list's, beside the sidebar.
			inside(t, name+" notice", main, locales.SendingIn(10), 222+384, 640)
			inside(t, name+" notice", main, undo, 222+384, 640)
			click(t, main, undo)
		}
		click(t, tt, locales.Send())
		if len(fs.sent) != 0 || len(openComposes(a)) != 1 {
			t.Errorf("%s: Undo at the smallest size: sent %+v", name, fs.sent)
		}
	}
}

// The words of writing and sending are in Simplified Chinese too, the
// quotes' among them.
func TestSendingStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	han := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff })
	}
	for name, text := range map[string]string{
		"showBcc": zh.ShowBcc(), "quoteDate": zh.QuoteDate(), "quoteOriginal": zh.QuoteOriginal(),
		"forwardAttachFailed": zh.ForwardAttachFailed("a.pdf", "x"), "forwardAttachMissing": zh.ForwardAttachMissing("a.pdf"),
		"forwardOriginalMissing": zh.ForwardOriginalMissing(), "forwardAttachmentsFailed": zh.ForwardAttachmentsFailed("x"),
		"sendingIn": zh.SendingIn(9), "undoSendOf": zh.UndoSendOf("plan"), "undoTooLate": zh.UndoTooLate(),
		"undoSendFailed": zh.UndoSendFailed("x"), "dismissSendNote": zh.DismissSendNote(),
		"waitingNotSent": zh.WaitingNotSent("plan", "x"), "sentAtStart": zh.SentAtStart(2), "notSentAtStart": zh.NotSentAtStart(2),
		"deliverAtStartFailed": zh.DeliverAtStartFailed("x"), "composeSection": zh.ComposeSection(), "sendDelay": zh.SendDelay(),
		"sendDelayDetail": zh.SendDelayDetail(), "sendDelayOff": zh.SendDelayOff(), "sendDelaySeconds": zh.SendDelaySeconds(10),
		"replyDefault": zh.ReplyDefault(), "replyDefaultSender": zh.ReplyDefaultSender(), "replyDefaultAll": zh.ReplyDefaultAll(),
		"replyDefaultDetail": zh.ReplyDefaultDetail(), "quoteStyle": zh.QuoteStyle(), "quoteStylePrefix": zh.QuoteStylePrefix(),
		"quoteStyleHeader": zh.QuoteStyleHeader(), "quoteStylePrefixDetail": zh.QuoteStylePrefixDetail(),
		"quoteStyleHeaderDetail": zh.QuoteStyleHeaderDetail(), "replyPosition": zh.ReplyPosition(),
		"replyAboveQuote": zh.ReplyAboveQuote(), "replyBelowQuote": zh.ReplyBelowQuote(), "forwardAttachments": zh.ForwardAttachments(),
		"forwardAttachmentsDetail": zh.ForwardAttachmentsDetail(), "composeFormat": zh.ComposeFormat(),
		"signatureSection": zh.SignatureSection(), "signatureText": zh.SignatureText(), "signaturePlaceholder": zh.SignaturePlaceholder(),
		"signatureDetail": zh.SignatureDetail(), "signatureNew": zh.SignatureNew(), "signatureReply": zh.SignatureReply(),
		"signatureForward": zh.SignatureForward(), "signatureSaveFailed": zh.SignatureSaveFailed("x"),
		"identitiesSection": zh.IdentitiesSection(), "identitiesDetail": zh.IdentitiesDetail(), "identityOwn": zh.IdentityOwn(),
		"addIdentity": zh.AddIdentity(), "identityName": zh.IdentityName(), "identityNamePlaceholder": zh.IdentityNamePlaceholder(),
		"identityEmail": zh.IdentityEmail(), "identitySave": zh.IdentitySave(), "identityCancel": zh.IdentityCancel(),
		"edit": zh.Edit(), "remove": zh.Remove(), "editIdentity": zh.EditIdentity("a@b.c"), "removeIdentity": zh.RemoveIdentity("a@b.c"),
		"identityNotAddress": zh.IdentityNotAddress(), "identityExists": zh.IdentityExists("a@b.c"),
		"identitySaveFailed": zh.IdentitySaveFailed("x"), "identityRemoveFailed": zh.IdentityRemoveFailed("x"),
	} {
		if !han(text) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.SendingIn(9); !strings.Contains(got, "9") {
		t.Errorf("sendingIn = %q", got)
	}
	en := locales.In("en")
	if got := en.SentAtStart(1); strings.Contains(got, "1") {
		t.Errorf("english singular = %q", got)
	}
	if got := en.NotSentAtStart(3); !strings.Contains(got, "3 messages") {
		t.Errorf("english plural = %q", got)
	}
}

// The first reply button and R answer as the settings say, and the button
// beside it and Shift+R the other way.
func TestReplyFollowsTheDefault(t *testing.T) {
	everyone := "Maya Chen <maya@example.com>, Lena Ortiz <lena@example.com>, Raj Patel <raj@example.com>"
	sender := "Maya Chen <maya@example.com>"
	for _, all := range []bool{false, true} {
		fs := inMessageView(composeService())
		first, second, byR, byShiftR := locales.Reply(), locales.ReplyAll(), sender, everyone
		if all {
			fs.compose.Reply = mail.ReplyAll
			first, second, byR, byShiftR = second, first, everyone, sender
		}
		a := openApp(fs)
		main := ui.NewTester(a.view, 1220, 800)
		openDesign(t, main)
		f, okF := main.Find(first)
		s, okS := main.Find(second)
		if !okF || !okS || f.X >= s.X {
			t.Errorf("all=%v: %q at %+v, %q at %+v", all, first, f, second, s)
		}
		main.Key(0, ui.KeyR)
		if cs, _ := lastCompose(t, a); cs.to != byR {
			t.Errorf("all=%v: R replies to %q", all, cs.to)
		}
		main.Key(ui.Shift, ui.KeyR)
		if cs, _ := lastCompose(t, a); cs.to != byShiftR {
			t.Errorf("all=%v: Shift+R replies to %q", all, cs.to)
		}
		click(t, main, second)
		if cs, _ := lastCompose(t, a); cs.to != byShiftR {
			t.Errorf("all=%v: the second button replies to %q", all, cs.to)
		}
	}

	// In conversation view, the toolbar's replies and each message's own
	// follow the setting too.
	fs := composeService()
	fs.compose.Reply = mail.ReplyAll
	a := openApp(fs)
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, "Design review — Thursday at 10")
	for _, pair := range [][2]string{
		{locales.ReplyAll(), locales.Reply()},
		{locales.ForMessage(locales.ReplyAll(), 1, 1), locales.ForMessage(locales.Reply(), 1, 1)},
	} {
		f, okF := main.Find(pair[0])
		s, okS := main.Find(pair[1])
		if !okF || !okS || f.X >= s.X {
			t.Errorf("conversation view: %q at %+v, %q at %+v", pair[0], f, pair[1], s)
		}
	}
	main.Key(0, ui.KeyR)
	if cs, _ := lastCompose(t, a); cs.to != everyone {
		t.Errorf("conversation view: R replies to %q", cs.to)
	}
}

// A reply drafted with AI goes where the user writes: above the quote by
// default, and below it, before the signature, when replies are written
// there.
func TestAIDraftGoesWhereTheReplyIsWritten(t *testing.T) {
	a, fs := composeApp(t)
	main := ui.NewTester(a.view, 1220, 800)
	quote := designQuote(t, fs)
	openDesign(t, main)
	click(t, main, locales.Reply())
	cs, _ := lastCompose(t, a)
	cs.insertDraft("Thursday works.")
	if want := "Thursday works.\n\n" + signed + "\n\n" + quote; cs.body != want {
		t.Errorf("drafted above the quote =\n%q\nwant\n%q", cs.body, want)
	}
	set := a.composeSet
	set.Position = mail.ReplyBelow
	a.setComposeSettings(set)
	click(t, main, locales.Reply())
	cs, _ = lastCompose(t, a)
	cs.insertDraft("Thursday works.")
	if want := quote + "\n\nThursday works.\n\n" + signed; cs.body != want {
		t.Errorf("drafted below the quote =\n%q\nwant\n%q", cs.body, want)
	}
	// Once the quote was edited, where it ended is not known: the draft
	// goes on top.
	click(t, main, locales.Reply())
	cs, _ = lastCompose(t, a)
	cs.body = strings.Replace(cs.body, "> Maya", "> M.", 1)
	cs.insertDraft("Thursday works.")
	if !strings.HasPrefix(cs.body, "Thursday works.\n\n") || !strings.HasSuffix(cs.body, "> M.\n\n\n\n"+signed) {
		t.Errorf("drafted into an edited quote = %q", cs.body)
	}
}
