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

// planBlocked takes the mail of blocked senders out of msgs, queues a move
// of each to the block's folder and hides it in the cache, all without
// touching the server: the account's worker makes the moves, woke by
// writeArrivals. It notes the messages for the watcher to announce none of
// them (see heldBack) and reports whether it queued any. Mail a refused
// move brings back is not sorted by the rules: they ran on the batch as it
// arrived. The caller holds arrivalMu.
func (s *Service) planBlocked(ctx context.Context, accountID string, folder Folder, msgs []Message) ([]Message, bool) {
	if len(msgs) == 0 {
		return msgs, false
	}
	blocks, err := s.store.BlockedSenders(ctx, accountID)
	if err != nil || len(blocks) == 0 {
		return msgs, false
	}
	held := map[UID]bool{}
	var ops []PendingOperation
	for _, m := range msgs {
		b, ok := Blocked(blocks, m.From)
		if !ok || b.Folder == folder.Name || held[m.UID] {
			continue
		}
		held[m.UID] = true
		ops = append(ops, PendingOperation{
			Kind: OpMove, Folder: folder.Name, UID: m.UID, Dest: b.Folder, Validity: folder.UIDValidity,
		})
	}
	if len(ops) == 0 {
		return msgs, false
	}
	// The writes outlive the check that found the mail: the batch is in the
	// cache already, and a check that ends must not leave it unfiled for a
	// later sync that will not count it as arrived again.
	bg := context.WithoutCancel(ctx)
	// The moves are this call's until their rows are hidden, so that the
	// account's worker cannot make one and put the row back meanwhile.
	ids, err := s.enqueueOperations(bg, accountID, ops)
	if err != nil {
		// A cache that cannot take the moves cannot hide the mail either:
		// it stays an ordinary arrival, in the list and announced, rather
		// than leave a row that no action would ever bring back.
		return msgs, false
	}
	groups := []uidGroup{{folder: folder.Name}}
	for _, op := range ops {
		groups[0].uids = append(groups[0].uids, op.UID)
	}
	// Only a watched account has a worker to make the moves: without one,
	// the mail stays in the list as an ordinary arrival, and the queue
	// keeps the moves for a worker to make when the account is watched.
	if !s.queueRunning(accountID) {
		_ = s.store.ReleaseOperations(bg, ids, s.now())
		return msgs, false
	}
	if err := s.hideQueued(bg, accountID, groups, ids); err != nil {
		return msgs, false
	}
	// The worker makes them once wakeQueue sets it on them.
	if err := s.store.ReleaseOperations(bg, ids, s.now()); err != nil {
		// The moves are not its to make: the mail comes back to the list,
		// as an arrival no block filed.
		s.showQueued(bg, accountID, groups)
		s.dropOperations(bg, ids)
		return msgs, false
	}
	note := accountID + "\x00" + folder.Name
	if s.heldBack[note] == nil {
		s.heldBack[note] = map[UID]bool{}
	}
	for uid := range held {
		s.heldBack[note][uid] = true
	}
	return slices.DeleteFunc(msgs, func(m Message) bool { return held[m.UID] }), true
}
