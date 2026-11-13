package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// convFolders are the sample folders with a Drafts folder of the server,
// and an inbox notifications can name.
func convFolders() []mail.Folder {
	folders := append(testFolders(), mail.Folder{Name: "Drafts", SpecialUse: mail.SpecialDrafts})
	folders[0].UIDValidity = 7
	return folders
}

var (
	lin   = mail.Address{Name: "Lin Wei", Email: "lin.wei@example.com"}
	ann   = mail.Address{Name: "Ann", Email: "ann@example.com"}
	noor  = mail.Address{Name: "Noor Haddad", Email: "noor@example.com"}
	priya = mail.Address{Name: "Priya Raman", Email: "priya@example.com"}
	ravi  = mail.Address{Name: "Ravi Kumar", Email: "ravi@example.com"}
)

// convMessages are three conversations of Ann's.
//
// The offsite (7): Lin's question in the inbox, Ann's answer in Sent and
// Lin's unread booking with the quote attached; a copy of the question in
// Archive, as Gmail's labels make, and, newer than all, a reply in Trash
// and a draft in Drafts.
//
// Lunch (8): Priya's invitation, Noor's unread and starred answer with
// the menu attached, and Noor's read follow-up, the newest.
//
// The numbers (9): one message of Ravi's.
func convMessages() []mail.Summary {
	now := time.Now()
	at := func(hours float64) time.Time { return now.Add(-time.Duration(hours * float64(time.Hour))) }
	to := func(list ...mail.Address) []mail.Address { return list }
	return []mail.Summary{
		{UID: 1, Folder: "INBOX", Thread: 7, MessageID: "venue-1@example.com", From: lin, To: to(ann),
			Subject: "Venue for the offsite", Preview: "Could you pick one of the two venues?", Date: at(5), Seen: true},
		{UID: 2, Folder: "Sent", Thread: 7, MessageID: "venue-2@example.com", InReplyTo: "venue-1@example.com", From: ann, To: to(lin),
			Subject: "Re: Venue for the offsite", Preview: "The one with the pergola.", Date: at(4), Seen: true},
		{UID: 3, Folder: "INBOX", Thread: 7, MessageID: "venue-3@example.com", InReplyTo: "venue-2@example.com", From: lin, To: to(ann),
			Subject: "Re: Venue for the offsite", Preview: "Booked it for Thursday.", Date: at(3), HasAttachments: true},
		{UID: 4, Folder: "Archive", Thread: 7, MessageID: "venue-1@example.com", From: lin, To: to(ann),
			Subject: "Venue for the offsite", Preview: "Could you pick one of the two venues?", Date: at(5), Seen: true},
		{UID: 5, Folder: "Trash", Thread: 7, MessageID: "venue-5@example.com", From: lin, To: to(ann),
			Subject: "Re: Venue for the offsite", Preview: "Ignore this one, wrong file.", Date: at(2), Seen: true},
		{UID: 6, Folder: "Drafts", Thread: 7, MessageID: "venue-6@example.com", From: ann, To: to(lin),
			Subject: "Re: Venue for the offsite", Preview: "Unfinished thanks", Date: at(1), Seen: true},
		{UID: 9, Folder: "INBOX", Thread: 8, MessageID: "lunch-1@example.com", From: priya, To: to(ann, noor),
			Subject: "Lunch on Friday?", Preview: "Shall we try the new place?", Date: at(8), Seen: true},
		{UID: 10, Folder: "INBOX", Thread: 8, MessageID: "lunch-2@example.com", From: noor, To: to(ann, priya),
			Subject: "Re: Lunch on Friday?", Preview: "Yes! Menu attached.", Date: at(7), Flagged: true, HasAttachments: true},
		{UID: 11, Folder: "INBOX", Thread: 8, MessageID: "lunch-3@example.com", From: noor, To: to(ann, priya),
			Subject: "Re: Lunch on Friday?", Preview: "See you at noon.", Date: at(6), Seen: true},
		{UID: 12, Folder: "INBOX", Thread: 9, MessageID: "numbers-1@example.com", From: ravi, To: to(ann),
			Subject: "Quarterly numbers", Preview: "The numbers are in.", Date: at(10), Seen: true},
	}
}

// convBodies are the texts of the offsite's messages.
var convBodies = map[string]string{
	"INBOX/1": "Hi Ann, could you pick one of the two venues for the offsite?",
	"Sent/2":  "The one with the pergola has the bigger room. Let's book it.",
	"INBOX/3": "Booked the lakeside room for Thursday. The quote is attached.",
	"Trash/5": "Ignore this one, wrong file.",
}

func convService() *fakeService {
	fs := newFakeService()
	fs.addAccount(testAccount("a1", "Ann", "ann@example.com"), "secret", convFolders(), convMessages())
	for _, f := range fs.folders["a1"] {
		fs.recount("a1", f.Name)
	}
	for key, text := range convBodies {
		folder, uid, _ := strings.Cut(key, "/")
		var n mail.UID
		fmt.Sscan(uid, &n)
		m, _ := fs.find("a1", folder, n)
		fs.bodies[bodyKey("a1", folder, n)] = mail.Message{Summary: m, BodyText: text, BodyCached: true}
	}
	return fs
}

// convApp is the window on convService, in conversation view as a new
// install is.
func convApp(t *testing.T) (*app, *fakeService) {
	t.Helper()
	fs := convService()
	a := newTestApp(fs)
	a.setLocale = func(string) {}
	a.start()
	if !a.threaded() {
		t.Fatal("the list does not show conversations")
	}
	return a, fs
}

func shownKeys(cv *convView) []string {
	var out []string
	for _, m := range cv.msgs {
		out = append(out, summaryKey(m))
	}
	return out
}

