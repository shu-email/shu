package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Leaving a mailing list, and the record of having left it. Everything
// here reads the List-Unsubscribe headers the message came with and does
// only what the user confirmed: nothing unsubscribes by itself, and the
// recipient of a one-click request or an email is what those headers
// name, never an address from the window.

// UnsubscribeMethod is how a message's list is left.
type UnsubscribeMethod string

const (
	// UnsubscribeOneClick is RFC 8058: one POST to the list's own URL,
	// with no page and no mail (see List-Unsubscribe-Post).
	UnsubscribeOneClick UnsubscribeMethod = "one-click"
	// UnsubscribeEmail is a message to the address the list names.
	UnsubscribeEmail UnsubscribeMethod = "email"
	// UnsubscribePage opens the list's own page, which asks the user
	// there; the app sends nothing.
	UnsubscribePage UnsubscribeMethod = "page"
)

// ErrNoUnsubscribe says the message's headers offer no way to leave its
// list.
var ErrNoUnsubscribe = errors.New("mail: the message offers no unsubscribe")

// unsubscribeTimeout bounds one one-click request. The list's server is
// answering a single POST, not a page.
const unsubscribeTimeout = 20 * time.Second

// UnsubscribeOffer is what a message's List-Unsubscribe headers offer:
// the lists' links, their addresses, and whether the list takes a
// one-click request (RFC 8058).
type UnsubscribeOffer struct {
	Links    []string `json:"links,omitempty"`
	Emails   []string `json:"emails,omitempty"`
	OneClick bool     `json:"oneClick,omitempty"`
}

// Empty reports whether the message is not from a list, or its list says
// nothing of how to leave it.
func (o UnsubscribeOffer) Empty() bool { return len(o.Links) == 0 && len(o.Emails) == 0 }

// ParseUnsubscribeOffer reads the List-Unsubscribe and
// List-Unsubscribe-Post values a message's headers carry. Each value may
// hold several URIs, in angle brackets as RFC 2369 writes them, and the
// post says the list takes a one-click request.
func ParseUnsubscribeOffer(list, post string) UnsubscribeOffer {
	var o UnsubscribeOffer
	for _, value := range strings.Split(list, "\n") {
		for _, uri := range splitUnsubscribe(value) {
			switch {
			case strings.HasPrefix(strings.ToLower(uri), "mailto:"):
				if address := mailtoAddress(uri); address != "" {
					o.Emails = append(o.Emails, address)
				}
			case isWebURL(uri):
				o.Links = append(o.Links, uri)
			}
		}
	}
	o.OneClick = strings.EqualFold(strings.TrimSpace(post), "List-Unsubscribe=One-Click")
	return o
}

// Preferred is the method to offer, with where it goes: the one-click
// request when the list asks for one and names a link it may be made to,
// an email when it names an address, the page otherwise.
func (o UnsubscribeOffer) Preferred() (UnsubscribeMethod, string, bool) {
	if link, ok := o.oneClickLink(); ok {
		return UnsubscribeOneClick, link, true
	}
	if len(o.Emails) > 0 {
		return UnsubscribeEmail, o.Emails[0], true
	}
	if len(o.Links) > 0 {
		return UnsubscribePage, o.Links[0], true
	}
	return "", "", false
}

// Destination is where method goes, when the offer has it.
func (o UnsubscribeOffer) Destination(method UnsubscribeMethod) (string, bool) {
	switch method {
	case UnsubscribeOneClick:
		return o.oneClickLink()
	case UnsubscribeEmail:
		if len(o.Emails) > 0 {
			return o.Emails[0], true
		}
	case UnsubscribePage:
		if len(o.Links) > 0 {
			return o.Links[0], true
		}
	}
	return "", false
}

// oneClickLink is the link a one-click request may go to: RFC 8058 asks
// for HTTPS, so that the list's token and the user's address do not travel
// in cleartext. A list that names only an HTTP link is left by email or on
// its page, where the browser decides.
func (o UnsubscribeOffer) oneClickLink() (string, bool) {
	if !o.OneClick {
		return "", false
	}
	for _, link := range o.Links {
		if isHTTPSURL(link) {
			return link, true
		}
	}
	return "", false
}

