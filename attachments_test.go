package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// userFile writes a file of the user's, to attach, and returns its path.
func userFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// largeFile makes a file of a size without writing it out: its bytes
// are zeros the file system need not store.
func largeFile(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return path
}

// attachedNames is the names of what a draft has attached, in order.
func attachedNames(list []mail.Attachment) string {
	names := make([]string, 0, len(list))
	for _, att := range list {
		names = append(names, att.Filename)
	}
	return strings.Join(names, ", ")
}

// jobQueue holds the app's jobs back until a test runs them, so that the
// test can look at the window while one is under way: a job run inline
// from a click is over before the click returns.
type jobQueue struct{ jobs []func() }

func queueJobs(a *app) *jobQueue {
	q := &jobQueue{}
	a.run = func(f func()) { q.jobs = append(q.jobs, f) }
	return q
}

// drain runs the jobs held back, and those they start, and then lets
// the app run its jobs inline again.
func (q *jobQueue) drain(a *app) {
	for len(q.jobs) > 0 {
		job := q.jobs[0]
		q.jobs = q.jobs[1:]
		job()
	}
	a.run = func(f func()) { f() }
}

// sendHeld clicks Send with the send held back, checks that the window
// shows a message on its way, and then lets the send run.
func sendHeld(t *testing.T, a *app, tt *ui.Tester) {
	t.Helper()
	q := queueJobs(a)
	click(t, tt, locales.Send())
	for _, text := range []string{locales.Sending(), locales.CancelSend()} {
		if !tt.HasText(text) {
			t.Errorf("a message on its way: the window lacks %q: %q", text, tt.Texts())
		}
	}
	q.drain(a)
	tt.Frame()
}

// composeWith opens a compose window and drops files on it.
func composeWith(t *testing.T, a *app, paths ...string) (*composeState, *ui.Tester) {
	t.Helper()
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if !tt.DropFiles(360, 300, paths...) {
		t.Fatal("the compose window did not take the dropped files")
	}
	return cs, tt
}

// The attach button opens a picker for several files, and each file
// chosen shows with its name, size and type. So does its shortcut. The
// files belong to the draft at once, though nothing was typed.
func TestAttachWithThePicker(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	report := userFile(t, dir, "report.pdf", make([]byte, 3<<10))
	plan := userFile(t, dir, "季度计划 第一版.txt", []byte("计划"))
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	if tt.HasText(locales.AttachmentsTotal(0, formatSize(0))) || len(fs.drafts) != 0 {
		t.Fatalf("an untouched window shows attachments: %q", tt.Texts())
	}

	// With no way to ask, the button does nothing and breaks nothing.
	click(t, tt, locales.AttachFiles())
	if len(fs.attachCalls) != 0 || cs.picking {
		t.Fatalf("attached without a picker: %v", fs.attachCalls)
	}

	asked := 0
	var parent *mygo.Window
	choice := []string{report, plan}
	a.pickFiles = func(win *mygo.Window) ([]string, error) {
		asked++
		parent = win
		return choice, nil
	}
	click(t, tt, locales.AttachFiles())
	if asked != 1 || parent != cs.win {
		t.Fatalf("the picker was asked %d times, for window %p", asked, parent)
	}
	d := theDraft(t, fs)
	if d.ID != cs.draftID || attachedNames(d.Attachments) != "report.pdf, 季度计划 第一版.txt" {
		t.Fatalf("draft = %+v", d)
	}
	for _, text := range []string{
		"report.pdf", "application/pdf · 3 KB",
		"季度计划 第一版.txt", "text/plain · 6 B",
		locales.AttachmentsTotal(2, formatSize(3<<10+6)),
		locales.DraftSaved(),
	} {
		if !tt.HasText(text) {
			t.Errorf("the window lacks %q: %q", text, tt.Texts())
		}
	}
	for _, name := range []string{"report.pdf", "季度计划 第一版.txt"} {
		if _, ok := tt.Find(locales.RemoveAttachment(name)); !ok {
			t.Errorf("no button to remove %s", name)
		}
	}

	// The shortcut asks too; choosing nothing attaches nothing.
	choice = nil
	tt.Key(ui.Cmd|ui.Shift, ui.KeyA)
	if asked != 2 || len(theDraft(t, fs).Attachments) != 2 {
		t.Errorf("the shortcut asked %d times and left %+v", asked-1, theDraft(t, fs).Attachments)
	}
	// A file chosen again is attached once.
	choice = []string{report}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyA)
	if got := theDraft(t, fs).Attachments; asked != 3 || len(got) != 2 || len(cs.attachments) != 2 {
		t.Errorf("the same file twice = %s", attachedNames(got))
	}
}

