package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNetworkSettingsRoundTrip(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	set, stored, err := svc.NetworkSettings(ctx)
	if err != nil || stored || set.Mode != ProxySystem {
		t.Fatalf("defaults = %+v, stored=%v, err=%v", set, stored, err)
	}
	want := NetworkSettings{
		Mode: ProxyManual, ProxyURL: "socks5://127.0.0.1:1080",
		ProxyUsername: "ann", Bypass: "localhost, .example.com",
		CA: "-----BEGIN CERTIFICATE-----\n",
	}
	if err := svc.SaveNetworkSettings(ctx, want, "secret", true); err != nil {
		t.Fatal(err)
	}
	got, stored, err := svc.NetworkSettings(ctx)
	if err != nil || !stored {
		t.Fatalf("stored=%v, err=%v", stored, err)
	}
	if got.Mode != want.Mode || got.ProxyURL != want.ProxyURL || got.ProxyUsername != want.ProxyUsername ||
		got.Bypass != want.Bypass || got.CA != want.CA {
		t.Errorf("settings = %+v", got)
	}
	// A bare host:port is an HTTP proxy.
	if err := svc.SaveNetworkSettings(ctx, NetworkSettings{Mode: ProxyManual, ProxyURL: "proxy.example:3128"}, "", false); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := svc.NetworkSettings(ctx); got.ProxyURL != "http://proxy.example:3128" {
		t.Errorf("bare address became %q", got.ProxyURL)
	}
	// Credentials typed into the address move to the user and the
	// keychain, and never stay in the stored address.
	if err := svc.SaveNetworkSettings(ctx, NetworkSettings{Mode: ProxyManual, ProxyURL: "http://ann:secret@proxy.example:3128"}, "", false); err != nil {
		t.Fatal(err)
	}
	clean, stored, _ := svc.NetworkSettings(ctx)
	if clean.ProxyURL != "http://proxy.example:3128" || clean.ProxyUsername != "ann" || !stored {
		t.Errorf("credentials in the address: %+v, stored=%v", clean, stored)
	}

	// The password goes when it is cleared.
	if err := svc.SaveNetworkSettings(ctx, want, "", true); err != nil {
		t.Fatal(err)
	}
	if _, stored, _ := svc.NetworkSettings(ctx); stored {
		t.Error("the proxy password stayed")
	}
	if err := svc.SaveNetworkSettings(ctx, NetworkSettings{Mode: ProxyManual, ProxyURL: "ftp://proxy"}, "", false); err == nil {
		t.Error("an unsupported scheme was accepted")
	}
}

func TestProxyFor(t *testing.T) {
	manual := NetworkSettings{Mode: ProxyManual, ProxyURL: "http://proxy.example:8080", Bypass: "localhost, .internal.example"}
	for _, c := range []struct {
		host string
		port int
		want string
	}{
		{"imap.example.com", 993, "http://proxy.example:8080"},
		{"localhost", 993, ""},
		{"mail.internal.example", 993, ""},
		{"internal.example", 993, "http://proxy.example:8080"},
	} {
		u, err := proxyFor(manual, c.host, c.port, "")
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.host, got, c.want)
		}
	}
	if u, err := proxyFor(NetworkSettings{Mode: ProxyDirect}, "imap.example.com", 993, ""); err != nil || u != nil {
		t.Errorf("direct = %v, %v", u, err)
	}
	u, err := proxyFor(NetworkSettings{Mode: ProxyManual, ProxyURL: "http://proxy.example:8080", ProxyUsername: "ann"},
		"imap.example.com", 993, "secret")
	if err != nil || u.User == nil {
		t.Fatalf("credentials: %v, %v", u, err)
	}
	if pass, _ := u.User.Password(); pass != "secret" {
		t.Errorf("password = %q", pass)
	}
}

// DialProxy goes through an HTTP CONNECT tunnel, with the credentials the
// proxy asks for.
func TestDialHTTPProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotAuth := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := br.ReadString('\n')
		if err != nil || !strings.HasPrefix(req, "CONNECT target.example:993 ") {
			return
		}
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.ToLower(line), "proxy-authorization:") {
				gotAuth <- strings.TrimSpace(line)
			}
			if line == "\r\n" {
				break
			}
		}
		io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		io.Copy(conn, conn)
	}()
	u, _ := url.Parse("http://ann:secret@" + ln.Addr().String())
	conn, err := dialProxy(context.Background(), u, "target.example:993")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "ping\n"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping\n" {
		t.Errorf("echo = %q, %v", buf, err)
	}
	select {
	case auth := <-gotAuth:
		if !strings.Contains(auth, "Basic ") {
			t.Errorf("auth = %q", auth)
		}
	case <-time.After(time.Second):
		t.Error("the proxy saw no credentials")
	}
}

func TestTLSTrustsImportedCA(t *testing.T) {
	svc, _, _ := newTestService()
	ctx := context.Background()
	certPEM := selfSignedPEM(t)
	if err := svc.SaveNetworkSettings(ctx, NetworkSettings{Mode: ProxyDirect, CA: certPEM}, "", false); err != nil {
		t.Fatal(err)
	}
	cfg := svc.tlsConfigFor(ctx, "mail.example.com")
	if cfg.RootCAs == nil {
		t.Fatal("the imported authority was not loaded")
	}
	if err := svc.SaveNetworkSettings(ctx, NetworkSettings{Mode: ProxyDirect}, "", false); err != nil {
		t.Fatal(err)
	}
	if cfg := svc.tlsConfigFor(ctx, "mail.example.com"); cfg.RootCAs != nil {
		t.Error("removing the authority left it trusted")
	}
}

func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Shu Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if der == nil {
		t.Fatal("no certificate")
	}
	return fmt.Sprintf("%s", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
