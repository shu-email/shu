package mail

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// recipients is what the fake SMTP server was told to deliver to.
func (f *fakeSMTP) recipients() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rcpts...)
}

// headerLines returns the lines of a raw message that start a header,
// folded continuations left out, across the message and all its parts.
// It is deliberately crude: any line of the form "Name: value" counts,
// so a header smuggled anywhere shows up.
func headerNames(raw string) []string {
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		name, _, found := strings.Cut(line, ":")
		if !found || strings.ContainsAny(name, " \t") {
			continue
		}
		names = append(names, strings.ToLower(name))
	}
	return names
}

// A line holding only a dot ends an SMTP message. The text a user types,
// and the bytes of a file, are data: whatever they contain, the server
// must get one message, to the recipients chosen, with the text intact.
func TestMessageTextCannotEndTheMessage(t *testing.T) {
	body := strings.Join([]string{
		"Before.",
		".",
		"..two dots",
		".",
		"MAIL FROM:<evil@example.com>",
		"RCPT TO:<victim@example.com>",
		"DATA",
		"Subject: smuggled",
		"",
		"smuggled body",
		".",
		"After.",
	}, "\n")
	for _, withFile := range []bool{false, true} {
		name := "text only"
		if withFile {
			name = "with a file"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			env := newAttachEnv(t, nil)
			d := Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Dots", Body: body}
			if withFile {
				d, _ = env.attach(t, d, env.file(t, "dots.txt", []byte(".\r\n.\r\nQUIT\r\n.\r\n")))
			} else {
				d = mustSaveDraft(t, env.svc, d)
			}
			out := d.outgoing()
			out.DraftID = d.ID
			if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
				t.Fatal(err)
			}
			received := env.smtp.received()
			if len(received) != 1 {
				t.Fatalf("the server got %d messages, want 1", len(received))
			}
			if got := env.smtp.recipients(); !slices.Equal(got, []string{"bob@example.com"}) {
				t.Errorf("recipients = %v", got)
			}
			m, err := ParseMessage([]byte(received[0]))
			if err != nil {
				t.Fatal(err)
			}
			if m.BodyText != body {
				t.Errorf("body arrived as\n%q\nwant\n%q", m.BodyText, body)
			}
			if m.Subject != "Dots" {
				t.Errorf("subject = %q", m.Subject)
			}
			if withFile {
				if len(m.Attachments) != 1 || string(m.Attachments[0].Data) != ".\r\n.\r\nQUIT\r\n.\r\n" {
					t.Errorf("attachment = %+v", m.Attachments)
				}
			}
		})
	}
}

// A file's name goes into the headers of its part. Names come from the
// file system and from other mail clients' drafts, and may hold anything,
// line breaks included: they must never become a header of their own.
func TestAttachmentNamesCannotAddHeaders(t *testing.T) {
	names := []string{
		"a\r\nBcc: evil@example.com\r\n\r\nbody.txt",
		"b\nX-Injected: yes.txt",
		"c\rX-Injected: yes.txt",
		"d\x00e\x7f\u0085.txt",
		`quote".txt`,
		`back\slash;semi.txt`,
		"=?UTF-8?B?eA==?=.txt",
		"tab\there.txt",
		strings.Repeat("长", 300) + ".txt",
		strings.Repeat("x", 2000) + ".txt",
		"",
	}
	types := []string{
		"application/pdf",
		"text/plain\r\nBcc: evil@example.com",
		"text/plain; charset=\"a\r\nX-Injected: yes\"",
		"application/pdf\nX-Injected: yes",
		"",
	}
	for _, name := range names {
		for _, kind := range types {
			contentType, disposition := attachmentHeader(name, kind)
			for what, value := range map[string]string{"Content-Type": contentType, "Content-Disposition": disposition} {
				for i, line := range strings.Split(value, "\r\n") {
					if len(line) > 998 {
						t.Errorf("%s of %q: a line of %d bytes", what, name, len(line))
					}
					if i > 0 && !strings.HasPrefix(line, " ") {
						t.Errorf("%s of %q: line %q is not a continuation", what, name, line)
					}
				}
				// Unfolded, nothing may be left that breaks a line.
				unfolded := strings.ReplaceAll(value, "\r\n ", " ")
				if strings.ContainsFunc(unfolded, unicode.IsControl) {
					t.Errorf("%s of %q with type %q holds a control character: %q", what, name, kind, value)
				}
			}
			// The name read back is the name without what cannot be shown.
			_, params, err := mime.ParseMediaType(strings.ReplaceAll(disposition, "\r\n ", " "))
			if err != nil {
				t.Errorf("disposition of %q does not parse: %v\n%s", name, err, disposition)
				continue
			}
			if got, want := params["filename"], cleanFilename(name); got != want {
				t.Errorf("name read back = %q, want %q", got, want)
			}
			if strings.ContainsAny(cleanFilename(name), "\r\n") {
				t.Errorf("a line break survived cleaning %q", name)
			}
		}
	}
}