func openKeys(cv *convView) []string {
	var out []string
	for _, m := range cv.msgs {
		if cv.open[summaryKey(m)] {
			out = append(out, summaryKey(m))
		}
	}
	return out
}

func lastBulk(t *testing.T, fs *fakeService) bulkFlag {
	t.Helper()
	if len(fs.bulkFlags) == 0 {
		t.Fatal("no flags were changed in bulk")
	}
	return fs.bulkFlags[len(fs.bulkFlags)-1]
}

// editFake changes a message of the fake server, as another mail client
// or a sync would.
func editFake(fs *fakeService, folder string, uid mail.UID, change func(*mail.Summary)) {
	list := fs.messages[listKey("a1", folder)]
	for i := range list {
		if list[i].UID == uid {
			change(&list[i])
		}
	}
}

func count(texts []string, s string) int {
	n := 0
	for _, text := range texts {
		if text == s {
			n++
		}
	}
	return n
}

// A row is a conversation: its newest message in the folder, how many it
// has there, and whether any of those is unread, starred or has
// attachments, whatever its newest one is.
func TestConversationRows(t *testing.T) {
	a, _ := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if len(a.convs) != 3 || len(a.all) != 3 {
		t.Fatalf("rows = %d conversations, %d messages", len(a.convs), len(a.all))
	}
	venue, lunch, numbers := a.convs[0], a.convs[1], a.convs[2]
	if venue.Thread != 7 || venue.Count != 2 || venue.Latest.UID != 3 || !venue.HasAttachments || a.all[0].UID != 3 {
		t.Errorf("offsite row = %+v", venue)
	}
	if lunch.Thread != 8 || lunch.Count != 3 || lunch.Latest.UID != 11 {
		t.Errorf("lunch row = %+v", lunch)
	}
	if !lunch.Unread || !lunch.Flagged || !lunch.HasAttachments {
		t.Errorf("the lunch row does not add up its older messages: %+v", lunch)
	}
	if numbers.Count != 1 || numbers.Unread || numbers.Flagged || numbers.HasAttachments {
		t.Errorf("numbers row = %+v", numbers)
	}
	for _, text := range []string{"Re: Venue for the offsite", "Re: Lunch on Friday?", "Quarterly numbers",
		locales.MessageCount(2), locales.MessageCount(3), locales.HasAttachments(), locales.MailboxSummary(6, 1)} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	// The offsite was opened with the folder, and read: lunch is the one
	// unread conversation.
	if a.unread() != 1 || venue.Unread {
		t.Errorf("unread conversations = %d", a.unread())
	}
	// In Starred a row is made of the starred messages.
	click(t, tt, locales.Starred())
	if len(a.convs) != 1 || a.convs[0].Thread != 8 || a.convs[0].Count != 1 || a.convs[0].Latest.UID != 10 {
		t.Fatalf("starred rows = %+v", a.convs)
	}
	if !tt.HasText(locales.ConversationCount(1)) {
		t.Errorf("the count of conversations is missing: %q", tt.Texts())
	}
	// Drafts lists drafts one by one, without the switch.
	click(t, tt, locales.Drafts())
	if a.threaded() || tt.HasText(locales.GroupByConversation()) {
		t.Error("the Drafts entry lists conversations")
	}
}

// A conversation opens with its messages oldest first: each once, the
// Sent reply among them and tagged, without what Trash and Drafts hold;
// the newest expanded and the others, read, collapsed. A click or Enter
// and Space on a header expand and collapse.
func TestOpenConversation(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	cv := a.conv
	if cv == nil || cv.thread != 7 {
		t.Fatalf("open conversation = %+v", cv)
	}
	if got := shownKeys(cv); !reflect.DeepEqual(got, []string{"a1/INBOX/1", "a1/Sent/2", "a1/INBOX/3"}) {
		t.Fatalf("shown = %v", got)
	}
	if got := openKeys(cv); !reflect.DeepEqual(got, []string{"a1/INBOX/3"}) {
		t.Errorf("expanded = %v", got)
	}
	texts := tt.Texts()
	if !tt.HasText(convBodies["INBOX/3"]) || tt.HasText(convBodies["INBOX/1"]) || tt.HasText(convBodies["Sent/2"]) {
		t.Errorf("bodies on show: %q", texts)
	}
	for _, text := range []string{"Venue for the offsite", "Could you pick one of the two venues?", "The one with the pergola.",
		locales.ConversationScope(2, locales.Inbox()) + " · " + locales.ConversationElsewhere(1, locales.Sent())} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, texts)
		}
	}
	// The sidebar's Sent, and the tag of the reply.
	if n := count(texts, locales.Sent()); n != 2 {
		t.Errorf("%q shows %d times: %q", locales.Sent(), n, texts)
	}
	for _, left := range []string{"Ignore this one, wrong file.", "Unfinished thanks"} {
		if tt.HasText(left) {
			t.Errorf("%q is shown", left)
		}
	}

	flagged := len(fs.bulkFlags)
	first := locales.MessageHeading(1, 3, "Lin Wei")
	click(t, tt, first)
	if !cv.open["a1/INBOX/1"] || !tt.HasText(convBodies["INBOX/1"]) {
		t.Fatalf("a click did not expand the first message: %q", tt.Texts())
	}
	if len(fs.bulkFlags) != flagged {
		t.Error("expanding a read message changed flags")
	}
	if !tt.Focused(first) {
		t.Fatalf("the header does not have the focus")
	}
	tt.Key(0, ui.KeyEnter)
	if cv.open["a1/INBOX/1"] || tt.HasText(convBodies["INBOX/1"]) {
		t.Error("Enter did not collapse the message")
	}
	tt.Key(0, ui.KeySpace)
	if !cv.open["a1/INBOX/1"] || !tt.HasText(convBodies["INBOX/1"]) {
		t.Error("Space did not expand the message")
	}
	if a, ok := tt.Access(first); !ok || !a.Expanded {
		t.Errorf("the header does not say it is expanded: %+v", a)
	}
	click(t, tt, locales.MessageHeading(3, 3, "Lin Wei"))
	if cv.open["a1/INBOX/3"] {
		t.Error("a click did not collapse the newest message")
	}

	// In Archive, the copy there is the one shown, without a tag.
	click(t, tt, locales.Archive())
	if got := shownKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/Archive/4", "a1/Sent/2", "a1/INBOX/3"}) {
		t.Errorf("shown from Archive = %v", got)
	}
	// In Trash, the Trash message joins; Drafts still stays out.
	click(t, tt, locales.Trash())
	if got := shownKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/INBOX/1", "a1/Sent/2", "a1/INBOX/3", "a1/Trash/5"}) {
		t.Errorf("shown from Trash = %v", got)
	}
}

