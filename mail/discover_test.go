package mail

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// discoverHTTP is the fake of the configuration endpoints: what it
// answers, and what was asked.
type discoverHTTP struct {
	mu       sync.Mutex
	requests []string
	bodies   []string
	headers  []http.Header
	serve    func(req *http.Request) (*http.Response, error)
}

func (d *discoverHTTP) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	d.mu.Lock()
	d.requests = append(d.requests, req.URL.String())
	d.bodies = append(d.bodies, body)
	d.headers = append(d.headers, req.Header.Clone())
	d.mu.Unlock()
	return d.serve(req)
}

func (d *discoverHTTP) asked() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}

// discoverClient makes a service read the endpoints from fake.
func discoverClient(svc *Service, fake *discoverHTTP) {
	svc.discoverClient = func(ctx context.Context) (*http.Client, error) {
		return &http.Client{Transport: fake}, nil
	}
}

func httpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status),
		Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// autoconfigDocument is a Thunderbird configuration for example.com.
const autoconfigDocument = `<?xml version="1.0"?>
<clientConfig version="1.1">
  <emailProvider id="example.com">
    <domain>example.com</domain>
    <incomingServer type="imap">
      <hostname>imap.example.com</hostname>
      <port>993</port>
      <socketType>SSL</socketType>
      <username>%EMAILADDRESS%</username>
    </incomingServer>
    <outgoingServer type="smtp">
      <hostname>smtp.example.com</hostname>
      <port>587</port>
      <socketType>STARTTLS</socketType>
      <username>%EMAILADDRESS%</username>
    </outgoingServer>
  </emailProvider>
</clientConfig>`

// autodiscoverDocument is a Microsoft response for example.com.
const autodiscoverDocument = `<?xml version="1.0"?>
<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006">
  <Response>
    <Account>
      <Protocol>
        <Type>IMAP</Type><Server>imap.example.com</Server><Port>993</Port><SSL>on</SSL>
      </Protocol>
      <Protocol>
        <Type>SMTP</Type><Server>smtp.example.com</Server><Port>587</Port><SSL>off</SSL><Encryption>TLS</Encryption>
      </Protocol>
    </Account>
  </Response>
</Autodiscover>`

// A known provider needs no network.
func TestDiscoverPreset(t *testing.T) {
	svc, _, _ := newTestService()
	fake := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		t.Errorf("the preset asked %s", req.URL)
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, fake)
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		t.Errorf("the preset looked up %s", name)
		return "", nil, errors.New("no record")
	}
	d, err := svc.Discover(context.Background(), "user@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoveryPreset || d.IMAP.Host != "imap.gmail.com" || d.SMTP.Host != "smtp.gmail.com" {
		t.Errorf("found %+v", d)
	}
	if len(fake.asked()) != 0 {
		t.Errorf("the endpoints asked: %v", fake.asked())
	}
}

// The domain's autoconfig host answers with the configuration, which
// names both servers; the address goes in the query, no password with
// it, and the finding is kept for the domain.
func TestDiscoverAutoconfig(t *testing.T) {
	svc, store, _ := newTestService()
	fake := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "autoconfig.example.com" && req.URL.Path == "/mail/config-v1.1.xml" {
			return httpResponse(200, autoconfigDocument), nil
		}
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, fake)
	d, err := svc.Discover(context.Background(), "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoveryAutoconfig {
		t.Errorf("source %q", d.Source)
	}
	if d.IMAP != (Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit}) {
		t.Errorf("IMAP %+v", d.IMAP)
	}
	if d.SMTP != (Server{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS}) {
		t.Errorf("SMTP %+v", d.SMTP)
	}
	asked := fake.asked()
	if len(asked) != 1 || !strings.Contains(asked[0], "emailaddress=ann%40example.com") {
		t.Errorf("asked %v", asked)
	}
	if strings.Contains(strings.Join(asked, " "), "ass") {
		t.Errorf("a request carried more than the address: %v", asked)
	}
	cached, ok, err := store.Discovery(context.Background(), "example.com")
	if err != nil || !ok || cached.IMAP != d.IMAP || cached.SMTP != d.SMTP {
		t.Errorf("kept %+v, %v, %v", cached, ok, err)
	}
	// The next form reads the cache, and asks nothing.
	second := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		t.Errorf("a cached domain asked %s", req.URL)
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, second)
	if d2, err := svc.Discover(context.Background(), "bob@example.com"); err != nil || d2.IMAP != d.IMAP {
		t.Errorf("the cached lookup: %+v, %v", d2, err)
	}
}

// The well-known path answers when the autoconfig host does not.
func TestDiscoverAutoconfigWellKnown(t *testing.T) {
	svc, _, _ := newTestService()
	fake := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "example.com" && req.URL.Path == "/.well-known/autoconfig/mail/config-v1.1.xml" {
			return httpResponse(200, autoconfigDocument), nil
		}
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, fake)
	d, err := svc.Discover(context.Background(), "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoveryAutoconfig || d.IMAP.Host != "imap.example.com" {
		t.Errorf("found %+v", d)
	}
	if asked := fake.asked(); len(asked) != 2 {
		t.Errorf("asked %v", asked)
	}
}