// The same from end to end, with a file whose name has line breaks in it
// and with a draft taken over from another mail client.
func TestHostileAttachmentNameIsSentAsAName(t *testing.T) {
	ctx := context.Background()
	check := func(t *testing.T, env *attachEnv, d Draft) {
		t.Helper()
		if len(d.Attachments) != 1 {
			t.Fatalf("attachments = %+v", d.Attachments)
		}
		out := d.outgoing()
		out.DraftID = d.ID
		if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
			t.Fatal(err)
		}
		received := env.smtp.received()
		if len(received) != 1 {
			t.Fatalf("the server got %d messages", len(received))
		}
		if got := env.smtp.recipients(); !slices.Equal(got, []string{"bob@example.com"}) {
			t.Errorf("recipients = %v: the name added one", got)
		}
		for _, name := range headerNames(received[0]) {
			if name == "bcc" || name == "x-injected" {
				t.Errorf("the name became the header %q:\n%s", name, received[0])
			}
		}
		m, err := ParseMessage([]byte(received[0]))
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Cc) != 0 || len(m.To) != 1 || m.To[0].Email != "bob@example.com" {
			t.Errorf("to %v cc %v", m.To, m.Cc)
		}
		if len(m.Attachments) != 1 || strings.ContainsAny(m.Attachments[0].Filename, "\r\n") ||
			!strings.Contains(m.Attachments[0].Filename, "evil@example.com") {
			t.Errorf("attachment = %+v", m.Attachments)
		}
		// The copy saved in Sent is the same message.
		for _, sent := range env.imap.messages(t, "Sent") {
			for _, name := range headerNames(sent.Raw) {
				if name == "bcc" || name == "x-injected" {
					t.Errorf("the sent copy has the header %q", name)
				}
			}
		}
	}

	t.Run("a file on disk", func(t *testing.T) {
		env := newAttachEnv(t, nil)
		path := filepath.Join(t.TempDir(), "report\r\nBcc: evil@example.com\r\nX-Injected: yes\r\n\r\n.pdf")
		if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0o600); err != nil {
			t.Skipf("this file system does not take a line break in a name: %v", err)
		}
		d, att := env.attach(t, Draft{AccountID: env.a.ID, To: "bob@example.com", Subject: "Report"}, path)
		if strings.ContainsFunc(att.Filename, unicode.IsControl) {
			t.Errorf("stored name = %q", att.Filename)
		}
		// The draft's copy on the server is built the same way.
		mustSyncDrafts(t, env.svc, env.a.ID)
		for _, m := range env.imap.messages(t, "Drafts") {
			for _, name := range headerNames(m.Raw) {
				if name == "bcc" || name == "x-injected" {
					t.Errorf("the draft's server copy has the header %q", name)
				}
			}
		}
		check(t, env, d)
	})

	t.Run("a draft from another mail client", func(t *testing.T) {
		env := newAttachEnv(t, nil)
		raw := strings.Join([]string{
			"From: Ann <ann@example.com>",
			"To: Bob <bob@example.com>",
			"Subject: From elsewhere",
			"Message-ID: <hostile-1@example.com>",
			"Date: Tue, 03 Jan 2023 10:20:00 +0000",
			"MIME-Version: 1.0",
			`Content-Type: multipart/mixed; boundary="b1"`,
			"",
			"--b1",
			"Content-Type: text/plain; charset=utf-8",
			"",
			"Body.",
			"--b1",
			`Content-Type: application/pdf; name="x.pdf"`,
			"Content-Disposition: attachment;",
			" filename*=UTF-8''a%0D%0ABcc%3A%20evil%40example.com%0D%0AX-Injected%3A%20yes%0D%0A%0D%0A.pdf",
			"Content-Transfer-Encoding: base64",
			"",
			"JVBERi0xLjQK",
			"--b1--",
			"",
		}, "\r\n")
		env.imap.append(t, "Drafts", []byte(raw))
		if err := env.svc.SyncFolder(ctx, env.a.ID, "Drafts", 50); err != nil {
			t.Fatal(err)
		}
		d, err := env.svc.AdoptDraft(ctx, env.a.ID, "Drafts", 1)
		if err != nil {
			t.Fatal(err)
		}
		check(t, env, d)
	})
}

