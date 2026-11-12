package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// draftsApp is the sample app on a server that has a Drafts folder.
func draftsApp(t *testing.T) (*app, *fakeService) {
	t.Helper()
	fs := newFakeService()
	folders := append(testFolders(), mail.Folder{Name: "Drafts", SpecialUse: mail.SpecialDrafts})
	fs.addAccount(testAccount("a1", "Shu Mail", "me@example.com"), "secret", folders, testMessages("a1"))
	return openApp(fs), fs
}

// openApp starts an app on a service, as launching the program does.
func openApp(fs *fakeService) *app {
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	return a
}

// typeIn clicks a field and types at the end of what it holds. The click
// puts the caret where it lands, so Ctrl+End moves it to the end first.
func typeIn(t *testing.T, tt *ui.Tester, field, text string) {
	t.Helper()
	click(t, tt, field)
	tt.Key(ui.Ctrl, ui.KeyEnd)
	tt.Type(text)
}

// fieldValues is what the window's text fields hold, as assistive
// technology reads them: a tester's texts leave out what was typed.
func fieldValues(tt *ui.Tester) []string {
	var out []string
	for _, info := range tt.AccessAll() {
		if info.Value != "" {
			out = append(out, info.Value)
		}
	}
	return out
}

// writeDraft opens a compose window and types a message with a recipient
// still half typed.
func writeDraft(t *testing.T, a *app) (*composeState, *ui.Tester) {
	t.Helper()
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "Bob <bob@example.com>, carol@exa")
	typeIn(t, tt, locales.Cc(), "dave@")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	typeIn(t, tt, locales.Body(), "First thoughts, unfinished")
	return cs, tt
}

// theDraft is the single draft the fake service holds.
func theDraft(t *testing.T, fs *fakeService) mail.Draft {
	t.Helper()
	if len(fs.drafts) != 1 {
		t.Fatalf("drafts = %d, want one: %+v", len(fs.drafts), fs.drafts)
	}
	for _, d := range fs.drafts {
		return d
	}
	return mail.Draft{}
}

// openComposes counts the compose windows still open.
func openComposes(a *app) []*composeState {
	var out []*composeState
	for _, cs := range a.composes {
		if !cs.closed {
			out = append(out, cs)
		}
	}
	return out
}

// timers stands in for the app's timer hook: tests fire what was armed.
type timers struct{ armed []func() }

func (ti *timers) after(d time.Duration, fn func()) *time.Timer {
	ti.armed = append(ti.armed, fn)
	return nil
}

func (ti *timers) fire() {
	armed := ti.armed
	ti.armed = nil
	for _, fn := range armed {
		fn()
	}
}

// Typing saves the draft as it is typed, every field as written, and
// without asking the server anything.
func TestComposeSavesWhileTyping(t *testing.T) {
	a, fs := draftsApp(t)
	syncs := fs.draftSyncs
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if len(fs.drafts) != 0 || tt.HasText(locales.DraftSaved()) {
		t.Fatalf("an untouched window has a draft: %+v", fs.drafts)
	}
	typeIn(t, tt, locales.To(), "bob@exa")
	d := theDraft(t, fs)
	if d.To != "bob@exa" || d.AccountID != "a1" || d.ID != cs.draftID || d.ID == "" {
		t.Fatalf("draft after typing a recipient = %+v", d)
	}
	typeIn(t, tt, locales.Cc(), "dave@")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	typeIn(t, tt, locales.Body(), "First thoughts")
	d = theDraft(t, fs)
	if d.To != "bob@exa" || d.Cc != "dave@" || d.Subject != "Quarterly plan" || d.Body != "First thoughts" {
		t.Fatalf("draft = %+v", d)
	}
	if d.Revision < 4 || !d.Dirty() {
		t.Errorf("revision = %d, synced %d", d.Revision, d.SyncedRevision)
	}
	if !tt.HasText(locales.DraftSaved()) {
		t.Errorf("no saved status: %q", tt.Texts())
	}
	// Saving is local: nothing was uploaded for it.
	if fs.draftSyncs != syncs {
		t.Errorf("typing synced %d times", fs.draftSyncs-syncs)
	}
}

// Closing the window keeps the draft without asking, and opening it from
// the Drafts folder restores every field exactly, the half-typed
// recipients included.
func TestCloseAndReopenRestoresDraft(t *testing.T) {
	a, fs := draftsApp(t)
	asked := 0
	a.confirmDiscard = func(*mygo.Window, int) bool { asked++; return true }
	cs, _ := writeDraft(t, a)
	syncs := fs.draftSyncs
	cs.close()
	if asked != 0 {
		t.Error("closing asked for a confirmation")
	}
	d := theDraft(t, fs)
	if fs.draftSyncs != syncs+1 || d.Dirty() || d.ServerUID == 0 {
		t.Errorf("closing did not sync the draft: syncs %d, draft %+v", fs.draftSyncs-syncs, d)
	}

	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if a.folder != draftsFolder || len(a.all) != 1 || a.all[0].DraftID != d.ID {
		t.Fatalf("drafts folder = %q, rows %+v", a.folder, a.all)
	}
	// The row shows whom the draft is to, its subject and its text; the
	// pane shows the draft and offers to go on with it.
	for _, text := range []string{"Bob", "Quarterly plan", "First thoughts, unfinished", locales.DraftNotSent(), locales.EditDraft()} {
		if !main.HasText(text) {
			t.Errorf("missing %q in %q", text, main.Texts())
		}
	}
	click(t, main, locales.EditDraft())
	open := openComposes(a)
	if len(open) != 1 || open[0] == cs {
		t.Fatalf("open compose windows = %d", len(open))
	}
	got := open[0]
	if got.to != "Bob <bob@example.com>, carol@exa" || got.cc != "dave@" || got.subject != "Quarterly plan" ||
		got.body != "First thoughts, unfinished" || got.draftID != d.ID || got.accountID != "a1" {
		t.Fatalf("restored window = to %q cc %q subject %q body %q draft %q", got.to, got.cc, got.subject, got.body, got.draftID)
	}
	tt := ui.NewTester(got.view, 720, 620)
	values := fieldValues(tt)
	for _, text := range []string{"Bob <bob@example.com>, carol@exa", "dave@", "Quarterly plan", "First thoughts, unfinished"} {
		if !slices.Contains(values, text) {
			t.Errorf("restored window does not show %q: %q", text, values)
		}
	}
	if !tt.HasText(locales.DraftSynced()) {
		t.Errorf("restored window's status: %q", tt.Texts())
	}
	// Going on writes the same draft, not a second one.
	typeIn(t, tt, locales.Subject(), " v2")
	if d := theDraft(t, fs); d.Subject != "Quarterly plan v2" || !d.Dirty() {
		t.Errorf("draft after editing the restored window = %+v", d)
	}
	if !tt.HasText(locales.DraftSaved()) {
		t.Errorf("status after an edit: %q", tt.Texts())
	}
}

