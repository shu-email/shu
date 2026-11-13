package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
)

// Blocked senders: an address or a whole domain whose mail the account
// files away as it arrives. Blocks are this device's, one account's each;
// they touch no mail already cached and no server. A block is a local
// choice, and the user takes it back here.

// ErrBlockInvalid says a block names no address, domain or folder.
var ErrBlockInvalid = errors.New("mail: the sender to block is not an address or a domain")

// BlockedSender is one of an account's blocks: whose mail it holds back,
// and where that mail goes.
type BlockedSender struct {
	AccountID string `json:"accountID"`
	// Value is the address, or the domain without its '@', in lower case.
	Value string `json:"value"`
	// Domain says Value is a whole domain, not one address.
	Domain bool `json:"domain"`
	// Folder is where the sender's mail goes as it arrives.
	Folder string    `json:"folder"`
	At     time.Time `json:"at"`
}

// BlockAddress normalizes an address to block: its bare address in lower
// case, whatever display name it came with.
func BlockAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if parsed, err := mail.ParseAddress(address); err == nil {
		address = parsed.Address
	}
	address = strings.ToLower(address)
	if local, _, ok := strings.Cut(address, "@"); !ok || local == "" || AddressDomain(address) == "" {
		return "", ErrBlockInvalid
	}
	return address, nil
}

// BlockDomain normalizes a domain to block: lower case, without a leading
// '@', and with at least one dot.
func BlockDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "@")
	if domain == "" || strings.ContainsAny(domain, " \t@<>") || !strings.Contains(domain, ".") {
		return "", ErrBlockInvalid
	}
	return domain, nil
}

// Blocked reports whether a sender is blocked, and the block that says
// so: an address matches its own, a domain block the sender's domain.
func Blocked(blocks []BlockedSender, from Address) (BlockedSender, bool) {
	address := strings.ToLower(strings.TrimSpace(from.Email))
	domain := AddressDomain(address)
	for _, b := range blocks {
		if b.Domain && domain != "" && b.Value == domain {
			return b, true
		}
		if !b.Domain && address != "" && b.Value == address {
			return b, true
		}
	}
	return BlockedSender{}, false
}

// BlockSender keeps a sender's mail out of the inbox: an address, or a
// whole domain, is filed to folder as it arrives from now on. Mail
// already cached stays where it is. Blocking the same sender again moves
// its mail to the new folder.
func (s *Service) BlockSender(ctx context.Context, accountID, address string, domain bool, folder string) (BlockedSender, error) {
	var value string
	var err error
	if domain {
		value, err = BlockDomain(address)
	} else {
		value, err = BlockAddress(address)
	}
	if err != nil {
		return BlockedSender{}, err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, folder); err != nil {
		return BlockedSender{}, err
	} else if !ok {
		return BlockedSender{}, fmt.Errorf("mail: folder %s is not cached: %w", folder, ErrNotFound)
	}
	b := BlockedSender{AccountID: accountID, Value: value, Domain: domain, Folder: folder, At: s.now()}
	if err := s.store.SaveBlockedSender(ctx, b); err != nil {
		return BlockedSender{}, err
	}
	return b, nil
}

// UnblockSender takes a block back, by the value BlockSender kept; the
// mail it held back stays where it went.
func (s *Service) UnblockSender(ctx context.Context, accountID, value string, domain bool) error {
	value = strings.TrimSpace(value)
	if domain {
		var err error
		if value, err = BlockDomain(value); err != nil {
			return err
		}
	} else {
		var err error
		if value, err = BlockAddress(value); err != nil {
			return err
		}
	}
	return s.store.RemoveBlockedSender(ctx, accountID, value, domain)
}

// BlockedSenders reads an account's blocks, newest first.
func (s *Service) BlockedSenders(ctx context.Context, accountID string) ([]BlockedSender, error) {
	return s.store.BlockedSenders(ctx, accountID)
}

// fileBlocked files the mail of blocked senders away as it arrives and
// returns the messages left for the rules: none of a blocked sender. The
// move is written to the queue before the server is touched, so it is made
// offline as well; the watcher announces none of the mail that was filed
// or is waiting (see heldBack). A move that failed leaves the message
// where it arrived, to be sorted and announced as any other, rather than
// held back in silence.
func (s *Service) fileBlocked(ctx context.Context, accountID string, folder Folder, msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}
	blocks, err := s.store.BlockedSenders(ctx, accountID)
	if err != nil || len(blocks) == 0 {
		return msgs
	}
	byDest := map[string][]MessageRef{}
	for _, m := range msgs {
		b, ok := Blocked(blocks, m.From)
		if !ok || b.Folder == folder.Name {
			continue
		}
		byDest[b.Folder] = append(byDest[b.Folder], MessageRef{Folder: folder.Name, UID: m.UID})
	}
	if len(byDest) == 0 {
		return msgs
	}
	// The move outlives the context of the check that found this mail: the
	// mail is filed even as a sync that began it ends.
	base := context.WithoutCancel(ctx)
	filed := map[UID]bool{}
	for dest, refs := range byDest {
		ctx, cancel := context.WithTimeout(base, ruleApplyTimeout)
		_, err := s.MoveMessages(ctx, accountID, refs, dest)
		cancel()
		if err != nil {
			continue
		}
		for _, ref := range refs {
			filed[ref.UID] = true
		}
	}
	if len(filed) == 0 {
		return msgs
	}
	note := accountID + "\x00" + folder.Name
	if s.heldBack[note] == nil {
		s.heldBack[note] = map[UID]bool{}
	}
	for uid := range filed {
		s.heldBack[note][uid] = true
	}
	return slices.DeleteFunc(msgs, func(m Message) bool { return filed[m.UID] })
}