// Microsoft's exchange answers when no autoconfig does.
func TestDiscoverAutodiscover(t *testing.T) {
	svc, _, _ := newTestService()
	fake := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "autodiscover.example.com" && req.Method == http.MethodPost {
			return httpResponse(200, autodiscoverDocument), nil
		}
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, fake)
	d, err := svc.Discover(context.Background(), "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoveryAutodiscover {
		t.Errorf("source %q", d.Source)
	}
	if d.IMAP != (Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit}) {
		t.Errorf("IMAP %+v", d.IMAP)
	}
	if d.SMTP != (Server{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS}) {
		t.Errorf("SMTP %+v", d.SMTP)
	}
	for i, body := range fake.bodies {
		if body == "" {
			continue
		}
		if !strings.Contains(body, "<EMailAddress>ann@example.com</EMailAddress>") {
			t.Errorf("request %d: %s", i, body)
		}
		if strings.Contains(strings.ToLower(body), "password") {
			t.Errorf("request %d sent a password: %s", i, body)
		}
	}
}

// DNS answers when every endpoint fails; the secure services are taken.
func TestDiscoverSRV(t *testing.T) {
	svc, _, _ := newTestService()
	discoverClient(svc, &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		return httpResponse(404, ""), nil
	}})
	var asked []string
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		asked = append(asked, "_"+service+"._"+proto+"."+name)
		switch service {
		case "imaps":
			return "", []*net.SRV{{Target: "imap.example.com.", Port: 993}}, nil
		case "submissions":
			return "", []*net.SRV{{Target: "smtp.example.com.", Port: 465}}, nil
		}
		return "", nil, errors.New("no such host")
	}
	d, err := svc.Discover(context.Background(), "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoverySRV || d.IMAP.Host != "imap.example.com" || d.IMAP.TLS != TLSImplicit ||
		d.SMTP.Host != "smtp.example.com" || d.SMTP.TLS != TLSImplicit {
		t.Errorf("found %+v", d)
	}
	if len(asked) != 2 {
		t.Errorf("looked up %v", asked)
	}
}

// The port a record names decides how the connection starts TLS: the
// older submission service on 587, the newer one on 465.
func TestDiscoverSRVPorts(t *testing.T) {
	svc, _, _ := newTestService()
	port := uint16(587)
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		if service == "submission" {
			return "", []*net.SRV{{Target: "smtp.example.com.", Port: port}}, nil
		}
		return "", nil, errors.New("no record")
	}
	smtp, err := svc.lookupMailSRV(context.Background(), "example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if smtp != (Server{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS}) {
		t.Errorf("587 reads %+v", smtp)
	}
	port = 465
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		if service == "submissions" {
			return "", []*net.SRV{{Target: "smtp.example.com.", Port: port}}, nil
		}
		return "", nil, errors.New("no record")
	}
	smtp, err = svc.lookupMailSRV(context.Background(), "example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if smtp != (Server{Host: "smtp.example.com", Port: 465, TLS: TLSImplicit}) {
		t.Errorf("465 reads %+v", smtp)
	}
}

// One server found is no complete answer: it is returned, with what is
// missing and the sources that failed, and nothing is kept.
func TestDiscoverPartialSRV(t *testing.T) {
	svc, store, _ := newTestService()
	discoverClient(svc, &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		return httpResponse(404, ""), nil
	}})
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		if service == "imaps" {
			return "", []*net.SRV{{Target: "imap.example.com.", Port: 993}}, nil
		}
		return "", nil, errors.New("no record")
	}
	d, err := svc.Discover(context.Background(), "ann@example.com")
	var fail *DiscoveryError
	if !errors.As(err, &fail) {
		t.Fatalf("err %v", err)
	}
	if d.IMAP.Host != "imap.example.com" || d.SMTP.Host != "" {
		t.Errorf("found %+v", d)
	}
	var stages []DiscoverySource
	for _, s := range fail.Steps {
		stages = append(stages, s.Source)
	}
	if len(stages) != 3 || stages[0] != DiscoveryAutoconfig || stages[2] != DiscoverySRV {
		t.Errorf("stages %v", stages)
	}
	if _, ok, _ := store.Discovery(context.Background(), "example.com"); ok {
		t.Error("a partial finding was kept")
	}
}