// Files dropped on the window are attached, and while they are dragged
// over it the window says it takes them.
func TestDropFilesAttaches(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	photo := userFile(t, dir, "photo.png", make([]byte, 2<<20))
	notes := userFile(t, dir, "notes.txt", []byte("notes"))
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.Subject(), "Photos")
	if tt.HasText(locales.DropToAttach()) {
		t.Fatal("the drop hint shows with nothing dragged")
	}
	// The copy starts in the pass that takes the drop: the frame before
	// it, which the window still shows, is the one with the files over
	// it.
	hinted := 0
	fs.onAttach = func(string) {
		if tt.HasText(locales.DropToAttach()) {
			hinted++
		}
	}
	if !tt.DropFiles(300, 200, photo, notes) {
		t.Fatal("the window did not take the files")
	}
	if hinted != 2 {
		t.Errorf("the window said it takes the files %d times of 2", hinted)
	}
	if tt.HasText(locales.DropToAttach()) {
		t.Error("the drop hint stays after the drop")
	}
	d := theDraft(t, fs)
	if attachedNames(d.Attachments) != "photo.png, notes.txt" || d.Subject != "Photos" {
		t.Fatalf("draft = %+v", d)
	}
	for _, text := range []string{"photo.png", "image/png · 2.0 MB", "notes.txt", "text/plain · 5 B"} {
		if !tt.HasText(text) {
			t.Errorf("the window lacks %q: %q", text, tt.Texts())
		}
	}

	// A folder among the files is refused in words, and the file beside
	// it is attached all the same.
	more := userFile(t, dir, "more.zip", []byte("zip"))
	if !tt.DropFiles(300, 200, dir, more) {
		t.Fatal("the window did not take the second drop")
	}
	if !tt.HasText(locales.AttachFolder(filepath.Base(dir))) {
		t.Errorf("no word about the folder: %q", tt.Texts())
	}
	if got := attachedNames(theDraft(t, fs).Attachments); got != "photo.png, notes.txt, more.zip" {
		t.Errorf("attachments after a folder = %s", got)
	}
	// The window is as usable as before.
	typeIn(t, tt, locales.Body(), "See attached")
	if d := theDraft(t, fs); d.Body != "See attached" || len(d.Attachments) != 3 {
		t.Errorf("draft after typing = %+v", d)
	}
	if cs.attaching != 0 {
		t.Errorf("attaching = %d after the copies", cs.attaching)
	}
}

// A file is removed with its button, or with Delete or Backspace on it,
// and the keyboard goes on to the next file, the one before, or the
// attach button: it is never left nowhere. What was removed is not sent.
func TestRemoveAttachment(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"one.txt", "two.txt", "three.txt", "four.txt"} {
		paths = append(paths, userFile(t, dir, name, []byte(name)))
	}
	cs, tt := composeWith(t, a, paths...)
	typeIn(t, tt, locales.To(), "bob@example.com")

	// By the pointer.
	click(t, tt, locales.RemoveAttachment("two.txt"))
	if got := attachedNames(theDraft(t, fs).Attachments); got != "one.txt, three.txt, four.txt" {
		t.Fatalf("after the button = %s", got)
	}
	if tt.HasText("two.txt") || !tt.Focused("three.txt") {
		t.Errorf("after the button the window shows two.txt (%v) and three.txt has the focus (%v)",
			tt.HasText("two.txt"), tt.Focused("three.txt"))
	}

	// By the keyboard: Delete on a file takes it off, and the next has
	// the focus.
	click(t, tt, "one.txt")
	if !tt.Focused("one.txt") {
		t.Fatal("a click on a file does not focus it")
	}
	tt.Key(0, ui.KeyDelete)
	if got := attachedNames(theDraft(t, fs).Attachments); got != "three.txt, four.txt" || !tt.Focused("three.txt") {
		t.Fatalf("after Delete = %s, three.txt focused %v", got, tt.Focused("three.txt"))
	}
	// Backspace on the last one: the one before it has the focus.
	click(t, tt, "four.txt")
	tt.Key(0, ui.KeyBackspace)
	if got := attachedNames(theDraft(t, fs).Attachments); got != "three.txt" || !tt.Focused("three.txt") {
		t.Fatalf("after Backspace = %s, three.txt focused %v", got, tt.Focused("three.txt"))
	}
	// The only one left: the focus goes to the attach button.
	tt.Key(0, ui.KeyDelete)
	if got := theDraft(t, fs).Attachments; len(got) != 0 || !tt.Focused(locales.AttachFiles()) {
		t.Fatalf("after the last = %s, attach button focused %v", attachedNames(got), tt.Focused(locales.AttachFiles()))
	}
	if tt.HasText(locales.AttachmentsTotal(1, formatSize(9))) || len(cs.attachments) != 0 {
		t.Errorf("the window still counts attachments: %q", tt.Texts())
	}

	// Backspace in the text is the text's: it removes no file.
	tt.DropFiles(360, 300, paths[0], paths[1])
	typeIn(t, tt, locales.Body(), "abc")
	tt.Key(0, ui.KeyBackspace)
	if d := theDraft(t, fs); d.Body != "ab" || len(d.Attachments) != 2 {
		t.Fatalf("Backspace in the body left %+v", d)
	}

	// A removal that fails says so and the file stays.
	fs.removeErr = errFake
	click(t, tt, locales.RemoveAttachment("one.txt"))
	if !tt.HasText(locales.AttachRemoveFailed("one.txt", errFake.Error())) || !tt.HasText("one.txt") ||
		len(theDraft(t, fs).Attachments) != 2 {
		t.Errorf("a failed removal: %q", tt.Texts())
	}
	fs.removeErr = nil

	// What goes out is what is attached now.
	click(t, tt, locales.RemoveAttachment("one.txt"))
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || attachedNames(fs.sent[0].Attachments) != "two.txt" {
		t.Fatalf("sent = %+v", fs.sent)
	}
	if !cs.closed || len(fs.drafts) != 0 {
		t.Errorf("after the send: closed %v, drafts %+v", cs.closed, fs.drafts)
	}
}

