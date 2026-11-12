package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// delayApp is the window on Ann's account with the engine's compose
// settings, a send delay of ten seconds among them, and a tester of the
// mail window, whose clock the fake schedules by.
func delayApp(t *testing.T) (*app, *fakeService, *ui.Tester) {
	t.Helper()
	fs := inMessageView(composeService())
	fs.compose = mail.DefaultComposeSettings()
	a := openApp(fs)
	main := ui.NewTester(a.view, 1220, 800)
	fs.clock = main.Now
	return a, fs, main
}

// writePlan writes a new message to Maya from the lab's address, with a
// blind copy to Erin and a file attached.
func writePlan(t *testing.T, a *app) (*composeState, *ui.Tester) {
	t.Helper()
	cs := a.openCompose(nil)
	tt := ui.NewTester(cs.view, 720, 620)
	typeIn(t, tt, locales.To(), "Maya Chen <maya@example.com>")
	click(t, tt, locales.ShowBcc())
	tt.Type("Erin <erin@example.com>")
	typeIn(t, tt, locales.Subject(), "Quarterly plan")
	click(t, tt, ownLabel)
	click(t, tt, labLabel)
	click(t, tt, locales.Body())
	tt.Key(ui.Ctrl, ui.KeyHome)
	tt.Type("The plan is attached.")
	if !tt.DropFiles(360, 300, userFile(t, t.TempDir(), "plan.pdf", []byte("%PDF-1.7 the plan"))) {
		t.Fatal("the window did not take the file")
	}
	return cs, tt
}

// With a send delay, Send saves the draft with what is scheduled, has the
// engine schedule it, and closes the window. The mail window counts the
// delay down, with Undo, which the keyboard reaches; Undo takes the
// message back, nothing of it sent, and opens it again as it was written:
// its text, its Bcc, the address it goes from and its file. Sent again,
// it leaves once its delay is over.
func TestSendDelayCountsDownAndUndoReopens(t *testing.T) {
	a, fs, main := delayApp(t)
	cs, tt := writePlan(t, a)
	want := cs.snapshot()
	undo := locales.UndoSendOf("Quarterly plan")
	waited := false
	fs.onScheduled = func(ctx context.Context, id string) {
		waited = true
		s := fs.scheduledSend(id)
		out := s.Envelope
		if s.delay != 10*time.Second || out.Identity != "lab" || out.Subject != "Quarterly plan" ||
			addressesText(out.Bcc) != "Erin <erin@example.com>" || attachedNames(out.Attachments) != "plan.pdf" || out.DraftID != cs.draftID {
			t.Errorf("scheduled %+v after %v", out, s.delay)
		}
		// The draft says what was scheduled, which Undo gives back.
		if d := s.draft; d.ID != cs.draftID || d.Body != out.Text || d.To != want.To || d.Bcc != want.Bcc ||
			d.Subject != want.Subject || d.Identity != "lab" || attachedNames(d.Attachments) != "plan.pdf" {
			t.Errorf("the draft as the message was scheduled = %+v", d)
		}
		if !cs.closed || len(fs.sent) != 0 {
			t.Fatalf("waiting: the window closed %v, sent %+v", cs.closed, fs.sent)
		}
		main.Frame()
		for _, text := range []string{locales.SendingIn(10), "Quarterly plan", locales.Undo()} {
			if !main.HasText(text) {
				t.Errorf("the mail window lacks %q: %q", text, main.Texts())
			}
		}
		main.Advance(time.Second)
		if !main.HasText(locales.SendingIn(9)) {
			t.Errorf("a second later: %q", main.Texts())
		}
		main.Advance(4 * time.Second)
		if !main.HasText(locales.SendingIn(5)) || main.HasText(locales.SendingIn(9)) {
			t.Errorf("five seconds later: %q", main.Texts())
		}
		// From the list, the keyboard reaches Undo.
		click(t, main, "Design review — Thursday at 10")
		for i := 0; i < 8 && !main.Focused(undo); i++ {
			main.Key(ui.Shift, ui.KeyTab)
		}
		if !main.Focused(undo) {
			t.Fatal("Shift+Tab from the list does not reach Undo")
		}
		main.Key(0, ui.KeyEnter)
	}
	click(t, tt, locales.Send())
	if !waited {
		t.Fatal("the message did not wait out its delay")
	}
	main.Frame()
	if len(fs.sent) != 0 || fs.scheduled[0].State != mail.StateCancelled || len(a.waiting) != 0 ||
		main.HasText(locales.SendingIn(5)) || a.sendNote != "" {
		t.Fatalf("after Undo: sent %+v, state %s, waiting %d, note %q", fs.sent, fs.scheduled[0].State, len(a.waiting), a.sendNote)
	}
	open := openComposes(a)
	if len(open) != 1 {
		t.Fatalf("Undo opened %d windows", len(open))
	}
	back := open[0]
	if back.draftID != cs.draftID || back.to != want.To || back.bcc != want.Bcc || !back.showBcc ||
		back.subject != want.Subject || back.body != want.Body || back.identity != "lab" || attachedNames(back.attachments) != "plan.pdf" {
		t.Fatalf("the message back = to %q bcc %q subject %q body %q from %q files %q",
			back.to, back.bcc, back.subject, back.body, back.identity, attachedNames(back.attachments))
	}
	bt := ui.NewTester(back.view, 720, 620)
	if from, _ := bt.AccessByRole(ui.RolePopUpButton, locales.From()); from.Value != labLabel || !bt.HasText("plan.pdf") {
		t.Errorf("the window opened again shows from %q: %q", from.Value, bt.Texts())
	}

	// Sent again, it leaves once, after its delay.
	fs.onScheduled = nil
	click(t, bt, locales.Send())
	if len(fs.scheduled) != 2 || len(fs.sent) != 1 || fs.sent[0].Identity != "lab" ||
		addressesText(fs.sent[0].Bcc) != "Erin <erin@example.com>" || len(fs.drafts) != 0 || !back.closed {
		t.Fatalf("sent again: scheduled %d, sent %+v, drafts %+v", len(fs.scheduled), fs.sent, fs.drafts)
	}
	main.Frame()
	if len(a.waiting) != 0 || main.HasText(locales.Sending()) || a.sendNote != "" {
		t.Errorf("after it left: waiting %d, note %q: %q", len(a.waiting), a.sendNote, main.Texts())
	}
}

