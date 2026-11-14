package app

import (
	"slices"
	"strings"
	"testing"

	"shu/locales"
)

// examineLink reads local addresses only: the domain the window shows
// first, and the signs of deception it can name. A benign address has no
// warning at all.
func TestExamineLink(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		domain string
		want   []linkWarningKind
	}{
		{"benign", "https://example.com/path", "example.com", nil},
		{"subdomain", "https://www.example.co.uk/a", "example.co.uk", nil},
		{"relevant-looking subdomain", "https://mail.example.com/", "example.com", nil},
		{"userinfo", "https://bank.com@evil.example/login", "evil.example",
			[]linkWarningKind{linkWarnUserinfo}},
		{"ip address", "https://192.168.0.1/a", "192.168.0.1", []linkWarningKind{linkWarnIP}},
		{"ipv6", "https://[::1]/a", "::1", []linkWarningKind{linkWarnIP}},
		{"legacy ipv4 number", "https://2130706433/", "2130706433", []linkWarningKind{linkWarnIP}},
		{"legacy ipv4 hex", "https://0x7f.1/", "0x7f.1", []linkWarningKind{linkWarnIP}},
		{"legacy ipv4 octal", "https://0177.0.0.1/", "0177.0.0.1", []linkWarningKind{linkWarnIP}},
		{"dotted ip with trailing dot", "https://127.0.0.1./", "127.0.0.1",
			[]linkWarningKind{linkWarnIP, linkWarnTrailingDot}},
		{"non-default port", "https://example.com:8443/a", "example.com", []linkWarningKind{linkWarnPort}},
		{"default port", "https://example.com:443/a", "example.com", nil},
		{"plain http port", "http://example.com:80/a", "example.com", nil},
		{"trailing dot", "https://example.com./a", "example.com", []linkWarningKind{linkWarnTrailingDot}},
		{"many subdomains", "https://a.b.c.d.example.com/", "example.com",
			[]linkWarningKind{linkWarnSubdomains}},
		{"lookalike domain", "https://bank.com.login.evil.example/", "evil.example",
			[]linkWarningKind{linkWarnLookalike, linkWarnSubdomains}},
		{"lookalike under a private suffix", "https://bank.github.io.evil.example/", "evil.example",
			[]linkWarningKind{linkWarnLookalike, linkWarnSubdomains}},
		{"punycode mixed alphabet", "https://xn--80ak6aa92e.com/", "xn--80ak6aa92e.com",
			[]linkWarningKind{linkWarnLetters}},
		{"punycode latin homoglyph", "https://xn--pple-p5b.com/", "xn--pple-p5b.com",
			[]linkWarningKind{linkWarnLetters}},
		{"latin idn is no deception", "https://bücher.de/", "bücher.de", nil},
		{"cyrillic idn", "https://пример.рф/", "пример.рф", []linkWarningKind{linkWarnLetters}},
		{"upper-case host", "https://WWW.EXAMPLE.CO.UK/A", "example.co.uk", nil},
		{"upper-case userinfo", "https://BANK.COM@EVIL.EXAMPLE/login", "evil.example",
			[]linkWarningKind{linkWarnUserinfo}},
		{"upper-case punycode", "https://XN--80AK6AA92E.COM/", "xn--80ak6aa92e.com",
			[]linkWarningKind{linkWarnLetters}},
		{"javascript", "javascript:alert(1)", "", []linkWarningKind{linkWarnScheme}},
		{"data", "data:text/html,<script>alert(1)</script>", "", []linkWarningKind{linkWarnScheme}},
		{"file", "file:///etc/passwd", "", []linkWarningKind{linkWarnScheme}},
		{"mailto", "mailto:a@b.example", "", nil},
		{"relative", "foo/bar", "", nil},
	}
	for _, c := range cases {
		r := examineLink(c.url)
		if r.Domain != c.domain {
			t.Errorf("%s: domain = %q, want %q", c.name, r.Domain, c.domain)
		}
		var got []linkWarningKind
		for _, w := range r.Warnings {
			got = append(got, w.kind)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: warnings = %v, want %v", c.name, got, c.want)
		}
	}
}

// The dialog's message names the real domain first, then the address as
// written, then one plain-language line per sign. A benign link gets no
// warning section.
func TestLinkReportText(t *testing.T) {
	r := examineLink("https://bank.com@evil.example/login")
	text := r.text()
	domain := strings.Index(text, "evil.example")
	address := strings.Index(text, "https://bank.com@evil.example/login")
	if domain < 0 || address < 0 || domain > address {
		t.Errorf("the domain is not named before the address:\n%s", text)
	}
	for _, want := range []string{locales.SecurityLinkWarnings(),
		locales.SecurityLinkWarnUserinfo("bank.com", "evil.example")} {
		if strings.Index(text, want) < 0 {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	benign := examineLink("https://example.com/a").text()
	if strings.Index(benign, locales.SecurityLinkWarnings()) >= 0 {
		t.Errorf("a benign link warns:\n%s", benign)
	}
	if strings.Index(benign, "example.com") < 0 || strings.Index(benign, "https://example.com/a") < 0 {
		t.Errorf("a benign link names its destination:\n%s", benign)
	}
}