// Tab goes from the text through each attached file to the attach
// button and on to the other actions.
func TestAttachmentsFocusOrder(t *testing.T) {
	a, _ := draftsApp(t)
	dir := t.TempDir()
	_, tt := composeWith(t, a,
		userFile(t, dir, "one.txt", []byte("1")), userFile(t, dir, "two.txt", []byte("22")))
	click(t, tt, locales.Body())
	for _, want := range []string{"one.txt", "two.txt", locales.AttachFiles(), locales.DiscardDraft(), locales.Send()} {
		tt.Key(0, ui.KeyTab)
		if !tt.Focused(want) {
			t.Fatalf("Tab did not reach %q", want)
		}
	}
	// And back.
	for _, want := range []string{locales.DiscardDraft(), locales.AttachFiles(), "two.txt", "one.txt", locales.Body()} {
		tt.Key(ui.Shift, ui.KeyTab)
		if !tt.Focused(want) {
			t.Fatalf("Shift+Tab did not reach %q", want)
		}
	}
	// The attach button answers to the keyboard.
	asked := 0
	a.pickFiles = func(*mygo.Window) ([]string, error) { asked++; return nil, nil }
	for i := 0; i < 3; i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused(locales.AttachFiles()) {
		t.Fatal("the attach button is not where Tab left it")
	}
	tt.Key(0, ui.KeyEnter)
	if asked != 1 {
		t.Errorf("Enter on the attach button asked %d times", asked)
	}
}

// Past the size that servers often refuse, the window warns, in words of
// likelihood, and stops nothing.
func TestAttachmentSizeWarning(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	// 14 MB of attachments are under 20 MB as sent; one more megabyte
	// is over.
	cs, tt := composeWith(t, a, largeFile(t, dir, "video.bin", 14<<20))
	warning := func(total int64) string {
		return locales.AttachmentsWarning(formatSize(mail.EncodedSize(total)), "20 MB")
	}
	if mail.EncodedSize(14<<20) > mail.AttachmentWarnBytes || tt.HasText(warning(14<<20)) {
		t.Fatalf("a warning under the threshold: %q", tt.Texts())
	}
	if !tt.HasText(locales.AttachmentsTotal(1, "14.0 MB")) {
		t.Errorf("no total: %q", tt.Texts())
	}
	tt.DropFiles(360, 300, largeFile(t, dir, "more.bin", 1<<20))
	if mail.EncodedSize(15<<20) <= mail.AttachmentWarnBytes || !tt.HasText(warning(15<<20)) {
		t.Fatalf("no warning past the threshold: %q", tt.Texts())
	}
	if !tt.HasText(locales.AttachmentsTotal(2, "15.0 MB")) {
		t.Errorf("no total: %q", tt.Texts())
	}
	// It is a warning: the message is sent if the user says so.
	typeIn(t, tt, locales.To(), "bob@example.com")
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || len(fs.sent[0].Attachments) != 2 || !cs.closed {
		t.Fatalf("the warning stopped the send: %+v", fs.sent)
	}

	// Removing a file takes the warning away.
	cs, tt = composeWith(t, a, filepath.Join(dir, "video.bin"), filepath.Join(dir, "more.bin"))
	if !tt.HasText(warning(15 << 20)) {
		t.Fatalf("no warning in the second window: %q", tt.Texts())
	}
	click(t, tt, locales.RemoveAttachment("more.bin"))
	if tt.HasText(warning(15<<20)) || tt.HasText(warning(14<<20)) {
		t.Errorf("the warning stays under the threshold: %q", tt.Texts())
	}
	cs.close()
}

