package mail

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Finding a domain's mail servers from an address, for the account form
// (discover.go). Nothing here sends a password: the address is all the
// configuration endpoints and the DNS lookups learn, and only the
// domain's own hosts are asked. Every request is bounded in time, size
// and redirects, and the whole lookup can be cancelled. What a lookup
// finds is cached in the store, and a setting the user entered for the
// domain stays and wins over later ones.

// DiscoverySource is the step that found a domain's servers.
type DiscoverySource string

// The sources of a Discovery, in the order they are tried.
const (
	// DiscoveryManual is the settings the user entered for the domain,
	// which a lookup never replaces.
	DiscoveryManual DiscoverySource = "manual"
	// DiscoveryPreset is a provider's known servers.
	DiscoveryPreset DiscoverySource = "preset"
	// DiscoveryAutoconfig is Thunderbird's configuration format, at the
	// domain's autoconfig host or well-known path.
	DiscoveryAutoconfig DiscoverySource = "autoconfig"
	// DiscoveryAutodiscover is Microsoft's XML exchange, at the
	// autodiscover host or the domain's own.
	DiscoveryAutodiscover DiscoverySource = "autodiscover"
	// DiscoverySRV is the DNS records that name the servers.
	DiscoverySRV DiscoverySource = "srv"
)

// Discovery is what a domain's lookup found. IMAP or SMTP may be empty
// when only a part was found, which the error says as well.
type Discovery struct {
	Domain string
	IMAP   Server
	SMTP   Server
	Source DiscoverySource
}

// Complete reports whether both servers are known.
func (d Discovery) Complete() bool { return d.IMAP.Host != "" && d.SMTP.Host != "" }

// DiscoveryStep is a source that was tried and what it could not do.
type DiscoveryStep struct {
	Source DiscoverySource
	Err    string
}

// DiscoveryError says that no complete settings were found, and which
// sources it tried. A Discovery returned with it holds what was found.
type DiscoveryError struct {
	Domain string
	Steps  []DiscoveryStep
}

func (e *DiscoveryError) Error() string {
	parts := make([]string, 0, len(e.Steps))
	for _, s := range e.Steps {
		parts = append(parts, string(s.Source)+": "+s.Err)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("mail: no server settings found for %s", e.Domain)
	}
	return fmt.Sprintf("mail: no complete server settings for %s (%s)", e.Domain, strings.Join(parts, "; "))
}

// The bounds of one lookup.
const (
	discoverTimeout        = 8 * time.Second
	discoverRequestTimeout = 3 * time.Second
	discoverMaxRedirects   = 3
	discoverMaxBody        = 256 << 10
)

// Discover finds the mail servers of an email address's domain: first
// what the user entered for it, then a provider's preset, then what an
// earlier lookup found, and then the configuration endpoints and DNS. A
// complete result is cached; a lookup that fails returns the steps it
// took in a *DiscoveryError, with what it did find.
func (s *Service) Discover(ctx context.Context, email string) (Discovery, error) {
	domain, err := discoveryDomain(email)
	if err != nil {
		return Discovery{}, err
	}
	d := Discovery{Domain: domain}
	cached, hasCache, cacheErr := s.store.Discovery(ctx, domain)
	if cacheErr == nil && hasCache && cached.Source == DiscoveryManual {
		return cached, nil
	}
	if p, ok := ProviderFor(email); ok {
		return Discovery{Domain: domain, IMAP: p.IMAP, SMTP: p.SMTP, Source: DiscoveryPreset}, nil
	}
	if cacheErr == nil && hasCache && cached.Complete() {
		return cached, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.discoverTimeout)
	defer cancel()
	var steps []DiscoveryStep
	fail := func(source DiscoverySource, err error) {
		steps = append(steps, DiscoveryStep{Source: source, Err: err.Error()})
	}

	if err := s.discoverAutoconfig(ctx, email, domain, &d); err != nil {
		fail(DiscoveryAutoconfig, err)
	}
	if !d.Complete() && ctx.Err() == nil {
		if err := s.discoverAutodiscover(ctx, email, domain, &d); err != nil {
			fail(DiscoveryAutodiscover, err)
		}
	}
	if !d.Complete() && ctx.Err() == nil {
		if err := s.discoverSRV(ctx, domain, &d); err != nil {
			fail(DiscoverySRV, err)
		}
	}
	if ctx.Err() != nil {
		// Cancelled, or out of time: the caller wanted none of this.
		return d, ctx.Err()
	}
	if !d.Complete() {
		return d, &DiscoveryError{Domain: domain, Steps: steps}
	}
	// A complete result is the domain's for later forms. A store that
	// cannot keep it does not make the lookup fail.
	_ = s.store.SaveDiscovery(ctx, d)
	return d, nil
}

