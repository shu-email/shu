package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
)

// Proxies and custom certificate authorities. The settings apply to every
// connection the engine makes: account tests, syncs, sending and
// attachment fetches. They are read from the store before each dial, so a
// change applies to the next connection without a restart. The proxy
// password is in the keychain, never in the store.

// ProxyMode is how a connection finds a proxy.
type ProxyMode string

// The ways the settings choose.
const (
	// ProxySystem follows the environment's proxy variables, as the
	// system's network settings put them there.
	ProxySystem ProxyMode = "system"
	// ProxyDirect connects to every server itself.
	ProxyDirect ProxyMode = "direct"
	// ProxyManual uses ProxyURL, but for the hosts Bypass names.
	ProxyManual ProxyMode = "manual"
)

// NetworkSettings are how connections reach the network.
type NetworkSettings struct {
	Mode ProxyMode
	// ProxyURL is an http, https, socks5 or socks5h proxy, with its
	// port; a bare host:port is an HTTP proxy. Credentials in the URL are
	// not stored: the user's and the keychain's password are.
	ProxyURL string
	// ProxyUsername is the proxy's user, and its password is in the
	// keychain (ProxyPasswordKey).
	ProxyUsername string
	// Bypass lists the hosts that connect directly, as NO_PROXY does:
	// comma or newline separated.
	Bypass string
	// CA is a PEM certificate authority to trust besides the system's.
	CA string
}

// DefaultNetworkSettings follow the system's proxy and trust the system's
// roots.
func DefaultNetworkSettings() NetworkSettings {
	return NetworkSettings{Mode: ProxySystem}
}

// The keys of the network settings in the store.
const (
	settingProxyMode     = "network.proxy_mode"
	settingProxyURL      = "network.proxy_url"
	settingProxyUsername = "network.proxy_username"
	settingProxyBypass   = "network.proxy_bypass"
	settingProxyCA       = "network.proxy_ca"
)

// ProxyPasswordKey is the keychain entry of the proxy's password.
func ProxyPasswordKey() string { return "network.proxy_password" }

// NetworkSettings reads the settings, with the defaults for what was
// never set, and whether the proxy password is stored.
func (s *Service) NetworkSettings(ctx context.Context) (NetworkSettings, bool, error) {
	out := DefaultNetworkSettings()
	read := func(key string, dst *string) error {
		raw, ok, err := s.store.Setting(ctx, key)
		if err == nil && ok {
			*dst = raw
		}
		return err
	}
	var mode string
	if err := read(settingProxyMode, &mode); err != nil {
		return out, false, err
	}
	switch ProxyMode(mode) {
	case ProxySystem, ProxyDirect, ProxyManual:
		out.Mode = ProxyMode(mode)
	}
	for key, dst := range map[string]*string{
		settingProxyURL:      &out.ProxyURL,
		settingProxyUsername: &out.ProxyUsername,
		settingProxyBypass:   &out.Bypass,
		settingProxyCA:       &out.CA,
	} {
		if err := read(key, dst); err != nil {
			return out, false, err
		}
	}
	_, err := s.secrets.Get(ProxyPasswordKey())
	return out, err == nil, nil
}