// A file that cannot be attached says why, in words for each reason, and
// the window goes on working.
func TestAttachErrorsKeepComposeUsable(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.Subject(), "Plan")
	huge := userFile(t, dir, "huge.bin", []byte("x"))

	fs.attachErr = mail.ErrAttachTooLarge
	tt.DropFiles(360, 300, huge)
	const limit = "100 MB"
	if !tt.HasText(locales.AttachTooLarge("huge.bin", limit)) || len(cs.attachments) != 0 {
		t.Fatalf("no word about the cap: %q", tt.Texts())
	}
	fs.attachErr = fmt.Errorf("open: %w", os.ErrPermission)
	tt.DropFiles(360, 300, huge)
	if !tt.HasText(locales.AttachDenied("huge.bin")) || tt.HasText(locales.AttachTooLarge("huge.bin", limit)) {
		t.Errorf("no word about the permission: %q", tt.Texts())
	}
	fs.attachErr = errFake
	tt.DropFiles(360, 300, huge)
	if !tt.HasText(locales.AttachFailed("huge.bin", errFake.Error())) {
		t.Errorf("no word about the failure: %q", tt.Texts())
	}
	fs.attachErr = nil
	// A file that is not there.
	tt.DropFiles(360, 300, filepath.Join(dir, "gone.txt"))
	if !tt.HasText(locales.AttachMissing("gone.txt")) {
		t.Errorf("no word about the missing file: %q", tt.Texts())
	}
	if d := theDraft(t, fs); len(d.Attachments) != 0 || d.Subject != "Plan" || cs.attaching != 0 {
		t.Fatalf("draft after the refusals = %+v, attaching %d", d, cs.attaching)
	}

	// The window still types, attaches and sends; the next attach
	// clears the old word.
	typeIn(t, tt, locales.To(), "bob@example.com")
	tt.DropFiles(360, 300, huge)
	if tt.HasText(locales.AttachMissing("gone.txt")) || !tt.HasText("huge.bin") {
		t.Errorf("after a good attach: %q", tt.Texts())
	}
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || attachedNames(fs.sent[0].Attachments) != "huge.bin" || fs.sent[0].Subject != "Plan" {
		t.Fatalf("sent = %+v", fs.sent)
	}
}

// The attachments are the draft's: they are there when its window is
// closed and opened again, and in an app started anew on the same data.
// The Drafts folder and its reading pane show that a draft has them.
func TestAttachmentsSurviveCloseReopenAndRestart(t *testing.T) {
	a, fs := draftsApp(t)
	if fs.sweeps != 1 {
		t.Errorf("starting the app swept the draft files %d times", fs.sweeps)
	}
	dir := t.TempDir()
	cs, tt := composeWith(t, a,
		userFile(t, dir, "report.pdf", make([]byte, 3<<10)), userFile(t, dir, "季度计划.txt", []byte("计划")))
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	cs.close()
	if d := theDraft(t, fs); attachedNames(d.Attachments) != "report.pdf, 季度计划.txt" || d.Dirty() {
		t.Fatalf("draft after closing = %+v", d)
	}

	// A window that held only files keeps its draft too.
	only, _ := composeWith(t, a, userFile(t, dir, "alone.zip", []byte("zip")))
	only.close()
	if len(fs.drafts) != 2 || len(fs.discarded) != 0 {
		t.Fatalf("a draft of files alone was not kept: %+v, discarded %v", fs.drafts, fs.discarded)
	}
	fs.removeDraft(only.draftID)

	shows := func(what string, a *app) {
		t.Helper()
		main := ui.NewTester(a.view, 1220, 800)
		click(t, main, locales.Drafts())
		if len(a.all) != 1 || !a.all[0].HasAttachments {
			t.Fatalf("%s: drafts = %+v", what, a.all)
		}
		// The row says so, and the pane lists them, to read only.
		if _, ok := main.Find(locales.HasAttachments()); !ok {
			t.Errorf("%s: the row does not show the draft has attachments", what)
		}
		for _, text := range []string{locales.Attachments(), "report.pdf", "application/pdf · 3 KB", "季度计划.txt"} {
			if !main.HasText(text) {
				t.Errorf("%s: the pane lacks %q: %q", what, text, main.Texts())
			}
		}
		if _, ok := main.Find(locales.SaveAs()); ok {
			t.Errorf("%s: the pane offers to save a draft's attachment", what)
		}
		if _, ok := main.Find(locales.RemoveAttachment("report.pdf")); ok {
			t.Errorf("%s: the pane offers to remove a draft's attachment", what)
		}
		click(t, main, locales.EditDraft())
		open := openComposes(a)
		if len(open) != 1 || attachedNames(open[0].attachments) != "report.pdf, 季度计划.txt" {
			t.Fatalf("%s: reopened window = %+v", what, open)
		}
		rt := ui.NewTester(open[0].view, 720, 620)
		for _, text := range []string{"report.pdf", "季度计划.txt", locales.AttachmentsTotal(2, formatSize(3<<10+6))} {
			if !rt.HasText(text) {
				t.Errorf("%s: the reopened window lacks %q: %q", what, text, rt.Texts())
			}
		}
		if _, ok := rt.Find(locales.RemoveAttachment("report.pdf")); !ok {
			t.Errorf("%s: the reopened window cannot remove a file", what)
		}
		open[0].close()
	}
	shows("reopened", a)
	// The program starts again on the same data.
	shows("restarted", openApp(fs))

	// A reopened draft takes and loses files like a new one.
	b := openApp(fs)
	main := ui.NewTester(b.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, locales.EditDraft())
	again := openComposes(b)[0]
	rt := ui.NewTester(again.view, 720, 620)
	click(t, rt, locales.RemoveAttachment("report.pdf"))
	rt.DropFiles(360, 300, userFile(t, dir, "new.png", []byte("png")))
	if got := attachedNames(theDraft(t, fs).Attachments); got != "季度计划.txt, new.png" {
		t.Errorf("after editing the reopened draft = %s", got)
	}
	main.Frame()
	if main.HasText("report.pdf") || !main.HasText("new.png") {
		t.Errorf("the pane did not follow the change: %q", main.Texts())
	}
}