// Opening a conversation marks its expanded unread messages read, in one
// request, and its row, the folder and the Dock follow. Nothing else is
// marked read: not the collapsed ones, and not a message that arrives
// while it is open.
func TestReadingAConversationMarksItRead(t *testing.T) {
	fs := convService()
	w := watchApp(t, fs)
	a := w.a
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	if b := lastBulk(t, fs); b.flag != mail.FlagSeen || !b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/3"}) {
		t.Fatalf("opening the folder's first conversation = %+v", b)
	}
	unread := a.folderUnread("INBOX")
	click(t, tt, "Re: Lunch on Friday?")
	if got := openKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/INBOX/10", "a1/INBOX/11"}) {
		t.Fatalf("expanded = %v", got)
	}
	if b := lastBulk(t, fs); b.flag != mail.FlagSeen || !b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/10"}) {
		t.Errorf("marked = %+v", b)
	}
	if m, _ := fs.find("a1", "INBOX", 10); !m.Seen {
		t.Error("the server did not mark the message read")
	}
	if a.convs[1].Unread || a.unread() != 0 {
		t.Errorf("the row is still unread: %+v", a.convs[1])
	}
	if got := a.folderUnread("INBOX"); got != unread-1 || w.badge != unread-1 {
		t.Errorf("inbox unread = %d, badge %d, want %d", got, w.badge, unread-1)
	}

	// Marked unread from its own menu, the message stays open; collapsed
	// and expanded again, it is read again.
	click(t, tt, locales.ForMessage(locales.MoreActions(), 2, 3))
	click(t, tt, locales.MarkThisUnread())
	if len(fs.flagCalls) != 1 || fs.flagCalls[0] != string(mail.FlagSeen) || !a.convs[1].Unread {
		t.Fatalf("flag calls = %v, row %+v", fs.flagCalls, a.convs[1])
	}
	if !a.conv.open["a1/INBOX/10"] {
		t.Error("marking the message unread collapsed it")
	}
	bulk := len(fs.bulkFlags)
	heading := locales.MessageHeading(2, 3, "Noor Haddad")
	click(t, tt, heading)
	click(t, tt, heading)
	if len(fs.bulkFlags) != bulk+1 || a.convs[1].Unread {
		t.Errorf("expanding the unread message again: %+v", fs.bulkFlags[bulk:])
	}

	// New mail of the open conversation joins it collapsed, and unread.
	bulk = len(fs.bulkFlags)
	reply := newMail(13, "Priya Raman", "Re: Lunch on Friday?")
	reply.Thread = 8
	w.arrive(t, "a1", reply)
	tt.Frame()
	if got := shownKeys(a.conv); len(got) != 4 || got[3] != "a1/INBOX/13" {
		t.Fatalf("shown after new mail = %v", got)
	}
	if a.conv.open["a1/INBOX/13"] || len(fs.bulkFlags) != bulk {
		t.Errorf("new mail was opened: expanded %v, flags %+v", openKeys(a.conv), fs.bulkFlags[bulk:])
	}
	if !a.convs[0].Unread || a.convs[0].Count != 4 || a.selectedKey() != "thread/a1/8" {
		t.Errorf("row after new mail = %+v, chosen %q", a.convs[0], a.selectedKey())
	}
}

// Archiving a conversation moves exactly the messages its row stands for,
// not the reply in Sent nor the copy in Archive, and Undo brings them
// back. Delete and Junk do the same; a failed move brings the row back.
func TestConversationArchiveAndUndo(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.ArchiveConversations(1))
	if want := []string{"INBOX/1->Archive", "INBOX/3->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Fatalf("moved = %v, want %v", fs.movedRefs, want)
	}
	if a.rowOf(convRef{"a1", 7}) >= 0 || tt.HasText("Re: Venue for the offsite") {
		t.Errorf("the row stays: %q", tt.Texts())
	}
	if _, ok := fs.find("a1", "Sent", 2); !ok {
		t.Error("the reply in Sent moved")
	}
	if a.undo == nil || !tt.HasText(locales.ConversationsArchived(1)) {
		t.Fatalf("no undo offered: %q", tt.Texts())
	}
	click(t, tt, locales.Undo())
	if want := []string{"Archive->INBOX", "Archive->INBOX"}; !reflect.DeepEqual(fs.moveBacks, want) {
		t.Fatalf("moved back = %v", fs.moveBacks)
	}
	if i := a.rowOf(convRef{"a1", 7}); i < 0 || a.convs[i].Count != 2 {
		t.Fatalf("the row did not come back: %+v", a.convs)
	}
	if _, ok := fs.find("a1", "INBOX", 1); !ok {
		t.Error("the message is not back in the inbox under its UID")
	}

	fs.movedRefs = nil
	click(t, tt, "Re: Venue for the offsite")
	click(t, tt, locales.DeleteConversations(1))
	if want := []string{"INBOX/1->Trash", "INBOX/3->Trash"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("deleted = %v", fs.movedRefs)
	}
	if !tt.HasText(locales.ConversationsDeleted(1)) {
		t.Errorf("no undo of the delete: %q", tt.Texts())
	}
	fs.movedRefs = nil
	click(t, tt, "Re: Lunch on Friday?")
	tt.Key(0, ui.KeyE)
	if want := []string{"INBOX/9->Archive", "INBOX/10->Archive", "INBOX/11->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("E archived %v", fs.movedRefs)
	}

	fs.moveErr = errFake
	fs.movedRefs = nil
	click(t, tt, "Quarterly numbers")
	click(t, tt, locales.JunkConversations(1))
	if len(fs.movedRefs) != 1 || fs.movedRefs[0] != "INBOX/12->Junk" {
		t.Errorf("junked = %v", fs.movedRefs)
	}
	if a.rowOf(convRef{"a1", 9}) < 0 || !tt.HasText("Quarterly numbers") {
		t.Errorf("the failed move took the row: %q", tt.Texts())
	}
	if a.err == "" || a.undo != nil {
		t.Errorf("error %q, undo %+v", a.err, a.undo)
	}
}

