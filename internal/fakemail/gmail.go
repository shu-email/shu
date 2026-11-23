package fakemail

import (
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// GmailProfile makes the server behave as Gmail's IMAP endpoint does: one
// [Gmail] parent that holds the system folders, named in the account's
// language and advertised with the attributes Gmail gives them (\All on
// All Mail, \Important on Important, and the rest); a cap on how many
// connections the account takes at once; and, when SMTPOptions.SaveSent
// names this server, a copy of every delivered message saved in Sent
// without being asked.
//
// nil leaves the server as a plain one.
type GmailProfile struct {
	// Language names the system folders as Gmail does for that UI
	// language. "" is English; "zh-Hans" is Gmail's Chinese.
	Language string
	// MaxConnections is how many connections the account takes at once.
	// Zero means Gmail's 15.
	MaxConnections int
	// Extensions serves the X-GM-EXT-1 test commands over implicit TLS.
	// The normal profile uses go-imap's server for general IMAP commands.
	Extensions bool
}

// gmailParent is Gmail's system-folder parent, which LIST returns
// noselect. Gmail keeps the name in English in every language.
const gmailParent = "[Gmail]"

// gmailNaming is Gmail's names for its system folders in one language.
type gmailNaming struct {
	all, important, sent, drafts, spam, starred, trash string
}

// gmailNamingFor returns the naming of a language, English for one it
// does not know.
func gmailNamingFor(language string) gmailNaming {
	switch strings.ToLower(strings.ReplaceAll(language, "_", "-")) {
	case "zh", "zh-hans", "zh-cn", "zh-sg":
		return gmailNaming{
			all: "所有邮件", important: "重要", sent: "已发邮件",
			drafts: "草稿", spam: "垃圾邮件", starred: "已加星标", trash: "回收站",
		}
	}
	return gmailNaming{
		all: "All Mail", important: "Important", sent: "Sent Mail",
		drafts: "Drafts", spam: "Spam", starred: "Starred", trash: "Trash",
	}
}

// gmailState is a server's Gmail behaviour, shared by its connections.
type gmailState struct {
	mu      sync.Mutex
	naming  gmailNaming
	folders []imap.ListData
	max     int
	open    int
}

// newGmailState builds the folders of a Gmail account: INBOX and the
// system folders Gmail always has, then the mailboxes the caller asked
// for that are not already there.
func newGmailState(p GmailProfile, mailboxes []string) *gmailState {
	max := p.MaxConnections
	if max <= 0 {
		max = 15
	}
	n := gmailNamingFor(p.Language)
	system := func(name string, attrs ...imap.MailboxAttr) imap.ListData {
		attrs = append([]imap.MailboxAttr{imap.MailboxAttrHasNoChildren}, attrs...)
		return imap.ListData{Mailbox: gmailParent + "/" + name, Delim: '/', Attrs: attrs}
	}
	s := &gmailState{
		naming: n,
		max:    max,
		folders: []imap.ListData{
			{Mailbox: gmailParent, Delim: '/', Attrs: []imap.MailboxAttr{
				imap.MailboxAttrNoSelect, imap.MailboxAttrHasChildren,
			}},
			{Mailbox: "INBOX", Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrHasNoChildren}},
			system(n.all, imap.MailboxAttrAll),
			system(n.important, imap.MailboxAttrImportant),
			system(n.sent, imap.MailboxAttrSent),
			system(n.drafts, imap.MailboxAttrDrafts),
			system(n.spam, imap.MailboxAttrJunk),
			system(n.starred, imap.MailboxAttrFlagged),
			system(n.trash, imap.MailboxAttrTrash),
		},
	}
	for _, name := range mailboxes {
		if name != "" && !s.has(name) {
			s.folders = append(s.folders, imap.ListData{
				Mailbox: name, Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
			})
		}
	}
	return s
}

// has reports whether a name is one of the folders, s.mu held or the
// state not yet shared.
func (s *gmailState) has(name string) bool {
	for _, f := range s.folders {
		if f.Mailbox == name {
			return true
		}
	}
	return false
}

// names returns the mailboxes to create, in order.
func (s *gmailState) names() []string {
	out := make([]string, 0, len(s.folders))
	for _, f := range s.folders {
		out = append(out, f.Mailbox)
	}
	return out
}

// sentMailbox is the folder Gmail saves a sent message in.
func (s *gmailState) sentMailbox() string { return gmailParent + "/" + s.naming.sent }

// opened counts a connection, and closed one that ended.
func (s *gmailState) opened() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open++
}

func (s *gmailState) closed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.open--
}

// tooMany reports whether a connection is one Gmail would refuse.
func (s *gmailState) tooMany() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open > s.max
}