// A message on its way shows how far it is and can be stopped. Stopped,
// the window is as it was, with the draft and what is attached, and says
// whether anything left.
func TestSendShowsProgressAndCancels(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	cs, tt := composeWith(t, a, userFile(t, dir, "report.pdf", make([]byte, 3<<10)))
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Subject(), "Report")
	const total = 4 << 20

	var saw []string
	look := func(what string, want ...string) {
		t.Helper()
		tt.Frame()
		for _, text := range want {
			if !tt.HasText(text) {
				saw = append(saw, fmt.Sprintf("%s: the window lacks %q: %q", what, text, tt.Texts()))
			}
		}
	}
	// The fake's send is the message on its way: it reports as the
	// engine does, and the test looks at the window meanwhile.
	fs.onSend = func(ctx context.Context, out mail.Outgoing) error {
		report := mail.SendProgress(ctx)
		if report == nil {
			saw = append(saw, "the send has no way to report progress")
			return nil
		}
		report(0, total)
		look("at the start", locales.SendProgress(formatSize(0), formatSize(total)), locales.CancelSend(), locales.Sending())
		report(1<<20, total)
		look("a quarter in", locales.SendProgress("1.0 MB", "4.0 MB"), "report.pdf")
		// Nothing can be attached to or taken off a message on its way.
		if err := tt.Click(locales.RemoveAttachment("report.pdf")); err == nil && len(cs.attachments) != 1 {
			saw = append(saw, "a file was removed while sending")
		}
		if tt.DropFiles(360, 300, filepath.Join(dir, "report.pdf")) || len(fs.attachCalls) != 1 {
			saw = append(saw, "a file was attached while sending")
		}
		report(total-1, total)
		look("all of it out", locales.SendFinishing())
		if err := tt.Click(locales.CancelSend()); err != nil {
			saw = append(saw, "no way to cancel: "+err.Error())
		}
		look("cancelling", locales.SendCancelling())
		// Cancelled: the engine gives up, and says what it knows.
		if ctx.Err() == nil {
			saw = append(saw, "Cancel did not cancel the send's context")
			return nil
		}
		return fmt.Errorf("smtp: %w", ctx.Err())
	}
	sendHeld(t, a, tt)
	for _, problem := range saw {
		t.Error(problem)
	}
	if len(fs.sent) != 0 || cs.closed || cs.sending || cs.sent {
		t.Fatalf("after the cancel: sent %d, closed %v, sending %v", len(fs.sent), cs.closed, cs.sending)
	}
	for _, text := range []string{locales.SendCancelled(), "report.pdf", locales.Send(), locales.DiscardDraft()} {
		if !tt.HasText(text) {
			t.Errorf("after the cancel the window lacks %q: %q", text, tt.Texts())
		}
	}
	if tt.HasText(locales.CancelSend()) || tt.HasText(locales.SendFinishing()) {
		t.Errorf("the progress stays after the cancel: %q", tt.Texts())
	}
	if d := theDraft(t, fs); attachedNames(d.Attachments) != "report.pdf" || d.Subject != "Report" {
		t.Fatalf("draft after the cancel = %+v", d)
	}

	// Cancelled once part of it had left: the outcome is not known, and
	// the window says that, not that nothing was sent.
	saw = nil
	fs.onSend = func(ctx context.Context, out mail.Outgoing) error {
		mail.SendProgress(ctx)(2<<20, total)
		look("half out", locales.SendProgress("2.0 MB", "4.0 MB"))
		if err := tt.Click(locales.CancelSend()); err != nil {
			saw = append(saw, "no way to cancel: "+err.Error())
		}
		return fmt.Errorf("%w: smtp: %w", mail.ErrDeliveryUnknown, ctx.Err())
	}
	sendHeld(t, a, tt)
	for _, problem := range saw {
		t.Error(problem)
	}
	if !tt.HasText(locales.SendCancelledUnknown()) || tt.HasText(locales.SendCancelled()) || cs.closed {
		t.Errorf("after a cancel on the way: %q", tt.Texts())
	}
	if d := theDraft(t, fs); len(d.Attachments) != 1 {
		t.Errorf("draft after a cancel on the way = %+v", d)
	}

	// The window still works: a file more, and the message goes.
	fs.onSend = nil
	tt.DropFiles(360, 300, userFile(t, dir, "more.txt", []byte("more")))
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || attachedNames(fs.sent[0].Attachments) != "report.pdf, more.txt" || !cs.closed {
		t.Fatalf("sent after the cancels = %+v", fs.sent)
	}
}