// Quitting and launching again: a new app on the same data lists the
// draft and opens it as it was. A reply keeps what it answers.
func TestDraftSurvivesRestart(t *testing.T) {
	a, fs := draftsApp(t)
	main := ui.NewTester(a.view, 1220, 800)
	fs.messages[listKey("a1", "INBOX")][0].MessageID = "review-1@example.com"
	fs.messages[listKey("a1", "INBOX")][0].References = []string{"root@example.com"}
	a.loadMessages(a.gen)
	main.Frame()
	click(t, main, "Design review — Thursday at 10")
	click(t, main, locales.Reply())
	reply := a.composes[0]
	quoted := reply.body
	if len(fs.drafts) != 0 {
		t.Fatalf("a reply nobody typed in is a draft already: %+v", fs.drafts)
	}
	tt := ui.NewTester(reply.view, 720, 620)
	typeIn(t, tt, locales.Cc(), "lena@exa")
	// The app quits with the window open: no close, no last save.

	b := openApp(fs)
	if len(b.composes) != 0 {
		t.Fatalf("composes after a restart = %d", len(b.composes))
	}
	restarted := ui.NewTester(b.view, 1220, 800)
	click(t, restarted, locales.Drafts())
	if len(b.all) != 1 || !restarted.HasText("Re: Design review — Thursday at 10") {
		t.Fatalf("drafts after a restart = %+v, texts %q", b.all, restarted.Texts())
	}
	// Enter on the row opens it, as a double click does.
	click(t, restarted, "Re: Design review — Thursday at 10")
	restarted.Key(0, ui.KeyEnter)
	if len(b.composes) != 1 {
		t.Fatalf("Enter opened %d compose windows", len(b.composes))
	}
	got := b.composes[0]
	if got.to != reply.to || got.cc != "lena@exa" || got.subject != reply.subject || got.body != quoted {
		t.Errorf("restored reply = to %q cc %q subject %q body %q", got.to, got.cc, got.subject, got.body)
	}
	if got.inReplyTo != "review-1@example.com" ||
		!slices.Equal(got.references, []string{"root@example.com", "review-1@example.com"}) {
		t.Errorf("reply linkage = %q %q", got.inReplyTo, got.references)
	}
	// Sending the restored reply still answers the message.
	got.to = "maya@example.com"
	got.send()
	if len(fs.sent) != 1 || fs.sent[0].InReplyTo != "review-1@example.com" || len(fs.sent[0].References) != 2 {
		t.Errorf("sent = %+v", fs.sent)
	}
}

// A draft left waiting by the last run goes up as the app starts and
// syncs the account, whatever folder it shows.
func TestStartupSyncsDrafts(t *testing.T) {
	a, fs := draftsApp(t)
	fs.draftSyncErr = errFake
	cs, _ := writeDraft(t, a)
	cs.close()
	if d := theDraft(t, fs); !d.Dirty() {
		t.Fatalf("draft before the restart = %+v", d)
	}
	fs.draftSyncErr = nil
	b := openApp(fs)
	if d := theDraft(t, fs); d.Dirty() || d.ServerUID == 0 {
		t.Fatalf("draft after the restart = %+v", d)
	}
	if b.folder == draftsFolder {
		t.Fatalf("folder = %q", b.folder)
	}
}

// A double click on a draft's row opens it.
func TestDoubleClickOpensDraft(t *testing.T) {
	a, fs := draftsApp(t)
	cs, _ := writeDraft(t, a)
	cs.close()
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, "Quarterly plan")
	if n := len(openComposes(a)); n != 0 {
		t.Fatalf("one click opened %d windows", n)
	}
	click(t, main, "Quarterly plan")
	open := openComposes(a)
	if len(open) != 1 || open[0].draftID != theDraft(t, fs).ID {
		t.Fatalf("a double click opened %d windows", len(open))
	}
	// In another folder a double click opens nothing: mail is read in
	// the pane.
	click(t, main, locales.Inbox())
	click(t, main, "Design review — Thursday at 10")
	click(t, main, "Design review — Thursday at 10")
	if n := len(openComposes(a)); n != 1 {
		t.Errorf("a double click in the inbox left %d windows", n)
	}
}

// Opening a draft that has a window already goes to that window: two
// editors of one draft would overwrite each other.
func TestOpeningDraftTwiceKeepsOneEditor(t *testing.T) {
	a, fs := draftsApp(t)
	cs, _ := writeDraft(t, a)
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	// The window that wrote it is still open.
	click(t, main, locales.EditDraft())
	if open := openComposes(a); len(open) != 1 || open[0] != cs {
		t.Fatalf("editing a draft whose window is open made %d windows", len(open))
	}
	cs.close()
	click(t, main, locales.EditDraft())
	click(t, main, locales.EditDraft())
	main.Key(0, ui.KeyEnter)
	open := openComposes(a)
	if len(open) != 1 {
		t.Fatalf("opening the draft three times made %d windows", len(open))
	}
	// Two requests to open it that were both under way, as a double
	// click makes them, end in the one window too.
	d := theDraft(t, fs)
	if again := a.resumeDraft(d); again != open[0] || len(openComposes(a)) != 1 {
		t.Fatalf("resuming an open draft made %d windows", len(openComposes(a)))
	}
}