// list returns the folders as LIST would report them, copying what a
// caller may change.
func (s *gmailState) list() []imap.ListData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]imap.ListData, len(s.folders))
	copy(out, s.folders)
	return out
}

// add records a folder the account created.
func (s *gmailState) add(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || s.has(name) {
		return
	}
	s.folders = append(s.folders, imap.ListData{
		Mailbox: name, Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrHasNoChildren},
	})
}

// remove forgets a folder the account deleted.
func (s *gmailState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.folders {
		if f.Mailbox == name {
			s.folders = append(s.folders[:i], s.folders[i+1:]...)
			return
		}
	}
}

// rename moves a folder's entry.
func (s *gmailState) rename(from, to string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.folders {
		if f.Mailbox == from {
			s.folders[i].Mailbox = to
			return
		}
	}
}

// Login refuses the connection Gmail answers NO to: one over what an
// account may open at once.
func (s PromptSession) Login(username, password string) error {
	if s.gmail != nil && s.gmail.tooMany() {
		return &imap.Error{
			Type: imap.StatusResponseTypeNo,
			Code: imap.ResponseCodeUnavailable,
			Text: "Too many simultaneous connections",
		}
	}
	return s.SessionIMAP4rev2.Login(username, password)
}

// Close ends the connection, and counts it out of what the account has
// open.
func (s PromptSession) Close() error {
	if s.once != nil {
		s.once.Do(s.gmail.closed)
	}
	return s.SessionIMAP4rev2.Close()
}

// List answers as Gmail lists: the system folders under a noselect
// [Gmail] parent, with the attributes Gmail gives them.
func (s PromptSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if s.gmail == nil {
		return s.SessionIMAP4rev2.List(w, ref, patterns, options)
	}
	// Subscriptions are not tracked: the server advertises no
	// LIST-EXTENDED, so no client asks for them.
	for _, data := range s.gmail.list() {
		if !matchesAny(data.Mailbox, ref, patterns) {
			continue
		}
		if options != nil && options.ReturnStatus != nil {
			status, err := s.SessionIMAP4rev2.Status(data.Mailbox, options.ReturnStatus)
			if err != nil {
				return err
			}
			data.Status = status
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}

// matchesAny reports whether a mailbox matches any of the patterns, as
// LIST matches them.
func matchesAny(name, ref string, patterns []string) bool {
	for _, pattern := range patterns {
		if imapserver.MatchList(name, '/', ref, pattern) {
			return true
		}
	}
	return false
}

// Create makes a folder, here as a label a Gmail account creates.
func (s PromptSession) Create(mailbox string, options *imap.CreateOptions) error {
	if err := s.SessionIMAP4rev2.Create(mailbox, options); err != nil {
		return err
	}
	if s.gmail != nil {
		s.gmail.add(strings.TrimRight(mailbox, "/"))
	}
	return nil
}

// Delete removes a folder.
func (s PromptSession) Delete(mailbox string) error {
	if err := s.SessionIMAP4rev2.Delete(mailbox); err != nil {
		return err
	}
	if s.gmail != nil {
		s.gmail.remove(mailbox)
	}
	return nil
}

// Rename moves a folder, and its entry in the list.
func (s PromptSession) Rename(mailbox, newName string, options *imap.RenameOptions) error {
	if err := s.SessionIMAP4rev2.Rename(mailbox, newName, options); err != nil {
		return err
	}
	if s.gmail != nil {
		s.gmail.rename(mailbox, strings.TrimRight(newName, "/"))
	}
	return nil
}

// AppendLabels adds one message to each of mailboxes, as Gmail shows one
// message under each label it has, and returns their UIDs in the same
// order.
//
// The in-memory server keeps a message per mailbox, so the bytes are
// copied where Gmail keeps one; what the engine sees, and what tests
// rely on, is one message with the same Message-ID under each mailbox,
// each with a UID of its own.
func (s *IMAP) AppendLabels(mailboxes []string, raw []byte, o *imap.AppendOptions) ([]imap.UID, error) {
	if o == nil {
		o = &imap.AppendOptions{}
	}
	out := make([]imap.UID, 0, len(mailboxes))
	for _, mailbox := range mailboxes {
		data, err := s.User.Append(mailbox, Literal(raw), o)
		if err != nil {
			return nil, err
		}
		out = append(out, data.UID)
	}
	if s.exts != nil {
		s.exts.add(mailboxes, out, raw, o.Flags)
	}
	return out, nil
}

// saveSent saves a delivered message in Sent, as Gmail does by itself.
func (s *IMAP) saveSent(raw []byte) error {
	mailbox := "Sent"
	if s.gmail != nil {
		mailbox = s.gmail.sentMailbox()
	}
	return s.Append(mailbox, raw, &imap.AppendOptions{
		Flags: []imap.Flag{imap.FlagSeen},
	})
}
