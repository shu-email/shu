package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// bookDay is a day of the mail the address book was made from.
func bookDay(month time.Month, day int) time.Time {
	return time.Date(2026, month, day, 9, 0, 0, 0, time.UTC)
}

// contactsService is composeService with Ann's address book: Maya, whom
// she writes to often and lately; Marta, whom she wrote to once last
// year; Mark, who wrote to her once, the newest of all; Lena and Bob Lee,
// whom she wrote to; Raj, who wrote to her; 王小明, whom she wrote to; and
// her lab's address, which is her own and no contact.
func contactsService() *fakeService {
	fs := composeService()
	for _, day := range []time.Time{bookDay(time.February, 10), bookDay(time.February, 20), bookDay(time.March, 1)} {
		fs.wrote("Maya Chen", "maya@example.com", day)
	}
	fs.heard("Maya Chen", "maya@example.com", bookDay(time.March, 1))
	fs.wrote("Marta Díaz", "marta@example.org", time.Date(2025, time.June, 1, 9, 0, 0, 0, time.UTC))
	fs.heard("Mark Stone", "mark.stone@example.net", bookDay(time.March, 2))
	fs.wrote("Lena Lee", "lena.lee@example.com", bookDay(time.February, 12))
	fs.wrote("Bob Lee", "bob.lee@example.com", bookDay(time.January, 5))
	fs.heard("Raj Patel", "raj@example.com", bookDay(time.January, 30))
	fs.wrote("王小明", "wang@example.cn", bookDay(time.February, 1))
	fs.heard("Ann Lee (Lab)", "ann@lab.example.org", bookDay(time.March, 2))
	return fs
}

// contactsApp starts the window on contactsService, with a compose window
// open and a tester of it.
func contactsApp(t *testing.T) (*app, *fakeService, *composeState, *ui.Tester) {
	t.Helper()
	fs := contactsService()
	a := openApp(fs)
	cs := a.openCompose(nil)
	return a, fs, cs, ui.NewTester(cs.view, 720, 620)
}

// What the list below a field offers, as assistive technology names each
// contact: its name and its address.
const (
	mayaOffer  = "Maya Chen maya@example.com"
	martaOffer = "Marta Díaz marta@example.org"
	markOffer  = "Mark Stone mark.stone@example.net"
)

// offers is what the list below a field offers, in order, as assistive
// technology reads it, and the one the arrows are on, "" for none.
func offers(tt *ui.Tester) (names []string, on string) {
	for _, info := range tt.AccessAll() {
		if info.Role != ui.RoleOption {
			continue
		}
		names = append(names, info.Name)
		if info.Focused {
			on = info.Name
		}
	}
	return names, on
}

// combo is a field of recipients as assistive technology reads it.
func combo(t *testing.T, tt *ui.Tester, field string) ui.AccessInfo {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleComboBox, field)
	if !ok {
		t.Fatalf("no combo box %q: %q", field, tt.Texts())
	}
	return info
}

