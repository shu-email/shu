package mail

import (
	"net/mail"
	"path/filepath"
	"strings"
)

// Sender authentication and the warnings that go with it (see Trust).
// Everything here reads headers the receiving server already added and
// bytes already in the message; it never looks anything up on the
// network and never changes the message.

// The headers that carry a message's authentication results, kept in
// RawHeaders so that the reading pane can explain them.
const (
	authenticationResultsHeader = "Authentication-Results"
	arcResultsHeader            = "ARC-Authentication-Results"
	receivedSPFHeader           = "Received-SPF"
)

// securityHeaders are the headers kept besides Delivered-To: what tells
// whether the sender is authentic, as the receiving server saw it.
var securityHeaders = []string{
	authenticationResultsHeader,
	arcResultsHeader,
	receivedSPFHeader,
	"DKIM-Signature",
}

// AuthVerdict is what the cached headers say of a message's sender.
// AuthAbsent is not AuthPassed's equal: a message without results is
// unknown, never safe.
type AuthVerdict string

// The three verdicts the reading pane shows.
const (
	AuthPassed AuthVerdict = "passed"
	AuthFailed AuthVerdict = "failed"
	AuthAbsent AuthVerdict = "absent"
)

// AuthResult is one method's reported result, in lower case as the
// server wrote it ("pass", "fail", "softfail", "none", …).
type AuthResult string

// The result values Authentication-Results is defined to carry.
const (
	ResultPass      AuthResult = "pass"
	ResultFail      AuthResult = "fail"
	ResultSoftFail  AuthResult = "softfail"
	ResultNeutral   AuthResult = "neutral"
	ResultNone      AuthResult = "none"
	ResultTempError AuthResult = "temperror"
	ResultPermError AuthResult = "permerror"
)

// AuthCheck is one method's result: SPF, DKIM or DMARC.
type AuthCheck struct {
	Method string
	Result AuthResult
}

// Trust is what a message's cached headers say about its sender.
type Trust struct {
	Verdict AuthVerdict
	// Checks are the results found, in the order the server reported
	// them. A method is listed once, its first result.
	Checks []AuthCheck
	// Server is the authserv-id that reported, when the header names one.
	Server string
	// Evidence holds the raw header lines the verdict came from, for a
	// user who wants to inspect them.
	Evidence []string
}

// Check returns the result of one method.
func (t Trust) Check(method string) (AuthResult, bool) {
	for _, c := range t.Checks {
		if strings.EqualFold(c.Method, method) {
			return c.Result, true
		}
	}
	return "", false
}

// Trust reads the sender-authentication results from the headers the
// receiving server added. It reads the first Authentication-Results
// header that reports any of SPF, DKIM or DMARC, which is the one added
// last, at the user's own boundary; when there is none it falls back to
// ARC's and to Received-SPF.
func (m Message) Trust() Trust {
	var t Trust
	for _, value := range headerValues(m.RawHeaders, authenticationResultsHeader) {
		server, checks := parseAuthResults(value)
		if len(checks) == 0 {
			continue
		}
		t.Server, t.Checks, t.Evidence = server, checks, []string{authenticationResultsHeader + ": " + value}
		break
	}
	if len(t.Checks) == 0 {
		for _, value := range headerValues(m.RawHeaders, arcResultsHeader) {
			server, checks := parseAuthResults(value)
			if len(checks) == 0 {
				continue
			}
			t.Server, t.Checks, t.Evidence = server, checks, []string{arcResultsHeader + ": " + value}
			break
		}
	}
	if _, ok := t.Check("spf"); !ok {
		for _, value := range headerValues(m.RawHeaders, receivedSPFHeader) {
			if result := firstTokenResult(value); result != "" {
				t.Checks = append(t.Checks, AuthCheck{Method: "spf", Result: AuthResult(result)})
				t.Evidence = append(t.Evidence, receivedSPFHeader+": "+value)
				break
			}
		}
	}
	for _, name := range []string{"DKIM-Signature"} {
		for _, value := range headerValues(m.RawHeaders, name) {
			t.Evidence = append(t.Evidence, name+": "+value)
		}
	}
	t.Verdict = verdictOf(t)
	return t
}

// verdictOf decides the one verdict the reading pane shows. DMARC speaks
// for the From domain, so it wins when it reports; otherwise any passing
// method passes and any failing one fails; no result at all is absent.
func verdictOf(t Trust) AuthVerdict {
	if dmarc, ok := t.Check("dmarc"); ok {
		switch dmarc {
		case ResultPass:
			return AuthPassed
		case ResultFail:
			return AuthFailed
		}
	}
	var passed, failed bool
	for _, c := range t.Checks {
		switch c.Result {
		case ResultPass:
			passed = true
		case ResultFail, ResultSoftFail, ResultPermError:
			failed = true
		}
	}
	switch {
	case passed:
		return AuthPassed
	case failed:
		return AuthFailed
	default:
		return AuthAbsent
	}
}