// SaveNetworkSettings writes the settings. setPassword writes password to
// the keychain, or removes the entry when it is empty; without it the
// stored password stays.
func (s *Service) SaveNetworkSettings(ctx context.Context, set NetworkSettings, password string, setPassword bool) error {
	if set.Mode == "" {
		set.Mode = ProxySystem
	}
	if set.Mode == ProxyManual {
		u, err := proxyURL(set.ProxyURL)
		if err != nil {
			return err
		}
		// Credentials typed into the address go where credentials
		// belong: the user's name in the settings, the password in the
		// keychain. The address itself is stored without them.
		if u.User != nil {
			if set.ProxyUsername == "" {
				set.ProxyUsername = u.User.Username()
			}
			if pass, ok := u.User.Password(); ok && pass != "" && !setPassword {
				password, setPassword = pass, true
			}
			u.User = nil
		}
		set.ProxyURL = u.String()
	}
	if setPassword {
		if password == "" {
			_ = s.secrets.Delete(ProxyPasswordKey())
		} else if err := s.secrets.Set(ProxyPasswordKey(), []byte(password)); err != nil {
			return fmt.Errorf("mail: store the proxy password: %w", err)
		}
	}
	for key, value := range map[string]string{
		settingProxyMode:     string(set.Mode),
		settingProxyURL:      strings.TrimSpace(set.ProxyURL),
		settingProxyUsername: strings.TrimSpace(set.ProxyUsername),
		settingProxyBypass:   strings.TrimSpace(set.Bypass),
		settingProxyCA:       set.CA,
	} {
		if err := s.store.SetSetting(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

// proxyURL parses a proxy as the user wrote it. A bare host:port is an
// HTTP proxy.
func proxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("mail: the proxy address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("mail: the proxy address: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("mail: unsupported proxy scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("mail: the proxy address has no host")
	}
	return u, nil
}

// proxyFor returns the proxy a connection to host and port goes through,
// or nil for a direct connection.
func proxyFor(set NetworkSettings, host string, port int, password string) (*url.URL, error) {
	switch set.Mode {
	case ProxyDirect:
		return nil, nil
	case ProxyManual:
		u, err := proxyURL(set.ProxyURL)
		if err != nil {
			return nil, err
		}
		if proxyBypassed(set.Bypass, host, port) {
			return nil, nil
		}
		if u.User == nil && set.ProxyUsername != "" {
			u.User = url.UserPassword(set.ProxyUsername, password)
		}
		return u, nil
	default:
		cfg := httpproxy.Config{
			HTTPProxy:  firstEnv("HTTP_PROXY", "http_proxy"),
			HTTPSProxy: firstEnv("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"),
			NoProxy:    firstEnv("NO_PROXY", "no_proxy"),
		}
		u, err := cfg.ProxyFunc()(&url.URL{Scheme: "https", Host: net.JoinHostPort(host, strconv.Itoa(port))})
		if err != nil || u == nil {
			return nil, nil
		}
		return u, nil
	}
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// proxyBypassed reports whether a host connects directly, by NO_PROXY's
// rules.
func proxyBypassed(list, host string, port int) bool {
	if strings.TrimSpace(list) == "" {
		return false
	}
	cfg := httpproxy.Config{HTTPProxy: "http://proxy.invalid", HTTPSProxy: "http://proxy.invalid", NoProxy: list}
	u, err := cfg.ProxyFunc()(&url.URL{Scheme: "https", Host: net.JoinHostPort(host, strconv.Itoa(port))})
	return err != nil || u == nil
}

// dialNetwork opens a connection to host:port through the settings'
// proxy, or directly.
func (s *Service) dialNetwork(ctx context.Context, host string, port int) (net.Conn, error) {
	set, err := s.networkSettings(ctx)
	if err != nil {
		return nil, err
	}
	password := ""
	if set.ProxyUsername != "" || set.Mode == ProxyManual {
		if data, err := s.secrets.Get(ProxyPasswordKey()); err == nil {
			password = string(data)
		}
	}
	u, err := proxyFor(set, host, port, password)
	if err != nil {
		return nil, err
	}
	target := net.JoinHostPort(host, strconv.Itoa(port))
	if u == nil {
		return (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", target)
	}
	return dialProxy(ctx, u, target)
}

// networkSettings reads the settings, and a failure is no reason not to
// connect directly: the system's defaults apply then.
func (s *Service) networkSettings(ctx context.Context) (NetworkSettings, error) {
	set, _, err := s.NetworkSettings(ctx)
	if err != nil {
		return DefaultNetworkSettings(), err
	}
	return set, nil
}

// dialProxy opens a connection through an HTTP CONNECT or SOCKS5 proxy.
func dialProxy(ctx context.Context, u *url.URL, target string) (net.Conn, error) {
	switch u.Scheme {
	case "socks5", "socks5h":
		return dialSOCKS5(ctx, u, target)
	default:
		return dialHTTPProxy(ctx, u, target)
	}
}

// dialSOCKS5 opens a connection through a SOCKS5 proxy, with its user and
// password when it has them.
func dialSOCKS5(ctx context.Context, u *url.URL, target string) (net.Conn, error) {
	var auth *proxy.Auth
	if u.User != nil {
		auth = &proxy.Auth{User: u.User.Username()}
		auth.Password, _ = u.User.Password()
	}
	port := u.Port()
	if port == "" {
		port = "1080"
	}
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort(u.Hostname(), port), auth, &net.Dialer{Timeout: dialTimeout})
	if err != nil {
		return nil, fmt.Errorf("mail: socks5 proxy: %w", err)
	}
	if cd, ok := d.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", target)
	}
	return d.Dial("tcp", target)
}

// dialHTTPProxy opens a tunnel through an HTTP proxy with CONNECT, adding
// the proxy's credentials when it has them.
func dialHTTPProxy(ctx context.Context, u *url.URL, target string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "8080")
	}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mail: proxy: %w", err)
	}
	if u.Scheme == "https" {
		cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
		tlsConn := tls.Client(conn, cfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("mail: proxy tls: %w", err)
		}
		conn = tlsConn
	}
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}
	var b strings.Builder
	b.WriteString("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n")
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		b.WriteString("Proxy-Authorization: Basic " + cred + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		conn.Close()
		return nil, fmt.Errorf("mail: proxy: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("mail: proxy: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("mail: proxy refused the tunnel: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn keeps the bytes a proxy's answer left in the reader.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// tlsConfigFor returns the TLS configuration to dial servers with, with
// the imported certificate authority's roots when there is one.
func (s *Service) tlsConfigFor(ctx context.Context, host string) *tls.Config {
	var cfg *tls.Config
	if s.tlsConfig != nil {
		cfg = s.tlsConfig.Clone()
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
	} else {
		cfg = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	set, err := s.networkSettings(ctx)
	if err != nil || strings.TrimSpace(set.CA) == "" {
		return cfg
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if pool.AppendCertsFromPEM([]byte(set.CA)) {
		cfg.RootCAs = pool
	}
	return cfg
}

// HTTPClient is the client the AI layer sends with: the settings' proxy,
// certificate authority and timeout.
func (s *Service) HTTPClient(ctx context.Context) (*http.Client, error) {
	set, err := s.networkSettings(ctx)
	if err != nil {
		return nil, err
	}
	password := ""
	if data, err := s.secrets.Get(ProxyPasswordKey()); err == nil {
		password = string(data)
	}
	transport := &http.Transport{
		TLSClientConfig: s.tlsConfigFor(ctx, ""),
		Proxy: func(req *http.Request) (*url.URL, error) {
			return proxyFor(set, req.URL.Hostname(), portOf(req.URL), password)
		},
	}
	return &http.Client{Transport: transport, Timeout: 90 * time.Second}, nil
}

func portOf(u *url.URL) int {
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if u.Scheme == "http" {
		return 80
	}
	return 443
}

// TestNetwork checks the same path real mail takes: the current network
// settings, an account's servers. It sends and fetches nothing.
func (s *Service) TestNetwork(ctx context.Context, accountID string) error {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	return s.TestConnection(ctx, a, password)
}