// A letter of a known name offers its contact first, chosen, with the
// others it begins a word of: those Ann writes to, best first, above one
// who wrote to her once, however lately. Enter makes a chip of the chosen
// contact, the draft keeps it and the message goes to it. The field is a
// combo box whose list completes what is typed, and assistive technology
// follows the contact the arrows are on.
func TestOneLetterOfANameOffersItFirst(t *testing.T) {
	a, fs, cs, tt := contactsApp(t)
	if info := combo(t, tt, locales.To()); info.Expanded || info.AutoComplete != ui.AutoCompleteList {
		t.Fatalf("the To field before typing = %+v", info)
	}
	typeIn(t, tt, locales.To(), "m")
	names, on := offers(tt)
	if want := []string{mayaOffer, martaOffer, markOffer}; !slices.Equal(names, want) || on != mayaOffer {
		t.Fatalf("“m” offers %q, the arrows on %q; want %q", names, on, want)
	}
	if !slices.Equal(fs.suggested, []string{"m"}) {
		t.Fatalf("one letter asked the address book for %q", fs.suggested)
	}
	tt.Type("a")
	names, on = offers(tt)
	if want := []string{mayaOffer, martaOffer, markOffer}; !slices.Equal(names, want) || on != mayaOffer {
		t.Fatalf("“ma” offers %q, the arrows on %q; want %q", names, on, want)
	}
	if info := combo(t, tt, locales.To()); !info.Expanded {
		t.Errorf("the To field with its list showing = %+v", info)
	}
	list, ok := tt.AccessByRole(ui.RoleListBox, locales.ContactSuggestions())
	if !ok || list.Bounds.Y < combo(t, tt, locales.To()).Bounds.Y {
		t.Errorf("the list = %+v, found %v", list, ok)
	}
	for _, text := range []string{"Maya Chen", "maya@example.com", "Mark Stone", "mark.stone@example.net"} {
		if !tt.HasText(text) {
			t.Errorf("the list lacks %q: %q", text, tt.Texts())
		}
	}
	tt.Key(0, ui.KeyEnter)
	if cs.to != "Maya Chen <maya@example.com>, " {
		t.Fatalf("Enter wrote %q", cs.to)
	}
	if names, _ := offers(tt); len(names) != 0 || combo(t, tt, locales.To()).Expanded || !tt.Focused(locales.To()) {
		t.Errorf("after Enter: offers %q, focused %v", names, tt.Focused(locales.To()))
	}
	if d := theDraftOf(t, fs, cs.draftID); d.To != "Maya Chen <maya@example.com>, " {
		t.Errorf("draft = %+v", d)
	}
	typeIn(t, tt, locales.Subject(), "Lunch")
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || !slices.Equal(fs.sent[0].To, []mail.Address{{Name: "Maya Chen", Email: "maya@example.com"}}) {
		t.Fatalf("sent %+v", fs.sent)
	}
	if !cs.closed || len(openComposes(a)) != 0 {
		t.Error("the window stays open after sending")
	}
}

// The arrows move among the contacts offered, Tab takes the one they are
// on and keeps the keyboard in the field, Escape closes the list and
// not the window, and a click takes a contact.
func TestOfferKeysAndClicks(t *testing.T) {
	_, _, cs, tt := contactsApp(t)
	typeIn(t, tt, locales.To(), "ma")
	tt.Key(0, ui.KeyDown)
	if _, on := offers(tt); on != martaOffer {
		t.Fatalf("Down went to %q", on)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	if _, on := offers(tt); on != markOffer {
		t.Fatalf("Down past the last went to %q", on)
	}
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyUp)
	if _, on := offers(tt); on != mayaOffer {
		t.Fatalf("Up past the first went to %q", on)
	}
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyTab)
	if cs.to != "Marta Díaz <marta@example.org>, " || !tt.Focused(locales.To()) {
		t.Fatalf("Tab wrote %q, the To field focused %v", cs.to, tt.Focused(locales.To()))
	}

	// Escape closes the list, and the window stays with what was typed.
	tt.Type("ma")
	if names, _ := offers(tt); !slices.Equal(names, []string{mayaOffer, markOffer}) {
		t.Fatalf("with Marta in the field, “ma” offers %q", names)
	}
	tt.Key(0, ui.KeyEscape)
	if names, _ := offers(tt); len(names) != 0 || cs.closed || cs.to != "Marta Díaz <marta@example.org>, ma" {
		t.Fatalf("after Escape: offers %q, closed %v, To %q", names, cs.closed, cs.to)
	}
	// Typing offers again; a click takes the contact.
	tt.Type("r")
	click(t, tt, "Mark Stone")
	if cs.to != "Marta Díaz <marta@example.org>, Mark Stone <mark.stone@example.net>, " || !tt.Focused(locales.To()) {
		t.Fatalf("the click wrote %q, the To field focused %v", cs.to, tt.Focused(locales.To()))
	}
	// With no list, Escape is the window's again.
	tt.Key(0, ui.KeyEscape)
	if !cs.closed {
		t.Error("Escape without a list did not close the window")
	}
}

