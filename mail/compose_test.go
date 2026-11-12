package mail

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A new install has a send delay of ten seconds, replies to the sender
// above a quote prefixed with "> ", and forwards with attachments. A
// stored value that is none of the choices is not obeyed: its default
// stands, and the other settings keep theirs.
func TestComposeSettingsDefaultsAndChoices(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	got, err := svc.ComposeSettings(ctx)
	want := ComposeSettings{SendDelay: 10, Reply: ReplySender, Quote: QuotePrefix, Position: ReplyAbove, ForwardAttachments: true}
	if err != nil || got != want || got != DefaultComposeSettings() {
		t.Fatalf("settings of a new install = %+v, %v; want %+v", got, err, want)
	}
	if ComposeFormat != "text/plain" {
		t.Errorf("compose format = %q", ComposeFormat)
	}

	for _, delay := range SendDelays {
		set := ComposeSettings{SendDelay: delay, Reply: ReplyAll, Quote: QuoteHeader, Position: ReplyBelow}
		if err := svc.SaveComposeSettings(ctx, set); err != nil {
			t.Fatal(err)
		}
		if got, _ := svc.ComposeSettings(ctx); got != set || got.Delay() != time.Duration(delay)*time.Second {
			t.Errorf("saved %+v, read %+v", set, got)
		}
	}

	// Stored values nobody offered, as a newer or a damaged install
	// might leave, read as the defaults one by one.
	for key, value := range map[string]string{
		settingSendDelay: "7", settingReplyMode: "everyone", settingQuoteStyle: "fancy",
		settingReplyPosition: "middle", settingForwardAttachments: "maybe",
	} {
		store.kv[key] = value
	}
	if got, _ := svc.ComposeSettings(ctx); got != DefaultComposeSettings() {
		t.Errorf("unknown values read as %+v, want the defaults", got)
	}
	store.kv[settingSendDelay] = "not a number"
	store.kv[settingQuoteStyle] = string(QuoteHeader)
	if got, _ := svc.ComposeSettings(ctx); got.SendDelay != DefaultSendDelay || got.Quote != QuoteHeader {
		t.Errorf("one value unknown and one known read as %+v", got)
	}
	store.kv[settingForwardAttachments] = "0"
	if got, _ := svc.ComposeSettings(ctx); got.ForwardAttachments {
		t.Error("forwards carry attachments after they were turned off")
	}

	// Nor are they written.
	if err := svc.SaveComposeSettings(ctx, ComposeSettings{SendDelay: -3, Reply: "x", Quote: "y", Position: "z"}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		settingSendDelay: "10", settingReplyMode: "reply", settingQuoteStyle: "prefix",
		settingReplyPosition: "above", settingForwardAttachments: "0",
	} {
		if store.kv[key] != want {
			t.Errorf("%s stored as %q, want %q", key, store.kv[key], want)
		}
	}
}

// An account signs nothing until it has a signature, which then goes into
// new mail, replies and forwards alike; each can be turned off. The
// signature is the account's, and goes with it.
func TestSignatureIsKeptForEachAccount(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	a := testAccount()
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Signature(ctx, a.ID); err != nil || got != DefaultSignature() ||
		got != (Signature{New: true, Reply: true, Forward: true}) {
		t.Fatalf("signature of a new account = %+v, %v", got, err)
	}
	sig := Signature{Text: "Ann\n安娜", New: true, Reply: false, Forward: true}
	if err := svc.SaveSignature(ctx, a.ID, sig); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Signature(ctx, a.ID); got != sig {
		t.Errorf("signature = %+v, want %+v", got, sig)
	}
	if got, _ := svc.Signature(ctx, "other"); got != DefaultSignature() {
		t.Errorf("another account's signature = %+v", got)
	}
	// What a stored value leaves out keeps its default, and one that does
	// not read is no signature.
	store.kv[SignatureKey(a.ID)] = `{"text":"Ann"}`
	if got, _ := svc.Signature(ctx, a.ID); got != (Signature{Text: "Ann", New: true, Reply: true, Forward: true}) {
		t.Errorf("a signature stored without its switches = %+v", got)
	}
	store.kv[SignatureKey(a.ID)] = `{"text":`
	if got, err := svc.Signature(ctx, a.ID); err != nil || got != DefaultSignature() {
		t.Errorf("a damaged signature = %+v, %v", got, err)
	}
	if err := svc.SaveSignature(ctx, "gone", sig); !errors.Is(err, ErrNotFound) {
		t.Errorf("a signature for no account = %v", err)
	}
	if _, ok := store.kv[SignatureKey("gone")]; ok {
		t.Error("a signature was stored for no account")
	}
	if err := svc.SaveSignature(ctx, a.ID, sig); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.kv[SignatureKey(a.ID)]; ok {
		t.Error("the signature outlived its account")
	}
}