// Discard asks first, deletes only on a yes, and deletes nothing when
// there is no way to ask.
func TestDiscardAsksAndDeletesOnConfirm(t *testing.T) {
	a, fs := draftsApp(t)
	cs, tt := writeDraft(t, a)
	id := cs.draftID

	// No dialog available: nothing may be deleted.
	click(t, tt, locales.DiscardDraft())
	if len(fs.drafts) != 1 || len(fs.discarded) != 0 || cs.closed {
		t.Fatalf("discarded without a confirmation: %+v", fs.discarded)
	}

	asked, answer, count := 0, false, 0
	a.confirmDiscard = func(parent *mygo.Window, n int) bool {
		asked++
		count = n
		return answer
	}
	click(t, tt, locales.DiscardDraft())
	if asked != 1 || count != 1 {
		t.Fatalf("asked %d times about %d drafts", asked, count)
	}
	if len(fs.drafts) != 1 || len(fs.discarded) != 0 || cs.closed {
		t.Fatal("a refused discard deleted the draft or closed the window")
	}
	// The window goes on saving after a no.
	typeIn(t, tt, locales.Subject(), "!")
	if d := theDraft(t, fs); d.Subject != "Quarterly plan!" {
		t.Errorf("draft after a refused discard = %+v", d)
	}

	answer = true
	click(t, tt, locales.DiscardDraft())
	if asked != 2 || len(fs.drafts) != 0 || !slices.Equal(fs.discarded, []string{id}) {
		t.Fatalf("confirmed discard: asked %d, drafts %+v, discarded %v", asked, fs.drafts, fs.discarded)
	}
	if !cs.closed {
		t.Error("the window stays open after its draft was discarded")
	}
	// The closing window does not save the draft back.
	if len(fs.drafts) != 0 {
		t.Errorf("the discarded draft came back: %+v", fs.drafts)
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 0 || !main.HasText(locales.EmptyFolder()) {
		t.Errorf("drafts after the discard = %+v", a.all)
	}
}

// In the Drafts folder, deleting a draft is discarding it, with the same
// confirmation; the actions meant for mail are off.
func TestDraftRowActions(t *testing.T) {
	a, fs := draftsApp(t)
	cs, _ := writeDraft(t, a)
	second := a.openCompose(nil)
	typeIn(t, ui.NewTester(second.view, 720, 620), locales.Subject(), "Second draft")
	second.close()
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 2 {
		t.Fatalf("drafts = %+v", a.all)
	}
	click(t, main, "Quarterly plan")

	// Archive, junk, star, read, reply, forward: none of them applies.
	for _, label := range []string{
		locales.ArchiveMessage(), locales.JunkMessage(), locales.StarMessage(),
		locales.Reply(), locales.ReplyAll(), locales.Forward(),
	} {
		info, ok := main.Access(label)
		if !ok || !info.Disabled {
			t.Errorf("%q on a draft: found %v, disabled %v", label, ok, info.Disabled)
		}
	}
	if _, ok := main.Access(locales.DeleteMessage()); ok {
		t.Error("a draft offers Delete message beside Discard draft")
	}
	// Their keys do nothing either.
	for _, key := range []ui.Key{ui.KeyE, ui.KeyS, ui.KeyU, ui.KeyR, ui.KeyF} {
		main.Key(0, key)
	}
	if len(fs.moved) != 0 || len(fs.flagCalls) != 0 || len(openComposes(a)) != 1 {
		t.Fatalf("a key acted on a draft: moved %v flags %v composes %d", fs.moved, fs.flagCalls, len(openComposes(a)))
	}

	asked, answer, count := 0, false, 0
	a.confirmDiscard = func(parent *mygo.Window, n int) bool {
		asked++
		count = n
		return answer
	}
	click(t, main, locales.DiscardDraft())
	if asked != 1 || len(fs.drafts) != 2 || len(fs.moved) != 0 {
		t.Fatalf("refused discard: asked %d, drafts %d, moved %v", asked, len(fs.drafts), fs.moved)
	}
	// The Delete key is the same action.
	click(t, main, "Quarterly plan")
	main.Key(0, ui.KeyDelete)
	if asked != 2 || len(fs.drafts) != 2 {
		t.Fatalf("Delete key: asked %d, drafts %d", asked, len(fs.drafts))
	}
	answer = true
	main.Key(0, ui.KeyDelete)
	if asked != 3 || count != 1 || len(fs.drafts) != 1 || len(fs.moved) != 0 {
		t.Fatalf("confirmed discard: asked %d about %d, drafts %d, moved %v", asked, count, len(fs.drafts), fs.moved)
	}
	// The row is gone, and so is the window that was editing the draft.
	// The choice moved to the draft that is left.
	if main.HasText("Quarterly plan") || len(a.all) != 1 {
		t.Errorf("the discarded draft's row remains: %+v", a.all)
	}
	if a.selected != 0 || !a.sel.Has(summaryKey(a.all[0])) || !main.HasText(locales.EditDraft()) {
		t.Errorf("choice after the discard = %d, texts %q", a.selected, main.Texts())
	}
	if !cs.closed {
		t.Error("the discarded draft's window stays open")
	}
	if len(fs.drafts) != 1 {
		t.Errorf("the closing window saved the draft back: %+v", fs.drafts)
	}
}

// Several drafts chosen together are discarded after one question.
func TestDiscardSeveralDrafts(t *testing.T) {
	a, fs := draftsApp(t)
	for _, subject := range []string{"First draft", "Second draft", "Third draft"} {
		cs := a.openCompose(nil)
		typeIn(t, ui.NewTester(cs.view, 720, 620), locales.Subject(), subject)
		cs.close()
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, "First draft")
	if err := main.ClickWith(ui.Cmd, "Third draft"); err != nil {
		t.Fatal(err)
	}
	asked, count := 0, 0
	a.confirmDiscard = func(parent *mygo.Window, n int) bool {
		asked++
		count = n
		return true
	}
	click(t, main, locales.DiscardDraft())
	if asked != 1 || count != 2 {
		t.Fatalf("asked %d times about %d drafts", asked, count)
	}
	d := theDraft(t, fs)
	if d.Subject != "Second draft" || len(a.all) != 1 {
		t.Errorf("left = %+v, rows %+v", d, a.all)
	}
}

// Sending a draft removes it everywhere: no row is left in Drafts, and
// the window's closing does not save it back.
func TestSendingDraftRemovesItsRow(t *testing.T) {
	a, fs := draftsApp(t)
	cs, tt := writeDraft(t, a)
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 1 {
		t.Fatalf("drafts = %+v", a.all)
	}
	// The half-typed addresses stop the send, and the draft stays.
	click(t, tt, locales.Send())
	if len(fs.sent) != 0 || cs.err == "" || len(fs.drafts) != 1 {
		t.Fatalf("sent %+v with an unfinished recipient, err %q", fs.sent, cs.err)
	}
	cs.to, cs.cc = "Bob <bob@example.com>", ""
	tt.Frame()
	id := cs.draftID
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || fs.sent[0].DraftID != id || fs.sent[0].Subject != "Quarterly plan" {
		t.Fatalf("sent = %+v", fs.sent)
	}
	if len(fs.drafts) != 0 {
		t.Fatalf("the sent draft remains: %+v", fs.drafts)
	}
	if !cs.closed || !cs.sent {
		t.Error("the window stays open after sending")
	}
	main.Frame()
	if len(a.all) != 0 || main.HasText("Quarterly plan") || !main.HasText(locales.EmptyFolder()) {
		t.Errorf("the sent draft's row remains: %+v, %q", a.all, main.Texts())
	}
	// Nothing saves or syncs the draft back afterwards.
	saves := fs.draftSaves
	cs.changed()
	cs.close()
	if fs.draftSaves != saves || len(fs.drafts) != 0 {
		t.Errorf("the sent draft was saved again: %+v", fs.drafts)
	}
}

// A send the server refuses leaves the draft, with what was about to go.
func TestFailedSendKeepsDraft(t *testing.T) {
	a, fs := draftsApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Body(), "Hello")
	fs.sendErr = errFake
	click(t, tt, locales.Send())
	if cs.closed || !strings.Contains(cs.err, errFake.Error()) {
		t.Fatalf("closed %v, err %q", cs.closed, cs.err)
	}
	if d := theDraft(t, fs); d.Body != "Hello" || d.To != "bob@example.com" {
		t.Fatalf("draft after a failed send = %+v", d)
	}
	// Typing goes on saving, and a later send takes the draft with it.
	typeIn(t, tt, locales.Body(), " again")
	if d := theDraft(t, fs); d.Body != "Hello again" {
		t.Fatalf("draft = %+v", d)
	}
	fs.sendErr = nil
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || len(fs.drafts) != 0 || !cs.closed {
		t.Errorf("sent %+v, drafts %+v, closed %v", fs.sent, fs.drafts, cs.closed)
	}
}