// A comma ends the entry typed and makes a chip of it, a contact taken
// joins the chips before the empty input, and a chip goes with its button
// or with Backspace in an empty input. A name in Chinese is looked up
// from its first character.
func TestCommasMakeChipsAndTakingOneAddsAnother(t *testing.T) {
	_, _, cs, tt := contactsApp(t)
	typeIn(t, tt, locales.To(), "Bob <bob@example.com>, lena@example.org, ")
	if want := "Bob <bob@example.com>, lena@example.org, "; cs.to != want {
		t.Fatalf("To = %q, want %q", cs.to, want)
	}
	if !tt.HasText("Bob") || !tt.HasText("lena@example.org") {
		t.Fatalf("the entries are no chips: %q", tt.Texts())
	}
	tt.Type("ma")
	if names, on := offers(tt); on != mayaOffer || len(names) != 3 {
		t.Fatalf("“ma” offers %q, the arrows on %q", names, on)
	}
	tt.Key(0, ui.KeyEnter)
	if want := "Bob <bob@example.com>, lena@example.org, Maya Chen <maya@example.com>, "; cs.to != want {
		t.Fatalf("To = %q, want %q", cs.to, want)
	}
	tt.Type("x")
	if want := "Bob <bob@example.com>, lena@example.org, Maya Chen <maya@example.com>, x"; cs.to != want {
		t.Errorf("the caret is not after the chips: %q", cs.to)
	}
	// Backspace takes the entry out first, then the last chip.
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	if want := "Bob <bob@example.com>, lena@example.org, "; cs.to != want {
		t.Errorf("Backspace left %q, want %q", cs.to, want)
	}
	click(t, tt, locales.RemoveRecipient("lena@example.org"))
	if want := "Bob <bob@example.com>, "; cs.to != want {
		t.Errorf("the chip's button left %q, want %q", cs.to, want)
	}

	// After a name in Chinese.
	typeIn(t, tt, locales.Cc(), "王")
	if names, on := offers(tt); !slices.Equal(names, []string{"王小明 wang@example.cn"}) || on != names[0] {
		t.Fatalf("“王” offers %q", names)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Type("Mar")
	if names, _ := offers(tt); !slices.Equal(names, []string{martaOffer, markOffer}) {
		t.Fatalf("with Maya in To, “Mar” offers %q", names)
	}
	tt.Key(0, ui.KeyEnter)
	if want := "王小明 <wang@example.cn>, Marta Díaz <marta@example.org>, "; cs.cc != want {
		t.Errorf("Cc = %q, want %q", cs.cc, want)
	}
}

// The addresses the fields hold already, in any case and in any of To,
// Cc and Bcc, are not offered again, and no contact is offered twice;
// the user's own addresses are never offered.
func TestOffersLeaveOutTheFieldsAddresses(t *testing.T) {
	_, fs, cs, tt := contactsApp(t)
	typeIn(t, tt, locales.To(), "MAYA@example.com, ")
	typeIn(t, tt, locales.Cc(), "ma")
	if names, _ := offers(tt); !slices.Equal(names, []string{martaOffer, markOffer}) {
		t.Fatalf("Cc offers %q with Maya in To", names)
	}
	tt.Key(0, ui.KeyEnter)
	click(t, tt, locales.ShowBcc())
	tt.Type("ma")
	if names, _ := offers(tt); !slices.Equal(names, []string{markOffer}) {
		t.Fatalf("Bcc offers %q with Maya in To and Marta in Cc", names)
	}
	typeIn(t, tt, locales.To(), "an")
	if names, _ := offers(tt); len(names) != 0 {
		t.Errorf("Ann's own addresses are offered: %q", names)
	}
	// The same contact under its addresses' cases is one.
	fs.wrote("Mark Stone", "MARK.STONE@example.net", bookDay(time.March, 3))
	cs.to = ""
	typeIn(t, tt, locales.To(), "mark")
	if names, _ := offers(tt); len(names) != 1 {
		t.Errorf("one contact offered as %q", names)
	}
}

// Queries run in jobs, and the window does not wait for them: a query
// that a later one overtook does not run, and an answer for text typed
// over since is dropped. Meanwhile the list follows the typing at once,
// keeping the contacts offered that the text still finds, and a contact
// the arrows went to stays chosen.
func TestStaleOffersAreDropped(t *testing.T) {
	a, fs, cs, tt := contactsApp(t)
	q := queueJobs(a)
	typeIn(t, tt, locales.To(), "ma")
	tt.Type("r")
	if cs.to != "mar" || len(fs.suggested) != 0 {
		t.Fatalf("typing waited for the address book: To %q, asked %q", cs.to, fs.suggested)
	}
	// The newest query first, as a slow goroutine would leave the older.
	jobs := q.jobs
	q.jobs = nil
	a.run = func(f func()) { f() }
	for i := len(jobs) - 1; i >= 0; i-- {
		jobs[i]()
	}
	tt.Frame()
	if names, _ := offers(tt); !slices.Equal(fs.suggested, []string{"mar"}) || !slices.Equal(names, []string{martaOffer, markOffer}) {
		t.Fatalf("asked %q, offered %q", fs.suggested, names)
	}

	// Typed on, the list keeps at once what the text still finds, and
	// the contact chosen.
	tt.Key(0, ui.KeyDown)
	q = queueJobs(a)
	tt.Type("k")
	if names, on := offers(tt); !slices.Equal(names, []string{markOffer}) || on != markOffer || len(q.jobs) == 0 {
		t.Fatalf("“mark” before its answer offers %q, the arrows on %q", names, on)
	}
	tt.Key(0, ui.KeyBackspace)
	if names, on := offers(tt); !slices.Equal(names, []string{markOffer}) || on != markOffer {
		t.Fatalf("“mar” again before its answer offers %q, the arrows on %q", names, on)
	}
	q.drain(a)
	tt.Frame()
	if names, on := offers(tt); !slices.Equal(names, []string{martaOffer, markOffer}) || on != markOffer {
		t.Fatalf("the answer for “mar” offers %q, the arrows on %q", names, on)
	}

	// An answer that comes after the text changed is dropped, also when
	// it comes last: Mark, whom “mar” finds and “mart” does not, stays
	// offered for “mar” when the answer for “mart” comes after its own.
	fs.wrote("Martin Alvarez", "martin@example.org", bookDay(time.March, 3))
	tt.Type("ta")
	if names, _ := offers(tt); !slices.Equal(names, []string{martaOffer}) {
		t.Fatalf("“marta” offers %q", names)
	}
	var posts []func()
	a.update = func(f func()) { posts = append(posts, f) }
	asked := len(fs.suggested)
	tt.Key(0, ui.KeyBackspace)
	tt.Key(0, ui.KeyBackspace)
	if !slices.Equal(fs.suggested[asked:], []string{"mart", "mar"}) {
		t.Fatalf("asked %q", fs.suggested[asked:])
	}
	a.update = nil
	for i := len(posts) - 1; i >= 0; i-- {
		posts[i]()
	}
	tt.Frame()
	want := []string{"Martin Alvarez martin@example.org", martaOffer, markOffer}
	if names, on := offers(tt); !slices.Equal(names, want) || on != want[0] {
		t.Errorf("“mar” offers %q, the arrows on %q; want %q", names, on, want)
	}
}

// Addresses typed whole go as before: one of nobody offers nothing and
// Tab moves on; one a contact's address begins with is offered with none
// chosen, so that Enter and Tab leave it as typed. They go as typed.
func TestFreeFormAddressesAsBefore(t *testing.T) {
	_, fs, cs, tt := contactsApp(t)
	typeIn(t, tt, locales.To(), "zed@nowhere.example")
	if names, _ := offers(tt); len(names) != 0 {
		t.Fatalf("an address of nobody offers %q", names)
	}
	tt.Key(0, ui.KeyTab)
	if !tt.Focused(locales.Cc()) || cs.to != "zed@nowhere.example, " {
		t.Fatalf("Tab: Cc focused %v, To %q", tt.Focused(locales.Cc()), cs.to)
	}
	tt.Type("maya@example.co")
	if names, on := offers(tt); !slices.Equal(names, []string{mayaOffer}) || on != "" {
		t.Fatalf("an address Maya's begins with offers %q, the arrows on %q", names, on)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyTab)
	if cs.cc != "maya@example.co, " || tt.Focused(locales.Cc()) {
		t.Fatalf("Enter and Tab: Cc %q, still focused %v", cs.cc, tt.Focused(locales.Cc()))
	}
	typeIn(t, tt, locales.Subject(), "Hello")
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 || !slices.Equal(fs.sent[0].To, []mail.Address{{Email: "zed@nowhere.example"}}) ||
		!slices.Equal(fs.sent[0].Cc, []mail.Address{{Email: "maya@example.co"}}) || len(fs.recipientChecks) != 0 {
		t.Errorf("sent %+v, checked %q", fs.sent, fs.recipientChecks)
	}
}

// A send stops on entries that are no address, and the window says, field
// by field, what each is: not an address; a name no contact has; a name
// several have, with each offered; a name one has, offered in its place.
// The fields are marked invalid, with the messages as their errors, and
// the keyboard goes to the first. An offer taken writes the contact in
// place of the entry; typing checks the field again; the message goes
// once all are fixed. Meanwhile the draft keeps what was typed.
func TestSendStopsOnRecipientsThatAreNoAddresses(t *testing.T) {
	_, fs, cs, _ := contactsApp(t)
	tt := ui.NewTester(cs.view, 720, 900)
	typeIn(t, tt, locales.To(), "bob@, lee, zed, maya")
	tt.Key(0, ui.KeyEscape)
	typeIn(t, tt, locales.Cc(), "Raj")
	tt.Key(0, ui.KeyEscape)
	click(t, tt, locales.ShowBcc())
	tt.Type("小明")
	tt.Key(0, ui.KeyEscape)
	typeIn(t, tt, locales.Subject(), "Plans")
	click(t, tt, locales.Send())
	if len(fs.sent) != 0 || cs.closed {
		t.Fatalf("sent %+v", fs.sent)
	}
	lena, bob, maya := "Lena Lee <lena.lee@example.com>", "Bob Lee <bob.lee@example.com>", "Maya Chen <maya@example.com>"
	wantTo := []string{locales.RecipientNotAddress("bob@"), locales.RecipientAmbiguous("lee"), locales.RecipientUnknown("zed"),
		locales.RecipientMatch("maya", "Maya Chen")}
	for _, text := range append(slices.Clone(wantTo), locales.RecipientsNotSent(), locales.RecipientMatch("Raj", "Raj Patel"),
		locales.RecipientMatch("小明", "王小明"), lena, bob, locales.UseContact(maya)) {
		if !tt.HasText(text) {
			t.Errorf("the window lacks %q: %q", text, tt.Texts())
		}
	}
	if to := combo(t, tt, locales.To()); !to.Invalid || !slices.Equal(to.ErrorMessage, wantTo) {
		t.Errorf("To = %+v", to)
	}
	if cc := combo(t, tt, locales.Cc()); !cc.Invalid || !slices.Equal(cc.ErrorMessage, []string{locales.RecipientMatch("Raj", "Raj Patel")}) {
		t.Errorf("Cc = %+v", cc)
	}
	if subject, _ := tt.AccessByRole(ui.RoleTextField, locales.Subject()); subject.Invalid {
		t.Error("the subject is marked invalid")
	}
	if !tt.Focused(locales.To()) {
		t.Error("the keyboard is not in To")
	}
	if d := theDraftOf(t, fs, cs.draftID); d.To != "bob@, lee, zed, maya" || d.Cc != "Raj" || d.Bcc != "小明" {
		t.Errorf("the draft = %+v", d)
	}

	// Each offer writes its contact in place of the entry.
	click(t, tt, locales.UseContactFor(lena, "lee"))
	if cs.to != "bob@, "+lena+", zed, maya" || !tt.Focused(locales.To()) {
		t.Fatalf("Lena chosen: To %q, focused %v", cs.to, tt.Focused(locales.To()))
	}
	if tt.HasText(locales.RecipientAmbiguous("lee")) || !tt.HasText(locales.RecipientNotAddress("bob@")) {
		t.Fatalf("after Lena: %q", tt.Texts())
	}
	click(t, tt, locales.UseContactFor(maya, "maya"))
	click(t, tt, locales.UseContactFor("Raj Patel <raj@example.com>", "Raj"))
	click(t, tt, locales.UseContactFor("王小明 <wang@example.cn>", "小明"))
	if cs.to != "bob@, "+lena+", zed, "+maya || cs.cc != "Raj Patel <raj@example.com>" || cs.bcc != "王小明 <wang@example.cn>" {
		t.Fatalf("after the offers: To %q, Cc %q, Bcc %q", cs.to, cs.cc, cs.bcc)
	}
	if combo(t, tt, locales.Cc()).Invalid || combo(t, tt, locales.Bcc()).Invalid || !combo(t, tt, locales.To()).Invalid {
		t.Errorf("fields still invalid: %q", tt.Texts())
	}
	click(t, tt, locales.Send())
	if len(fs.sent) != 0 || !tt.HasText(locales.RecipientUnknown("zed")) {
		t.Fatalf("sent with “bob@” and “zed”: %+v", fs.sent)
	}

	// Removing a chip and typing a new entry checks the field again.
	click(t, tt, locales.RemoveRecipient("bob@"))
	if tt.HasText(locales.RecipientNotAddress("bob@")) || !tt.HasText(locales.RecipientUnknown("zed")) {
		t.Fatalf("after removing “bob@”: %q", tt.Texts())
	}
	click(t, tt, locales.RemoveRecipient("zed"))
	if tt.HasText(locales.RecipientUnknown("zed")) {
		t.Fatalf("after removing “zed”: %q", tt.Texts())
	}
	click(t, tt, locales.To())
	tt.Key(ui.Ctrl, ui.KeyEnd)
	tt.Type(", zed@example.org")
	if cs.hasProblems() || combo(t, tt, locales.To()).Invalid || tt.HasText(locales.RecipientsNotSent()) {
		t.Fatalf("all fixed, the window says %q", tt.Texts())
	}
	click(t, tt, locales.Send())
	if len(fs.sent) != 1 {
		t.Fatalf("not sent once fixed: %q", tt.Texts())
	}
	want := []mail.Address{{Name: "Lena Lee", Email: "lena.lee@example.com"}, {Name: "Maya Chen", Email: "maya@example.com"}, {Email: "zed@example.org"}}
	if got := fs.sent[0]; !slices.Equal(got.To, want) || got.Cc[0].Email != "raj@example.com" || got.Bcc[0].Email != "wang@example.cn" {
		t.Errorf("sent %+v", got)
	}
}

// A check the address book cannot answer says so, and sends nothing.
func TestRecipientCheckFailure(t *testing.T) {
	_, fs, cs, tt := contactsApp(t)
	typeIn(t, tt, locales.To(), "lee")
	tt.Key(0, ui.KeyEscape)
	fs.contactErr = errFake
	click(t, tt, locales.Send())
	if len(fs.sent) != 0 || !tt.HasText(locales.RecipientsCheckFailed(errFake.Error())) || cs.hasProblems() {
		t.Errorf("sent %+v: %q", fs.sent, tt.Texts())
	}
}

// The words of the suggestions and the checks of recipients are in
// Simplified Chinese too.
func TestRecipientStringsInChinese(t *testing.T) {
	zh := locales.In("zh-Hans")
	for name, text := range map[string]string{
		"contactSuggestions": zh.ContactSuggestions(), "recipientsNotSent": zh.RecipientsNotSent(),
		"recipientNotAddress": zh.RecipientNotAddress("bob@"), "recipientUnknown": zh.RecipientUnknown("Zed"),
		"recipientAmbiguous": zh.RecipientAmbiguous("Lee"), "recipientMatch": zh.RecipientMatch("Maya", "Maya Chen"),
		"useContact": zh.UseContact("Maya Chen <maya@example.com>"), "useContactFor": zh.UseContactFor("Maya", "m"),
		"removeRecipient":       zh.RemoveRecipient("Maya"),
		"recipientsCheckFailed": zh.RecipientsCheckFailed("x"),
	} {
		if !strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff }) {
			t.Errorf("%s in zh-Hans = %q", name, text)
		}
	}
	if got := zh.RecipientMatch("Maya", "Maya Chen"); !strings.Contains(got, "Maya") || !strings.Contains(got, "Maya Chen") {
		t.Errorf("recipientMatch = %q", got)
	}
}