// The Bcc header of a Sent copy, and of a draft's server copy, is written
// here from addresses the user typed. A name is encoded whatever it holds,
// an address that does not read back as itself is left out, and nothing
// adds a header or ends the header early.
func TestBccHeaderCannotAddHeaders(t *testing.T) {
	bcc := []Address{
		{Name: "Eve\r\nX-Injected: yes", Email: "eve@example.com"},
		{Name: `"Quoted" <name>; and, more`, Email: "q@example.com"},
		{Email: "x@evil.example>\r\nX-Injected: yes"},
		{Email: "x@evil.example\r\n\r\nbody"},
		{Name: "长的名字" + strings.Repeat("长", 80), Email: "long@example.com"},
	}
	value := headerAddresses(bcc)
	for i, line := range strings.Split(value, "\r\n") {
		if len(line) > 998 || (i > 0 && !strings.HasPrefix(line, " ")) {
			t.Errorf("line %d of the header = %q", i, line)
		}
	}
	if unfolded := strings.ReplaceAll(value, "\r\n ", " "); strings.ContainsFunc(unfolded, unicode.IsControl) {
		t.Errorf("the header holds a control character: %q", value)
	}
	raw := "From: ann@example.com\r\nTo: bob@example.com\r\nSubject: x\r\n\r\nBody\r\n"
	kept := strings.Join(func() []string {
		var parts []string
		for _, p := range sentCopy([]byte(raw), bcc) {
			parts = append(parts, string(p))
		}
		return parts
	}(), "")
	if names := headerNames(kept); slices.Contains(names, "x-injected") || !slices.Contains(names, "bcc") {
		t.Errorf("headers of the Sent copy = %v:\n%s", names, kept)
	}
	if !strings.HasSuffix(kept, "\r\n\r\nBody\r\n") || !strings.HasPrefix(kept, "From: ann@example.com\r\n") {
		t.Errorf("the Sent copy moved the body:\n%s", kept)
	}
	named, _ := draftHeaders([]byte(kept))
	want := []Address{bcc[0], bcc[1], bcc[4]}
	if !slices.Equal(named, want) {
		t.Errorf("the Sent copy names %+v, want %+v", named, want)
	}
	if got := sentCopy([]byte(raw), nil); len(got) != 1 || string(got[0]) != raw {
		t.Errorf("a message without Bcc is saved as %q", got)
	}
}

// A message goes from one of the account's identities, whatever the From
// it comes with: that is the engine's to set, so a caller cannot put an
// address of its own, or a header, there.
func TestFromIsTheIdentitys(t *testing.T) {
	ctx := context.Background()
	env := newAttachEnv(t, nil)
	out := Outgoing{
		To: []Address{{Email: "bob@example.com"}}, Subject: "Hi", Text: "Hello",
		From: Address{Name: "Boss\r\nBcc: evil@example.com", Email: "boss@evil.example"},
	}
	if _, err := env.svc.Send(ctx, env.a.ID, out); err != nil {
		t.Fatal(err)
	}
	received := env.smtp.received()
	if len(received) != 1 {
		t.Fatalf("delivered %d messages", len(received))
	}
	if got := env.smtp.senders(); !slices.Equal(got, []string{env.a.Email}) {
		t.Errorf("envelope sender = %v", got)
	}
	if got := env.smtp.recipients(); !slices.Equal(got, []string{"bob@example.com"}) {
		t.Errorf("recipients = %v", got)
	}
	m, err := ParseMessage([]byte(received[0]))
	if err != nil || m.From != (Address{Name: env.a.Name, Email: env.a.Email}) || strings.Contains(received[0], "evil") {
		t.Errorf("sent from %+v, %v:\n%s", m.From, err, received[0])
	}
}
