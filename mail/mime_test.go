package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParsePlainMessage(t *testing.T) {
	m, err := ParseMessage(readFixture(t, "plain.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Plain message" || m.MessageID != "plain-1@example.com" {
		t.Errorf("subject/id = %q %q", m.Subject, m.MessageID)
	}
	if m.InReplyTo != "previous@example.com" {
		t.Errorf("in reply to = %q", m.InReplyTo)
	}
	if len(m.References) != 2 || m.References[0] != "root@example.com" {
		t.Errorf("references = %v", m.References)
	}
	if m.From.Name != "Alice Example" || m.From.Email != "alice@example.com" {
		t.Errorf("from = %+v", m.From)
	}
	if len(m.To) != 1 || m.To[0].Email != "bob@example.com" {
		t.Errorf("to = %+v", m.To)
	}
	if len(m.Cc) != 1 || m.Cc[0].Email != "carol@example.com" {
		t.Errorf("cc = %+v", m.Cc)
	}
	if !strings.Contains(m.BodyText, "This is a plain text message.") {
		t.Errorf("body = %q", m.BodyText)
	}
	if m.BodyHTML != "" {
		t.Errorf("html = %q", m.BodyHTML)
	}
	if !strings.Contains(m.Preview, "Hello Bob") {
		t.Errorf("preview = %q", m.Preview)
	}
	want := time.Date(2023, 1, 3, 10, 20, 0, 0, time.UTC)
	if !m.Date.Equal(want) {
		t.Errorf("date = %v, want %v", m.Date, want)
	}
}

func TestParseHTMLOnlyExtractsText(t *testing.T) {
	m, err := ParseMessage(readFixture(t, "html-only.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.BodyHTML == "" {
		t.Fatal("html body is empty")
	}
	for _, want := range []string{"Weekly", "One bold idea.", "First", "Second"} {
		if !strings.Contains(m.BodyText, want) {
			t.Errorf("text missing %q:\n%s", want, m.BodyText)
		}
	}
	for _, unwanted := range []string{"color:red", "alert", "<b>"} {
		if strings.Contains(m.BodyText, unwanted) {
			t.Errorf("text has %q:\n%s", unwanted, m.BodyText)
		}
	}
}

func TestParseAlternativeAndEncodedHeaders(t *testing.T) {
	m, err := ParseMessage(readFixture(t, "alternative.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "项目进度" {
		t.Errorf("encoded subject = %q", m.Subject)
	}
	if m.From.Name != "Maya Chen" {
		t.Errorf("from name = %q", m.From.Name)
	}
	if !strings.Contains(m.BodyText, "Plain version.") {
		t.Errorf("preferred text = %q", m.BodyText)
	}
	if !strings.Contains(m.BodyHTML, "<b>version</b>") {
		t.Errorf("html = %q", m.BodyHTML)
	}

	enc, err := ParseMessage(readFixture(t, "encoded.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if enc.Subject != "Grüße" || enc.From.Name != "Jürgen" {
		t.Errorf("charset headers = %q %q", enc.Subject, enc.From.Name)
	}
	if !strings.Contains(enc.BodyText, "Café und Grüße.") {
		t.Errorf("charset body = %q", enc.BodyText)
	}
}

func TestParseAttachment(t *testing.T) {
	m, err := ParseMessage(readFixture(t, "attachment.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %+v", m.Attachments)
	}
	a := m.Attachments[0]
	if a.Filename != "report.pdf" || a.ContentType != "application/pdf" {
		t.Errorf("attachment = %+v", a)
	}
	if string(a.Data) != "%PDF-1.4 test" {
		t.Errorf("data = %q", a.Data)
	}
	if a.ContentHash == "" || a.Size != int64(len(a.Data)) {
		t.Errorf("hash/size = %q %d", a.ContentHash, a.Size)
	}
	if !m.HasAttachments {
		t.Error("HasAttachments is false")
	}
	if !strings.Contains(m.BodyText, "Please see") {
		t.Errorf("body = %q", m.BodyText)
	}
}

func TestParseTruncatesHugeBody(t *testing.T) {
	body := strings.Repeat("x", maxBodyPart+100)
	raw := "From: a@example.com\r\nSubject: big\r\nContent-Type: text/plain\r\n\r\n" + body
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !m.BodyTruncated {
		t.Error("BodyTruncated is false")
	}
	if len(m.BodyText) != maxBodyPart {
		t.Errorf("body length = %d, want %d", len(m.BodyText), maxBodyPart)
	}
}

func TestPreviewIsOneLineAndBounded(t *testing.T) {
	long := strings.Repeat("word ", 100) + "\nnext\tline"
	p := preview(normalizeText(long))
	if strings.ContainsAny(p, "\n\t") {
		t.Errorf("preview has control whitespace: %q", p)
	}
	if len([]rune(p)) > 181 {
		t.Errorf("preview is too long: %d runes", len([]rune(p)))
	}
	if !strings.HasSuffix(p, "…") {
		t.Errorf("long preview lacks an ellipsis: %q", p)
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"paragraphs", "<p>One</p><p>Two</p>", "One\nTwo"},
		{"breaks", "a<br>b", "a\nb"},
		{"lists", "<ul><li>a</li><li>b</li></ul>", "a\nb"},
		{"entities", "<p>a &amp; b &lt;c&gt;</p>", "a & b <c>"},
		{"script", "<script>bad()</script><style>x</style><p>ok</p>", "ok"},
		{"blank lines", "<p>a</p><div><br></div><p>b</p>", "a\nb"},
		{"table", "<table><tr><td>a</td><td>b</td></tr></table>", "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToText(tt.in); got != tt.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAddressListParsing(t *testing.T) {
	list, err := ParseAddressList([]string{"Ann <ann@example.com>, bob@example.com", "carl@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "Ann" || list[2].Email != "carl@example.com" {
		t.Errorf("addresses = %+v", list)
	}
	if _, err := ParseAddressList([]string{"not an address"}); err == nil {
		t.Error("invalid address parsed")
	}
}

func TestFolderSpecialUse(t *testing.T) {
	tests := []struct {
		name  string
		attrs []string
		want  string
	}{
		{"INBOX", []string{`\Inbox`}, SpecialInbox},
		{"Anything", []string{`\Sent`}, SpecialSent},
		{"INBOX", nil, SpecialInbox},
		{"Sent Items", nil, SpecialSent},
		{"[Gmail]/Sent Mail", nil, SpecialSent},
		{"Deleted Items", nil, SpecialTrash},
		{"Junk E-mail", nil, SpecialJunk},
		{"All Mail", nil, SpecialArchive},
		{"Starred", nil, SpecialFlagged},
		{"Projects", nil, ""},
	}
	for _, tt := range tests {
		if got := folderSpecialUse(tt.name, tt.attrs); got != tt.want {
			t.Errorf("folderSpecialUse(%q, %v) = %q, want %q", tt.name, tt.attrs, got, tt.want)
		}
	}
	if !hasNoSelect([]string{`\Noselect`}) || hasNoSelect([]string{`\Sent`}) {
		t.Error("hasNoSelect is wrong")
	}
}