// RememberDiscovery keeps servers the user entered for a domain, which
// then win over a preset and every later lookup.
func (s *Service) RememberDiscovery(ctx context.Context, email string, imap, smtp Server) error {
	domain, err := discoveryDomain(email)
	if err != nil {
		return err
	}
	if imap.Host == "" || smtp.Host == "" {
		return errors.New("mail: the servers are incomplete")
	}
	return s.store.SaveDiscovery(ctx, Discovery{Domain: domain, IMAP: imap, SMTP: smtp, Source: DiscoveryManual})
}

// discoveryDomain reads the domain of an email address.
func discoveryDomain(email string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || addr.Name != "" {
		return "", fmt.Errorf("mail: %q is not an email address", email)
	}
	at := strings.LastIndexByte(addr.Address, '@')
	if at < 0 || at == len(addr.Address)-1 {
		return "", fmt.Errorf("mail: %q is not an email address", email)
	}
	domain := strings.ToLower(addr.Address[at+1:])
	if strings.ContainsAny(domain, " \t/") {
		return "", fmt.Errorf("mail: %q is not an email address", email)
	}
	return domain, nil
}

// autoconfigURLs are the endpoints of Thunderbird's configuration
// format, the domain's own host first.
func autoconfigURLs(email, domain string) []string {
	q := url.Values{"emailaddress": {email}}.Encode()
	return []string{
		"https://autoconfig." + domain + "/mail/config-v1.1.xml?" + q,
		"https://" + domain + "/.well-known/autoconfig/mail/config-v1.1.xml?" + q,
	}
}

