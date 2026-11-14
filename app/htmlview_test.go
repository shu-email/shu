package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

// htmlRule is one promise about a rendered document: a text that must be
// there, or must not, in both modes or in one of them.
type htmlRule struct {
	keep  bool
	text  string
	allow *bool
}

// htmlFixture is one case of ../testdata/html.
type htmlFixture struct {
	name   string
	source string
	rules  []htmlRule
}

var (
	// htmlMarker is a fixture's comment line naming what the sanitized
	// document must keep or drop, in both modes or in one of them. Any
	// other comment stays in the document, as a message's does.
	htmlMarker  = regexp.MustCompile(`(?m)^[ \t]*<!--[ \t]*(keep|drop)(?:-(blocked|allowed))?:[ \t]*(.*?)[ \t]*-->[ \t]*\r?$`)
	htmlHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	htmlRemote  = regexp.MustCompile(`(?i)<img[^>]*\bsrc\s*=\s*"https?:`)
	htmlUnsafe  = []string{"<script", "<iframe", "<frame", "<object", "<embed", "<form", "<input", "<button", "<textarea", "<select", "<base", "<template", "<meta http-equiv=\"refresh", "<svg", "<math", "srcdoc=", "javascript:", "vbscript:", "xlink:", "file://", "about:blank"}
)

// parseHTMLFixture reads one document's markers, and drops only their
// lines: what a fixture says of itself is no part of the message, but its
// ordinary comments stay, as a real message's would.
func parseHTMLFixture(name, src string) htmlFixture {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	f := htmlFixture{name: name}
	for _, m := range htmlMarker.FindAllStringSubmatch(src, -1) {
		rule := htmlRule{keep: m[1] == "keep", text: m[3]}
		switch m[2] {
		case "blocked":
			no := false
			rule.allow = &no
		case "allowed":
			yes := true
			rule.allow = &yes
		}
		f.rules = append(f.rules, rule)
	}
	f.source = htmlMarker.ReplaceAllString(src, "")
	return f
}

// loadHTMLCorpus reads ../testdata/html, so that adding a fixture adds a case:
// its comments name what must survive and must not.
func loadHTMLCorpus(t *testing.T) []htmlFixture {
	t.Helper()
	const dir = "../testdata/html"
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
		out = append(out, parseHTMLFixture(strings.TrimSuffix(entry.Name(), ".html"), string(data)))
	}
	if len(out) == 0 {
		t.Fatal("../testdata/html has no fixtures")
	}
	return out
}

// A fixture's markers are read whatever its line endings, and a file with
// none is no case at all rather than a case that promises nothing.
func TestHTMLFixtureMarkers(t *testing.T) {
	f := parseHTMLFixture("crlf", "<!-- keep: one -->\r\n<!-- drop-blocked: two -->\r\n<!-- drop-allowed: three -->\r\n<p>x</p>")
	if len(f.rules) != 3 {
		t.Fatalf("rules = %+v", f.rules)
	}
	if !f.rules[0].keep || f.rules[0].text != "one" || f.rules[0].allow != nil {
		t.Errorf("keep rule = %+v", f.rules[0])
	}
	if f.rules[1].keep || f.rules[1].text != "two" || f.rules[1].allow == nil || *f.rules[1].allow {
		t.Errorf("blocked rule = %+v", f.rules[1])
	}
	if f.rules[2].allow == nil || !*f.rules[2].allow {
		t.Errorf("allowed rule = %+v", f.rules[2])
	}
	if strings.Contains(f.source, "keep:") || strings.Contains(f.source, "drop") {
		t.Errorf("a marker stayed in the source: %q", f.source)
	}
	if !strings.Contains(f.source, "<p>x</p>") {
		t.Errorf("the document went: %q", f.source)
	}
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
				if len(f.rules) == 0 {
					t.Fatalf("%s makes no promise of its own", f.name)
				}
				out := renderHTML(f.source, allow)
				for _, rule := range f.rules {
					if rule.allow != nil && *rule.allow != allow {
						continue
					}
					if got := strings.Contains(out, rule.text); got != rule.keep {
						if rule.keep {
							t.Errorf("lacks %q:\n%s", rule.text, out)
						} else {
							t.Errorf("still has %q:\n%s", rule.text, out)
						}
					}
				}
				checkHTMLSafety(t, out, allow)
			})
		}
	}
}