// The line "-- " marks a signature in plain text (RFC 3676), on a line of
// its own. A marker the user typed is the one; blank lines around the
// text go, and a signature without text is none.
func TestSignatureBlock(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", ""},
		{" \n\t\n", ""},
		{"-- ", ""},
		{"Ann", "-- \nAnn"},
		{"Ann\r\nCEO\r\n\r\n", "-- \nAnn\nCEO"},
		{"\n\n-- \nAnn\n", "-- \nAnn"},
		{"--\nAnn", "-- \nAnn"},
		{"---\nAnn", "-- \n---\nAnn"},
		{"Ann\n\nwww.example.com", "-- \nAnn\n\nwww.example.com"},
		{"安娜\r王", "-- \n安娜\n王"},
	} {
		if got := SignatureBlock(c.text); got != c.want {
			t.Errorf("SignatureBlock(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

// quoteWords are the words of the tests' quotes, English as the window's
// catalog has them.
func quoteWords() QuoteWords {
	return QuoteWords{
		Wrote:     func(date, name string) string { return "On " + date + ", " + name + " wrote:" },
		Original:  "-------- Original Message --------",
		Forwarded: "---------- Forwarded message ----------",
		From:      "From", To: "To", Cc: "Cc", Date: "Date", Subject: "Subject",
	}
}

// quoted is the message the tests reply to and forward.
func quoted() Summary {
	return Summary{
		From:    Address{Name: "Bob", Email: "bob@example.com"},
		To:      []Address{{Name: "Ann", Email: "ann@example.com"}},
		Cc:      []Address{{Email: "carol@example.com"}},
		Subject: "Plans",
		Date:    time.Date(2026, 3, 2, 9, 30, 0, 0, time.UTC),
	}
}

// The signature goes where the account's preferences put it: below the
// room to write in a new message; above the quote of a reply written
// above it and at the end of one written below; above a forwarded
// message. Each kind can do without it, and one without text adds
// nothing. The caret is where the user writes.
func TestSignaturePlacement(t *testing.T) {
	const text = "Hi Ann,\n\nSee you.\n"
	quote := "On Mon, 02 Mar 2026 09:30:00 +0000, Bob wrote:\n> Hi Ann,\n>\n> See you."
	forwarded := "---------- Forwarded message ----------\nFrom: Bob <bob@example.com>\n" +
		"Date: Mon, 02 Mar 2026 09:30:00 +0000\nSubject: Plans\nTo: Ann <ann@example.com>\nCc: carol@example.com\n\n" +
		"Hi Ann,\n\nSee you."
	const block = "-- \nAnn Lee\nExample Inc."
	all := Signature{Text: "Ann Lee\nExample Inc.\n", New: true, Reply: true, Forward: true}
	none := Signature{Text: all.Text}
	empty := Signature{Text: " \n", New: true, Reply: true, Forward: true}
	above := DefaultComposeSettings()
	below := DefaultComposeSettings()
	below.Position = ReplyBelow
	m := quoted()
	for _, c := range []struct {
		name string
		got  Body
		want Body
	}{
		{"new", NewBody(all), Body{"\n\n" + block, 0}},
		{"new without", NewBody(none), Body{"", 0}},
		{"new, empty signature", NewBody(empty), Body{"", 0}},
		{"reply above", ReplyBody(above, all, m, text, quoteWords()), Body{"\n\n" + block + "\n\n" + quote, 0}},
		{"reply above without", ReplyBody(above, none, m, text, quoteWords()), Body{"\n\n" + quote, 0}},
		{"reply above, empty signature", ReplyBody(above, empty, m, text, quoteWords()), Body{"\n\n" + quote, 0}},
		{"reply below", ReplyBody(below, all, m, text, quoteWords()),
			Body{quote + "\n\n" + "\n\n" + block, utf8.RuneCountInString(quote + "\n\n")}},
		{"reply below without", ReplyBody(below, none, m, text, quoteWords()),
			Body{quote + "\n\n", utf8.RuneCountInString(quote + "\n\n")}},
		{"reply below, empty signature", ReplyBody(below, empty, m, text, quoteWords()),
			Body{quote + "\n\n", utf8.RuneCountInString(quote + "\n\n")}},
		{"forward", ForwardBody(all, m, text, quoteWords()), Body{"\n\n" + block + "\n\n" + forwarded, 0}},
		{"forward without", ForwardBody(none, m, text, quoteWords()), Body{"\n\n" + forwarded, 0}},
		{"forward, empty signature", ForwardBody(empty, m, text, quoteWords()), Body{"\n\n" + forwarded, 0}},
		// One switch leaves the others as they are.
		{"reply only, new", NewBody(Signature{Text: "Ann Lee\nExample Inc.", Reply: true}), Body{"", 0}},
		{"reply only, forward", ForwardBody(Signature{Text: "Ann Lee\nExample Inc.", Reply: true}, m, text, quoteWords()),
			Body{"\n\n" + forwarded, 0}},
		{"reply only, reply", ReplyBody(above, Signature{Text: "Ann Lee\nExample Inc.", Reply: true}, m, text, quoteWords()),
			Body{"\n\n" + block + "\n\n" + quote, 0}},
		// A reply to a message without text quotes nothing.
		{"reply above to nothing", ReplyBody(above, all, m, " \n", quoteWords()), Body{"\n\n" + block, 0}},
		{"reply below to nothing", ReplyBody(below, all, m, "", quoteWords()), Body{"\n\n" + block, 0}},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got %q at %d\nwant %q at %d", c.name, c.got.Text, c.got.Caret, c.want.Text, c.want.Caret)
		}
		if c.got.Caret < 0 || c.got.Caret > utf8.RuneCountInString(c.got.Text) {
			t.Errorf("%s: caret %d outside the text", c.name, c.got.Caret)
		}
	}
	// The caret counts runes, as text inputs do.
	body := ReplyBody(below, none, m, "你好\n再见", quoteWords())
	if runes := []rune(body.Text); string(runes[body.Caret-2:body.Caret]) != "\n\n" || body.Caret != len(runes) ||
		!strings.Contains(body.Text, "> 你好\n> 再见") {
		t.Errorf("reply below a Chinese text = %q at %d", body.Text, body.Caret)
	}
}

// A reply quotes the message line by line after "> ", the default, or
// puts it as it was under an "original message" block of its headers.
// The words are the window's, in the user's language, and its dates.
func TestQuoteStyles(t *testing.T) {
	m := quoted()
	none := Signature{}
	header := DefaultComposeSettings()
	header.Quote = QuoteHeader
	got := ReplyBody(header, none, m, "Line one\n\n> earlier\nLine two", quoteWords())
	want := "\n\n-------- Original Message --------\nFrom: Bob <bob@example.com>\n" +
		"Date: Mon, 02 Mar 2026 09:30:00 +0000\nSubject: Plans\nTo: Ann <ann@example.com>\nCc: carol@example.com\n\n" +
		"Line one\n\n> earlier\nLine two"
	if got.Text != want {
		t.Errorf("original message block =\n%q\nwant\n%q", got.Text, want)
	}
	prefix := ReplyBody(DefaultComposeSettings(), none, m, "Line one\r\n\r\n> earlier\r\nLine two", quoteWords())
	if want := "\n\nOn Mon, 02 Mar 2026 09:30:00 +0000, Bob wrote:\n> Line one\n>\n> > earlier\n> Line two"; prefix.Text != want {
		t.Errorf("prefixed quote =\n%q\nwant\n%q", prefix.Text, want)
	}
	// A style nobody offered quotes as the default does.
	odd := DefaultComposeSettings()
	odd.Quote, odd.Position = "fancy", "middle"
	if got := ReplyBody(odd, none, m, "Line one\r\n\r\n> earlier\r\nLine two", quoteWords()); got != prefix {
		t.Errorf("unknown style = %q, want the default's", got.Text)
	}

	// Chinese words, a date of the window's and a sender without a name.
	zh := QuoteWords{
		Wrote:    func(date, name string) string { return name + " 于 " + date + " 写道：" },
		Original: "-------- 原始邮件 --------",
		From:     "发件人", To: "收件人", Cc: "抄送", Date: "日期", Subject: "主题",
		FormatDate: func(t time.Time) string { return t.Format("2006年1月2日 15:04") },
	}
	m.From.Name = ""
	if got := ReplyBody(DefaultComposeSettings(), none, m, "好的", zh); got.Text != "\n\nbob@example.com 于 2026年3月2日 09:30 写道：\n> 好的" {
		t.Errorf("quote in Chinese = %q", got.Text)
	}
	m.Cc, m.Date = nil, time.Time{}
	if got := ReplyBody(header, none, m, "好的", zh); got.Text != "\n\n-------- 原始邮件 --------\n发件人: bob@example.com\n主题: Plans\n收件人: Ann <ann@example.com>\n\n好的" {
		t.Errorf("header block in Chinese, without date or Cc = %q", got.Text)
	}
}