// A server that refuses the message is quoted, and the window keeps the
// draft and its attachments, to change and send again.
func TestSendRejectionKeepsTheDraft(t *testing.T) {
	a, fs := draftsApp(t)
	dir := t.TempDir()
	cs, tt := composeWith(t, a, userFile(t, dir, "big.zip", make([]byte, 5<<10)),
		userFile(t, dir, "small.txt", []byte("small")))
	typeIn(t, tt, locales.To(), "bob@example.com")
	typeIn(t, tt, locales.Subject(), "Files")

	const reply = "5.3.4 Message size exceeds fixed limit"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&mail.RejectedError{Code: 552, Reply: reply}, locales.SendRejectedSize(reply)},
		{fmt.Errorf("send: %w", &mail.RejectedError{Code: 550, Reply: "5.7.1 Not allowed"}), locales.SendRejected("5.7.1 Not allowed")},
		{&mail.SizeLimitError{Size: 30 << 20, Limit: 25 << 20}, locales.SendOverLimit("30.0 MB", "25.0 MB")},
		{fmt.Errorf("%w: connection reset", mail.ErrDeliveryUnknown),
			locales.SendInterrupted(fmt.Errorf("%w: connection reset", mail.ErrDeliveryUnknown).Error())},
		{mail.ErrAttachmentMissing, locales.SendAttachmentMissing()},
		{errFake, locales.SendFailed(errFake.Error())},
	} {
		fs.sendErr = tc.err
		click(t, tt, locales.Send())
		if !tt.HasText(tc.want) {
			t.Errorf("%v: the window lacks %q: %q", tc.err, tc.want, tt.Texts())
		}
		if cs.closed || cs.sending || len(fs.sent) != 0 {
			t.Fatalf("%v: closed %v, sending %v, sent %d", tc.err, cs.closed, cs.sending, len(fs.sent))
		}
		if d := theDraft(t, fs); attachedNames(d.Attachments) != "big.zip, small.txt" || d.Subject != "Files" {
			t.Fatalf("%v: draft = %+v", tc.err, d)
		}
		if !tt.HasText("big.zip") || !tt.HasText("small.txt") {
			t.Errorf("%v: the attachments are gone from the window: %q", tc.err, tt.Texts())
		}
	}
	// The user takes the large file off and sends again.
	fs.sendErr = nil
	click(t, tt, locales.RemoveAttachment("big.zip"))
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || attachedNames(fs.sent[0].Attachments) != "small.txt" || !cs.closed {
		t.Fatalf("sent after the refusal = %+v", fs.sent)
	}
}