// discoverAutoconfig reads the domain's configuration in Thunderbird's
// format.
func (s *Service) discoverAutoconfig(ctx context.Context, email, domain string, d *Discovery) error {
	var last error
	for _, u := range autoconfigURLs(email, domain) {
		body, err := s.discoverFetch(ctx, http.MethodGet, u, "")
		if err != nil {
			last = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		got, err := parseAutoconfig(body, domain)
		if err != nil {
			last = err
			continue
		}
		mergeDiscovery(d, got, DiscoveryAutoconfig)
		return nil
	}
	if last == nil {
		last = errors.New("no configuration")
	}
	return last
}

// autodiscoverURLs are Microsoft's endpoints, the autodiscover host
// first.
func autodiscoverURLs(domain string) []string {
	return []string{
		"https://autodiscover." + domain + "/autodiscover/autodiscover.xml",
		"https://" + domain + "/autodiscover/autodiscover.xml",
	}
}

// autodiscoverRequest asks for an address's settings in Microsoft's
// format. Only the address is there.
func autodiscoverRequest(email string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006">` +
		`<Request><EMailAddress>` + xmlEscape(email) + `</EMailAddress>` +
		`<AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema>` +
		`</Request></Autodiscover>`
}

// discoverAutodiscover reads the domain's configuration in Microsoft's
// format.
func (s *Service) discoverAutodiscover(ctx context.Context, email, domain string, d *Discovery) error {
	var last error
	body := autodiscoverRequest(email)
	for _, u := range autodiscoverURLs(domain) {
		resp, err := s.discoverFetch(ctx, http.MethodPost, u, body)
		if err != nil {
			last = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		got, err := parseAutodiscover(resp)
		if err != nil {
			last = err
			continue
		}
		mergeDiscovery(d, got, DiscoveryAutodiscover)
		return nil
	}
	if last == nil {
		last = errors.New("no configuration")
	}
	return last
}

// discoverSRV looks for the DNS records that name the servers. The
// secure services are preferred; a plain one is still STARTTLS.
func (s *Service) discoverSRV(ctx context.Context, domain string, d *Discovery) error {
	var errs []string
	if d.IMAP.Host == "" {
		imap, err := s.lookupMailSRV(ctx, domain, true)
		if err != nil {
			errs = append(errs, "IMAP: "+err.Error())
		} else {
			d.IMAP = imap
		}
	}
	if d.SMTP.Host == "" {
		smtp, err := s.lookupMailSRV(ctx, domain, false)
		if err != nil {
			errs = append(errs, "SMTP: "+err.Error())
		} else {
			d.SMTP = smtp
		}
	}
	if d.Source == "" && (d.IMAP.Host != "" || d.SMTP.Host != "") {
		d.Source = DiscoverySRV
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// lookupMailSRV reads the IMAP or SMTP record of a domain, the secure
// service first.
func (s *Service) lookupMailSRV(ctx context.Context, domain string, imap bool) (Server, error) {
	services := []struct {
		name string
		tls  TLSMode
	}{
		{"imaps", TLSImplicit},
		{"imap", TLSStartTLS},
	}
	if !imap {
		services = []struct {
			name string
			tls  TLSMode
		}{
			{"submissions", TLSStartTLS},
			{"submission", TLSStartTLS},
		}
	}
	var errs []string
	for _, svc := range services {
		_, addrs, err := s.lookupSRV(ctx, svc.name, "tcp", domain)
		if err != nil {
			errs = append(errs, err.Error())
			if ctx.Err() != nil {
				return Server{}, ctx.Err()
			}
			continue
		}
		for _, rec := range addrs {
			host := strings.TrimSuffix(strings.TrimSpace(rec.Target), ".")
			if host == "" || host == "." || rec.Port == 0 {
				continue
			}
			return Server{Host: host, Port: int(rec.Port), TLS: svc.tls}, nil
		}
		errs = append(errs, "_"+svc.name+"._tcp."+domain+": no target")
	}
	if len(errs) == 0 {
		errs = append(errs, "no record")
	}
	return Server{}, errors.New(errs[len(errs)-1])
}

// discoverFetch reads a configuration endpoint, bounded in time and
// size. A redirect must stay on https.
func (s *Service) discoverFetch(ctx context.Context, method, u, body string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.discoverRequestTimeout)
	defer cancel()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Shu")
	if body != "" {
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	}
	client, err := s.discoverHTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", req.URL.Hostname(), resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, discoverMaxBody))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// discoverHTTPClient is the client the endpoints are read with: the
// settings' proxy and certificate authority, and a cap on redirects.
func (s *Service) discoverHTTPClient(ctx context.Context) (*http.Client, error) {
	base, err := s.discoverClient(ctx)
	if err != nil {
		return nil, err
	}
	policy := func(req *http.Request, via []*http.Request) error {
		if len(via) >= discoverMaxRedirects {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to a URL that is not https")
		}
		return nil
	}
	if base.CheckRedirect != nil {
		inner := base.CheckRedirect
		policy = func(req *http.Request, via []*http.Request) error {
			if err := inner(req, via); err != nil {
				return err
			}
			return policy(req, via)
		}
	}
	return &http.Client{Transport: base.Transport, Timeout: base.Timeout, CheckRedirect: policy}, nil
}

// mergeDiscovery takes what a source found, the sources before it
// filling first.
func mergeDiscovery(d *Discovery, got Discovery, source DiscoverySource) {
	if d.IMAP.Host == "" {
		d.IMAP = got.IMAP
	}
	if d.SMTP.Host == "" {
		d.SMTP = got.SMTP
	}
	if d.Source == "" || d.IMAP.Host != "" && d.SMTP.Host != "" {
		d.Source = source
	}
}

// xmlEscape writes text as XML content.
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// autoconfigXML is the part of Thunderbird's configuration format the
// lookup reads.
type autoconfigXML struct {
	XMLName  xml.Name `xml:"clientConfig"`
	Provider struct {
		Incoming []autoconfigServer `xml:"incomingServer"`
		Outgoing []autoconfigServer `xml:"outgoingServer"`
	} `xml:"emailProvider"`
}

type autoconfigServer struct {
	Type       string `xml:"type,attr"`
	Hostname   string `xml:"hostname"`
	Port       string `xml:"port"`
	SocketType string `xml:"socketType"`
}

// parseAutoconfig reads a configuration in Thunderbird's format. A
// server without TLS is no setting of ours, and one without a host is
// skipped.
func parseAutoconfig(data []byte, domain string) (Discovery, error) {
	var cfg autoconfigXML
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return Discovery{}, fmt.Errorf("not an autoconfig file: %v", err)
	}
	var out Discovery
	for _, srv := range cfg.Provider.Incoming {
		if !strings.EqualFold(srv.Type, "imap") {
			continue
		}
		if server, ok := autoconfigServerOf(srv, domain, true); ok {
			out.IMAP = server
			break
		}
	}
	for _, srv := range cfg.Provider.Outgoing {
		if !strings.EqualFold(srv.Type, "smtp") {
			continue
		}
		if server, ok := autoconfigServerOf(srv, domain, false); ok {
			out.SMTP = server
			break
		}
	}
	if out.IMAP.Host == "" && out.SMTP.Host == "" {
		return Discovery{}, errors.New("no IMAP or SMTP server in the configuration")
	}
	return out, nil
}

// autoconfigServerOf reads one server of a configuration, false for one
// that cannot be used.
func autoconfigServerOf(srv autoconfigServer, domain string, imap bool) (Server, bool) {
	host := strings.TrimSpace(srv.Hostname)
	host = strings.ReplaceAll(host, "%EMAILDOMAIN%", domain)
	host = strings.ReplaceAll(host, "%EMAILDOMAINDOMAIN%", domain)
	if host == "" {
		return Server{}, false
	}
	var tls TLSMode
	switch strings.ToLower(strings.TrimSpace(srv.SocketType)) {
	case "ssl", "tls":
		tls = TLSImplicit
	case "starttls":
		tls = TLSStartTLS
	case "":
		if port, err := strconv.Atoi(strings.TrimSpace(srv.Port)); err == nil {
			tls = tlsForPort(port)
		}
	default:
		// Plain text, which is not offered.
		return Server{}, false
	}
	port := defaultPort(imap, tls)
	if p, err := strconv.Atoi(strings.TrimSpace(srv.Port)); err == nil && p > 0 && p < 65536 {
		port = p
	}
	if tls == "" {
		return Server{}, false
	}
	return Server{Host: host, Port: port, TLS: tls}, true
}

// defaultPort is the port a server that names none listens on.
func defaultPort(imap bool, tls TLSMode) int {
	switch {
	case imap && tls == TLSImplicit:
		return 993
	case imap:
		return 143
	case tls == TLSImplicit:
		return 465
	default:
		return 587
	}
}

// tlsForPort guesses how a server starts TLS from its port.
func tlsForPort(port int) TLSMode {
	switch port {
	case 143, 587, 25:
		return TLSStartTLS
	case 993, 465:
		return TLSImplicit
	}
	return ""
}

// autodiscoverXML is the part of Microsoft's response the lookup reads.
type autodiscoverXML struct {
	Response struct {
		Account struct {
			Protocols []autodiscoverProtocol `xml:"Protocol"`
		} `xml:"Account"`
	} `xml:"Response"`
}

type autodiscoverProtocol struct {
	Type       string `xml:"Type"`
	Server     string `xml:"Server"`
	Port       int    `xml:"Port"`
	SSL        string `xml:"SSL"`
	Encryption string `xml:"Encryption"`
}

// parseAutodiscover reads a configuration in Microsoft's format.
func parseAutodiscover(data []byte) (Discovery, error) {
	var cfg autodiscoverXML
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return Discovery{}, fmt.Errorf("not an autodiscover file: %v", err)
	}
	var out Discovery
	for _, p := range cfg.Response.Account.Protocols {
		host := strings.TrimSpace(p.Server)
		if host == "" {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(p.Type)) {
		case "IMAP":
			if out.IMAP.Host != "" {
				continue
			}
			tls := TLSStartTLS
			if truthy(p.SSL) {
				tls = TLSImplicit
			}
			out.IMAP = Server{Host: host, Port: portOr(p.Port, defaultPort(true, tls)), TLS: tls}
		case "SMTP":
			if out.SMTP.Host != "" {
				continue
			}
			tls := TLSStartTLS
			switch {
			case truthy(p.SSL):
				tls = TLSImplicit
			case truthy(p.Encryption) || strings.EqualFold(strings.TrimSpace(p.Encryption), "starttls") ||
				strings.EqualFold(strings.TrimSpace(p.Encryption), "tls"):
				tls = TLSStartTLS
			case strings.TrimSpace(p.Encryption) == "":
			default:
				// No encryption, which is not offered.
				continue
			}
			out.SMTP = Server{Host: host, Port: portOr(p.Port, defaultPort(false, tls)), TLS: tls}
		}
	}
	if out.IMAP.Host == "" && out.SMTP.Host == "" {
		return Discovery{}, errors.New("no IMAP or SMTP server in the configuration")
	}
	return out, nil
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes":
		return true
	}
	return false
}

func portOr(port, fallback int) int {
	if port > 0 && port < 65536 {
		return port
	}
	return fallback
}