// checkHTMLSafety is what every sanitized document promises, whatever the
// message was: nothing that runs or embeds, no handler, no address the
// policy does not allow, and the policy itself, in the head.
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
	if !allowRemote {
		if htmlRemote.MatchString(out) {
			t.Errorf("a remote picture survived while blocked:\n%s", out)
		}
		if strings.Contains(lower, "srcset=") {
			t.Errorf("a candidate list survived while blocked:\n%s", out)
		}
	}
	policies := cspPolicies(out)
	if len(policies) != 1 {
		t.Fatalf("the head has %d CSP meta tags:\n%s", len(policies), out)
	}
	want := contentSecurityPolicy
	if allowRemote {
		want = contentSecurityPolicyImages
	}
	if policies[0] != want {
		t.Errorf("policy = %q, want %q", policies[0], want)
	}
}

// cspPolicies reads the CSP meta tags of a rendered document's head. A
// policy anywhere else governs nothing, so it is no policy here either.
func cspPolicies(out string) []string {
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		return nil
	}
	head := findElement(doc, atom.Head)
	if head == nil {
		return nil
	}
	var policies []string
	for n := head.FirstChild; n != nil; n = n.NextSibling {
		if n.Type != html.ElementNode || n.DataAtom != atom.Meta {
			continue
		}
		if !strings.EqualFold(attrValue(n, "http-equiv"), "Content-Security-Policy") {
			continue
		}
		policies = append(policies, attrValue(n, "content"))
	}
	return policies
}

// attrValue is an element's attribute, without regard to case.
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// The policy goes ahead of everything the message brought: a CSP in a
// meta element governs only what is parsed after it, and a message's own
// stylesheet would otherwise load before it.
func TestRenderHTMLPolicyFirst(t *testing.T) {
	src := `<html><head><style>@import url(https://tracker.example/open.css);</style></head><body><p>Body text</p></body></html>`
	out := renderHTML(src, false)
	doc, err := html.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	head := findElement(doc, atom.Head)
	if head == nil {
		t.Fatalf("no head:\n%s", out)
	}
	first, second := head.FirstChild, (*html.Node)(nil)
	if first != nil {
		second = first.NextSibling
	}
	if first == nil || first.Type != html.ElementNode || first.DataAtom != atom.Meta || attrValue(first, "charset") == "" {
		t.Fatalf("the head does not start with the charset:\n%s", out)
	}
	if second == nil || second.Type != html.ElementNode || second.DataAtom != atom.Meta ||
		!strings.EqualFold(attrValue(second, "http-equiv"), "Content-Security-Policy") {
		t.Fatalf("the policy is not the second node of the head:\n%s", out)
	}
	if policy, importAt := strings.Index(out, "Content-Security-Policy"), strings.Index(out, "@import"); policy < 0 || importAt < 0 || policy > importAt {
		t.Errorf("the message's stylesheet comes before the policy:\n%s", out)
	}
}

// Allowing a message's pictures widens img-src alone.
func TestRenderHTMLImagesWidenOnlyImages(t *testing.T) {
	src := `<p><img src="https://example.com/a.png"></p>`
	blocked := cspPolicies(renderHTML(src, false))
	allowed := cspPolicies(renderHTML(src, true))
	if len(blocked) != 1 || len(allowed) != 1 {
		t.Fatalf("policies = %q, %q", blocked, allowed)
	}
	if blocked[0] != contentSecurityPolicy {
		t.Errorf("blocked policy = %q", blocked[0])
	}
	if allowed[0] != contentSecurityPolicyImages {
		t.Errorf("allowed policy = %q", allowed[0])
	}
	if got := strings.Replace(allowed[0], "img-src data: http: https:", "img-src data:", 1); got != blocked[0] {
		t.Errorf("allowing images changed more than img-src: %q", got)
	}
}