// parseAuthResults reads one Authentication-Results value: the
// authserv-id before the first semicolon, then method=result segments,
// with their parenthesized comments dropped.
func parseAuthResults(value string) (server string, checks []AuthCheck) {
	segments := splitAuthSegments(value)
	if len(segments) == 0 {
		return "", nil
	}
	server = strings.TrimSpace(segments[0])
	if strings.Contains(server, "=") {
		// A header without an authserv-id: the first segment is a result.
		server = ""
	} else {
		segments = segments[1:]
	}
	seen := map[string]bool{}
	for _, seg := range segments {
		key, rest, found := strings.Cut(seg, "=")
		if !found {
			continue
		}
		method := strings.ToLower(strings.TrimSpace(key))
		switch method {
		case "spf", "dkim", "dmarc":
		default:
			continue
		}
		if seen[method] {
			continue
		}
		result := AuthResult(firstTokenResult(rest))
		if result == "" {
			continue
		}
		seen[method] = true
		checks = append(checks, AuthCheck{Method: method, Result: result})
	}
	return server, checks
}

// splitAuthSegments splits an authentication header on its semicolons,
// ignoring those inside a parenthesized comment, and strips the comments'
// contents from the segments it returns.
func splitAuthSegments(value string) []string {
	var out []string
	var b strings.Builder
	depth := 0
	for _, r := range value {
		switch r {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		if r == ';' {
			if seg := strings.TrimSpace(b.String()); seg != "" {
				out = append(out, seg)
			}
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if seg := strings.TrimSpace(b.String()); seg != "" {
		out = append(out, seg)
	}
	return out
}

// firstTokenResult reads the result word that starts a value, as in
// "pass (…)" or "fail reason=…", in lower case.
func firstTokenResult(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.IndexAny(value, " \t("); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(value)
}

// ReplyToMismatch reports whether the message asks for replies at
// another domain than the one it says it is from. It is advisory: a
// mailing list or a shared mailbox can do so honestly.
func (m Summary) ReplyToMismatch() bool {
	if len(m.ReplyTo) == 0 {
		return false
	}
	from := AddressDomain(m.From.Email)
	if from == "" {
		return false
	}
	for _, a := range m.ReplyTo {
		if d := AddressDomain(a.Email); d != "" && d != from {
			return true
		}
	}
	return false
}

// AddressDomain returns the lower-case part of an address after its last
// @, or "".
func AddressDomain(addr string) string {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

// ExternalSender reports whether a sender is outside every domain the
// user receives mail at. An address without a domain is not external:
// there is nothing to compare.
func ExternalSender(from Address, ownDomains []string) bool {
	d := AddressDomain(from.Email)
	if d == "" {
		return false
	}
	for _, own := range ownDomains {
		if strings.EqualFold(strings.TrimSpace(own), d) {
			return false
		}
	}
	return true
}

// AttachmentRisk is why an attachment is shown with a warning, if it is.
type AttachmentRisk int

// The reasons an attachment draws one.
const (
	AttachmentSafe AttachmentRisk = iota
	// AttachmentExecutable is an attached program or script, or a format
	// that can carry one when opened.
	AttachmentExecutable
	// AttachmentDisguised is a name with two extensions, the last one
	// executable, such as "invoice.pdf.exe".
	AttachmentDisguised
	// AttachmentTypeMismatch is a name that says one kind of file and a
	// content type that says another.
	AttachmentTypeMismatch
)

// executableExtensions are the suffixes Windows runs, or that carry
// programs and macros that run when opened.
var executableExtensions = map[string]bool{
	"exe": true, "com": true, "bat": true, "cmd": true, "scr": true,
	"pif": true, "vbs": true, "vbe": true, "js": true, "jse": true,
	"wsf": true, "wsh": true, "ps1": true, "psm1": true, "msi": true,
	"msp": true, "hta": true, "lnk": true, "reg": true, "jar": true,
	"iso": true, "img": true, "docm": true, "xlsm": true, "pptm": true,
	"dotm": true, "xlam": true,
}

// harmlessExtensions are the names ordinary mail attachments have. A
// content type that is not these, or is an executable's, with such a
// name, is a mismatch.
var harmlessTypes = map[string]bool{
	"pdf": true, "txt": true, "csv": true, "rtf": true, "doc": true,
	"docx": true, "xls": true, "xlsx": true, "ppt": true, "pptx": true,
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"svg": true, "heic": true, "tif": true, "tiff": true, "mp3": true,
	"wav": true, "mp4": true, "mov": true, "zip": true, "7z": true,
	"gz": true, "tar": true, "ics": true, "eml": true, "html": true,
	"htm": true,
}

// RiskOf says whether an attachment should carry a warning. It reads the
// name and content type only, and never opens the file.
func RiskOf(att Attachment) AttachmentRisk {
	name := strings.ToLower(strings.TrimSpace(att.Filename))
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if ext == "" {
		return AttachmentSafe
	}
	if executableExtensions[ext] {
		if inner := strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(name, "."+ext)), "."); inner != "" && harmlessTypes[inner] {
			return AttachmentDisguised
		}
		return AttachmentExecutable
	}
	if harmlessTypes[ext] && executableContentType(att.ContentType) {
		return AttachmentTypeMismatch
	}
	return AttachmentSafe
}

// executableContentType reports whether a content type is one a program
// is sent as, rather than a document or media type.
func executableContentType(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/x-msdownload", "application/x-msdos-program",
		"application/x-executable", "application/x-dosexec",
		"application/vnd.microsoft.portable-executable",
		"application/x-sh", "application/x-bat", "application/javascript",
		"application/x-javascript", "text/javascript":
		return true
	}
	return false
}

// MailFrom parses just enough of a From value to compare its domain,
// for callers that hold no Message.
func MailFrom(value string) Address {
	if a, err := mail.ParseAddress(strings.TrimSpace(value)); err == nil {
		return Address{Name: a.Name, Email: a.Address}
	}
	return Address{}
}
