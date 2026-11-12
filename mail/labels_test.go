package mail

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// atomSafe reports whether a keyword is an IMAP atom a server takes as
// it is: printable ASCII without the atom-specials, and no system flag.
func atomSafe(keyword string) bool {
	if keyword == "" || strings.HasPrefix(keyword, `\`) {
		return false
	}
	for i := 0; i < len(keyword); i++ {
		if c := keyword[i]; c <= ' ' || c >= 0x7f || strings.IndexByte(`(){%*"\]`, c) >= 0 {
			return false
		}
	}
	return true
}

// A label's keyword is its name where that is an atom, and the name
// written as MIME's Q encoding writes text elsewhere; it reads back as the
// name, whatever case a server keeps it in, and is never a keyword of the
// system or of other mail clients' own.
func TestLabelKeywordRoundTrip(t *testing.T) {
	for _, tc := range []struct{ name, keyword string }{
		{"Work", "Work"},
		{"Q3-plans", "Q3-plans"},
		{"follow.up+later:now@home!#&',;?^|~", "follow.up+later:now@home!#&',;?^|~"},
		{"US$", "US$"},
		{"To Do", "To_Do"},
		{"a  b", "a__b"},
		{"snake_case", "snake=5Fcase"},
		{"a=b", "a=3Db"},
		{"Café", "Caf=C3=A9"},
		{"工作", "=E5=B7=A5=E4=BD=9C"},
		{"项目 Q3", "=E9=A1=B9=E7=9B=AE_Q3"},
		{"(draft) 100% *done* \"quoted\" back\\slash [x]{y}", "=28draft=29_100=25_=2Adone=2A_=22quoted=22_back=5Cslash_=5Bx=5D=7By=7D"},
		{"a/b", "a=2Fb"},
		{"😀", "=F0=9F=98=80"},
		// The keywords of the system and of other mail clients.
		{"$label1", "=24label1"},
		{"$Forwarded", "=24Forwarded"},
		{"$100", "=24100"},
		{"Junk", "=4Aunk"},
		{"nonjunk", "=6Eonjunk"},
		{"NotJunk", "=4EotJunk"},
		{"JunkRecorded", "=4AunkRecorded"},
		{"Junk mail", "Junk_mail"},
	} {
		got := LabelKeyword(tc.name)
		if got != tc.keyword {
			t.Errorf("LabelKeyword(%q) = %q, want %q", tc.name, got, tc.keyword)
		}
		if !atomSafe(got) || !IsLabelKeyword(got) {
			t.Errorf("the keyword of %q, %q, is no label's atom", tc.name, got)
		}
		for _, kept := range []string{got, strings.ToLower(got), strings.ToUpper(got)} {
			back := LabelName(kept)
			if kept == got && back != tc.name {
				t.Errorf("LabelName(%q) = %q, want %q", kept, back, tc.name)
			}
			if LabelNameKey(back) != LabelNameKey(tc.name) && strings.ToLower(back) != strings.ToLower(tc.name) {
				t.Errorf("kept as %q by a server, %q reads back as %q", kept, tc.name, back)
			}
		}
	}
	// What no label can be called still reads as a name.
	for _, name := range []string{"Work", "To Do", "工作", "$x", "Junk"} {
		if back, err := CleanLabelName(LabelName(LabelKeyword(name))); err != nil || back != name {
			t.Errorf("%q reads back as %q, %v", name, back, err)
		}
	}
}

// Keywords found on the server are labels, but the system's and those
// other mail clients set for their own use; Thunderbird's tags have its
// names and colors, and a keyword no label wrote is its own name.
func TestKeywordsThatAreLabels(t *testing.T) {
	for _, kw := range []string{
		`\Seen`, `\Answered`, `\Flagged`, `\Deleted`, `\Draft`, `\Recent`, `\*`,
		"$Forwarded", "$forwarded", "$MDNSent", "$Junk", "$NotJunk", "Junk", "NonJunk", "junk", "NotJunk",
		"$Phishing", "$NotPhishing", "$Submitted", "$SubmitPending", "$MailFlagBit0", "$mailflagbit1", "$MailFlagBit2",
		"", "two words", "tab\tbed", "ünïcödé", "par(en", "per%cent", "star*", `quo"te`, "brack]et", "{x",
		strings.Repeat("x", maxKeyword+1),
	} {
		if IsLabelKeyword(kw) {
			t.Errorf("%q is taken for a label", kw)
		}
	}
	for _, kw := range []string{"Work", "work", "$label1", "$Important", "clients", "=E5=B7=A5", "&xevpxa-", strings.Repeat("x", maxKeyword)} {
		if !IsLabelKeyword(kw) {
			t.Errorf("%q is not taken for a label", kw)
		}
	}
	for _, tc := range []struct {
		keyword, name string
		color         LabelColor
	}{
		{"$label1", "Important", LabelRed}, {"$Label2", "Work", LabelOrange}, {"$LABEL3", "Personal", LabelGreen},
		{"$label4", "To Do", LabelBlue}, {"$label5", "Later", LabelPurple},
		{"$label6", "$label6", ""}, {"$Important", "$Important", ""},
		{"clients", "clients", ""}, {"project_x", "project x", ""},
		// Thunderbird's keys of tags with other names, and what no label
		// wrote: as they are.
		{"&xevpxa-", "&xevpxa-", ""}, {"a=zz", "a=zz", ""}, {"a=4", "a=4", ""}, {"=FF", "=FF", ""},
		{"_lead", "_lead", ""}, {"=00x", "=00x", ""},
		{strings.Repeat("y", 100), strings.Repeat("y", MaxLabelName), ""},
	} {
		if got := LabelName(tc.keyword); got != tc.name {
			t.Errorf("LabelName(%q) = %q, want %q", tc.keyword, got, tc.name)
		}
		if got := KeywordColor(tc.keyword); got != tc.color {
			t.Errorf("KeywordColor(%q) = %q, want %q", tc.keyword, got, tc.color)
		}
		if _, err := CleanLabelName(LabelName(tc.keyword)); err != nil {
			t.Errorf("the label of %q has no name a label can have", tc.keyword)
		}
	}
}