// A window nothing was written in leaves no draft, whichever way it
// closes; one emptied again drops the draft it had.
func TestEmptyComposeLeavesNoDraft(t *testing.T) {
	a, fs := draftsApp(t)
	asked := 0
	a.confirmDiscard = func(*mygo.Window, int) bool { asked++; return true }

	untouched := a.openCompose(nil)
	tt := ui.NewTester(untouched.view, 720, 620)
	click(t, tt, locales.To())
	click(t, tt, locales.Subject())
	untouched.close()

	// Discard on an empty window just closes it: there is nothing to ask
	// about.
	discarded := a.openCompose(nil)
	click(t, ui.NewTester(discarded.view, 720, 620), locales.DiscardDraft())
	if !discarded.closed || asked != 0 {
		t.Errorf("discarding an empty window: closed %v, asked %d", discarded.closed, asked)
	}

	// Spaces are not content.
	blank := a.openCompose(nil)
	typeIn(t, ui.NewTester(blank.view, 720, 620), locales.Subject(), "   ")
	blank.close()
	if len(fs.drafts) != 0 || fs.draftSaves != 0 {
		t.Fatalf("empty windows left drafts %+v after %d saves", fs.drafts, fs.draftSaves)
	}

	// Typed, then deleted again: the draft made meanwhile goes with the
	// window, here and on the server.
	emptied := a.openCompose(nil)
	et := ui.NewTester(emptied.view, 720, 620)
	typeIn(t, et, locales.Subject(), "x")
	if len(fs.drafts) != 1 {
		t.Fatalf("drafts after typing = %+v", fs.drafts)
	}
	id := emptied.draftID
	et.Key(0, ui.KeyBackspace)
	if emptied.subject != "" {
		t.Fatalf("subject = %q after deleting it", emptied.subject)
	}
	emptied.close()
	if len(fs.drafts) != 0 || !slices.Equal(fs.discarded, []string{id}) || asked != 0 {
		t.Fatalf("an emptied window left %+v, discarded %v, asked %d", fs.drafts, fs.discarded, asked)
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 0 {
		t.Errorf("rows left by empty windows: %+v", a.all)
	}
}

// The draft uploads once the typing pauses, and the status line says
// where the draft is.
func TestDraftStatusFollowsSync(t *testing.T) {
	a, fs := draftsApp(t)
	var ti timers
	a.after = ti.after
	syncs := fs.draftSyncs
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.Subject(), "Plan")
	if len(ti.armed) == 0 {
		t.Fatal("typing armed no upload")
	}
	if !tt.HasText(locales.DraftSaved()) || tt.HasText(locales.DraftSynced()) {
		t.Fatalf("status before the pause: %q", tt.Texts())
	}
	// Each keystroke arms a timer and retires the one before it: when
	// they all come due, one sync runs.
	ti.fire()
	if fs.draftSyncs != syncs+1 {
		t.Fatalf("the pause ran %d syncs", fs.draftSyncs-syncs)
	}
	tt.Frame()
	if !tt.HasText(locales.DraftSynced()) {
		t.Fatalf("status after the upload: %q", tt.Texts())
	}

	// An edit while the server cannot be reached is saved all the same,
	// and says that it waits.
	fs.draftSyncErr = errFake
	typeIn(t, tt, locales.Subject(), " B")
	if d := theDraft(t, fs); d.Subject != "Plan B" || !d.Dirty() {
		t.Fatalf("offline edit = %+v", d)
	}
	ti.fire()
	tt.Frame()
	if !tt.HasText(locales.DraftOffline()) {
		t.Fatalf("status offline: %q", tt.Texts())
	}
	if a.err != "" {
		t.Errorf("a draft that waits raised the list's error banner: %q", a.err)
	}

	// The network comes back: the draft that waited goes up by itself.
	fs.draftSyncErr = nil
	syncs = fs.draftSyncs
	a.networkBack()
	tt.Frame()
	if d := theDraft(t, fs); d.Dirty() || d.Subject != "Plan B" {
		t.Fatalf("draft after the network came back = %+v", d)
	}
	if !tt.HasText(locales.DraftSynced()) {
		t.Fatalf("status back online: %q", tt.Texts())
	}
	// Nothing waits any more, so another network change syncs nothing.
	a.networkBack()
	if fs.draftSyncs != syncs+1 {
		t.Errorf("network changes ran %d syncs", fs.draftSyncs-syncs)
	}

	// The mail window's Sync uploads an edit too.
	typeIn(t, tt, locales.Subject(), "!")
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Sync())
	tt.Frame()
	if d := theDraft(t, fs); d.Dirty() || d.Subject != "Plan B!" {
		t.Fatalf("draft after the manual sync = %+v", d)
	}

	// A timer that comes due after its window closed does nothing.
	typeIn(t, tt, locales.Subject(), "?")
	cs.close()
	syncs = fs.draftSyncs
	ti.fire()
	if fs.draftSyncs != syncs {
		t.Errorf("a closed window's timer synced")
	}
}