// The draft Undo gives back says what was scheduled, the last keystroke
// too when its save was still waiting as Send was pressed: that save
// gives way to the one Send makes.
func TestUndoKeepsTheLastKeystroke(t *testing.T) {
	a, fs, main := delayApp(t)
	cs, tt := writePlan(t, a)
	q := queueJobs(a)
	typeIn(t, tt, locales.Body(), " Page 3 changed.")
	click(t, tt, locales.Send())
	fs.onScheduled = func(ctx context.Context, id string) {
		if s := fs.scheduledSend(id); s.draft.Body != s.Envelope.Text || !strings.Contains(s.draft.Body, "Page 3 changed.") {
			t.Errorf("scheduled %q while the draft said %q", s.Envelope.Text, s.draft.Body)
		}
		a.run = func(f func()) { f() }
		main.Frame()
		click(t, main, locales.UndoSendOf("Quarterly plan"))
	}
	q.drain(a)
	open := openComposes(a)
	if len(open) != 1 || open[0].body != cs.body || len(fs.sent) != 0 {
		t.Fatalf("Undo gave back %+v, sent %+v", open, fs.sent)
	}
}

// Undo that comes once the message has begun to leave says so and takes
// nothing back: the message goes, and no window opens.
func TestUndoTooLate(t *testing.T) {
	a, fs, main := delayApp(t)
	_, tt := writePlan(t, a)
	fs.unsendErr = mail.ErrAlreadyLeft
	fs.onScheduled = func(context.Context, string) {
		main.Frame()
		click(t, main, locales.UndoSendOf("Quarterly plan"))
		if !main.HasText(locales.UndoTooLate()) || !main.HasText(locales.Sending()) || main.HasText(locales.SendingIn(10)) {
			t.Errorf("Undo too late: %q", main.Texts())
		}
	}
	click(t, tt, locales.Send())
	main.Frame()
	if len(fs.sent) != 1 || len(openComposes(a)) != 0 || len(a.waiting) != 0 || !main.HasText(locales.UndoTooLate()) {
		t.Fatalf("sent %+v, windows %d, waiting %d: %q", fs.sent, len(openComposes(a)), len(a.waiting), main.Texts())
	}
	click(t, main, locales.DismissSendNote())
	if main.HasText(locales.UndoTooLate()) {
		t.Error("the note stays after it was dismissed")
	}
}