func TestLabelNames(t *testing.T) {
	for in, want := range map[string]string{"  Work ": "Work", "To Do": "To Do", "工作": "工作", strings.Repeat("é", MaxLabelName): strings.Repeat("é", MaxLabelName)} {
		if got, err := CleanLabelName(in); err != nil || got != want {
			t.Errorf("CleanLabelName(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "   ", "a\tb", "line\nbreak", "bell\a", strings.Repeat("x", MaxLabelName+1), "bad\xffutf8"} {
		if _, err := CleanLabelName(in); !errors.Is(err, ErrLabelName) {
			t.Errorf("CleanLabelName(%q) = %v", in, err)
		}
	}
	for _, same := range [][2]string{{"Work", "WORK"}, {"Ärger", "ärger"}, {"Straße", "STRAßE"}, {"K", "\u212a"}, {"Σίσυφος", "ΣΊΣΥΦΟΣ"}} {
		if LabelNameKey(same[0]) != LabelNameKey(same[1]) {
			t.Errorf("%q and %q are two names", same[0], same[1])
		}
	}
	for _, other := range [][2]string{{"Work", "Works"}, {"工作", "工 作"}, {"a", "á"}} {
		if LabelNameKey(other[0]) == LabelNameKey(other[1]) {
			t.Errorf("%q and %q are one name", other[0], other[1])
		}
	}
	taken := map[string]bool{LabelNameKey("Work"): true, LabelNameKey("work 2"): true}
	if got := FreeLabelName("WORK", func(k string) bool { return taken[k] }); got != "WORK 3" {
		t.Errorf("a name taken twice = %q", got)
	}
	long := strings.Repeat("x", MaxLabelName)
	taken[LabelNameKey(long)] = true
	if got := FreeLabelName(long, func(k string) bool { return taken[k] }); utf8.RuneCountInString(got) > MaxLabelName || !strings.HasSuffix(got, " 2") {
		t.Errorf("a long name taken = %q", got)
	}
	used := map[string]bool{"work": true, "work-2": true}
	if got := FreeKeyword("Work", func(k string) bool { return used[k] }); got != "Work-3" {
		t.Errorf("a keyword taken twice = %q", got)
	}
	if got := FreeKeyword("To_Do", func(k string) bool { return used[k] }); got != "To_Do" {
		t.Errorf("a free keyword = %q", got)
	}
	for i, c := range LabelColors {
		if !c.Valid() || LabelColorAt(i) != c || LabelColorAt(i+len(LabelColors)) != c {
			t.Errorf("color %d = %q", i, c)
		}
	}
	if LabelColor("mauve").Valid() || LabelColor("").Valid() {
		t.Error("a color of no palette is valid")
	}
}

// A folder keeps a keyword when its PERMANENTFLAGS take any or name it;
// one never opened to change says nothing, and an account's label is
// kept here only when the folders that said something keep none of it.
func TestFoldersThatKeepLabels(t *testing.T) {
	unknown := Folder{Name: "Unknown"}
	any := Folder{Name: "Any", PermanentFlags: []string{`\Seen`, `\Flagged`, `\*`}}
	none := Folder{Name: "None", PermanentFlags: []string{}}
	system := Folder{Name: "System", PermanentFlags: []string{`\Seen`, `\Deleted`}}
	listed := Folder{Name: "Listed", PermanentFlags: []string{`\Seen`, "$label1", "Work"}}
	for _, tc := range []struct {
		f                    Folder
		known, newOnes, work bool
	}{
		{unknown, false, false, false}, {any, true, true, true}, {none, true, false, false},
		{system, true, false, false}, {listed, true, false, true},
	} {
		if tc.f.KeywordsKnown() != tc.known || tc.f.KeepsNewKeywords() != tc.newOnes || tc.f.KeepsKeyword("WORK") != tc.work {
			t.Errorf("%s: known %v, new %v, work %v", tc.f.Name, tc.f.KeywordsKnown(), tc.f.KeepsNewKeywords(), tc.f.KeepsKeyword("WORK"))
		}
	}
	if !listed.KeepsKeyword("$Label1") || listed.KeepsKeyword("Personal") {
		t.Error("a folder that lists its keywords keeps others")
	}
	for _, tc := range []struct {
		folders []Folder
		keyword string
		local   bool
	}{
		{nil, "Work", false},
		{[]Folder{unknown}, "Work", false},
		{[]Folder{none, system}, "Work", true},
		{[]Folder{none, unknown}, "Work", true},
		{[]Folder{none, any}, "Work", false},
		{[]Folder{none, listed}, "Work", false},
		{[]Folder{none, listed}, "Personal", true},
	} {
		if got := KeptLocally(tc.folders, tc.keyword); got != tc.local {
			t.Errorf("%s in %d folders: kept locally %v", tc.keyword, len(tc.folders), got)
		}
	}
	for _, f := range []Folder{{SpecialUse: SpecialInbox}, {SpecialUse: SpecialSent}, {SpecialUse: SpecialArchive}, {}} {
		if !f.InLabelScope() {
			t.Errorf("a label's list leaves out %q", f.SpecialUse)
		}
	}
	for _, special := range []string{SpecialJunk, SpecialTrash, SpecialDrafts} {
		if (Folder{SpecialUse: special}).InLabelScope() {
			t.Errorf("a label's list shows %s", special)
		}
	}
}
