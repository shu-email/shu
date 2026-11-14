package mail

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strconv"
	"strings"
)

// TLSMode is how a server connection starts TLS. Plain text is not
// offered: mail servers all speak TLS.
type TLSMode string

// The supported TLS modes.
const (
	TLSImplicit TLSMode = "tls"
	TLSStartTLS TLSMode = "starttls"
)

// Server is an IMAP or SMTP endpoint.
type Server struct {
	Host string  `json:"host"`
	Port int     `json:"port"`
	TLS  TLSMode `json:"tls"`
}

// Addr is host:port.
func (s Server) Addr() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

// Account is a mail account. Its password, or the refresh token of one
// that signs in with OAuth, is in the keychain, never here.
type Account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Username string `json:"username"`
	IMAP     Server `json:"imap"`
	SMTP     Server `json:"smtp"`
	// OAuth is the provider the account signs in with through OAuth
	// (OAuthGoogle, OAuthMicrosoft), "" for one that signs in with a
	// password.
	OAuth string `json:"oauth"`
}

// NewAccountID returns a random account ID.
func NewAccountID() string { return randomID(func() string { return "acct" }) }

// randomID is eight random bytes in hex. Randomness that is not there is
// no reason to fail a save: the fallback names the row instead, though a
// fallback that is one name, as an account's is, may name two rows alike
// if it fails twice.
func randomID(fallback func() string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fallback()
	}
	return hex.EncodeToString(b[:])
}

// Provider is a preset of a mail provider's servers.
type Provider struct {
	ID   string
	Name string
	IMAP Server
	SMTP Server
}

// Providers are the presets the account view offers.
var Providers = []Provider{
	{ID: "gmail", Name: "Gmail", IMAP: Server{"imap.gmail.com", 993, TLSImplicit}, SMTP: Server{"smtp.gmail.com", 465, TLSImplicit}},
	{ID: "icloud", Name: "iCloud", IMAP: Server{"imap.mail.me.com", 993, TLSImplicit}, SMTP: Server{"smtp.mail.me.com", 587, TLSStartTLS}},
	{ID: "outlook", Name: "Outlook", IMAP: Server{"outlook.office365.com", 993, TLSImplicit}, SMTP: Server{"smtp.office365.com", 587, TLSStartTLS}},
	{ID: "fastmail", Name: "Fastmail", IMAP: Server{"imap.fastmail.com", 993, TLSImplicit}, SMTP: Server{"smtp.fastmail.com", 465, TLSImplicit}},
	{ID: "yahoo", Name: "Yahoo", IMAP: Server{"imap.mail.yahoo.com", 993, TLSImplicit}, SMTP: Server{"smtp.mail.yahoo.com", 465, TLSImplicit}},
}

// ProviderByID returns a preset, or false for "custom".
func ProviderByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ProviderFor guesses the provider of an email address from its domain.
func ProviderFor(email string) (Provider, bool) {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return Provider{}, false
	}
	domain := strings.ToLower(email[at+1:])
	switch domain {
	case "gmail.com", "googlemail.com":
		return ProviderByID("gmail")
	case "icloud.com", "me.com", "mac.com":
		return ProviderByID("icloud")
	case "outlook.com", "hotmail.com", "live.com":
		return ProviderByID("outlook")
	case "fastmail.com", "fastmail.fm":
		return ProviderByID("fastmail")
	case "yahoo.com", "ymail.com":
		return ProviderByID("yahoo")
	}
	return Provider{}, false
}

// Validate checks what a network call needs, so the view can explain the
// problem before spending a connection.
func (a Account) Validate() error {
	if strings.TrimSpace(a.Email) == "" {
		return errors.New("the email address is empty")
	}
	if addr, err := mail.ParseAddress(a.Email); err != nil || addr.Address != a.Email {
		return fmt.Errorf("the email address %q is not valid", a.Email)
	}
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("the account name is empty")
	}
	if strings.TrimSpace(a.Username) == "" {
		return errors.New("the username is empty")
	}
	if a.OAuth != "" && a.OAuth != OAuthGoogle && a.OAuth != OAuthMicrosoft {
		return fmt.Errorf("signing in with %q is not supported", a.OAuth)
	}
	if err := a.IMAP.validate("IMAP"); err != nil {
		return err
	}
	return a.SMTP.validate("SMTP")
}

func (s Server) validate(kind string) error {
	if strings.TrimSpace(s.Host) == "" {
		return fmt.Errorf("the %s host is empty", kind)
	}
	if strings.ContainsAny(s.Host, " /\\") {
		return fmt.Errorf("the %s host %q is not valid", kind, s.Host)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("the %s port %d is not valid", kind, s.Port)
	}
	if s.TLS != TLSImplicit && s.TLS != TLSStartTLS {
		return fmt.Errorf("the %s TLS mode %q is not supported", kind, s.TLS)
	}
	return nil
}

// Label is the name the sidebar shows: the person's name, else the
// address.
func (a Account) Label() string {
	if strings.TrimSpace(a.Name) != "" {
		return a.Name
	}
	return a.Email
}