// One message's own Archive moves it alone, and keeps the conversation
// open while its row has other messages; the last one takes the row.
func TestArchiveOneMessageKeepsTheConversation(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Re: Lunch on Friday?")
	click(t, tt, locales.ForMessage(locales.MoreActions(), 2, 3))
	click(t, tt, locales.ArchiveThisMessage())
	if want := []string{"INBOX/10->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Fatalf("moved = %v", fs.movedRefs)
	}
	if a.conv == nil || a.conv.thread != 8 || a.selectedKey() != "thread/a1/8" {
		t.Fatalf("the conversation closed: chosen %q", a.selectedKey())
	}
	if got := shownKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/INBOX/9", "a1/Archive/1010", "a1/INBOX/11"}) {
		t.Errorf("shown = %v", got)
	}
	if c := a.convs[a.rowOf(convRef{"a1", 8})]; c.Count != 2 || c.Flagged || c.HasAttachments {
		t.Errorf("row = %+v", c)
	}
	if a.undo == nil || a.undo.label != locales.Archived() {
		t.Fatalf("undo = %+v", a.undo)
	}
	click(t, tt, locales.Undo())
	if c := a.convs[a.rowOf(convRef{"a1", 8})]; c.Count != 3 {
		t.Errorf("after the undo the row has %d messages", c.Count)
	}

	click(t, tt, "Quarterly numbers")
	click(t, tt, locales.ForMessage(locales.MoreActions(), 1, 1))
	click(t, tt, locales.DeleteThisMessage())
	if a.rowOf(convRef{"a1", 9}) >= 0 {
		t.Error("the row of its one message stays")
	}
}

// Starring a conversation stars its newest message; unstarring unstars
// all its starred messages; read and unread change all of them. Each is
// one bulk request. A message's own star is that message's alone, and a
// failure shows the cache's state again.
func TestConversationStarAndRead(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.StarConversations(1))
	if b := lastBulk(t, fs); b.flag != mail.FlagFlagged || !b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/3"}) {
		t.Fatalf("star = %+v", b)
	}
	if !a.convs[0].Flagged || !tt.HasText(locales.UnstarConversations(1)) {
		t.Fatalf("not shown starred: %q", tt.Texts())
	}
	// Another mail client starred the question too, which a sync shows.
	editFake(fs, "INBOX", 1, func(m *mail.Summary) { m.Flagged = true })
	click(t, tt, locales.Sync())
	click(t, tt, locales.UnstarConversations(1))
	if b := lastBulk(t, fs); b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/1", "INBOX/3"}) {
		t.Errorf("unstar = %+v", b)
	}
	unread := a.folderUnread("INBOX")
	click(t, tt, locales.MarkConversationsUnread(1))
	if b := lastBulk(t, fs); b.flag != mail.FlagSeen || b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/1", "INBOX/3"}) {
		t.Errorf("mark unread = %+v", b)
	}
	if !a.convs[0].Unread || a.folderUnread("INBOX") != unread+2 {
		t.Errorf("row %+v, inbox unread %d", a.convs[0], a.folderUnread("INBOX"))
	}
	tt.Key(0, ui.KeyU)
	if b := lastBulk(t, fs); !b.on || !reflect.DeepEqual(b.refs, []string{"INBOX/1", "INBOX/3"}) {
		t.Errorf("U = %+v", b)
	}
	tt.Key(0, ui.KeyS)
	if b := lastBulk(t, fs); b.flag != mail.FlagFlagged || !b.on {
		t.Errorf("S = %+v", b)
	}

	// The reply in Sent, starred from its own menu: that message alone.
	click(t, tt, locales.MessageHeading(2, 3, "Ann"))
	bulk := len(fs.bulkFlags)
	click(t, tt, locales.ForMessage(locales.MoreActions(), 2, 3))
	click(t, tt, locales.StarThisMessage())
	if m, _ := fs.find("a1", "Sent", 2); !m.Flagged || len(fs.flagCalls) != 1 || len(fs.bulkFlags) != bulk {
		t.Errorf("one message's star: sent %+v, flag calls %v", m, fs.flagCalls)
	}

	fs.flagErr = errFake
	click(t, tt, locales.UnstarConversations(1))
	if a.err == "" || !a.convs[0].Flagged {
		t.Errorf("a refused unstar: error %q, row %+v", a.err, a.convs[0])
	}
}

