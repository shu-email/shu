package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
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
	out := renderHTML(source, false)
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

// Remote pictures stay out until the user allowed them.
func TestRenderHTMLRemoteImages(t *testing.T) {
	src := `<p><img src="https://example.com/a.png" alt="a"></p>`
	blocked := renderHTML(src, false)
	if strings.Contains(blocked, "https://example.com/a.png") {
		t.Errorf("the remote image stayed: %q", blocked)
	}
	if !strings.Contains(blocked, "img-src data:;") {
		t.Errorf("the blocked policy changed: %q", blocked)
	}
	allowed := renderHTML(src, true)
	if !strings.Contains(allowed, "https://example.com/a.png") {
		t.Errorf("the allowed image went: %q", allowed)
	}
	if !strings.Contains(allowed, "img-src data: http: https:;") {
		t.Errorf("the allowed policy is missing: %q", allowed)
	}
	for _, out := range []string{blocked, allowed} {
		if strings.Contains(out, "<script") || strings.Contains(out, "onerror=") {
			t.Errorf("a script survived: %q", out)
		}
	}
}

// htmlFixture is one case of testdata/html: what the window must keep and
// must drop, as the fixture's comments say.
type htmlFixture struct {
	name   string
	source string
	keep   []string
	drop   []string
}

var (
	// htmlMarker is a fixture's comment line naming what the sanitized
	// document must keep or drop. Any other comment is no case at all.
	htmlMarker = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*(keep|drop):[ \t]*(.*?)[ \t]*-->[ \t]*$`)
	// htmlComment strips every comment before rendering, so that what a
	// fixture says of itself is no part of the message.
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	htmlRemote  = regexp.MustCompile(`(?i)<img[^>]*\bsrc\s*=\s*"https?:`)
	htmlPolicy  = regexp.MustCompile(`(?i)<meta[^>]*http-equiv="content-security-policy"[^>]*>`)
	htmlContent = regexp.MustCompile(`(?i)\bcontent="([^"]*)"`)
	htmlUnsafe  = []string{"<script", "<iframe", "<frame", "<object", "<embed", "<form", "<input", "<button", "<textarea", "<select", "<base", "<template", "<meta http-equiv=\"refresh", "<svg", "<math", "srcdoc=", "javascript:", "vbscript:", "xlink:", "file://", "about:blank"}
)

// loadHTMLCorpus reads testdata/html, so that adding a fixture adds a case:
// its "keep:" and "drop:" comments name what must survive and must not.
func loadHTMLCorpus(t *testing.T) []htmlFixture {
	t.Helper()
	const dir = "testdata/html"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []htmlFixture
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		f := htmlFixture{name: strings.TrimSuffix(entry.Name(), ".html")}
		for _, m := range htmlMarker.FindAllStringSubmatch(src, -1) {
			switch m[1] {
			case "keep":
				f.keep = append(f.keep, m[2])
			case "drop":
				f.drop = append(f.drop, m[2])
			}
		}
		f.source = htmlComment.ReplaceAllString(src, "")
		out = append(out, f)
	}
	if len(out) == 0 {
		t.Fatal("testdata/html has no fixtures")
	}
	return out
}

// The corpus runs both ways, images blocked and allowed: a message must
// keep what it says and lose what it may not do, and the policy must hold
// whatever the message holds.
func TestHTMLCorpus(t *testing.T) {
	for _, f := range loadHTMLCorpus(t) {
		for _, allow := range []bool{false, true} {
			name := f.name
			if allow {
				name += "/images-allowed"
			}
			t.Run(name, func(t *testing.T) {
				out := renderHTML(f.source, allow)
				for _, want := range f.keep {
					if !strings.Contains(out, want) {
						t.Errorf("lacks %q:\n%s", want, out)
					}
				}
				for _, bad := range f.drop {
					if strings.Contains(out, bad) {
						t.Errorf("still has %q:\n%s", bad, out)
					}
				}
				checkHTMLSafety(t, out, allow)
			})
		}
	}
}

// checkHTMLSafety is what every sanitized document promises, whatever the
// message was: nothing that runs or embeds, no handler, no address the
// policy does not allow, and the policy itself.
func checkHTMLSafety(t *testing.T, out string, allowRemote bool) {
	t.Helper()
	lower := strings.ToLower(out)
	for _, bad := range htmlUnsafe {
		if strings.Contains(lower, bad) {
			t.Errorf("unsafe %q survived:\n%s", bad, out)
		}
	}
	if htmlHandler.MatchString(out) {
		t.Errorf("an event handler survived:\n%s", out)
	}
	if strings.Contains(lower, "cid:") {
		t.Errorf("a cid reference survived, with no part to show:\n%s", out)
	}
	if !allowRemote && htmlRemote.MatchString(out) {
		t.Errorf("a remote picture survived while blocked:\n%s", out)
	}
	tags := htmlPolicy.FindAllString(out, -1)
	if len(tags) != 1 {
		t.Fatalf("the document has %d CSP meta tags:\n%s", len(tags), out)
	}
	want := contentSecurityPolicy
	if allowRemote {
		want = contentSecurityPolicyImages
	}
	if got := policyContent(tags[0]); got != want {
		t.Errorf("policy = %q, want %q", got, want)
	}
}

// policyContent is a CSP meta tag's content.
func policyContent(tag string) string {
	m := htmlContent.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

// Allowing a message's pictures widens img-src alone.
func TestRenderHTMLImagesWidenOnlyImages(t *testing.T) {
	src := `<p><img src="https://example.com/a.png"></p>`
	blocked := renderHTML(src, false)
	allowed := renderHTML(src, true)
	blockedTag := htmlPolicy.FindString(blocked)
	allowedTag := htmlPolicy.FindString(allowed)
	if blockedTag == "" || allowedTag == "" {
		t.Fatalf("no CSP meta: %q\n%s", blockedTag, allowed)
	}
	blockedPolicy := policyContent(blockedTag)
	allowedPolicy := policyContent(allowedTag)
	if blockedPolicy != contentSecurityPolicy {
		t.Errorf("blocked policy = %q", blockedPolicy)
	}
	if allowedPolicy != contentSecurityPolicyImages {
		t.Errorf("allowed policy = %q", allowedPolicy)
	}
	if got := strings.Replace(allowedPolicy, "img-src data: http: https:", "img-src data:", 1); got != blockedPolicy {
		t.Errorf("allowing images changed more than img-src: %q", got)
	}
}