// Without a Drafts folder on the server the drafts stay on this device:
// the folder is in the sidebar all the same, and the window says so.
func TestDraftsWithoutServerFolder(t *testing.T) {
	a, fs := sampleApp(t)
	main := ui.NewTester(a.view, 1220, 800)
	if !main.HasText(locales.Drafts()) {
		t.Fatalf("no Drafts entry without a server folder: %q", main.Texts())
	}
	cs, tt := writeDraft(t, a)
	a.syncDrafts("a1")
	tt.Frame()
	if !tt.HasText(locales.DraftLocalOnly()) {
		t.Fatalf("status = %q", tt.Texts())
	}
	if a.err != "" {
		t.Errorf("a server without a Drafts folder is reported as an error: %q", a.err)
	}
	cs.close()
	click(t, main, locales.Drafts())
	if len(a.all) != 1 || !main.HasText("Quarterly plan") {
		t.Fatalf("drafts = %+v", a.all)
	}
	if d := theDraft(t, fs); !d.Dirty() || d.Body != "First thoughts, unfinished" {
		t.Errorf("draft = %+v", d)
	}
	click(t, main, locales.EditDraft())
	if open := openComposes(a); len(open) != 1 || open[0].subject != "Quarterly plan" {
		t.Errorf("open = %d", len(open))
	}
}

// A conflict with another mail client is said in the window, and stays
// said until the user closes the notice.
func TestConflictNotice(t *testing.T) {
	a, fs := draftsApp(t)
	cs, tt := writeDraft(t, a)
	a.syncDrafts("a1")
	tt.Frame()
	if tt.HasText(locales.DraftConflict()) {
		t.Fatal("a conflict is shown before there is one")
	}
	// The next sync finds the server copy changed by another client and
	// the draft edited here: the engine keeps both and marks the draft.
	typeIn(t, tt, locales.Body(), ", edited here")
	fs.onDraftSync = func() {
		d := fs.drafts[cs.draftID]
		d.Conflict = true
		fs.drafts[cs.draftID] = d
	}
	a.syncDrafts("a1")
	fs.onDraftSync = nil
	tt.Frame()
	if !tt.HasText(locales.DraftConflict()) {
		t.Fatalf("no conflict notice: %q", tt.Texts())
	}
	if d := theDraft(t, fs); d.Body != "First thoughts, unfinished, edited here" {
		t.Errorf("the conflict changed the draft: %+v", d)
	}
	// The notice outlives the window: it shows again when the draft is
	// reopened.
	cs.close()
	reopened := a.resumeDraft(theDraft(t, fs))
	rt := ui.NewTester(reopened.view, 720, 620)
	if !rt.HasText(locales.DraftConflict()) {
		t.Fatalf("the notice did not survive reopening: %q", rt.Texts())
	}
	click(t, rt, locales.DraftConflictDismiss())
	if rt.HasText(locales.DraftConflict()) || theDraft(t, fs).Conflict {
		t.Errorf("the notice stays after its dismissal: %q", rt.Texts())
	}
}