// The toolbar's replies answer the newest message, not the newer copies
// in Trash and Drafts; a message's own Forward forwards that message, and
// a reply to the user's own message goes to the people it was sent to.
func TestConversationRepliesAndForward(t *testing.T) {
	a, _ := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.Reply())
	cs := a.composes[len(a.composes)-1]
	if cs.to != lin.String() || cs.subject != "Re: Venue for the offsite" || cs.inReplyTo != "venue-3@example.com" {
		t.Errorf("reply = to %q, subject %q, in reply to %q", cs.to, cs.subject, cs.inReplyTo)
	}
	if !strings.Contains(cs.body, "> "+convBodies["INBOX/3"]) {
		t.Errorf("reply quotes %q", cs.body)
	}
	tt.Key(0, ui.KeyR)
	if len(a.composes) != 2 || a.composes[1].inReplyTo != "venue-3@example.com" {
		t.Errorf("R answered %+v", a.composes[len(a.composes)-1].inReplyTo)
	}
	click(t, tt, locales.Forward())
	if fwd := a.composes[len(a.composes)-1]; fwd.subject != "Fwd: Re: Venue for the offsite" || !strings.Contains(fwd.body, convBodies["INBOX/3"]) {
		t.Errorf("forward = %q: %q", fwd.subject, fwd.body)
	}
	click(t, tt, locales.MessageHeading(1, 3, "Lin Wei"))
	click(t, tt, locales.ForMessage(locales.Forward(), 1, 3))
	if fwd := a.composes[len(a.composes)-1]; fwd.subject != "Fwd: Venue for the offsite" || !strings.Contains(fwd.body, convBodies["INBOX/1"]) {
		t.Errorf("forward of the first message = %q: %q", fwd.subject, fwd.body)
	}
	click(t, tt, locales.MessageHeading(2, 3, "Ann"))
	click(t, tt, locales.ForMessage(locales.Reply(), 2, 3))
	if own := a.composes[len(a.composes)-1]; own.to != lin.String() || own.inReplyTo != "venue-2@example.com" {
		t.Errorf("reply to the user's own message = to %q, in reply to %q", own.to, own.inReplyTo)
	}
}

// A search lists the conversations any message of which matches, in any
// folder: a word of the reply in Sent finds the inbox's conversation, and
// opening it expands the message found.
func TestConversationSearch(t *testing.T) {
	a, _ := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.SearchMailLabel())
	tt.Type("pergola")
	if len(a.convs) != 1 || a.convs[0].Thread != 7 {
		t.Fatalf("results = %+v", a.convs)
	}
	if got := a.convs[0].Matches; len(got) != 1 || got[0] != (mail.MessageRef{Folder: "Sent", UID: 2}) {
		t.Errorf("matches = %+v", got)
	}
	if !tt.HasText(locales.SearchResults()) || !tt.HasText(locales.ConversationCount(1)) {
		t.Errorf("search header: %q", tt.Texts())
	}
	click(t, tt, "Re: Venue for the offsite")
	if got := openKeys(a.conv); !reflect.DeepEqual(got, []string{"a1/Sent/2", "a1/INBOX/3"}) {
		t.Errorf("expanded = %v", got)
	}
	if !tt.HasText(convBodies["Sent/2"]) {
		t.Errorf("the message found is not open: %q", tt.Texts())
	}

	// A sender of one message only.
	a.query = "priya"
	a.search()
	tt.Frame()
	if len(a.convs) != 1 || a.convs[0].Thread != 8 || a.convs[0].Latest.UID != 11 {
		t.Fatalf("results for a sender = %+v", a.convs)
	}
	click(t, tt, "Re: Lunch on Friday?")
	if !a.conv.open["a1/INBOX/9"] {
		t.Errorf("Priya's message is not expanded: %v", openKeys(a.conv))
	}
	a.query = "nothing like this"
	a.search()
	tt.Frame()
	if !tt.HasText(locales.SearchNoResults("nothing like this")) {
		t.Errorf("no results: %q", tt.Texts())
	}
}

// The switch in the list header lists messages one by one, as before, and
// conversations again, keeping the mail chosen; the choice is stored and
// a new window starts with it.
func TestViewToggle(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Re: Lunch on Friday?")
	click(t, tt, locales.GroupByConversation())
	if a.threaded() || a.convs != nil || len(a.all) != 6 {
		t.Fatalf("message view: threaded %v, %d rows", a.threaded(), len(a.all))
	}
	if fs.settings.Conversations || len(fs.saved) != 1 {
		t.Errorf("stored = %+v", fs.saved)
	}
	if a.selectedKey() != "a1/INBOX/11" {
		t.Errorf("chosen = %q, want the conversation's newest message", a.selectedKey())
	}
	for _, text := range []string{locales.ArchiveMessage(), locales.MarkUnread(), "See you at noon."} {
		if !tt.HasText(text) {
			t.Errorf("message view lacks %q: %q", text, tt.Texts())
		}
	}
	if acc, ok := tt.AccessByRole(ui.RoleToggleButton, locales.GroupByConversation()); !ok || acc.Checked {
		t.Errorf("the switch = %+v, %v", acc, ok)
	}
	// Back, from Priya's message: its conversation is chosen, with the
	// message open.
	click(t, tt, "Lunch on Friday?")
	click(t, tt, locales.GroupByConversation())
	if !a.threaded() || a.selectedKey() != "thread/a1/8" || !a.conv.open["a1/INBOX/9"] {
		t.Fatalf("conversation view: chosen %q", a.selectedKey())
	}
	if !fs.settings.Conversations || len(fs.saved) != 2 {
		t.Errorf("stored = %+v", fs.saved)
	}
	click(t, tt, locales.GroupByConversation())
	b := newTestApp(fs)
	b.start()
	if b.threaded() || b.convToggle {
		t.Error("a new window forgot the view")
	}
}