// The user's own entry wins over a preset and over a lookup, and is
// what a later form reads.
func TestDiscoverManualOverride(t *testing.T) {
	svc, _, _ := newTestService()
	fake := &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		t.Errorf("a manual domain asked %s", req.URL)
		return httpResponse(404, ""), nil
	}}
	discoverClient(svc, fake)
	imap := Server{Host: "mail.example.org", Port: 143, TLS: TLSStartTLS}
	smtp := Server{Host: "send.example.org", Port: 587, TLS: TLSStartTLS}
	if err := svc.RememberDiscovery(context.Background(), "ann@gmail.com", imap, smtp); err != nil {
		t.Fatal(err)
	}
	d, err := svc.Discover(context.Background(), "bob@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != DiscoveryManual || d.IMAP != imap || d.SMTP != smtp {
		t.Errorf("found %+v", d)
	}
	if len(fake.asked()) != 0 {
		t.Errorf("asked %v", fake.asked())
	}
	if err := svc.RememberDiscovery(context.Background(), "not-an-address", imap, smtp); err == nil {
		t.Error("an address that is not one was kept")
	}
	if err := svc.RememberDiscovery(context.Background(), "ann@example.com", Server{}, smtp); err == nil {
		t.Error("incomplete servers were kept")
	}
}

// A lookup that is cancelled ends with the context's error, at once.
func TestDiscoverCancelled(t *testing.T) {
	svc, _, _ := newTestService()
	blocked := make(chan struct{})
	defer close(blocked)
	discoverClient(svc, &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-blocked:
			return nil, errors.New("closed")
		}
	}})
	ctx, cancel := context.WithCancel(context.Background())
	svc.discoverTimeout = time.Minute
	done := make(chan error, 1)
	go func() {
		_, err := svc.Discover(ctx, "ann@example.com")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lookup outlived its context")
	}
}

// A lookup is bounded in time.
func TestDiscoverTimeout(t *testing.T) {
	svc, _, _ := newTestService()
	discoverClient(svc, &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}})
	svc.discoverTimeout = 30 * time.Millisecond
	svc.discoverRequestTimeout = 20 * time.Millisecond
	start := time.Now()
	_, err := svc.Discover(context.Background(), "ann@example.com")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the lookup took %v", time.Since(start))
	}
}

// A redirect loop ends, and a redirect off https is refused.
func TestDiscoverRedirects(t *testing.T) {
	svc, _, _ := newTestService()
	loops := 0
	discoverClient(svc, &discoverHTTP{serve: func(req *http.Request) (*http.Response, error) {
		loops++
		resp := httpResponse(302, "")
		resp.Header.Set("Location", "https://autoconfig.example.com/again")
		return resp, nil
	}})
	svc.lookupSRV = func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error) {
		return "", nil, errors.New("no record")
	}
	if _, err := svc.Discover(context.Background(), "ann@example.com"); err == nil {
		t.Fatal("a redirect loop found settings")
	}
	if loops > 12 {
		t.Errorf("the redirects went round %d times", loops)
	}
}

// The configuration formats are read by the fields they carry.
func TestParseAutoconfig(t *testing.T) {
	d, err := parseAutoconfig([]byte(autoconfigDocument), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.IMAP != (Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit}) ||
		d.SMTP != (Server{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS}) {
		t.Errorf("read %+v", d)
	}
	// A placeholder host takes the domain, and a server without TLS is
	// no setting of ours.
	doc := `<clientConfig><emailProvider>
		<incomingServer type="imap"><hostname>imap.%EMAILDOMAIN%</hostname><socketType>SSL</socketType></incomingServer>
		<incomingServer type="imap"><hostname>plain.example.com</hostname><port>143</port><socketType>plain</socketType></incomingServer>
		<outgoingServer type="smtp"><hostname>smtp.example.com</hostname><socketType>plain</socketType></outgoingServer>
	</emailProvider></clientConfig>`
	d, err = parseAutoconfig([]byte(doc), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.IMAP != (Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit}) {
		t.Errorf("IMAP %+v", d.IMAP)
	}
	if d.SMTP.Host != "" {
		t.Errorf("a plain SMTP server was taken: %+v", d.SMTP)
	}
	if _, err := parseAutoconfig([]byte("<html>gone</html>"), "example.com"); err == nil {
		t.Error("a page that is no configuration was read")
	}
}

// Microsoft's format says how each server starts TLS, and a server
// without it is skipped.
func TestParseAutodiscover(t *testing.T) {
	d, err := parseAutodiscover([]byte(autodiscoverDocument))
	if err != nil {
		t.Fatal(err)
	}
	if d.IMAP != (Server{Host: "imap.example.com", Port: 993, TLS: TLSImplicit}) ||
		d.SMTP != (Server{Host: "smtp.example.com", Port: 587, TLS: TLSStartTLS}) {
		t.Errorf("read %+v", d)
	}
	doc := `<Autodiscover><Response><Account>
		<Protocol><Type>SMTP</Type><Server>smtp.example.com</Server><Port>25</Port><SSL>off</SSL><Encryption>None</Encryption></Protocol>
		<Protocol><Type>SMTP</Type><Server>smtp.example.com</Server><Port>465</Port><SSL>on</SSL></Protocol>
	</Account></Response></Autodiscover>`
	d, err = parseAutodiscover([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if d.SMTP != (Server{Host: "smtp.example.com", Port: 465, TLS: TLSImplicit}) {
		t.Errorf("SMTP %+v", d.SMTP)
	}
	if _, err := parseAutodiscover([]byte("no xml")); err == nil {
		t.Error("text that is no configuration was read")
	}
}
