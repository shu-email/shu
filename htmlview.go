package main

import (
	"strings"

	"github.com/egoist/mygo"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// contentSecurityPolicy forbids everything an email does not need: no
// scripts, no network, no forms, only inline styles and data images.
const contentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; form-action 'none'; base-uri 'none'"

// ensureWebView creates the isolated web view the first time HTML mail is
// shown. The page is untrusted: it calls no Go method and navigates
// nowhere.
func (a *app) ensureWebView() {
	if a.webview != nil || a.win == nil {
		return
	}
	v, err := a.win.NewWebView(mygo.WebViewOptions{
		Page:            mygo.PageOptions{Untrusted: true},
		BackgroundColor: "light-dark(#ffffff, #18181b)",
	})
	if err != nil {
		return
	}
	v.Page().OnWillNavigate(func(e *mygo.NavigateEvent) {
		e.PreventDefault()
		if !isWebURL(e.URL) {
			return
		}
		// The page never opens a link itself: the window asks the user
		// first and shows the real destination (security.go).
		url := e.URL
		a.post(func() { a.askOpenLink(url) })
	})
	a.webview = v
}

// syncHTMLView loads the chosen message's HTML once, and only when it
// changes.
func (a *app) syncHTMLView() {
	if a.threaded() {
		a.syncThreadHTML()
		return
	}
	if !a.htmlOn || a.webview == nil {
		return
	}
	m, msg := a.current()
	if m == nil || msg == nil || msg.BodyHTML == "" {
		return
	}
	key := summaryKey(*m) + "/html"
	if a.htmlKey == key {
		return
	}
	a.htmlKey = key
	a.webview.Page().LoadHTML(renderHTML(msg.BodyHTML), "about:blank")
}

// syncThreadHTML loads the HTML of the message of the conversation on
// show that is to show as HTML, once.
func (a *app) syncThreadHTML() {
	cv := a.conv
	if cv == nil || cv.html == "" || a.webview == nil {
		return
	}
	b, ok := a.bodies[cv.html]
	if !ok || b.Message.BodyHTML == "" {
		return
	}
	key := cv.html + "/html"
	if a.htmlKey == key {
		return
	}
	a.htmlKey = key
	a.webview.Page().LoadHTML(renderHTML(b.Message.BodyHTML), "about:blank")
}

// isWebURL reports whether a link may leave the reading pane.
func isWebURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:")
}

// renderHTML sanitizes a message's HTML and wraps it with the CSP and a
// readable base style.
func renderHTML(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return "<!doctype html><html><body></body></html>"
	}
	sanitizeNode(doc)
	head := findElement(doc, atom.Head)
	if head == nil {
		head = &html.Node{Type: html.ElementNode, Data: "head"}
		if doc.FirstChild != nil {
			doc.InsertBefore(head, doc.FirstChild)
		} else {
			doc.AppendChild(head)
		}
	}
	charset := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta}
	charset.Attr = []html.Attribute{{Key: "charset", Val: "utf-8"}}
	head.InsertBefore(charset, head.FirstChild)
	meta := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta}
	meta.Attr = []html.Attribute{
		{Key: "http-equiv", Val: "Content-Security-Policy"},
		{Key: "content", Val: contentSecurityPolicy},
	}
	viewport := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta}
	viewport.Attr = []html.Attribute{
		{Key: "name", Val: "viewport"},
		{Key: "content", Val: "width=device-width, initial-scale=1"},
	}
	style := &html.Node{Type: html.ElementNode, Data: "style", DataAtom: atom.Style}
	style.AppendChild(&html.Node{Type: html.TextNode, Data: `
		html, body { background: transparent; }
		body {
			margin: 0; padding: 4px 2px 24px;
			font: 14px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
			color: light-dark(#18181b, #e4e4e7);
			overflow-wrap: anywhere;
		}
		a { color: #2563eb; }
		img { max-width: 100%; height: auto; }
		blockquote { margin: 0 0 0 12px; padding-left: 12px; border-left: 2px solid #d4d4d8; color: #71717a; }
		pre, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px; }
		pre { white-space: pre-wrap; }
		table { border-collapse: collapse; max-width: 100%; }
		td, th { padding: 3px 6px; border: 1px solid #e4e4e7; }
	`})
	head.AppendChild(meta)
	head.AppendChild(viewport)
	head.AppendChild(style)
	var b strings.Builder
	if err := html.Render(&b, doc); err != nil {
		return "<!doctype html><html><body></body></html>"
	}
	return b.String()
}

// sanitizeNode removes what a message may not run or load.
func sanitizeNode(n *html.Node) {
	if n.Type == html.ElementNode {
		switch n.DataAtom {
		case atom.Script, atom.Noscript, atom.Iframe, atom.Object, atom.Embed,
			atom.Frame, atom.Frameset, atom.Form, atom.Input, atom.Button,
			atom.Textarea, atom.Select, atom.Option, atom.Link, atom.Base,
			atom.Template, atom.Meta:
			removeNode(n)
			return
		}
	}
	for child := n.FirstChild; child != nil; {
		next := child.NextSibling
		sanitizeNode(child)
		child = next
	}
	if n.Type == html.ElementNode {
		n.Attr = safeAttributes(n.Data, n.Attr)
	}
}

// safeAttributes drops event handlers, form actions and unsafe links.
func safeAttributes(tag string, attrs []html.Attribute) []html.Attribute {
	out := attrs[:0]
	for _, attr := range attrs {
		key := strings.ToLower(attr.Key)
		if strings.HasPrefix(key, "on") {
			continue
		}
		switch key {
		case "srcdoc", "action", "formaction", "ping", "nonce":
			continue
		case "href", "src":
			if key == "href" && isWebURL(attr.Val) {
				out = append(out, attr)
				continue
			}
			if key == "src" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(attr.Val)), "data:") {
				out = append(out, attr)
			}
			continue
		}
		out = append(out, attr)
	}
	return out
}

func removeNode(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func findElement(root *html.Node, a atom.Atom) *html.Node {
	if root.Type == html.ElementNode && root.DataAtom == a {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, a); found != nil {
			return found
		}
	}
	return nil
}