// What became of a message sent with a delay is said in the mail window,
// its compose window being gone, as a send's result is: the copy Sent
// could not keep, or why it was not sent, which leaves it among the
// unsent messages to send again, and in Drafts.
func TestDelayedSendResults(t *testing.T) {
	// Sent, but the copy could not be saved.
	a, fs, main := delayApp(t)
	_, tt := writePlan(t, a)
	fs.sendWarn = errFake
	click(t, tt, locales.Send())
	main.Frame()
	if len(fs.sent) != 1 || len(fs.drafts) != 0 || !main.HasText(locales.SentCopyWarning()) {
		t.Errorf("sent %+v, drafts %+v: %q", fs.sent, fs.drafts, main.Texts())
	}

	// Refused.
	a, fs, main = delayApp(t)
	cs, tt := writePlan(t, a)
	fs.sendErr = &mail.RejectedError{Code: 550, Reply: "5.1.1 No such user"}
	click(t, tt, locales.Send())
	main.Frame()
	for _, text := range []string{locales.WaitingNotSent("Quarterly plan", locales.SendRejected("5.1.1 No such user")),
		locales.PendingSends(), locales.RetrySend()} {
		if !main.HasText(text) {
			t.Errorf("a refused message: the mail window lacks %q: %q", text, main.Texts())
		}
	}
	if _, ok := fs.drafts[cs.draftID]; !ok || len(fs.sent) != 0 {
		t.Errorf("a refused message: drafts %+v, sent %+v", fs.drafts, fs.sent)
	}
	fs.sendErr = nil
	click(t, main, locales.RetrySend())
	if len(fs.pending) != 0 {
		t.Errorf("Retry left %+v", fs.pending)
	}

	// The account had to sign in again meanwhile.
	a, fs, main = delayApp(t)
	_, tt = writePlan(t, a)
	fs.onScheduled = func(context.Context, string) { fs.needSignIn["a1"] = true }
	click(t, tt, locales.Send())
	main.Frame()
	if !a.needSignIn["a1"] || !main.HasText(locales.WaitingNotSent("Quarterly plan", locales.ActionNeedsSignIn())) {
		t.Errorf("a message whose account has to sign in again: %q", main.Texts())
	}
	noRawTexts(t, main, "a message whose account has to sign in again")
}

// A message the engine does not schedule stays in its window, saying why,
// as one Send refuses does, and nothing waits in the mail window; one of
// an account that has to sign in again offers the sign-in.
func TestScheduleRefusedKeepsTheWindow(t *testing.T) {
	a, fs, _ := delayApp(t)
	cs, tt := writePlan(t, a)
	fs.scheduleErr = errFake
	click(t, tt, locales.Send())
	if cs.closed || cs.sending || cs.err != locales.SendFailed(errFake.Error()) || len(a.waiting) != 0 || len(fs.scheduled) != 0 {
		t.Fatalf("refused: closed %v, sending %v, err %q, waiting %d", cs.closed, cs.sending, cs.err, len(a.waiting))
	}
	// The window takes edits, and sends.
	fs.scheduleErr = nil
	typeIn(t, tt, locales.Subject(), ", second draft")
	click(t, tt, locales.Send())
	if !cs.closed || len(fs.sent) != 1 || fs.sent[0].Subject != "Quarterly plan, second draft" {
		t.Fatalf("sent %+v", fs.sent)
	}

	cs, tt = writePlan(t, a)
	fs.needSignIn["a1"] = true
	click(t, tt, locales.Send())
	if cs.closed || !cs.needsSignIn || cs.err != locales.SendNeedsSignIn("Ann Lee") || len(a.waiting) != 0 {
		t.Errorf("an account that has to sign in again: closed %v, err %q", cs.closed, cs.err)
	}
}