// A draft with large attachments is not uploaded at every pause in the
// typing: it goes up when its window closes, and the status line says
// so. One with small attachments goes up at a pause like any draft.
func TestHeavyDraftUploadsWhenItsWindowCloses(t *testing.T) {
	a, fs := draftsApp(t)
	ti := &timers{}
	a.after = ti.after
	dir := t.TempDir()

	cs, tt := composeWith(t, a, userFile(t, dir, "small.txt", []byte("small")))
	typeIn(t, tt, locales.Subject(), "Light")
	ti.fire()
	tt.Frame()
	if d := fs.drafts[cs.draftID]; d.Dirty() || !tt.HasText(locales.DraftSynced()) {
		t.Fatalf("a draft with a small attachment waits after a pause: %+v, %q", d, tt.Texts())
	}
	cs.close()
	fs.removeDraft(cs.draftID)

	cs, tt = composeWith(t, a, largeFile(t, dir, "large.bin", mail.DraftAutoUploadLimit+1))
	typeIn(t, tt, locales.Subject(), "Heavy")
	full := fs.draftFullSyncs
	ti.fire()
	tt.Frame()
	d := fs.drafts[cs.draftID]
	if !d.Dirty() || fs.draftFullSyncs != full {
		t.Fatalf("a pause uploaded a draft with a large attachment: %+v", d)
	}
	if !tt.HasText(locales.DraftUploadsLater()) || tt.HasText(locales.DraftSynced()) || tt.HasText(locales.DraftSaved()) {
		t.Errorf("status of a waiting draft: %q", tt.Texts())
	}
	// More typing, more pauses: still nothing goes up.
	typeIn(t, tt, locales.Body(), "More")
	ti.fire()
	if fs.drafts[cs.draftID].Dirty() == false || fs.draftFullSyncs != full {
		t.Fatal("a second pause uploaded the draft")
	}
	cs.close()
	if d := fs.drafts[cs.draftID]; d.Dirty() || fs.draftFullSyncs == full {
		t.Fatalf("closing the window did not upload the draft: %+v", d)
	}

	// Reopened and edited, it waits again; a sync of the account takes
	// it.
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, locales.Drafts())
	click(t, main, locales.EditDraft())
	again := openComposes(a)[0]
	rt := ui.NewTester(again.view, 720, 620)
	if !rt.HasText(locales.DraftSynced()) {
		t.Errorf("status of the uploaded draft: %q", rt.Texts())
	}
	typeIn(t, rt, locales.Body(), " and more")
	ti.fire()
	rt.Frame()
	if !fs.drafts[cs.draftID].Dirty() || !rt.HasText(locales.DraftUploadsLater()) {
		t.Fatalf("an edit of the reopened draft: %+v, %q", fs.drafts[cs.draftID], rt.Texts())
	}
	a.refresh()
	rt.Frame()
	if fs.drafts[cs.draftID].Dirty() || !rt.HasText(locales.DraftSynced()) {
		t.Errorf("a sync of the account left the draft waiting: %+v, %q", fs.drafts[cs.draftID], rt.Texts())
	}
}

// The window shows many attachments without losing its actions: at its
// smallest size and in both appearances the files scroll, and Send,
// Discard and the attach button stay on screen and work.
func TestManyAttachmentsAtMinimumSizeAndDark(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, fs := draftsApp(t)
		dir := t.TempDir()
		var paths []string
		for i := 1; i <= 14; i++ {
			paths = append(paths, userFile(t, dir, fmt.Sprintf("file-%02d.txt", i), []byte("x")))
		}
		paths = append(paths, largeFile(t, dir, "large.bin", 16<<20))
		cs := a.openCompose(nil)
		tt := ui.NewTesterWith(cs.view, 520, 420, ui.TesterOptions{Dark: dark})
		typeIn(t, tt, locales.To(), "bob@example.com")
		typeIn(t, tt, locales.Subject(), "Many files")
		typeIn(t, tt, locales.Body(), "See attached")
		if !tt.DropFiles(260, 200, paths...) {
			t.Fatalf("dark=%v: the window did not take the files", dark)
		}
		if len(cs.attachments) != 15 {
			t.Fatalf("dark=%v: attachments = %d", dark, len(cs.attachments))
		}
		// A refusal on top: the notice takes room too.
		tt.DropFiles(260, 200, dir)
		inside := func(what string) {
			t.Helper()
			for _, label := range []string{
				locales.AttachFiles(), locales.DiscardDraft(), locales.Send(),
				locales.AttachmentsTotal(15, "16.0 MB"), locales.Body(), locales.Subject(),
			} {
				box, ok := tt.Find(label)
				if !ok || box.H <= 0 || box.Y < 0 || box.Y+box.H > 420 || box.X < 0 || box.X+box.W > 520 {
					t.Errorf("dark=%v, %s: %q is at %+v in a 520x420 window (found %v)", dark, what, label, box, ok)
				}
			}
		}
		inside("with many files, a warning and a refusal")
		if !tt.HasText(locales.AttachFolder(filepath.Base(dir))) {
			t.Errorf("dark=%v: no word about the folder: %q", dark, tt.Texts())
		}
		// The body keeps room to write in.
		if box, ok := tt.Find(locales.Body()); !ok || box.H < 40 {
			t.Errorf("dark=%v: the body is %v high", dark, box.H)
		}
		// The first files are on screen and can be removed there.
		click(t, tt, locales.RemoveAttachment("file-01.txt"))
		if len(theDraft(t, fs).Attachments) != 14 {
			t.Errorf("dark=%v: removing at the minimum size left %d", dark, len(theDraft(t, fs).Attachments))
		}
		// On its way, the progress and Cancel fit as well.
		var problems []string
		fs.onSend = func(ctx context.Context, out mail.Outgoing) error {
			mail.SendProgress(ctx)(1<<20, 22<<20)
			tt.Frame()
			for _, label := range []string{locales.CancelSend(), locales.Send(), locales.AttachFiles(),
				locales.SendProgress("1.0 MB", "22.0 MB")} {
				box, ok := tt.Find(label)
				if !ok || box.H <= 0 || box.Y+box.H > 420 || box.X < 0 || box.X+box.W > 520 {
					problems = append(problems, fmt.Sprintf("%q is at %+v while sending (found %v)", label, box, ok))
				}
			}
			return nil
		}
		sendHeld(t, a, tt)
		for _, problem := range problems {
			t.Errorf("dark=%v: %s", dark, problem)
		}
		if len(fs.sent) != 1 || len(fs.sent[0].Attachments) != 14 {
			t.Errorf("dark=%v: sent = %+v", dark, fs.sent)
		}
	}
}