// Another client replaced a draft whose window is open here, with no
// edits waiting. The sync lets the draft go, but the window still shows
// the text: it is kept as a draft of its own, and the window says so.
func TestDraftReplacedElsewhereWhileOpen(t *testing.T) {
	a, fs := draftsApp(t)
	cs, tt := writeDraft(t, a)
	a.syncDrafts("a1")
	tt.Frame()
	if !tt.HasText(locales.DraftSynced()) {
		t.Fatalf("status = %q", tt.Texts())
	}
	id := cs.draftID
	fs.onDraftSync = func() {
		delete(fs.drafts, id)
		fs.replaced[id] = true
		fs.onDraftSync = nil
		// The server is out of reach right after, so the kept draft
		// cannot go up yet.
		fs.draftSyncErr = errFake
	}
	a.syncDrafts("a1")
	tt.Frame()
	d := theDraft(t, fs)
	if d.ID != id || d.Body != "First thoughts, unfinished" || d.To != "Bob <bob@example.com>, carol@exa" {
		t.Fatalf("the window's text was lost: %+v", d)
	}
	if !d.Conflict || !d.Dirty() {
		t.Errorf("kept draft = %+v, want it marked and waiting", d)
	}
	if !tt.HasText(locales.DraftConflict()) {
		t.Errorf("no notice: %q", tt.Texts())
	}
	// It is a new message the server has not seen: the status of the
	// draft it replaces must not stand.
	if tt.HasText(locales.DraftSynced()) || !tt.HasText(locales.DraftOffline()) {
		t.Errorf("status of the kept draft = %q", tt.Texts())
	}
	fs.draftSyncErr = nil
	a.networkBack()
	tt.Frame()
	if d := theDraft(t, fs); d.Dirty() || !tt.HasText(locales.DraftSynced()) {
		t.Errorf("kept draft after the upload = %+v, status %q", d, tt.Texts())
	}
}

// A draft written in another mail client shows in Drafts with the local
// ones, and editing it takes it over.
func TestEditServerDraft(t *testing.T) {
	a, fs := draftsApp(t)
	fs.messages[listKey("a1", "Drafts")] = []mail.Summary{{
		UID: 41, Folder: "Drafts", Subject: "From the phone", Preview: "typed on the phone",
		MessageID: "phone-1@example.com",
		From:      mail.Address{Name: "Shu Mail", Email: "me@example.com"},
		To:        []mail.Address{{Name: "Maya Chen", Email: "maya@example.com"}},
		Date:      time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC), Seen: true,
	}}
	fs.bodies[bodyKey("a1", "Drafts", 41)] = mail.Message{BodyText: "typed on the phone", BodyCached: true}
	local, _ := writeDraft(t, a)
	local.close()
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	// The local draft leads; the server's follows.
	if len(a.all) != 2 || a.all[0].DraftID == "" || a.all[1].UID != 41 {
		t.Fatalf("drafts = %+v", a.all)
	}
	click(t, main, "From the phone")
	if !main.HasText(locales.DraftOnServer()) || !main.HasText("typed on the phone") {
		t.Fatalf("server draft pane: %q", main.Texts())
	}
	// A draft the server holds is mail still: it can be deleted as such.
	if info, ok := main.Access(locales.DeleteMessage()); !ok || info.Disabled {
		t.Errorf("Delete message on a server draft: found %v, disabled %v", ok, info.Disabled)
	}

	// One with attachments is refused, with the reason.
	fs.adoptErr = mail.ErrDraftHasAttachments
	click(t, main, locales.EditDraft())
	if len(openComposes(a)) != 0 || !main.HasText(locales.DraftHasAttachments()) {
		t.Fatalf("attachments: composes %d, texts %q", len(openComposes(a)), main.Texts())
	}
	fs.adoptErr = errFake
	click(t, main, locales.EditDraft())
	if !main.HasText(locales.DraftOpenFailed(errFake.Error())) || main.HasText(locales.DraftHasAttachments()) {
		t.Fatalf("failure not shown: %q", main.Texts())
	}
	// The reason belongs to that draft: another row does not show it.
	click(t, main, "Quarterly plan")
	if main.HasText(locales.DraftOpenFailed(errFake.Error())) {
		t.Errorf("another draft shows the failure: %q", main.Texts())
	}
	click(t, main, "From the phone")
	if a.err != "" {
		t.Errorf("the list's banner took the draft's failure: %q", a.err)
	}
	// A draft the server no longer has asks for a sync, which is what
	// takes such a row away.
	syncs := fs.draftSyncs
	fs.adoptErr = mail.ErrNotFound
	click(t, main, locales.EditDraft())
	if fs.draftSyncs != syncs+1 || !main.HasText(locales.DraftOpenFailed(mail.ErrNotFound.Error())) {
		t.Errorf("a vanished draft: %d syncs, texts %q", fs.draftSyncs-syncs, main.Texts())
	}
	fs.adoptErr = nil

	click(t, main, locales.EditDraft())
	open := openComposes(a)
	if len(open) != 1 {
		t.Fatalf("open = %d", len(open))
	}
	if main.HasText(locales.DraftOpenFailed(errFake.Error())) {
		t.Errorf("the failure outlived a successful open: %q", main.Texts())
	}
	cs := open[0]
	if cs.to != "Maya Chen <maya@example.com>" || cs.subject != "From the phone" || cs.body != "typed on the phone" ||
		cs.draftID == "" {
		t.Fatalf("adopted window = to %q subject %q body %q", cs.to, cs.subject, cs.body)
	}
	// It is one row still, a draft of this device now, and still the
	// one chosen.
	if len(a.all) != 2 || localDrafts(a.all) != 2 {
		t.Errorf("drafts after the adoption = %+v", a.all)
	}
	if a.selectedKey() != "draft/"+cs.draftID || !main.HasText(locales.DraftNotSent()) {
		t.Errorf("chosen after the adoption = %q, texts %q", a.selectedKey(), main.Texts())
	}
	// Adopting again goes to the same window.
	click(t, main, locales.EditDraft())
	if len(openComposes(a)) != 1 {
		t.Errorf("a second edit opened another window")
	}
}