// With no delay, chosen in the settings, Send sends at once as it did
// before: nothing is scheduled, the window shows the message on its way
// and closes once it is out, and a send that fails keeps it open.
func TestNoDelaySendsAsBefore(t *testing.T) {
	a, fs, main := delayApp(t)
	a.openSettings(settingsTabGeneral)
	main.SetSize(1220, 1800)
	click(t, main, locales.SendDelayOff())
	if fs.compose.SendDelay != 0 || a.composeSet.SendDelay != 0 {
		t.Fatalf("Off saved %+v", fs.compose)
	}
	click(t, main, locales.Done())
	cs, tt := writePlan(t, a)
	sendHeld(t, a, tt)
	if len(fs.scheduled) != 0 || len(fs.sent) != 1 || !cs.closed || len(a.waiting) != 0 {
		t.Fatalf("scheduled %d, sent %+v, closed %v", len(fs.scheduled), fs.sent, cs.closed)
	}
	if out := fs.sent[0]; out.Identity != "lab" || addressesText(out.Bcc) != "Erin <erin@example.com>" {
		t.Errorf("sent %+v", out)
	}
	cs, tt = writePlan(t, a)
	fs.sendErr = errFake
	click(t, tt, locales.Send())
	if cs.closed || !strings.Contains(cs.err, errFake.Error()) || len(fs.scheduled) != 0 {
		t.Errorf("a failed send: closed %v, err %q", cs.closed, cs.err)
	}
}

// The settings of writing and sending show the engine's defaults on a new
// install: ten seconds to undo a send, Reply to the sender, quotes with
// “>” and the reply above them, forwards with their attachments, and a
// word that mail is plain text. Each choice is saved as it is made, and
// compose follows it; one the store refuses says so. A start reads them.
func TestComposeSettingsDefaultsAndSaving(t *testing.T) {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Ann Lee", "ann@example.com"), "secret", testFolders(), testMessages("a1"))
	fs.compose = mail.DefaultComposeSettings()
	a := openApp(fs)
	a.openSettings(settingsTabGeneral)
	tt := ui.NewTester(a.view, 1220, 1800)
	chosen := func(label string) bool {
		t.Helper()
		info, ok := tt.AccessByRole(ui.RoleRadio, label)
		if !ok {
			t.Fatalf("no choice %q: %q", label, tt.Texts())
		}
		return info.Checked
	}
	for label, want := range map[string]bool{
		locales.SendDelayOff(): false, locales.SendDelaySeconds(5): false, locales.SendDelaySeconds(10): true,
		locales.SendDelaySeconds(20): false, locales.SendDelaySeconds(30): false,
		locales.ReplyDefaultSender(): true, locales.ReplyDefaultAll(): false,
		locales.QuoteStylePrefix(): true, locales.QuoteStyleHeader(): false,
		locales.ReplyAboveQuote(): true, locales.ReplyBelowQuote(): false,
	} {
		if chosen(label) != want {
			t.Errorf("%q chosen %v, want %v", label, !want, want)
		}
	}
	if !switchOn(t, tt, locales.ForwardAttachments()) {
		t.Error("forwards go without their attachments by default")
	}
	for _, text := range []string{locales.ComposeSection(), locales.SendDelayDetail(), locales.QuoteStylePrefixDetail(), locales.ComposeFormat()} {
		if !tt.HasText(text) {
			t.Errorf("the settings lack %q", text)
		}
	}

	click(t, tt, locales.SendDelaySeconds(30))
	click(t, tt, locales.ReplyDefaultAll())
	click(t, tt, locales.QuoteStyleHeader())
	click(t, tt, locales.ReplyBelowQuote())
	click(t, tt, locales.ForwardAttachments())
	want := mail.ComposeSettings{SendDelay: 30, Reply: mail.ReplyAll, Quote: mail.QuoteHeader, Position: mail.ReplyBelow}
	if fs.compose != want || len(fs.composeSaved) != 5 || a.composeSet != want {
		t.Fatalf("saved %+v in %d saves, compose writes with %+v", fs.compose, len(fs.composeSaved), a.composeSet)
	}
	if !tt.HasText(locales.QuoteStyleHeaderDetail()) || tt.HasText(locales.QuoteStylePrefixDetail()) {
		t.Errorf("the quote's line does not follow the choice: %q", tt.Texts())
	}
	fs.composeErr = errFake
	click(t, tt, locales.SendDelaySeconds(5))
	if !tt.HasText(locales.SettingsSaveFailed()) || fs.compose.SendDelay != 30 {
		t.Errorf("a save the store refused: %+v: %q", fs.compose, tt.Texts())
	}
	fs.composeErr = nil

	// Read as the app starts.
	b := openApp(fs)
	b.openSettings(settingsTabGeneral)
	tt = ui.NewTester(b.view, 1220, 1800)
	if b.composeSet != want || !chosen(locales.SendDelaySeconds(30)) || !chosen(locales.ReplyBelowQuote()) || switchOn(t, tt, locales.ForwardAttachments()) {
		t.Errorf("after a start: %+v", b.composeSet)
	}
}

