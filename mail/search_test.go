package mail

import (
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestParseSearch(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, shanghai) }
	for _, tc := range []struct {
		in   string
		want Search
	}{
		{"", Search{}},
		{"   ", Search{}},
		{"budget", Search{Text: []string{"budget"}}},
		{"budget  march\tq3", Search{Text: []string{"budget", "march", "q3"}}},
		{`"budget march" q3`, Search{Text: []string{"budget march", "q3"}}},
		{`“预算 三月”`, Search{Text: []string{"预算 三月"}}},
		{`"unterminated phrase`, Search{Text: []string{"unterminated phrase"}}},
		{`""`, Search{}},
		{"from:maya", Search{From: []string{"maya"}}},
		{`FROM:Maya`, Search{From: []string{"Maya"}}},
		{`from:"Maya Lin" to:bob@example.com`, Search{From: []string{"Maya Lin"}, To: []string{"bob@example.com"}}},
		{"from:maya from:lin", Search{From: []string{"maya", "lin"}}},
		{"subject:report body:invoice", Search{Subject: []string{"report"}, Body: []string{"invoice"}}},
		{`subject:"Re: Q3"`, Search{Subject: []string{"Re: Q3"}}},
		{"has:attachment is:unread is:starred", Search{Attachment: true, Unread: true, Starred: true}},
		{"IS:Unread HAS:Attachment", Search{Attachment: true, Unread: true}},
		{"in:Sent", Search{In: "Sent"}},
		{`in:"Sent Items" budget`, Search{In: "Sent Items", Text: []string{"budget"}}},
		{"in:Archive in:Sent", Search{In: "Sent"}},
		{"after:2026-03-01 before:2026-03-08", Search{Since: day(2026, 3, 1), Before: day(2026, 3, 8)}},
		{"after:2026/03/01", Search{Since: day(2026, 3, 1)}},
		{"after:2026-03-01 after:2026-02-01 before:2026-05-01 before:2026-04-01", Search{Since: day(2026, 3, 1), Before: day(2026, 4, 1)}},
		// Chinese, with the colon and quotes its input types.
		{"项目进度 from:张三", Search{Text: []string{"项目进度"}, From: []string{"张三"}}},
		{"from：张三 subject：发票", Search{From: []string{"张三"}, Subject: []string{"发票"}}},
		{"发票", Search{Text: []string{"发票"}}},
		// Colons inside words are text, and so is an operator nobody knows.
		{"10:30 re:meeting http://example.com", Search{Text: []string{"10:30", "re:meeting", "http://example.com"}}},
		{"cc:maya label:work", Search{Text: []string{"cc:maya", "label:work"}}},
		{"from:maya:x", Search{From: []string{"maya:x"}}},
		{":leading", Search{Text: []string{":leading"}}},
		// A value an operator does not take makes it text.
		{"has:pdf is:read before:yesterday after:2026-02-30", Search{Text: []string{"has:pdf", "is:read", "before:yesterday", "after:2026-02-30"}}},
		{`has:"two words"`, Search{Text: []string{`has:"two words"`}}},
		// An operator without a value narrows nothing.
		{"from: maya", Search{Text: []string{"maya"}}},
		{`from:"" subject: in: before:`, Search{}},
		// A quoted operator is a phrase.
		{`"from:maya"`, Search{Text: []string{"from:maya"}}},
		// Characters the full-text index would read as operators are text.
		{`budget OR NEAR(x) -draft *`, Search{Text: []string{"budget", "OR", "NEAR(x)", "-draft", "*"}}},
	} {
		got := ParseSearch(tc.in, shanghai)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseSearch(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
		}
	}
}

// A day is the caller's: it starts at their midnight, and lasts as long
// as it does there, 23 hours on the day clocks go forward.
func TestParseSearchDaysInTheCallersZone(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	s := ParseSearch("after:2026-03-08 before:2026-03-09", newYork)
	if got := s.Before.Sub(s.Since); got != 23*time.Hour {
		t.Errorf("the day clocks go forward lasts %v", got)
	}
	if want := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC); !s.Since.Equal(want) {
		t.Errorf("after: starts at %v, want %v", s.Since.UTC(), want)
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseSearch("after:2026-03-08", shanghai).Since; !got.Equal(time.Date(2026, 3, 7, 16, 0, 0, 0, time.UTC)) {
		t.Errorf("after: in Shanghai starts at %v", got.UTC())
	}
	if got := ParseSearch("before:2026-03-08", nil).Before; !got.Equal(time.Date(2026, 3, 8, 0, 0, 0, 0, time.Local)) {
		t.Errorf("without a zone, a day is the local one: %v", got)
	}
}

func TestSearchIsZero(t *testing.T) {
	if !(Search{}).IsZero() || !(Search{In: "  "}).IsZero() {
		t.Error("an empty search asks something")
	}
	for _, s := range []Search{
		{Text: []string{"x"}}, {From: []string{"x"}}, {To: []string{"x"}}, {Subject: []string{"x"}}, {Body: []string{"x"}},
		{Since: time.Unix(1, 0)}, {Before: time.Unix(1, 0)}, {Unread: true}, {Starred: true}, {Attachment: true}, {In: "Sent"},
	} {
		if s.IsZero() {
			t.Errorf("%+v asks nothing", s)
		}
	}
}

func TestFolderNamedBy(t *testing.T) {
	sent := Folder{Name: "Sent Items", SpecialUse: SpecialSent}
	gmail := Folder{Name: "[Gmail]/All Mail", SpecialUse: SpecialArchive}
	inbox := Folder{Name: "INBOX", SpecialUse: SpecialInbox}
	for _, tc := range []struct {
		f    Folder
		name string
		want bool
	}{
		{sent, "Sent Items", true}, {sent, "sent items", true}, {sent, "sent", true}, {sent, "Sent Messages", true},
		{sent, "drafts", false}, {sent, "", false},
		{gmail, "archive", true}, {gmail, "all mail", true}, {gmail, "[gmail]/all mail", true},
		{inbox, "inbox", true}, {inbox, " Inbox ", true}, {inbox, "trash", false},
		{Folder{Name: "Projects"}, "projects", true}, {Folder{Name: "Projects"}, "inbox", false},
		{Folder{Name: "项目"}, "项目", true},
	} {
		if got := tc.f.NamedBy(tc.name); got != tc.want {
			t.Errorf("%q named by %q = %v", tc.f.Name, tc.name, got)
		}
	}
}

func TestScopeCheck(t *testing.T) {
	for _, sc := range []Scope{{}, AllInboxes(), AccountScope("a1", ""), AccountScope("a1", "starred"), AccountScope("a1", "INBOX"),
		{Accounts: []string{"a1", "a2"}, Folder: "INBOX"}} {
		if err := sc.Check(); err != nil {
			t.Errorf("%+v: %v", sc, err)
		}
	}
	for _, sc := range []Scope{{Folder: "INBOX", Starred: true}, {Folder: "INBOX", Special: SpecialInbox}, {Special: SpecialSent, Starred: true}} {
		if err := sc.Check(); err != ErrBadScope {
			t.Errorf("%+v = %v", sc, err)
		}
	}
	if sc := AccountScope("a1", "starred"); !sc.Starred || sc.Folder != "" || len(sc.Accounts) != 1 {
		t.Errorf("starred scope = %+v", sc)
	}
}