// Cmd-click chooses several conversations, and the toolbar acts on all
// the messages their rows stand for.
func TestConversationMultiSelect(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.ClickWith(ui.Cmd, "Quarterly numbers"); err != nil {
		t.Fatal(err)
	}
	if a.sel.Len() != 2 || !tt.HasText(locales.ConversationsSelected(2)) {
		t.Fatalf("selection = %d: %q", a.sel.Len(), tt.Texts())
	}
	click(t, tt, locales.ArchiveConversations(2))
	if want := []string{"INBOX/1->Archive", "INBOX/3->Archive", "INBOX/12->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("moved = %v", fs.movedRefs)
	}
	if len(a.convs) != 1 || !tt.HasText(locales.ConversationsArchived(2)) {
		t.Errorf("rows %d: %q", len(a.convs), tt.Texts())
	}
	if a.sel.Len() != 1 || a.selectedKey() != "thread/a1/8" {
		t.Errorf("after the move: %d chosen, %q", a.sel.Len(), a.selectedKey())
	}
}

// A conversation-wide action acts on what the rows showed: mail the cache
// got after the list was drawn, before the window read it again, stays
// where it is, in the conversation on show as in another row chosen.
func TestArchiveSparesMailNotShown(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.ClickWith(ui.Cmd, "Quarterly numbers"); err != nil {
		t.Fatal(err)
	}
	if a.conv == nil || a.conv.thread != 9 || a.sel.Len() != 2 {
		t.Fatalf("open %+v, %d chosen", a.conv, a.sel.Len())
	}
	key := listKey("a1", "INBOX")
	for _, late := range []mail.Summary{
		{UID: 30, Folder: "INBOX", Thread: 7, From: lin, Subject: "Re: Venue for the offsite", Date: time.Now().Add(time.Minute)},
		{UID: 31, Folder: "INBOX", Thread: 9, From: ravi, Subject: "Re: Quarterly numbers", Date: time.Now().Add(time.Minute)},
	} {
		fs.messages[key] = append(fs.messages[key], late)
	}
	click(t, tt, locales.ArchiveConversations(2))
	if want := []string{"INBOX/1->Archive", "INBOX/3->Archive", "INBOX/12->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("moved = %v, want %v", fs.movedRefs, want)
	}
	for _, uid := range []mail.UID{30, 31} {
		if _, ok := fs.find("a1", "INBOX", uid); !ok {
			t.Errorf("INBOX/%d, which no row showed, moved", uid)
		}
	}
}

// When the conversation chosen merges into another, as a sync finding the
// message that links them does, the choice follows to the conversation
// the message is in now, which stays open as it was.
func TestSelectionFollowsAMerge(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Quarterly numbers")
	if a.selectedKey() != "thread/a1/9" {
		t.Fatalf("chosen = %q", a.selectedKey())
	}
	// The lunch conversation, not the first row, which a list read again
	// would choose anyway.
	editFake(fs, "INBOX", 12, func(m *mail.Summary) { m.Thread = 8 })
	click(t, tt, locales.Sync())
	if a.selectedKey() != "thread/a1/8" || a.sel.Len() != 1 || !a.sel.Has("thread/a1/8") {
		t.Fatalf("after the merge: chosen %q", a.selectedKey())
	}
	if a.conv == nil || a.conv.thread != 8 || !a.conv.open["a1/INBOX/12"] {
		t.Fatalf("open conversation = %+v", a.conv)
	}
	if !tt.HasText("Body of Quarterly numbers") || len(a.convs) != 2 {
		t.Errorf("rows %d: %q", len(a.convs), tt.Texts())
	}
}

// A notification for a message opens its conversation with the message
// expanded, even one the conversation would show collapsed.
func TestNotificationOpensItsConversation(t *testing.T) {
	w := watchApp(t, convService())
	a := w.a
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	a.openNotification(noteTarget{account: "a1", folder: "INBOX", validity: 7, uid: 9, message: true}.id())
	tt.Frame()
	if a.selectedKey() != "thread/a1/8" || !a.conv.open["a1/INBOX/9"] || w.raised != 1 {
		t.Fatalf("opened %q, expanded %v, raised %d", a.selectedKey(), openKeys(a.conv), w.raised)
	}
	if !tt.HasText("Body of Lunch on Friday?") {
		t.Errorf("the message is not open: %q", tt.Texts())
	}
	// Mail that arrives in a conversation: its notification opens it.
	reply := newMail(20, "Lin Wei", "Re: Venue for the offsite")
	reply.Thread = 7
	w.arrive(t, "a1", reply)
	if len(w.notes) != 1 {
		t.Fatalf("notifications = %+v", w.notes)
	}
	a.openNotification(w.notes[0].ID)
	tt.Frame()
	if a.selectedKey() != "thread/a1/7" || !a.conv.open["a1/INBOX/20"] {
		t.Fatalf("opened %q, expanded %v", a.selectedKey(), openKeys(a.conv))
	}
	if m, _ := w.fs.find("a1", "INBOX", 20); !m.Seen {
		t.Error("the message opened from its notification is unread")
	}
	// Unread, and the one asked for, it is marked read once.
	if b := lastBulk(t, w.fs); !reflect.DeepEqual(b.refs, []string{"INBOX/20"}) {
		t.Errorf("marked read = %+v", b)
	}
}