// The Drafts row and the reading pane follow a compose window as it
// types, and the draft chosen stays chosen.
func TestDraftsFolderFollowsEdits(t *testing.T) {
	a, fs := draftsApp(t)
	other := a.openCompose(nil)
	typeIn(t, ui.NewTester(other.view, 720, 620), locales.Subject(), "Older draft")
	other.close()
	cs, tt := writeDraft(t, a)
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, "Older draft")
	typeIn(t, tt, locales.Body(), ", and more")
	typeIn(t, tt, locales.Subject(), " (v2)")
	main.Frame()
	if !main.HasText("Quarterly plan (v2)") || !main.HasText("First thoughts, unfinished, and more") {
		t.Fatalf("the list did not follow the edit: %q", main.Texts())
	}
	if a.selectedKey() != "draft/"+other.draftID {
		t.Errorf("the choice moved to %q", a.selectedKey())
	}
	if a.loading {
		t.Error("following an edit shows the list as loading")
	}
	// The pane shows the edited draft once it is chosen.
	click(t, main, "Quarterly plan (v2)")
	typeIn(t, tt, locales.Body(), ". Done")
	main.Frame()
	_, msg := a.current()
	if msg == nil || msg.BodyText != "First thoughts, unfinished, and more. Done" {
		t.Fatalf("pane body = %+v", msg)
	}
	// A read of the list that began before the user chose another row
	// keeps the row chosen since, not the one chosen as it began.
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	a.reloadDrafts("a1")
	click(t, main, "Older draft")
	a.run = func(f func()) { f() }
	for _, job := range jobs {
		job()
	}
	main.Frame()
	if a.selectedKey() != "draft/"+other.draftID {
		t.Errorf("a read that began earlier moved the choice to %q", a.selectedKey())
	}
	// With nothing chosen, an edit elsewhere does not choose a row for
	// the user.
	if err := main.ClickWith(ui.Cmd, "Quarterly plan (v2)"); err != nil {
		t.Fatal(err)
	}
	click(t, main, locales.ClearSelection())
	typeIn(t, tt, locales.Body(), "!")
	main.Frame()
	if a.selected != -1 || a.sel.Len() != 0 || !main.HasText(locales.NoMessageSelected()) {
		t.Errorf("an edit chose row %d of the list: %q", a.selected, main.Texts())
	}
	if d := fs.drafts[cs.draftID]; !strings.HasSuffix(d.Body, "Done!") {
		t.Errorf("draft = %+v", d)
	}
}

// Saves run in jobs of their own and may finish in any order. Only the
// newest writes, so the last thing typed is the last thing saved.
func TestSavesOutOfOrder(t *testing.T) {
	a, fs := draftsApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	click(t, tt, locales.Body())
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	for _, r := range "abc" {
		tt.Type(string(r))
	}
	if len(jobs) != 3 || len(fs.drafts) != 0 {
		t.Fatalf("jobs = %d, drafts %+v", len(jobs), fs.drafts)
	}
	// The oldest job runs last, as a slow goroutine would.
	for i := len(jobs) - 1; i >= 0; i-- {
		jobs[i]()
	}
	if d := theDraft(t, fs); d.Body != "abc" || fs.draftSaves != 1 {
		t.Fatalf("draft = %+v after %d saves", d, fs.draftSaves)
	}
	// A save still waiting when the draft is sent does not bring it back.
	jobs = nil
	cs.to = "bob@example.com"
	tt.Type("d")
	stale := jobs
	a.run = func(f func()) { f() }
	cs.send()
	for _, job := range stale {
		job()
	}
	if len(fs.sent) != 1 || fs.sent[0].Text != "abcd" || len(fs.drafts) != 0 {
		t.Fatalf("sent %+v, drafts %+v", fs.sent, fs.drafts)
	}
}

// A send pressed right after the last keystroke, and refused, leaves that
// keystroke in the draft, although its own save had not run yet.
func TestFailedSendKeepsTheLastKeystroke(t *testing.T) {
	a, fs := draftsApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Body(), "Hell")
	var waiting []func()
	a.run = func(f func()) { waiting = append(waiting, f) }
	tt.Type("o")
	a.run = func(f func()) { f() }
	if d := theDraft(t, fs); d.Body != "Hell" || len(waiting) != 1 {
		t.Fatalf("draft before the send = %+v, %d saves waiting", d, len(waiting))
	}
	fs.sendErr = errFake
	cs.send()
	for _, job := range waiting {
		job()
	}
	if cs.closed || cs.err == "" {
		t.Fatalf("closed %v, err %q", cs.closed, cs.err)
	}
	if d := theDraft(t, fs); d.Body != "Hello" {
		t.Fatalf("draft after the refused send = %+v", d)
	}
}

