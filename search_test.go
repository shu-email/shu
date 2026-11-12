package main

import (
	"reflect"
	"testing"
	"time"

	"shu/mail"
)

// mergeSearch adds what one part of a search asks to the rest, as
// mail.ParseSearch combines its parts.
func mergeSearch(s *mail.Search, p mail.Search) {
	s.Text = append(s.Text, p.Text...)
	s.From = append(s.From, p.From...)
	s.To = append(s.To, p.To...)
	s.Subject = append(s.Subject, p.Subject...)
	s.Body = append(s.Body, p.Body...)
	if p.Since.After(s.Since) {
		s.Since = p.Since
	}
	if !p.Before.IsZero() && (s.Before.IsZero() || p.Before.Before(s.Before)) {
		s.Before = p.Before
	}
	s.Unread = s.Unread || p.Unread
	s.Starred = s.Starred || p.Starred
	s.Attachment = s.Attachment || p.Attachment
	if p.In != "" {
		s.In = p.In
	}
}

// engineTexts are searches of the engine's operators alone, as typed.
var engineTexts = []string{
	"", "budget", "budget  report", `"budget report"`, "from:maya", `from:"Maya Lin" budget`,
	`from:"Maya Lin`, `"an open phrase`, `subject:“Q3 plan” is:unread`, `in:"Sent Items" lunch`,
	`is:"unread"`, `has:"attachment"`, `after:"2026-03-01"`, "after:2026-03-01 before:2026/04/01",
	"after:2026-03-09 after:2026-03-01 before:2026-04-01 before:2026-03-20", "is:foo has:pdf", "from:",
	"to：maya", `abc"def"`, "x:y", "FROM:Maya IS:Unread", `budget  "q3 plan"  from:maya in:sent has:attachment is:starred`,
	"body:invoice after:not-a-day", `"" empty`, "from:a:b", "in:sent in:archive", `“curly phrase” to:"`,
}

// The window splits a search's text where the engine does: each part read
// on its own asks what it asks in the whole, and the parts written back
// ask what the text did. Writing a filter into the text relies on both.
// An account: part is the window's: around one, the engine's operators
// split and read as they do without it.
func TestSplitQueryAgreesWithTheEngine(t *testing.T) {
	for _, text := range engineTexts {
		for _, typed := range []string{text, "account:home " + text, `ACCOUNT："Home Mail" ` + text} {
			want := mail.ParseSearch(text, searchZone)
			parts := splitQuery(typed)
			var merged mail.Search
			for _, p := range parts {
				if !p.account {
					mergeSearch(&merged, p.search.Search)
				}
			}
			if !reflect.DeepEqual(merged, want) {
				t.Errorf("%q: the parts ask %+v, the text %+v", typed, merged, want)
			}
			if got := parseQuery(typed); !reflect.DeepEqual(got.Search, want) || len(got.Accounts) != len(parts)-len(splitQuery(text)) {
				t.Errorf("%q reads as %+v, want %+v", typed, got, want)
			}
			if again := parseQuery(joinQuery(parts)); !reflect.DeepEqual(again, parseQuery(typed)) {
				t.Errorf("%q written back as %q asks %+v, not %+v", typed, joinQuery(parts), again, parseQuery(typed))
			}
		}
	}
}

// account: is read as the other operators are: in any case, after the
// full-width colon, with a quoted value; one without a value narrows
// nothing, and none of it is text for the engine to find.
func TestAccountOperator(t *testing.T) {
	for _, c := range []struct {
		text     string
		accounts []string
		search   mail.Search
	}{
		{"account:home", []string{"home"}, mail.Search{}},
		{"Account：Home pergola", []string{"Home"}, mail.Search{Text: []string{"pergola"}}},
		{`account:"Home Mail" from:maya`, []string{"Home Mail"}, mail.Search{From: []string{"maya"}}},
		{`account:“ann@home.example”`, []string{"ann@home.example"}, mail.Search{}},
		{"account: pergola", nil, mail.Search{Text: []string{"pergola"}}},
		{`account:"home`, []string{"home"}, mail.Search{}},
		{"account:work account:home in:sent", []string{"work", "home"}, mail.Search{In: "sent"}},
		{`"account:home"`, nil, mail.Search{Text: []string{"account:home"}}},
	} {
		got := parseQuery(c.text)
		if !reflect.DeepEqual(got.Accounts, c.accounts) || !reflect.DeepEqual(got.Search, c.search) {
			t.Errorf("%q = %+v, want %v %+v", c.text, got, c.accounts, c.search)
		}
	}
	if n := filterCount("account:home budget account:"); n != 1 {
		t.Errorf("filters of an account = %d", n)
	}
	if got := withFilter("account:work budget", func(s searchQuery) bool { return len(s.Accounts) > 0 }, operator("account", "Home Mail")); got != `budget account:"Home Mail"` {
		t.Errorf("an account written = %q", got)
	}
}