// Triage takes one item per conversation, told by its newest message,
// shows its suggestions on the rows, and choosing them chooses the rows.
func TestConversationTriage(t *testing.T) {
	a, fs, provider, _ := aiApp(t, true)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.TriageInbox())
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 || strings.Contains(provider.texts[0], "thread/") {
		t.Fatalf("triage sent %q", provider.texts)
	}
	if !tt.HasText("Automated notification") || !tt.HasText(locales.AiTriageCounts(1, 1, 1)) {
		t.Errorf("suggestions missing: %q", tt.Texts())
	}
	click(t, tt, locales.AiSelectArchive())
	github := a.rowKey(1)
	if a.sel.Len() != 1 || !a.sel.Has(github) || !strings.HasPrefix(github, "thread/") {
		t.Fatalf("selection = %d, %q", a.sel.Len(), github)
	}
	click(t, tt, locales.ArchiveConversations(1))
	if want := []string{"INBOX/2->Archive"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("moved = %v", fs.movedRefs)
	}
}

// The summary of a conversation is of every message shown, the collapsed
// ones and the reply in Sent included, once each: the review shows what
// is sent, nothing is sent before it, nothing is marked read, and the
// summary comes from the cache the second time.
func TestConversationSummary(t *testing.T) {
	a, fs, provider, _ := aiAppOf(t, true, convApp)
	tt := ui.NewTester(a.view, 1220, 800)
	flags, bulk := len(fs.flagCalls), len(fs.bulkFlags)
	click(t, tt, locales.SummarizeConversation())
	if a.review == nil || len(provider.texts) != 0 {
		t.Fatalf("no review before sending: %q", tt.Texts())
	}
	p := a.review.payload
	for _, want := range []string{convBodies["INBOX/1"], convBodies["Sent/2"], convBodies["INBOX/3"], "From: Ann"} {
		if strings.Count(p.Text, want) != 1 {
			t.Errorf("the payload has %q %d times:\n%s", want, strings.Count(p.Text, want), p.Text)
		}
	}
	for _, left := range []string{convBodies["Trash/5"], "lin.wei@example.com"} {
		if strings.Contains(p.Text, left) {
			t.Errorf("the payload has %q", left)
		}
	}
	if !tt.HasText(locales.AiReviewConversationSize(3, p.Chars, ai.ConversationLimit)) {
		t.Errorf("size line missing: %q", tt.Texts())
	}
	if len(fs.flagCalls) != flags || len(fs.bulkFlags) != bulk {
		t.Error("reading the bodies to summarize marked mail read")
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 || provider.texts[0] != p.Text {
		t.Fatalf("sent %q, reviewed %q", provider.texts, p.Text)
	}
	if !tt.HasText(testThreadSummary) {
		t.Fatalf("no summary: %q", tt.Texts())
	}
	click(t, tt, locales.AiWhatWasSent())
	if a.review == nil || a.review.send != nil || a.review.payload.Text != p.Text {
		t.Fatalf("what was sent = %+v", a.review)
	}
	click(t, tt, locales.Close())
	click(t, tt, locales.AiDismissSummary())
	click(t, tt, locales.SummarizeConversation())
	if len(provider.texts) != 1 || !tt.HasText(locales.AiCachedNote()) {
		t.Errorf("the second summary was sent again: %d requests", len(provider.texts))
	}
}

// With AI off, summarizing a conversation explains why and sends nothing.
func TestConversationSummaryWithAIOff(t *testing.T) {
	a, _, provider, _ := aiAppOf(t, false, convApp)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.SummarizeConversation())
	if !tt.HasText(locales.AiOff()) || len(provider.texts) != 0 || a.review != nil {
		t.Errorf("AI off: %d requests, %q", len(provider.texts), tt.Texts())
	}
}

// Each expanded message has its attachments, its plain or HTML toggle and
// its header fields, its own.
func TestConversationMessageParts(t *testing.T) {
	fs := convService()
	menu, _ := fs.find("a1", "INBOX", 10)
	fs.bodies[bodyKey("a1", "INBOX", 10)] = mail.Message{
		Summary: menu, BodyText: "Menu attached.", BodyHTML: "<p>Menu <b>attached</b>.</p>", BodyCached: true,
		Attachments: []mail.Attachment{{Part: "2", Filename: "menu.pdf", ContentType: "application/pdf", Size: 2048}},
	}
	a := newTestApp(fs)
	a.start()
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, "Re: Lunch on Friday?")
	if !tt.HasText("menu.pdf") || !tt.HasText("Menu attached.") {
		t.Fatalf("the attachment is missing: %q", tt.Texts())
	}
	click(t, tt, locales.ForMessage(locales.ShowHTML(), 2, 3))
	if a.conv.html != "a1/INBOX/10" || !tt.HasText(locales.ShowPlain()) {
		t.Fatalf("HTML of %q", a.conv.html)
	}
	click(t, tt, locales.ForMessage(locales.ShowPlain(), 2, 3))
	if a.conv.html != "" {
		t.Errorf("still HTML: %q", a.conv.html)
	}
	click(t, tt, locales.ForMessage(locales.Details(), 2, 3))
	if !tt.HasText(locales.MessageID()+": lunch-2@example.com") || tt.HasText(locales.MessageID()+": lunch-3@example.com") {
		t.Errorf("details: %q", tt.Texts())
	}
}

// The keys move between conversations and act on them: J and K choose
// the next and previous, F forwards the newest message, Delete moves the
// conversation to the trash.
func TestConversationShortcuts(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Key(0, ui.KeyJ)
	if a.selectedKey() != "thread/a1/8" || a.conv == nil || a.conv.thread != 8 {
		t.Fatalf("J chose %q", a.selectedKey())
	}
	tt.Key(0, ui.KeyK)
	if a.selectedKey() != "thread/a1/7" || a.conv.thread != 7 {
		t.Fatalf("K chose %q", a.selectedKey())
	}
	tt.Key(0, ui.KeyF)
	if len(a.composes) != 1 || a.composes[0].subject != "Fwd: Re: Venue for the offsite" {
		t.Errorf("F opened %d windows", len(a.composes))
	}
	tt.Key(0, ui.KeyDelete)
	if want := []string{"INBOX/1->Trash", "INBOX/3->Trash"}; !reflect.DeepEqual(fs.movedRefs, want) {
		t.Errorf("Delete moved %v", fs.movedRefs)
	}
}

