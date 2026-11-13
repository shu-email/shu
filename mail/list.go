package mail

import "errors"

// DefaultPageSize is how many rows a page has when the caller does not
// say.
const DefaultPageSize = 50

// Scope is the part of the cache a list or a search reads: a folder of an
// account, every folder of one or more accounts, in each account its
// folder of one special use, which for the inbox is the unified inbox, or
// a label's messages. A conversation is one account's, so every row of
// any scope is too.
type Scope struct {
	// Accounts are the accounts read, by ID; none is every account.
	Accounts []string `json:"accounts,omitempty"`
	// At most one of these narrows the folders of the accounts: Folder
	// to the folder of that name, Special to the folder of that special
	// use (SpecialInbox and the like), Starred to the flagged messages of
	// every folder, Label to the messages with that label, of its account,
	// in every folder Folder.InLabelScope keeps (LabelScope), Reminded to
	// the conversations an account has a reminder for, in every folder.
	// With none, every folder is read.
	Folder   string  `json:"folder,omitempty"`
	Special  string  `json:"special,omitempty"`
	Starred  bool    `json:"starred,omitempty"`
	Label    LabelID `json:"label,omitempty"`
	Reminded bool    `json:"reminded,omitempty"`
}

// AccountScope is the scope of the reads that take an account and a
// folder: a folder's name, "starred" for the flagged messages, or "" for
// every folder of the account.
func AccountScope(accountID, folder string) Scope {
	sc := Scope{Accounts: []string{accountID}}
	switch folder {
	case "":
	case "starred":
		sc.Starred = true
	default:
		sc.Folder = folder
	}
	return sc
}

// AllInboxes is the unified inbox: the inbox of every account.
func AllInboxes() Scope { return Scope{Special: SpecialInbox} }

// RemindersScope is an account's conversations that have a reminder, in
// every folder.
func RemindersScope(accountID string) Scope {
	return Scope{Accounts: []string{accountID}, Reminded: true}
}

// ErrBadScope is a scope that narrows its folders in more than one way.
var ErrBadScope = errors.New("mail: a scope narrows to a folder, a special use, the starred messages, a label or the reminders, not to several")

// Check reports a scope the reads refuse.
func (sc Scope) Check() error {
	narrowed := 0
	for _, set := range []bool{sc.Folder != "", sc.Special != "", sc.Starred, sc.Label != 0, sc.Reminded} {
		if set {
			narrowed++
		}
	}
	if narrowed > 1 {
		return ErrBadScope
	}
	return nil
}

// Cursor is where a page ended: the next page starts after it. It holds
// the date and the cache's number of the last row, so mail that arrives
// meanwhile, which is newer, neither repeats a row in the pages after it
// nor pushes one out. The zero Cursor starts at the newest. Callers pass
// it back as they got it.
type Cursor string

// ListQuery asks for a page: the rows of a scope a search finds, newest
// first, after a cursor.
type ListQuery struct {
	Scope  Scope
	Search Search
	After  Cursor
	// Limit is the most rows a page has; DefaultPageSize when not above
	// zero.
	Limit int
}

// MessagePage is a page of messages, newest first, each with its account
// and, for a search, the parts of it the terms were found in. Next is
// where the following page starts, and More reports whether the cache
// has rows after this page.
type MessagePage struct {
	Messages []Summary `json:"messages"`
	Next     Cursor    `json:"next,omitempty"`
	More     bool      `json:"more"`
}

// ConversationPage is a page of conversations, the one whose newest
// message in the scope is newest first. Next and More are as in a
// MessagePage.
type ConversationPage struct {
	Conversations []Conversation `json:"conversations"`
	Next          Cursor         `json:"next,omitempty"`
	More          bool           `json:"more"`
}

// FolderCache is how much of a folder is on this device.
type FolderCache struct {
	// Cached is how many of its messages the cache holds, those being
	// moved away left out. Total is how many the server has, as it last
	// said.
	Cached int `json:"cached"`
	Total  int `json:"total"`
	// Complete reports that the cache lacks none of the messages the
	// server had up to the last sync: there is no older mail on the
	// server to fetch.
	Complete bool `json:"complete"`
}

// AccountFolder is a folder with the account it is in.
type AccountFolder struct {
	AccountID string `json:"accountID"`
	Folder
}