// A filter written into the text takes the place of the filters of its
// kind and goes after the rest; free text, phrases and other filters stay
// as they were typed, and what is quoted stays quoted.
func TestWritingFilters(t *testing.T) {
	from := func(s searchQuery) bool { return len(s.From) > 0 }
	in := func(s searchQuery) bool { return s.In != "" }
	unread := func(s searchQuery) bool { return s.Unread }
	for _, c := range []struct {
		text string
		drop func(searchQuery) bool
		add  string
		want string
	}{
		{"budget from:maya is:unread", from, "from:lin", "budget is:unread from:lin"},
		{"budget", from, operator("from", "Maya Lin"), `budget from:"Maya Lin"`},
		{`"q3 plan" in:sent`, in, "", `"q3 plan"`},
		{`"from:maya" from:lin`, from, "", `"from:maya"`},
		{"is:UNREAD budget is:unread", unread, "", "budget"},
		{`"an open phrase`, unread, "is:unread", `"an open phrase" is:unread`},
		{`from:"Maya`, from, "", ""},
		{"is:foo", unread, "is:unread", "is:foo is:unread"},
	} {
		got := withFilter(c.text, c.drop, c.add)
		if got != c.want {
			t.Errorf("withFilter(%q, %q) = %q, want %q", c.text, c.add, got, c.want)
		}
	}
	if got := mail.ParseSearch(withFilter(`"an open phrase`, unread, "is:unread"), searchZone); !got.Unread || len(got.Text) != 1 {
		t.Errorf("a filter written after an open phrase went into it: %+v", got)
	}
	if got := withoutPart("budget from:maya is:unread", 1); got != "budget is:unread" {
		t.Errorf("withoutPart = %q", got)
	}
	for _, c := range [][3]string{
		{"from", "maya", "from:maya"},
		{"from", " Maya Lin ", `from:"Maya Lin"`},
		{"from", `"Maya"`, "from:Maya"},
		{"subject", `say “hi” now`, `subject:"say hi now"`},
		{"in", "Sent Items", `in:"Sent Items"`},
		{"from", "  ", ""},
	} {
		if got := operator(c[0], c[1]); got != c[2] {
			t.Errorf("operator(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, searchZone)
	if got := mail.ParseSearch(dayOperator("after", day), searchZone); !got.Since.Equal(day) {
		t.Errorf("after: reads back as %v, want %v", got.Since, day)
	}
}

// A result says in words where its terms were found: a message the parts
// of it, a conversation how many of its messages and where.
func TestMatchedLines(t *testing.T) {
	if got := matchedLine(mail.FieldBody | mail.FieldFrom); got != "Matched: from, body" {
		t.Errorf("message = %q", got)
	}
	if got := matchedLine(0); got != "" {
		t.Errorf("a search of flags alone = %q", got)
	}
	c := mail.Conversation{Found: []mail.Match{{Fields: mail.FieldBody}, {Fields: mail.FieldSubject | mail.FieldBody}}}
	if got := convMatchedLine(c); got != "Matched in 2 messages: subject, body" {
		t.Errorf("conversation = %q", got)
	}
	c.Found = c.Found[:1]
	if got := convMatchedLine(c); got != "Matched in 1 message: body" {
		t.Errorf("one message = %q", got)
	}
}
