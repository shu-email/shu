package mail

import (
	"strings"
	"testing"
)

// The headers a message keeps include what its servers said about the
// sender, so that the reading pane can explain it.
func TestParseMessageKeepsAuthenticationResults(t *testing.T) {
	raw := strings.Join([]string{
		"Authentication-Results: mx.example; spf=pass smtp.mailfrom=shop.example; dkim=pass header.d=shop.example; dmarc=pass action=none header.from=shop.example",
		"Received-SPF: pass (mx.example: domain of shop.example designates 192.0.2.1)",
		"DKIM-Signature: v=1; a=rsa-sha256; d=shop.example; s=mail",
		"Delivered-To: ann@example.com",
		"From: Shop <news@shop.example>",
		"To: ann@example.com",
		"Subject: Hi",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hello",
	}, "\r\n")
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Authentication-Results", "Received-SPF", "DKIM-Signature", "Delivered-To"} {
		if len(headerValues(m.RawHeaders, name)) == 0 {
			t.Errorf("%s not kept in %q", name, m.RawHeaders)
		}
	}
}

func TestTrustReadsTheBoundaryResults(t *testing.T) {
	header := func(value string) Message {
		return Message{RawHeaders: authenticationResultsHeader + ": " + value + "\r\n"}
	}
	for _, c := range []struct {
		name   string
		value  string
		want   AuthVerdict
		checks map[string]AuthResult
	}{
		{
			name:  "all pass",
			value: "mx.example; spf=pass smtp.mailfrom=shop.example; dkim=pass header.d=shop.example; dmarc=pass header.from=shop.example",
			want:  AuthPassed,
			checks: map[string]AuthResult{
				"spf": ResultPass, "dkim": ResultPass, "dmarc": ResultPass,
			},
		},
		{
			name:  "dmarc fails despite a passing spf",
			value: "mx.example; spf=pass smtp.mailfrom=evil.example; dkim=none; dmarc=fail header.from=shop.example",
			want:  AuthFailed,
		},
		{
			name:  "dkim carries a message whose spf fails",
			value: "mx.example; spf=softfail smtp.mailfrom=shop.example; dkim=pass header.d=shop.example",
			want:  AuthPassed,
		},
		{
			name:  "only failures",
			value: "mx.example; spf=softfail smtp.mailfrom=shop.example; dkim=none; dmarc=none",
			want:  AuthFailed,
		},
		{
			name:  "no policy and no result",
			value: "mx.example; spf=none; dkim=none; dmarc=none",
			want:  AuthAbsent,
		},
		{
			name:  "a temporary error is not a pass",
			value: "mx.example; spf=temperror; dkim=temperror; dmarc=temperror",
			want:  AuthAbsent,
		},
		{
			name:  "comments do not confuse the results",
			value: "mx.example; spf=pass (mx.example: domain of shop.example designates 192.0.2.1) smtp.mailfrom=shop.example; dkim=pass (good signature; key 2048 bits) header.d=shop.example",
			want:  AuthPassed,
		},
		{
			name:  "one method repeated keeps the first result",
			value: "mx.example; dkim=pass header.d=shop.example; dkim=fail header.d=other.example",
			want:  AuthPassed,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := header(c.value).Trust()
			if got.Verdict != c.want {
				t.Fatalf("verdict %q, want %q (checks %+v)", got.Verdict, c.want, got.Checks)
			}
			for method, want := range c.checks {
				if result, ok := got.Check(method); !ok || result != want {
					t.Errorf("%s = %q (found %v), want %q", method, result, ok, want)
				}
			}
			if len(got.Evidence) == 0 {
				t.Error("no evidence kept")
			}
		})
	}
}

// The topmost Authentication-Results is the one the user's own server
// added; an older hop's header must not speak for it.
func TestTrustPrefersTheTopHeader(t *testing.T) {
	m := Message{RawHeaders: "" +
		"Authentication-Results: mx.example; spf=fail smtp.mailfrom=evil.example; dmarc=fail header.from=shop.example\r\n" +
		"Authentication-Results: hop.example; spf=pass; dkim=pass; dmarc=pass\r\n"}
	got := m.Trust()
	if got.Server != "mx.example" {
		t.Errorf("server %q", got.Server)
	}
	if got.Verdict != AuthFailed {
		t.Errorf("verdict %q", got.Verdict)
	}
}

