package main

import (
	"strings"
	"testing"
)

func TestRenderHTMLSanitizes(t *testing.T) {
	source := `<html><head><title>x</title><style>p{color:red}</style></head><body>
		<p onclick="steal()">Hello <b>there</b></p>
		<script>alert(1)</script>
		<img src="https://evil.example/pixel.png" onerror="steal()">
		<img src="data:image/png;base64,AAAA">
		<a href="javascript:alert(1)">bad</a>
		<a href="https://example.com">good</a>
		<iframe src="https://evil.example"></iframe>
		<form action="https://evil.example"><input value="x"></form>
	</body></html>`
	out := renderHTML(source)
	for _, unwanted := range []string{"<script", "onclick", "onerror", "javascript:", "evil.example/pixel", "<iframe", "<form", "<input"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("sanitized HTML still has %q:\n%s", unwanted, out)
		}
	}
	for _, want := range []string{"Content-Security-Policy", "default-src", "Hello", "<b>there</b>", "data:image/png", "https://example.com", "color:red"} {
		if !strings.Contains(out, want) {
			t.Errorf("sanitized HTML lacks %q:\n%s", want, out)
		}
	}
}

func TestIsWebURL(t *testing.T) {
	for _, ok := range []string{"https://example.com", "http://a.b", "mailto:a@b.c", " HTTPS://x "} {
		if !isWebURL(ok) {
			t.Errorf("isWebURL(%q) = false", ok)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "", "about:blank"} {
		if isWebURL(bad) {
			t.Errorf("isWebURL(%q) = true", bad)
		}
	}
}

func TestSafeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":           "report.pdf",
		"../../etc/passwd":     "passwd",
		`..\..\windows\system`: "system",
		"":                     "attachment",
		"..":                   "attachment",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