// splitUnsubscribe splits one header value on the commas outside angle
// brackets, and drops the brackets.
func splitUnsubscribe(value string) []string {
	var out []string
	var b strings.Builder
	inBrackets := false
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, s)
		}
		b.Reset()
	}
	for _, r := range value {
		switch r {
		case '<':
			inBrackets = true
		case '>':
			inBrackets = false
			flush()
		case ',':
			if inBrackets {
				b.WriteRune(r)
			} else {
				flush()
			}
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

// mailtoAddress is the address of a mailto URI, without what the list
// asks a message to say.
func mailtoAddress(uri string) string {
	address, _, _ := mailtoRequest(uri)
	return address
}

// mailtoRequest reads a mailto URI: its address, and the subject and body
// its query asks an unsubscribe message to carry, when it asks any.
func mailtoRequest(uri string) (address, subject, body string) {
	rest := uri[len("mailto:"):]
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	address, query, _ := strings.Cut(rest, "?")
	address = strings.ToLower(strings.TrimSpace(address))
	if query == "" {
		return address, "", ""
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return address, "", ""
	}
	return address, values.Get("subject"), values.Get("body")
}

// unsubscribeEmail reads what the list asks of the email to address, from
// its List-Unsubscribe value; "" is what lists without a query expect.
func unsubscribeEmail(list, address string) (subject, body string) {
	for _, value := range strings.Split(list, "\n") {
		for _, uri := range splitUnsubscribe(value) {
			if !strings.HasPrefix(strings.ToLower(uri), "mailto:") {
				continue
			}
			if got, subject, body := mailtoRequest(uri); got == address {
				return subject, body
			}
		}
	}
	return "", ""
}

// isWebURL reports whether a value is a link the app may offer: web, with
// a host, and no credentials in it.
func isWebURL(value string) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return u.User == nil
}

// isHTTPSURL reports whether a value is a link a one-click request may be
// posted to: web, with a host, no credentials, and encrypted.
func isHTTPSURL(value string) bool {
	u, err := url.Parse(strings.TrimSpace(value))
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// UnsubscribeOffer reads what a cached message offers, from the cache
// alone: the headers were kept when the message was cached (see
// Security).
func (s *Service) UnsubscribeOffer(ctx context.Context, accountID, folder string, uid UID) (UnsubscribeOffer, error) {
	m, ok, err := s.store.GetMessage(ctx, accountID, folder, uid)
	if err != nil {
		return UnsubscribeOffer{}, err
	}
	if !ok {
		return UnsubscribeOffer{}, ErrNotFound
	}
	list, post := m.Unsubscribe()
	o := ParseUnsubscribeOffer(list, post)
	if o.Empty() {
		return UnsubscribeOffer{}, ErrNoUnsubscribe
	}
	return o, nil
}

// UnsubscribeRecord is a list left from this device: the sender the list
// writes as, the method, where the request went, and when.
type UnsubscribeRecord struct {
	AccountID   string            `json:"accountID"`
	Email       string            `json:"email"`
	Method      UnsubscribeMethod `json:"method"`
	Destination string            `json:"destination"`
	At          time.Time         `json:"at"`
}

// Unsubscribe leaves the message's list by the method the user chose,
// which the headers must offer at dest, the destination the window showed
// before the user confirmed it: a message the folder gave another UID to
// meanwhile offers another, and nothing is sent to it. A one-click request
// is one POST; an email is sent from the account as any message is, and a
// page is only named for the window to open. The choice is kept in the
// store, and nothing else leaves the device.
func (s *Service) Unsubscribe(ctx context.Context, accountID, folder string, uid UID, method UnsubscribeMethod, dest string) (UnsubscribeRecord, error) {
	m, ok, err := s.store.GetMessage(ctx, accountID, folder, uid)
	if err != nil {
		return UnsubscribeRecord{}, err
	}
	if !ok {
		return UnsubscribeRecord{}, ErrNotFound
	}
	list, post := m.Unsubscribe()
	offer := ParseUnsubscribeOffer(list, post)
	if offered, ok := offer.Destination(method); !ok || offered != dest {
		return UnsubscribeRecord{}, fmt.Errorf("mail: the message no longer offers %q: %w", dest, ErrNoUnsubscribe)
	}
	switch method {
	case UnsubscribeOneClick:
		if err := s.postUnsubscribe(ctx, dest); err != nil {
			return UnsubscribeRecord{}, err
		}
	case UnsubscribeEmail:
		subject, body := unsubscribeEmail(list, dest)
		if subject == "" {
			subject = "unsubscribe"
		}
		if body == "" {
			body = "unsubscribe"
		}
		if _, err := s.Send(ctx, accountID, Outgoing{
			To:      []Address{{Email: dest}},
			Subject: subject,
			Text:    body,
		}); err != nil {
			return UnsubscribeRecord{}, fmt.Errorf("mail: unsubscribe by email: %w", err)
		}
	case UnsubscribePage:
		// The window opens the page; nothing is sent from here.
	default:
		return UnsubscribeRecord{}, ErrNoUnsubscribe
	}
	rec := UnsubscribeRecord{
		AccountID: accountID, Email: strings.ToLower(m.From.Email),
		Method: method, Destination: dest, At: s.now(),
	}
	if err := s.store.SaveUnsubscribe(ctx, rec); err != nil {
		return UnsubscribeRecord{}, err
	}
	return rec, nil
}

// postUnsubscribe makes the one-click request RFC 8058 names: one POST
// with one form field, to the link the list gave, with no cookie of any
// page and nothing of the message.
func (s *Service) postUnsubscribe(ctx context.Context, link string) error {
	if !isHTTPSURL(link) {
		return ErrNoUnsubscribe
	}
	client, err := s.HTTPClient(ctx)
	if err != nil {
		return err
	}
	// RFC 8058 forbids a redirecting one-click endpoint, and following one
	// would POST the list's token to a host the confirmation never named.
	// The redirect's answer is then the failure it is.
	refused := *client
	refused.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &refused
	ctx, cancel := context.WithTimeout(ctx, unsubscribeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, link,
		strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		return fmt.Errorf("mail: unsubscribe: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mail: unsubscribe: %w", err)
	}
	defer resp.Body.Close()
	// The body says nothing the app needs; reading it lets the connection
	// be used again.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mail: unsubscribe: the list answered %s", resp.Status)
	}
	return nil
}

// Unsubscribes reads every list the account left from this device, the
// most recent first.
func (s *Service) Unsubscribes(ctx context.Context, accountID string) ([]UnsubscribeRecord, error) {
	return s.store.Unsubscribes(ctx, accountID)
}