// At the compose window's smallest size, in both appearances, the list
// of offers shows inside the window, and the problems of a send that
// stopped leave the fields, the first problem's offers whole and Send on
// screen, with the From picker and Bcc shown.
func TestRecipientsAtMinimumSizeAndDark(t *testing.T) {
	inside := func(t *testing.T, name string, tt *ui.Tester, info ui.AccessInfo, ok bool, w, h float32) {
		t.Helper()
		box := info.Bounds
		if !ok || box.W <= 0 || box.H <= 0 || box.X < 0 || box.Y < 0 || box.X+box.W > w || box.Y+box.H > h {
			t.Errorf("%s: %q is at %+v (found %v): %q", name, info.Name, box, ok, tt.Texts())
		}
	}
	lena := locales.UseContactFor("Lena Lee <lena.lee@example.com>", "lee")
	stopped := func(t *testing.T, w, h int, dark bool) (*ui.Tester, *composeState) {
		t.Helper()
		_, fs, cs, _ := contactsApp(t)
		tt := ui.NewTesterWith(cs.view, w, h, ui.TesterOptions{Dark: dark})
		click(t, tt, locales.ShowBcc())
		typeIn(t, tt, locales.To(), "lee")
		tt.Key(0, ui.KeyEscape)
		click(t, tt, locales.Send())
		if len(fs.sent) != 0 || !cs.hasProblems() {
			t.Fatal("the send did not stop")
		}
		return tt, cs
	}
	// The offer as high as it is with room to spare: none of it cut off.
	roomy, _ := stopped(t, 720, 900, false)
	whole, _ := roomy.AccessByRole(ui.RoleButton, lena)
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		_, _, cs, _ := contactsApp(t)
		tt := ui.NewTesterWith(cs.view, 520, 420, ui.TesterOptions{Dark: dark})
		click(t, tt, locales.ShowBcc())
		typeIn(t, tt, locales.To(), "ma")
		for _, opt := range []string{mayaOffer, markOffer} {
			info, ok := tt.AccessByRole(ui.RoleOption, opt)
			inside(t, name+" offers", tt, info, ok, 520, 420)
		}

		tt, _ = stopped(t, 520, 420, dark)
		for _, label := range []string{locales.Send(), lena} {
			info, ok := tt.AccessByRole(ui.RoleButton, label)
			inside(t, name+" problems", tt, info, ok, 520, 420)
		}
		if info, _ := tt.AccessByRole(ui.RoleButton, lena); info.Bounds.H < whole.Bounds.H-0.5 {
			t.Errorf("%s: the offer is cut to %v of %v", name, info.Bounds.H, whole.Bounds.H)
		}
		for _, field := range []string{locales.From(), locales.Subject()} {
			box, ok := tt.Find(field)
			inside(t, name+" fields", tt, ui.AccessInfo{Name: field, Bounds: box}, ok, 520, 420)
		}
	}
}