// What an earlier run left waiting out its delay leaves as the app
// starts, once, and the mail window says what came of it: what was sent,
// and what could not be, which stays among the unsent messages.
func TestDeliverWaitingAtStartup(t *testing.T) {
	plan := mail.PendingSend{ID: "o1", AccountID: "a1", Envelope: mail.Outgoing{Subject: "Quarterly plan"}}
	notes := mail.PendingSend{ID: "o2", AccountID: "a1", Envelope: mail.Outgoing{Subject: "Notes"}}
	start := func(results []mail.ScheduledResult, err error) (*app, *fakeService, *ui.Tester) {
		fs := inMessageView(composeService())
		fs.delivered, fs.deliverErr = results, err
		for _, r := range results {
			if r.Err != nil {
				failed := r.Outbox
				failed.State = mail.StateFailed
				fs.pending = append(fs.pending, failed)
			}
		}
		a := openApp(fs)
		return a, fs, ui.NewTester(a.view, 1220, 800)
	}

	a, fs, main := start([]mail.ScheduledResult{{Outbox: plan, Result: mail.SendResult{Copied: true}}, {Outbox: notes, Err: errFake}}, nil)
	for _, text := range []string{locales.SentAtStart(1), locales.NotSentAtStart(1), locales.PendingSends()} {
		if !main.HasText(text) {
			t.Errorf("the mail window lacks %q: %q", text, main.Texts())
		}
	}
	a.start()
	if fs.deliverCalls != 1 {
		t.Errorf("asked to deliver %d times", fs.deliverCalls)
	}
	click(t, main, locales.DismissSendNote())
	if main.HasText(locales.SentAtStart(1)) {
		t.Error("the note stays after it was dismissed")
	}

	// Nothing waited: nothing is said.
	a, _, main = start(nil, nil)
	if a.sendNote != "" || main.HasText(locales.DismissSendNote()) {
		t.Errorf("with nothing waiting the window says %q", a.sendNote)
	}
	// Sent, but a copy could not be saved.
	_, _, main = start([]mail.ScheduledResult{{Outbox: plan, Result: mail.SendResult{Warning: errFake}}}, nil)
	if !main.HasText(locales.SentAtStart(1)) || !main.HasText(locales.SentCopyWarning()) {
		t.Errorf("a copy not saved: %q", main.Texts())
	}
	// The outbox could not be read.
	_, _, main = start(nil, errFake)
	if !main.HasText(locales.DeliverAtStartFailed(errFake.Error())) {
		t.Errorf("a failure to deliver: %q", main.Texts())
	}
}
