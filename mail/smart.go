package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"
)

// SmartMailbox is a search the user saved: a name, the list it was saved
// from, the typed search and the scope it covered. Opening it reads the
// cache exactly as the search did, page by page; the list it was saved
// from is a folder's name, "starred", the drafts', the reminders', the
// follow-ups', a label's entry or the unified inbox, or "" for the whole
// account.
type SmartMailbox struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	Name    string `json:"name"`
	Folder  string `json:"folder"`
	Query   string `json:"query"`
	// Scope is how much of the cache the search covered: the list's own
	// (SmartScopeHere), every folder of the account (SmartScopeAccount) or
	// every folder of every account (SmartScopeAll).
	Scope string `json:"scope"`
	// Unread is how many unread messages the search finds, which the
	// window fills from CountUnread for the sidebar's badge.
	Unread int `json:"unread,omitempty"`
}

// The scopes a smart mailbox's search covered, as SaveSmartMailbox stores
// them.
const (
	SmartScopeHere    = "here"
	SmartScopeAccount = "account"
	SmartScopeAll     = "all"
)

const (
	// MaxSmartName is how many characters a smart mailbox's name takes.
	MaxSmartName = 120
	// MaxSmartQuery is how many characters its search takes. A search is a
	// line typed, not a message body.
	MaxSmartQuery = 2000
)

var (
	// ErrSmartName is a name no smart mailbox can have: empty, or longer
	// than MaxSmartName.
	ErrSmartName = errors.New("mail: a smart mailbox's name is empty or too long")
	// ErrSmartQuery is a search longer than MaxSmartQuery.
	ErrSmartQuery = errors.New("mail: a smart mailbox's search is too long")
	// ErrSmartScope is a scope that is not here, account or all.
	ErrSmartScope = errors.New("mail: a smart mailbox's scope is not here, account or all")
	// ErrSmartExists is a name another smart mailbox of the account has.
	ErrSmartExists = errors.New("mail: a smart mailbox of that name exists")
)

// CleanSmartName trims a smart mailbox's name and checks it.
func CleanSmartName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxSmartName {
		return "", ErrSmartName
	}
	return name, nil
}

// SmartNameKey is a name as smart mailboxes compare: trimmed, in lower
// case, so that "Reports" and "reports" are one name.
func SmartNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// SmartMailboxes lists an account's smart mailboxes in their order,
// without their unread counts. It reads the cache alone.
func (s *Service) SmartMailboxes(ctx context.Context, accountID string) ([]SmartMailbox, error) {
	return s.store.SmartMailboxes(ctx, accountID)
}

// SaveSmartMailbox adds a smart mailbox after the account's others, or
// changes the name, the search and the scope of one it has. It touches no
// mail: the mailbox is a read of the cache.
func (s *Service) SaveSmartMailbox(ctx context.Context, m SmartMailbox) (SmartMailbox, error) {
	name, err := CleanSmartName(m.Name)
	if err != nil {
		return SmartMailbox{}, err
	}
	m.Name = name
	m.Query = strings.TrimSpace(m.Query)
	if utf8.RuneCountInString(m.Query) > MaxSmartQuery {
		return SmartMailbox{}, ErrSmartQuery
	}
	if m.Scope == "" {
		m.Scope = SmartScopeHere
	}
	switch m.Scope {
	case SmartScopeHere, SmartScopeAccount, SmartScopeAll:
	default:
		return SmartMailbox{}, ErrSmartScope
	}
	if _, ok, err := s.store.Account(ctx, m.Account); err != nil {
		return SmartMailbox{}, err
	} else if !ok {
		return SmartMailbox{}, ErrNotFound
	}
	return s.store.SaveSmartMailbox(ctx, m)
}

// DeleteSmartMailbox drops a smart mailbox. It deletes no mail.
func (s *Service) DeleteSmartMailbox(ctx context.Context, accountID string, id int64) error {
	return s.store.DeleteSmartMailbox(ctx, accountID, id)
}

// CountUnread counts the unread messages a list query reads, for the
// badge of a smart mailbox. A page's cursor and limit say nothing of it.
func (s *Service) CountUnread(ctx context.Context, q ListQuery) (int, error) {
	q.After, q.Limit = "", 0
	return s.store.CountUnread(ctx, q)
}