// While a conversation is read from the cache the pane says so.
func TestConversationLoading(t *testing.T) {
	a, _ := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	var jobs []func()
	a.run = func(f func()) { jobs = append(jobs, f) }
	click(t, tt, "Quarterly numbers")
	if !tt.HasText(locales.Loading()) || tt.HasText("Body of Quarterly numbers") {
		t.Fatalf("no progress while the conversation loads: %q", tt.Texts())
	}
	for len(jobs) > 0 {
		next := jobs[0]
		jobs = jobs[1:]
		next()
	}
	tt.Frame()
	if !tt.HasText("Body of Quarterly numbers") {
		t.Errorf("the conversation did not show: %q", tt.Texts())
	}
}

// An empty folder says so, and a conversation that cannot be read says
// why and reads again on Retry.
func TestConversationEmptyAndFailure(t *testing.T) {
	a, fs := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.Junk())
	if !tt.HasText(locales.EmptyFolder()) || !tt.HasText(locales.NoConversationSelected()) {
		t.Errorf("empty folder: %q", tt.Texts())
	}
	fs.convErr = errFake
	click(t, tt, locales.Inbox())
	if !tt.HasText(locales.ConversationFailed(errFake.Error())) {
		t.Fatalf("no failure shown: %q", tt.Texts())
	}
	fs.convErr = nil
	click(t, tt, locales.Retry())
	if a.conv == nil || a.conv.err != "" || !tt.HasText(convBodies["INBOX/3"]) {
		t.Errorf("Retry did not read it: %q", tt.Texts())
	}
}

// At the window's smallest the conversation's controls are on screen, in
// light and dark: the toolbar, a message's menu and the switch.
func TestConversationsAtMinimumSize(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a, _, _, _ := aiAppOf(t, true, convApp)
		tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
		for _, label := range []string{locales.MoveActions(), locales.MarkActions(),
			locales.Reply(), locales.ReplyAll(), locales.Forward(), locales.SummarizeConversation(), locales.GroupByConversation(),
			locales.ForMessage(locales.MoreActions(), 3, 3)} {
			r, ok := tt.Find(label)
			if !ok || r.W == 0 || r.X+r.W > 1000 || r.Y+r.H > 640 {
				t.Errorf("dark=%v: %q is off screen: %+v", dark, label, r)
			}
		}
		// The moves and marks the narrow bar folded away are in its menus.
		click(t, tt, locales.MoveActions())
		if got := strings.Join(tt.Menu(), ","); !strings.Contains(got, locales.ArchiveConversations(1)) ||
			!strings.Contains(got, locales.DeleteConversations(1)) || !strings.Contains(got, locales.JunkConversations(1)) {
			t.Errorf("dark=%v: the move menu holds %q", dark, got)
		}
		tt.CloseMenu()
		click(t, tt, locales.MarkActions())
		if got := strings.Join(tt.Menu(), ","); !strings.Contains(got, locales.StarConversations(1)) ||
			!strings.Contains(got, locales.MarkConversationsUnread(1)) {
			t.Errorf("dark=%v: the mark menu holds %q", dark, got)
		}
		tt.CloseMenu()
		click(t, tt, locales.SummarizeConversation())
		if a.review == nil {
			t.Errorf("dark=%v: Summarize did not open the review", dark)
		}
		a.review = nil
		tt.Frame()
		click(t, tt, locales.ForMessage(locales.MoreActions(), 3, 3))
		if !tt.HasText(locales.ArchiveThisMessage()) {
			t.Errorf("dark=%v: the message's menu did not open: %q", dark, tt.Texts())
		}
	}
}

// A long conversation scrolls, at the window's smallest, to its newest
// message and that message's actions.
func TestLongConversationScrolls(t *testing.T) {
	fs := newFakeService()
	var msgs []mail.Summary
	now := time.Now()
	for i := 1; i <= 20; i++ {
		msgs = append(msgs, mail.Summary{
			UID: mail.UID(i), Folder: "INBOX", Thread: 5, From: lin, Subject: "Re: Plans",
			Preview: fmt.Sprintf("Note %d", i), Date: now.Add(time.Duration(i-30) * time.Minute), Seen: true,
		})
	}
	fs.addAccount(testAccount("a1", "Ann", "ann@example.com"), "secret", testFolders(), msgs)
	a := newTestApp(fs)
	a.start()
	tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{})
	reply := locales.ForMessage(locales.Reply(), 20, 20)
	before, ok := tt.Find(reply)
	if !ok || before.Y < 640 {
		t.Fatalf("the newest message is not below the fold: %+v", before)
	}
	tt.Scroll(800, 400, 0, 3000)
	after, _ := tt.Find(reply)
	if after.Y >= 640 || after.Y < 0 {
		t.Fatalf("scrolling did not bring the newest message into view: %+v", after)
	}
	click(t, tt, reply)
	if len(a.composes) != 1 || !strings.Contains(a.composes[0].body, "Body of Re: Plans") {
		t.Errorf("composes = %d", len(a.composes))
	}
}

// The conversation view speaks Simplified Chinese.
func TestConversationsInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, _ := convApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	for _, text := range []string{"按会话分组", "归档会话", "收件箱中的 2 封邮件 · 已发送中 1 封", "第 3 封（共 3 封），来自 Lin Wei", "2 封邮件"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if got := locales.ConversationsArchived(1); got != "已归档会话" {
		t.Errorf("one conversation archived = %q", got)
	}
	if got := locales.ConversationsArchived(3); got != "已归档 3 个会话" {
		t.Errorf("three conversations archived = %q", got)
	}
}