func TestTrustFallsBackToReceivedSPF(t *testing.T) {
	m := Message{RawHeaders: "Received-SPF: Pass (mx.example: domain of shop.example designates 192.0.2.1)\r\n"}
	got := m.Trust()
	if got.Verdict != AuthPassed {
		t.Fatalf("verdict %q, checks %+v", got.Verdict, got.Checks)
	}
	if result, _ := got.Check("spf"); result != ResultPass {
		t.Errorf("spf %q", result)
	}
}

func TestTrustFallsBackToARC(t *testing.T) {
	m := Message{RawHeaders: "ARC-Authentication-Results: i=1; mx.example; dmarc=pass header.from=shop.example\r\n"}
	if got := m.Trust(); got.Verdict != AuthPassed {
		t.Fatalf("verdict %q", got.Verdict)
	}
}

func TestTrustWithoutResults(t *testing.T) {
	for _, raw := range []string{"", "Delivered-To: ann@example.com\r\n", "Authentication-Results: mx.example\r\n"} {
		got := (Message{RawHeaders: raw}).Trust()
		if got.Verdict != AuthAbsent {
			t.Errorf("%q: verdict %q", raw, got.Verdict)
		}
	}
}

func TestReplyToMismatch(t *testing.T) {
	from := Address{Name: "Billing", Email: "billing@shop.example"}
	for _, c := range []struct {
		name string
		m    Summary
		want bool
	}{
		{"no reply-to", Summary{From: from}, false},
		{"same domain", Summary{From: from, ReplyTo: []Address{{Email: "help@shop.example"}}}, false},
		{"same domain, other case", Summary{From: from, ReplyTo: []Address{{Email: "help@SHOP.example"}}}, false},
		{"another domain", Summary{From: from, ReplyTo: []Address{{Email: "collect@evil.example"}}}, true},
		{"one of them differs", Summary{From: from, ReplyTo: []Address{{Email: "a@evil.example"}, {Email: "b@shop.example"}}}, true},
		{"no from domain", Summary{From: Address{}, ReplyTo: []Address{{Email: "collect@evil.example"}}}, false},
	} {
		if got := c.m.ReplyToMismatch(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExternalSender(t *testing.T) {
	own := []string{"example.com", "company.example"}
	for _, c := range []struct {
		name string
		from Address
		want bool
	}{
		{"own domain", Address{Email: "ann@example.com"}, false},
		{"own domain, other case", Address{Email: "ann@EXAMPLE.com"}, false},
		{"another domain", Address{Email: "bob@outside.example"}, true},
		{"no address", Address{}, false},
		{"no domain", Address{Email: "bob"}, false},
	} {
		if got := ExternalSender(c.from, own); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAttachmentRisk(t *testing.T) {
	for _, c := range []struct {
		name string
		att  Attachment
		want AttachmentRisk
	}{
		{"a picture", Attachment{Filename: "photo.jpg", ContentType: "image/jpeg"}, AttachmentSafe},
		{"a pdf", Attachment{Filename: "invoice.pdf", ContentType: "application/pdf"}, AttachmentSafe},
		{"an archive", Attachment{Filename: "papers.zip", ContentType: "application/zip"}, AttachmentSafe},
		{"a program", Attachment{Filename: "setup.exe", ContentType: "application/octet-stream"}, AttachmentExecutable},
		{"a script in any case", Attachment{Filename: "run.PS1", ContentType: "text/plain"}, AttachmentExecutable},
		{"a macro document", Attachment{Filename: "budget.xlsm", ContentType: "application/vnd.ms-excel.sheet.macroEnabled.12"}, AttachmentExecutable},
		{"a disguised program", Attachment{Filename: "invoice.pdf.exe", ContentType: "application/pdf"}, AttachmentDisguised},
		{"a type that hides a program", Attachment{Filename: "invoice.pdf", ContentType: "application/x-msdownload"}, AttachmentTypeMismatch},
		{"an unknown name", Attachment{Filename: "notes", ContentType: "text/plain"}, AttachmentSafe},
		{"a harmless type", Attachment{Filename: "notes.txt", ContentType: "text/plain; charset=utf-8"}, AttachmentSafe},
	} {
		if got := RiskOf(c.att); got != c.want {
			t.Errorf("%s: risk %v, want %v", c.name, got, c.want)
		}
	}
}
