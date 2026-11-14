package main

import (
	"net"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"shu/locales"
)

// Link safety, before a link leaves the isolated view (issue: deceptive
// links). The window shows where a link really goes and explains the
// signs attackers use; every check reads the address alone. Nothing is
// looked up, no reputation is asked for, nothing is blocked: the user
// still decides.
//
// The registrable domain comes from the public suffix list embedded in
// golang.org/x/net/publicsuffix. That list is the project's, so a private
// or brand-new suffix may be missing, and the window then shows the host
// as it is. The visible text of a link never reaches the window: the
// isolated view reports a navigation, not the element clicked, so a name
// that pretends to be a site is caught only where the address carries it,
// as in its userinfo.

type linkWarningKind int

const (
	// linkWarnScheme is an address that is not the web.
	linkWarnScheme linkWarningKind = iota + 1
	// linkWarnUserinfo is a name before @, which is not the site.
	linkWarnUserinfo
	// linkWarnIP is a site named by an address rather than a name.
	linkWarnIP
	// linkWarnLookalike is a site name buried in the labels before the
	// registrable domain.
	linkWarnLookalike
	// linkWarnPort is a port other than the web's usual one.
	linkWarnPort
	// linkWarnTrailingDot is a host name ending in a dot.
	linkWarnTrailingDot
	// linkWarnSubdomains is a name with many parts before its domain.
	linkWarnSubdomains
	// linkWarnLetters is a name written with letters that can imitate
	// another site.
	linkWarnLetters
)

// linkWarning is one sign of deception, with what it names.
type linkWarning struct {
	kind   linkWarningKind
	name   string
	host   string
	domain string
	port   string
	count  int
}

// linkReport is what the window shows of a link: where it goes, and why
// that may not be what it seems.
type linkReport struct {
	// URL is the address as the message wrote it.
	URL string
	// Host is the host as the address writes it, without a userinfo or a
	// port.
	Host string
	// Domain is the registrable domain when the public suffix list knows
	// one, else the host. The window shows it before the full address.
	Domain string
	// Destination is what the window shows first: the domain, a mail
	// address, or the URL when the address names neither.
	Destination string
	Warnings    []linkWarning
}

// examineLink reads a link's destination, locally.
func examineLink(raw string) linkReport {
	r := linkReport{URL: raw, Destination: raw}
	u, err := url.Parse(raw)
	if err != nil {
		return r
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https":
	case "mailto":
		// A mail address has no host to explain; the address is its own
		// destination.
		if u.Opaque != "" {
			r.Destination = u.Opaque
		}
		return r
	default:
		r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnScheme, name: scheme})
		return r
	}
	host := u.Hostname()
	if host == "" {
		return r
	}
	r.Host = host
	r.Destination, r.Domain = host, host
	bare := strings.TrimSuffix(host, ".")
	if domain, err := publicsuffix.EffectiveTLDPlusOne(bare); err == nil {
		r.Domain = domain
		r.Destination = domain
	}
	if u.User != nil {
		if name := u.User.Username(); name != "" {
			r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnUserinfo, name: name})
		}
	}
	if net.ParseIP(host) != nil {
		r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnIP})
	} else {
		if name := lookalikePrefix(bare, r.Domain); name != "" {
			r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnLookalike, name: name})
		}
		if count := strings.Count(host, ".") + 1 - strings.Count(r.Domain, ".") - 1; count >= 3 {
			r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnSubdomains, count: count})
		}
	}
	if port := u.Port(); port != "" && !defaultPort(scheme, port) {
		r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnPort, port: port})
	}
	if strings.HasSuffix(host, ".") {
		r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnTrailingDot})
	}
	if display := unicodeHost(host); lookalikeLetters(display) {
		r.Warnings = append(r.Warnings, linkWarning{kind: linkWarnLetters, name: display})
	}
	return r
}

// warnLines says each warning in the window's own words.
func (r linkReport) warnLines() []string {
	var out []string
	for _, w := range r.Warnings {
		switch w.kind {
		case linkWarnScheme:
			out = append(out, locales.SecurityLinkWarnScheme(w.name))
		case linkWarnUserinfo:
			out = append(out, locales.SecurityLinkWarnUserinfo(w.name, r.Host))
		case linkWarnIP:
			out = append(out, locales.SecurityLinkWarnIP())
		case linkWarnLookalike:
			out = append(out, locales.SecurityLinkWarnLookalike(w.name, r.Domain))
		case linkWarnPort:
			out = append(out, locales.SecurityLinkWarnPort(w.port))
		case linkWarnTrailingDot:
			out = append(out, locales.SecurityLinkWarnTrailingDot())
		case linkWarnSubdomains:
			out = append(out, locales.SecurityLinkWarnSubdomains(w.count, r.Domain))
		case linkWarnLetters:
			out = append(out, locales.SecurityLinkWarnLetters(w.name))
		}
	}
	return out
}

// text is the dialog's message: the real destination first, then the
// address as the message wrote it, then, when there are any, the signs
// worth checking.
func (r linkReport) text() string {
	message := locales.SecurityLinkMessage(r.Destination, r.URL)
	if lines := r.warnLines(); len(lines) > 0 {
		message += "\n\n" + locales.SecurityLinkWarnings() + "\n• " + strings.Join(lines, "\n• ")
	}
	return message
}

// lookalikePrefix finds a site name in the labels before the registrable
// domain, as bank.com is in bank.com.example.evil: the part that reads
// as a different site than the one the address goes to.
func lookalikePrefix(host, domain string) string {
	suffix := "." + domain
	if domain == "" || !strings.HasSuffix(host, suffix) {
		return ""
	}
	labels := strings.Split(strings.TrimSuffix(host, suffix), ".")
	for i := range labels {
		name := strings.Join(labels[:i+1], ".")
		if !strings.Contains(name, ".") {
			continue
		}
		if _, icann := publicsuffix.PublicSuffix(name); icann {
			return name
		}
	}
	return ""
}

// defaultPort reports whether a port is the one its scheme uses.
func defaultPort(scheme, port string) bool {
	switch scheme {
	case "http":
		return port == "80"
	case "https":
		return port == "443"
	}
	return false
}

// unicodeHost is a host as a person reads it, with its punycode labels
// decoded.
func unicodeHost(host string) string {
	if !strings.Contains(strings.ToLower(host), "xn--") {
		return host
	}
	display, err := idna.ToUnicode(host)
	if err != nil {
		return host
	}
	return display
}

// lookalikeLetters reports whether a host name is written with letters
// other than the plain Latin ones an ASCII name uses: a name that can
// pass for another site, whether it mixes alphabets or is all one other
// script.
func lookalikeLetters(host string) bool {
	for _, label := range strings.Split(host, ".") {
		for _, r := range label {
			if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
				return true
			}
		}
	}
	return false
}