// Attachments are no part of what an AI draft reads: the request for a
// reply is the same, byte for byte, with files attached and without.
func TestAIDraftNeverSeesAttachments(t *testing.T) {
	a, _, provider, _ := aiApp(t, true)
	dir := t.TempDir()
	secret := userFile(t, dir, "salary-figures.txt", []byte("the figures nobody else may see"))
	main := ui.NewTester(a.view, 1220, 800)
	click(t, main, "Design review — Thursday at 10")

	draft := func(attach bool) string {
		t.Helper()
		before := len(provider.texts)
		click(t, main, locales.Reply())
		cs := a.composes[len(a.composes)-1]
		tt := ui.NewTester(cs.view, 720, 620)
		if attach {
			if !tt.DropFiles(360, 300, secret) || len(cs.attachments) != 1 {
				t.Fatalf("the reply did not take the file: %+v", cs.attachments)
			}
		}
		click(t, tt, locales.AiGuidance())
		tt.Type("accept the new time")
		click(t, tt, locales.AiDraft())
		// The first use of the capability shows what it sends, and
		// waits for a yes.
		if tt.HasText(locales.AiReviewTitle()) {
			click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
		}
		if len(provider.texts) != before+1 {
			t.Fatalf("requests = %d, want %d", len(provider.texts), before+1)
		}
		if attach && len(cs.attachments) != 1 {
			t.Errorf("the draft lost its attachment: %+v", cs.attachments)
		}
		cs.close()
		return provider.texts[before]
	}
	without := draft(false)
	with := draft(true)
	if without == "" || !strings.Contains(without, "accept the new time") {
		t.Fatalf("the request = %q", without)
	}
	if with != without {
		t.Errorf("the request differs with a file attached:\n%s\n---\n%s", with, without)
	}
	for _, leak := range []string{"salary-figures", "the figures nobody", "text/plain"} {
		if strings.Contains(with, leak) {
			t.Errorf("the request holds %q", leak)
		}
	}
}

// The attachment strings exist in Simplified Chinese too, and the window
// shows them.
func TestAttachmentStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"attachFiles":           zh.AttachFiles(),
		"attachFilesTip":        zh.AttachFilesTip("⇧⌘A"),
		"removeAttachment":      zh.RemoveAttachment("报告.pdf"),
		"attachmentsTotal":      zh.AttachmentsTotal(2, "3.4 MB"),
		"attachmentsWarning":    zh.AttachmentsWarning("27.4 MB", "20.0 MB"),
		"dropToAttach":          zh.DropToAttach(),
		"attaching":             zh.Attaching(2),
		"attachBusy":            zh.AttachBusy(),
		"attachFolder":          zh.AttachFolder("照片"),
		"attachMissing":         zh.AttachMissing("a.txt"),
		"attachDenied":          zh.AttachDenied("a.txt"),
		"attachTooLarge":        zh.AttachTooLarge("a.mov", "100.0 MB"),
		"attachFailed":          zh.AttachFailed("a.txt", "x"),
		"attachRemoveFailed":    zh.AttachRemoveFailed("a.txt", "x"),
		"sendProgress":          zh.SendProgress("1.0 MB", "4.0 MB"),
		"sendFinishing":         zh.SendFinishing(),
		"cancelSend":            zh.CancelSend(),
		"sendCancelling":        zh.SendCancelling(),
		"sendCancelled":         zh.SendCancelled(),
		"sendCancelledUnknown":  zh.SendCancelledUnknown(),
		"sendInterrupted":       zh.SendInterrupted("x"),
		"sendRejected":          zh.SendRejected("550"),
		"sendRejectedSize":      zh.SendRejectedSize("552"),
		"sendOverLimit":         zh.SendOverLimit("30.0 MB", "25.0 MB"),
		"sendAttachmentMissing": zh.SendAttachmentMissing(),
		"draftUploadsLater":     zh.DraftUploadsLater(),
		"hasAttachments":        zh.HasAttachments(),
		"attach":                zh.Attach(),
	} {
		if text == "" || text == name || !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.AttachmentsTotal(3, "1 KB"); !strings.Contains(got, "3") || !strings.Contains(got, "1 KB") {
		t.Errorf("total = %q", got)
	}
	if got := locales.In("en").AttachmentsTotal(1, "5 B"); got != "1 attachment · 5 B" {
		t.Errorf("english singular total = %q", got)
	}
	if got := locales.In("en").AttachmentsTotal(2, "5 B"); got != "2 attachments · 5 B" {
		t.Errorf("english plural total = %q", got)
	}
}
