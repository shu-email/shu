package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode"

	gomsgmail "github.com/emersion/go-message/mail"
)

// Identity is an address an account sends from. The first of an
// account's is its own address, the account's; the others are addresses
// its provider lets it send from too, aliases, which the user enters:
// neither IMAP nor SMTP has a way to list them.
type Identity struct {
	// ID is "" for the account's own address.
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountID"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email"`
}

// Address is the identity as a message's From.
func (i Identity) Address() Address { return Address{Name: i.Name, Email: i.Email} }

// Identity errors the UI explains.
var (
	// ErrIdentityAddress is an identity whose address is not one.
	ErrIdentityAddress = errors.New("mail: the address to send from is not valid")
	// ErrIdentityExists is an address the account sends from already, as
	// its own or as another identity.
	ErrIdentityExists = errors.New("mail: the account already sends from that address")
)

// ownIdentity is an account's own address as an identity.
func ownIdentity(a Account) Identity {
	return Identity{AccountID: a.ID, Name: a.Name, Email: a.Email}
}

// newIdentityID returns a random identity ID.
func newIdentityID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("identity%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Identities lists the addresses an account sends from: its own first,
// then those added, in the order they were.
func (s *Service) Identities(ctx context.Context, accountID string) ([]Identity, error) {
	a, ok, err := s.store.Account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	added, err := s.store.Identities(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return append([]Identity{ownIdentity(a)}, added...), nil
}

// AddIdentity adds an address the account sends from, after the others:
// one its provider lets it use. name is what recipients see beside it.
func (s *Service) AddIdentity(ctx context.Context, accountID, name, email string) (Identity, error) {
	id := Identity{ID: newIdentityID(), AccountID: accountID, Name: name, Email: email}
	if err := s.checkIdentity(ctx, &id); err != nil {
		return Identity{}, err
	}
	if err := s.store.SaveIdentity(ctx, id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

// UpdateIdentity changes the name and the address of an identity that
// was added. The account's own changes with the account.
func (s *Service) UpdateIdentity(ctx context.Context, id Identity) (Identity, error) {
	stored, err := s.addedIdentity(ctx, id.AccountID, id.ID)
	if err != nil {
		return Identity{}, err
	}
	stored.Name, stored.Email = id.Name, id.Email
	if err := s.checkIdentity(ctx, &stored); err != nil {
		return Identity{}, err
	}
	if err := s.store.SaveIdentity(ctx, stored); err != nil {
		return Identity{}, err
	}
	return stored, nil
}

// DeleteIdentity removes an identity that was added. The drafts written
// from it go from the account's own address.
func (s *Service) DeleteIdentity(ctx context.Context, accountID, id string) error {
	if _, err := s.addedIdentity(ctx, accountID, id); err != nil {
		return err
	}
	return s.store.DeleteIdentity(ctx, id)
}

// addedIdentity reads an identity added to an account.
func (s *Service) addedIdentity(ctx context.Context, accountID, id string) (Identity, error) {
	added, err := s.store.Identities(ctx, accountID)
	if err != nil {
		return Identity{}, err
	}
	for _, have := range added {
		if have.ID != "" && have.ID == id {
			return have, nil
		}
	}
	return Identity{}, ErrNotFound
}

// checkIdentity cleans an identity's name and checks its address: one
// address alone, which the account does not send from already. Both go
// into the From of the messages it sends.
func (s *Service) checkIdentity(ctx context.Context, id *Identity) error {
	a, ok, err := s.store.Account(ctx, id.AccountID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	id.Email = strings.TrimSpace(id.Email)
	if parsed, err := mail.ParseAddress(id.Email); err != nil || parsed.Address != id.Email {
		return ErrIdentityAddress
	}
	id.Name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(id.Name, "")))
	if strings.EqualFold(id.Email, a.Email) {
		return ErrIdentityExists
	}
	added, err := s.store.Identities(ctx, a.ID)
	if err != nil {
		return err
	}
	for _, have := range added {
		if have.ID != id.ID && strings.EqualFold(have.Email, id.Email) {
			return ErrIdentityExists
		}
	}
	return nil
}

// identity is the account's identity with an ID, or its own address when
// it has none by that ID: one that was deleted.
func (s *Service) identity(ctx context.Context, a Account, id string) (Identity, error) {
	if id == "" {
		return ownIdentity(a), nil
	}
	added, err := s.store.Identities(ctx, a.ID)
	if err != nil {
		return Identity{}, err
	}
	for _, have := range added {
		if have.ID == id {
			return have, nil
		}
	}
	return ownIdentity(a), nil
}

// ReplyIdentity picks the identity a reply to m is written from: the one
// m was addressed to, by its To, then its Cc, then the Delivered-To its
// servers added, which names an alias the message was forwarded from.
// identities is the account's, as Identities lists them; the first, its
// own address, is the answer when m was addressed to none of them.
func ReplyIdentity(identities []Identity, m Message) Identity {
	for _, list := range [][]Address{m.To, m.Cc, m.DeliveredTo()} {
		for _, addr := range list {
			if id, ok := identityFor(identities, addr); ok {
				return id
			}
		}
	}
	if len(identities) == 0 {
		return Identity{}
	}
	return identities[0]
}

// identityFor finds the identity that sends from an address. Addresses
// are compared without regard to case.
func identityFor(identities []Identity, addr Address) (Identity, bool) {
	email := strings.TrimSpace(addr.Email)
	for _, id := range identities {
		if email != "" && strings.EqualFold(email, id.Email) {
			return id, true
		}
	}
	return Identity{}, false
}

// deliveredToHeader is the header a server adds with the address it
// delivered a message to.
const deliveredToHeader = "Delivered-To"

// keptHeaders writes the header lines of a message that RawHeaders keeps,
// each on one line: the Delivered-To that says which address it reached,
// the authentication results that say whether its sender is who it
// claims (see Trust), and how its list is left (see Security).
func keptHeaders(h gomsgmail.Header) string {
	var b strings.Builder
	for _, name := range slices.Concat([]string{deliveredToHeader}, securityHeaders, unsubscribeHeaders) {
		for _, value := range h.Values(name) {
			if value = strings.Join(strings.Fields(value), " "); value != "" {
				if len(value) > maxHeaderLine {
					value = value[:maxHeaderLine]
				}
				b.WriteString(name + ": " + value + "\r\n")
			}
		}
	}
	return b.String()
}

// DeliveredTo is the addresses the message's Delivered-To headers name,
// the one its last server added first.
func (m Message) DeliveredTo() []Address {
	var out []Address
	for _, line := range strings.Split(m.RawHeaders, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(name), deliveredToHeader) {
			continue
		}
		if a, err := mail.ParseAddress(strings.TrimSpace(value)); err == nil {
			out = append(out, Address{Name: a.Name, Email: a.Address})
		}
	}
	return out
}