// A save that fails is said in the window, and the next one clears it.
func TestDraftSaveFailure(t *testing.T) {
	a, fs := draftsApp(t)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	fs.draftSaveErr = errors.New("disk full")
	typeIn(t, tt, locales.Subject(), "Plan")
	if !tt.HasText(locales.DraftSaveFailed("disk full")) || tt.HasText(locales.DraftSaved()) {
		t.Fatalf("status = %q", tt.Texts())
	}
	fs.draftSaveErr = nil
	tt.Type("s")
	if tt.HasText(locales.DraftSaveFailed("disk full")) || !tt.HasText(locales.DraftSaved()) {
		t.Fatalf("status after a good save = %q", tt.Texts())
	}
	if d := theDraft(t, fs); d.Subject != "Plans" {
		t.Errorf("draft = %+v", d)
	}
	// A discard that fails is reported and leaves the draft listed.
	a.confirmDiscard = func(*mygo.Window, int) bool { return true }
	fs.discardErr = errFake
	click(t, tt, locales.DiscardDraft())
	if a.err != locales.DraftDiscardFailed(errFake.Error()) || len(fs.drafts) != 1 {
		t.Errorf("err = %q, drafts %+v", a.err, fs.drafts)
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 1 || !main.HasText("Plans") {
		t.Errorf("the draft that could not be discarded is not listed: %+v", a.all)
	}
	// From the list it fails the same way, and the row comes back.
	click(t, main, "Plans")
	click(t, main, locales.DiscardDraft())
	if len(a.all) != 1 || !main.HasText("Plans") {
		t.Errorf("a failed discard removed the row: %+v", a.all)
	}
	// A draft without a subject or recipients still has a row to click.
	blank := a.openCompose(nil)
	typeIn(t, ui.NewTester(blank.view, 720, 620), locales.Body(), "only a body")
	blank.close()
	main.Frame()
	if !main.HasText(locales.NoSubject()) || !main.HasText(locales.NoRecipientsYet()) {
		t.Errorf("an untitled draft's row: %q", main.Texts())
	}
	click(t, main, locales.NoSubject())
	if _, msg := a.current(); msg == nil || msg.BodyText != "only a body" || !main.HasText(locales.NoSubject()) {
		t.Errorf("untitled draft in the pane: %+v", msg)
	}
}

// Text an AI draft writes into the window is saved like typed text.
func TestAIDraftIsSaved(t *testing.T) {
	a, fs, _, _ := aiApp(t, true)
	if err := a.ai.MarkReviewed(context.Background(), ai.FeatureDraft); err != nil {
		t.Fatal(err)
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, "Design review — Thursday at 10")
	click(t, main, locales.Reply())
	cs := a.composes[0]
	tt := ui.NewTester(cs.view, 720, 620)
	click(t, tt, locales.AiDraft())
	d := theDraft(t, fs)
	if !strings.HasPrefix(d.Body, testDraft) || d.Body != cs.body {
		t.Fatalf("draft body = %q", d.Body)
	}
	// A draft is not mail to summarize or triage.
	cs.close()
	click(t, main, locales.Drafts())
	if info, ok := main.Access(locales.Summarize()); !ok || !info.Disabled {
		t.Errorf("Summarize on a draft: found %v, disabled %v", ok, info.Disabled)
	}
	if info, ok := main.Access(locales.TriageInbox()); !ok || !info.Disabled {
		t.Errorf("Triage in Drafts: found %v, disabled %v", ok, info.Disabled)
	}
}

// A compose window belongs to the account it was opened under: its draft
// and its send stay there when the mail window switches accounts.
func TestDraftKeepsItsAccount(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "First", "first@example.com"), "secret", testFolders(), nil)
	fs.addAccount(testAccount("a2", "Second", "second@example.com"), "secret", testFolders(), nil)
	a := openApp(fs)
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	a.switchAccount("a2")
	typeIn(t, tt, locales.Subject(), "From the first account")
	if d := theDraft(t, fs); d.AccountID != "a1" {
		t.Fatalf("draft account = %q", d.AccountID)
	}
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	if len(a.all) != 0 {
		t.Errorf("the second account lists the first one's draft: %+v", a.all)
	}
}

// At the windows' minimum sizes, in both appearances, the draft controls
// are on screen and work.
func TestDraftsAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, fs := draftsApp(t)
		a.confirmDiscard = func(*mygo.Window, int) bool { return true }
		cs := a.openCompose(nil)
		tt := ui.NewTesterWith(cs.view, 520, 420, ui.TesterOptions{Dark: dark})
		typeIn(t, tt, locales.To(), "bob@example.com")
		typeIn(t, tt, locales.Subject(), "Quarterly plan")
		typeIn(t, tt, locales.Body(), "First thoughts")
		a.syncDrafts("a1")
		tt.Frame()
		for _, text := range []string{locales.DraftSynced(), locales.DiscardDraft(), locales.Send()} {
			if !tt.HasText(text) {
				t.Errorf("dark=%v: compose lacks %q: %q", dark, text, tt.Texts())
			}
		}
		d := fs.drafts[cs.draftID]
		d.Conflict = true
		fs.drafts[cs.draftID] = d
		a.syncDrafts("a1")
		tt.Frame()
		// With the notice showing, the actions under it are still inside
		// the window, where a click reaches them.
		for _, label := range []string{locales.DiscardDraft(), locales.Send(), locales.DraftSynced()} {
			box, ok := tt.Find(label)
			if !ok || box.H <= 0 || box.Y+box.H > 420 || box.X+box.W > 520 {
				t.Errorf("dark=%v: %q is at %+v in a 520x420 window (found %v)", dark, label, box, ok)
			}
		}
		asked := 0
		a.confirmDiscard = func(*mygo.Window, int) bool { asked++; return false }
		click(t, tt, locales.DiscardDraft())
		if asked != 1 {
			t.Errorf("dark=%v: Discard at the minimum size asked %d times", dark, asked)
		}
		a.confirmDiscard = func(*mygo.Window, int) bool { return true }
		click(t, tt, locales.DraftConflictDismiss())
		cs.close()

		main := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		click(t, main, locales.Drafts())
		for _, text := range []string{"Quarterly plan", locales.DraftNotSent(), locales.EditDraft()} {
			if !main.HasText(text) {
				t.Errorf("dark=%v: main window lacks %q: %q", dark, text, main.Texts())
			}
		}
		click(t, main, locales.EditDraft())
		if n := len(openComposes(a)); n != 1 {
			t.Fatalf("dark=%v: Edit draft opened %d windows", dark, n)
		}
		// The toolbar's discard is on screen, not in its overflow menu.
		click(t, main, locales.DiscardDraft())
		if len(fs.drafts) != 0 || len(a.all) != 0 {
			t.Errorf("dark=%v: discard at the minimum size left %+v", dark, fs.drafts)
		}
	}
}

// The draft strings exist in Simplified Chinese too.
func TestDraftStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"editDraft":           zh.EditDraft(),
		"discardDraft":        zh.DiscardDraft(),
		"discardDraftConfirm": zh.DiscardDraftConfirm(),
		"draftSaved":          zh.DraftSaved(),
		"draftSynced":         zh.DraftSynced(),
		"draftOffline":        zh.DraftOffline(),
		"draftLocalOnly":      zh.DraftLocalOnly(),
		"draftConflict":       zh.DraftConflict(),
		"draftNotSent":        zh.DraftNotSent(),
		"draftHasAttachments": zh.DraftHasAttachments(),
		"noSubject":           zh.NoSubject(),
	} {
		if text == "" || text == name || !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.DiscardDraftTitle(1); strings.Contains(got, "1") || got == "" {
		t.Errorf("singular title = %q", got)
	}
	if got := zh.DiscardDraftTitle(3); !strings.Contains(got, "3") {
		t.Errorf("plural title = %q", got)
	}
	if got := locales.In("en").DiscardDraftTitle(3); got != "Discard 3 drafts?" {
		t.Errorf("english plural title = %q", got)
	}
}
