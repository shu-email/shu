package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// A label is a category of the user's, of one account; a message has any
// number. On the server it is an IMAP keyword, which a folder keeps when
// its PERMANENTFLAGS have \* or name it, so other mail clients see it and
// a keyword they set is a label here. Where a folder keeps no keyword the
// label is kept on this device only, by the message's LabelKey, so that
// it stays on the message through moves, resyncs and restarts. A label's
// name and color are this device's: a rename keeps its keyword, and the
// color never leaves.

// LabelID names a label in the cache. IDs are never given again.
type LabelID int64

// LabelColor is a color of the palette, by name; the window draws each
// in a shade that reads in the light appearance and the dark.
type LabelColor string

// The palette.
const (
	LabelRed    LabelColor = "red"
	LabelOrange LabelColor = "orange"
	LabelYellow LabelColor = "yellow"
	LabelGreen  LabelColor = "green"
	LabelTeal   LabelColor = "teal"
	LabelBlue   LabelColor = "blue"
	LabelPurple LabelColor = "purple"
	LabelPink   LabelColor = "pink"
)

// LabelColors is the palette in the order the window offers it.
var LabelColors = []LabelColor{LabelRed, LabelOrange, LabelYellow, LabelGreen, LabelTeal, LabelBlue, LabelPurple, LabelPink}

// Valid reports whether the color is one of the palette.
func (c LabelColor) Valid() bool { return slices.Contains(LabelColors, c) }

// LabelColorAt is the color a label takes when none is chosen for it:
// the palette in turn, by the label's place among its account's.
func LabelColorAt(position int) LabelColor {
	return LabelColors[((position%len(LabelColors))+len(LabelColors))%len(LabelColors)]
}

// Label is one of an account's labels.
type Label struct {
	ID      LabelID    `json:"id"`
	Account string     `json:"account"`
	Name    string     `json:"name"`
	Color   LabelColor `json:"color"`
	// Keyword is the label on the server, as other mail clients see it.
	// It is fixed when the label is made: a rename keeps it.
	Keyword string `json:"keyword"`
	// Local reports that the label is kept on this device only: of the
	// account's folders, those whose PERMANENTFLAGS are known keep none
	// of its keyword (see KeptLocally).
	Local bool `json:"local,omitempty"`
	// Unread is how many unread messages of its list (LabelScope) have
	// it.
	Unread int `json:"unread"`
}

// MessageLabel is a label a message has. Local is set when the message
// has it on this device only, not as a keyword on the server.
type MessageLabel struct {
	ID    LabelID `json:"id"`
	Local bool    `json:"local,omitempty"`
}

// HasLabel reports whether a message has a label.
func (m Summary) HasLabel(id LabelID) bool {
	return slices.ContainsFunc(m.Labels, func(l MessageLabel) bool { return l.ID == id })
}

var (
	// ErrLabelName is a name no label can have: empty, longer than
	// MaxLabelName, or with control characters.
	ErrLabelName = errors.New("mail: a label's name is empty, too long or has control characters")
	// ErrLabelExists is a name another label of the account has, in any
	// case.
	ErrLabelExists = errors.New("mail: the account has a label of that name")
	// ErrLabelColor is a color that is not one of the palette.
	ErrLabelColor = errors.New("mail: the color is not one of the palette")
	// ErrLabelPartlyRemoved is a deletion of a label that failed after
	// it took the label's keyword off some messages on the server: the
	// label stays, on the others, and deleting it again finishes.
	ErrLabelPartlyRemoved = errors.New("mail: the label came off some messages but not all")
)

// MaxLabelName is the most characters a label's name has.
const MaxLabelName = 64

// CleanLabelName returns a name as a label has it, without the spaces
// around it, or ErrLabelName.
func CleanLabelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxLabelName ||
		strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrLabelName
	}
	return name, nil
}

// LabelNameKey is what label names compare by: two names that differ in
// case alone are one. Each character is the least of its case folding.
func LabelNameKey(name string) string {
	var b strings.Builder
	for _, r := range name {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		b.WriteRune(least)
	}
	return b.String()
}

// LabelKey is what a label kept on this device knows a message by, the
// key it has in whatever folder it is: its Message-ID, or a digest of its
// sender, date and subject for one without (ContactMessageKey). Two
// copies of a message, as in Sent and in the inbox, share it.
func LabelKey(m Summary) string {
	return ContactMessageKey(m.MessageID, m.From.Email, m.Date, m.Subject)
}

// reservedKeywords are keywords of the system and of mail clients that
// are no labels, in lower case: the marks of forwarding, receipts,
// sending and junk, Apple Mail's flag colors and the annotations of
// IANA's registry. None is made a label, and no label's keyword is one.
var reservedKeywords = map[string]bool{
	"$forwarded": true, "$mdnsent": true, "$submitted": true, "$submitpending": true,
	"$junk": true, "$notjunk": true, "$nonjunk": true, "junk": true, "nonjunk": true, "notjunk": true,
	"junkrecorded": true, "$phishing": true, "$notphishing": true,
	"$mailflagbit0": true, "$mailflagbit1": true, "$mailflagbit2": true,
	"$autosent": true, "$canunsubscribe": true, "$unsubscribed": true, "$hasattachment": true,
	"$hasnoattachment": true, "$muted": true, "$followed": true, "$notify": true, "$memo": true,
	"$hasmemo": true, "$imported": true, "$istrusted": true,
}

// thunderbirdLabels are the keywords of Thunderbird's own tags, with the
// names and colors it gives them.
var thunderbirdLabels = map[string]struct {
	name  string
	color LabelColor
}{
	"$label1": {"Important", LabelRed},
	"$label2": {"Work", LabelOrange},
	"$label3": {"Personal", LabelGreen},
	"$label4": {"To Do", LabelBlue},
	"$label5": {"Later", LabelPurple},
}

// maxKeyword is the longest keyword taken from a server: the longest
// LabelKeyword writes, twelve characters for each of a name's.
const maxKeyword = 12 * MaxLabelName

// IsLabelKeyword reports whether a flag a server gave a message is a
// label: a keyword, an IMAP atom, that is none of the system's flags and
// none of reservedKeywords.
func IsLabelKeyword(flag string) bool {
	if flag == "" || len(flag) > maxKeyword || flag[0] == '\\' {
		return false
	}
	for i := 0; i < len(flag); i++ {
		if c := flag[i]; c <= ' ' || c >= 0x7f || strings.IndexByte(`(){%*"\]`, c) >= 0 {
			return false
		}
	}
	return !reservedKeywords[strings.ToLower(flag)]
}

// keywordSafe reports whether a byte stands for itself in a keyword
// LabelKeyword writes: a letter, a digit, or punctuation an IMAP atom
// takes. "_" and "=" are its escapes.
func keywordSafe(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("-.+:@!#&',;?^|~$", c) >= 0
}

// LabelKeyword is the keyword a label of a name is on the server as. A
// name of letters, digits and - . + : @ ! # & ' , ; ? ^ | ~ $ is its own
// keyword: "Work", "Q3-plans". Any other is written as the "Q" encoding of
// MIME headers (RFC 2047) writes text: a space as "_", and each other
// byte of its UTF-8 as "=" and two hex digits, so "To Do" is "To_Do" and
// "工作" is "=E5=B7=A5=E4=BD=9C". A name whose keyword would begin with "$",
// which marks the keywords of the system and of mail clients, or would be
// one of reservedKeywords, has its first character written so too:
// "$100" is "=24100" and "Junk" is "=4Aunk". Hex digits read in either
// case, as IMAP compares keywords, so a server that changes their case
// changes no name but the case of its ASCII letters; LabelName reads it
// back.
func LabelKeyword(name string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	escape := func(c byte) {
		b.WriteByte('=')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == ' ':
			b.WriteByte('_')
		case keywordSafe(c) && (i > 0 || c != '$'):
			b.WriteByte(c)
		default:
			escape(c)
		}
	}
	keyword := b.String()
	if reservedKeywords[strings.ToLower(keyword)] {
		b.Reset()
		escape(keyword[0])
		b.WriteString(keyword[1:])
		keyword = b.String()
	}
	return keyword
}

// LabelName is the name of the label a keyword found on the server
// becomes: the name Thunderbird gives one of its own, else the name
// LabelKeyword would have written as the keyword, else the keyword
// itself, cut to MaxLabelName.
func LabelName(keyword string) string {
	if tb, ok := thunderbirdLabels[strings.ToLower(keyword)]; ok {
		return tb.name
	}
	if name, ok := decodeKeyword(keyword); ok {
		return name
	}
	if utf8.RuneCountInString(keyword) > MaxLabelName {
		keyword = string([]rune(keyword)[:MaxLabelName])
	}
	return keyword
}

// KeywordColor is the color of the label a keyword found on the server
// becomes, when the keyword says one: Thunderbird's tags have theirs.
// It is "" for any other.
func KeywordColor(keyword string) LabelColor {
	return thunderbirdLabels[strings.ToLower(keyword)].color
}

// decodeKeyword reads a keyword as LabelKeyword writes one, and reports
// false for one it could not have written.
func decodeKeyword(keyword string) (string, bool) {
	unhex := func(c byte) (byte, bool) {
		switch {
		case '0' <= c && c <= '9':
			return c - '0', true
		case 'a' <= c && c <= 'f':
			return c - 'a' + 10, true
		case 'A' <= c && c <= 'F':
			return c - 'A' + 10, true
		}
		return 0, false
	}
	b := make([]byte, 0, len(keyword))
	for i := 0; i < len(keyword); i++ {
		switch c := keyword[i]; c {
		case '_':
			b = append(b, ' ')
		case '=':
			if i+2 >= len(keyword) {
				return "", false
			}
			hi, ok1 := unhex(keyword[i+1])
			lo, ok2 := unhex(keyword[i+2])
			if !ok1 || !ok2 {
				return "", false
			}
			b = append(b, hi<<4|lo)
			i += 2
		default:
			b = append(b, c)
		}
	}
	name := string(b)
	if clean, err := CleanLabelName(name); err != nil || clean != name {
		return "", false
	}
	return name, true
}

// FreeLabelName returns name, or, when taken reports a label of the
// account has it, name with the first of " 2", " 3"... that none has:
// the name of a label made from a keyword whose name another label has.
// taken is given LabelNameKey of each name tried.
func FreeLabelName(name string, taken func(key string) bool) string {
	if !taken(LabelNameKey(name)) {
		return name
	}
	base := []rune(name)
	if len(base) > MaxLabelName-4 {
		base = base[:MaxLabelName-4]
	}
	for n := 2; ; n++ {
		if next := strings.TrimSpace(string(base)) + " " + strconv.Itoa(n); !taken(LabelNameKey(next)) {
			return next
		}
	}
}

// FreeKeyword returns keyword, or, when taken reports a label of the
// account has it, keyword with the first of -2, -3... that none has: a
// label renamed since it was made keeps its keyword, which may be that
// of a new label's name. taken is given each keyword tried in lower
// case, as keywords compare.
func FreeKeyword(keyword string, taken func(lower string) bool) string {
	if !taken(strings.ToLower(keyword)) {
		return keyword
	}
	for n := 2; ; n++ {
		if next := keyword + "-" + strconv.Itoa(n); !taken(strings.ToLower(next)) {
			return next
		}
	}
}

// KeywordsKnown reports whether the folder's PERMANENTFLAGS are known: it
// was opened to change since labels came.
func (f Folder) KeywordsKnown() bool { return f.PermanentFlags != nil }

// KeepsKeyword reports whether the folder keeps a keyword on the server,
// as its PERMANENTFLAGS said: any keyword, or that one in any case.
func (f Folder) KeepsKeyword(keyword string) bool {
	return slices.ContainsFunc(f.PermanentFlags, func(p string) bool {
		return p == string(imap.FlagWildcard) || strings.EqualFold(p, keyword)
	})
}

// KeepsNewKeywords reports whether the folder keeps any keyword, those
// it does not list too: a label put on its messages is on the server.
func (f Folder) KeepsNewKeywords() bool {
	return slices.Contains(f.PermanentFlags, string(imap.FlagWildcard))
}

// InLabelScope reports whether a label's list shows the folder's
// messages: those of every folder but Junk, Trash and Drafts, whose mail
// is dealt with or not sent, as the counts at the foot of the sidebar
// leave them out.
func (f Folder) InLabelScope() bool {
	return f.SpecialUse != SpecialJunk && f.SpecialUse != SpecialTrash && f.SpecialUse != SpecialDrafts
}

// KeptLocally reports whether a label of a keyword is kept on this device
// only: of an account's folders, some were opened to change, and none of
// those keeps the keyword. Folders not opened so say nothing.
func KeptLocally(folders []Folder, keyword string) bool {
	known := false
	for _, f := range folders {
		if !f.KeywordsKnown() {
			continue
		}
		known = true
		if f.KeepsKeyword(keyword) {
			return false
		}
	}
	return known
}

// LabelScope is the list of a label: its messages in every folder of its
// account but Junk, Trash and Drafts (Folder.InLabelScope).
func LabelScope(accountID string, id LabelID) Scope {
	return Scope{Accounts: []string{accountID}, Label: id}
}

// Labels lists an account's labels in their order, each with how many
// unread messages of its list have it and whether it is kept on this
// device only. It reads the cache alone.
func (s *Service) Labels(ctx context.Context, accountID string) ([]Label, error) {
	return s.store.Labels(ctx, accountID)
}

// CreateLabel adds a label to an account, after those it has. Its keyword
// is LabelKeyword of its name. A color of "" is the palette's next.
// ErrLabelName, ErrLabelColor and ErrLabelExists say what is wrong with
// what was asked. Nothing reaches the server until the label is put on a
// message.
func (s *Service) CreateLabel(ctx context.Context, accountID, name string, color LabelColor) (Label, error) {
	name, err := CleanLabelName(name)
	if err != nil {
		return Label{}, err
	}
	if color != "" && !color.Valid() {
		return Label{}, ErrLabelColor
	}
	if _, ok, err := s.store.Account(ctx, accountID); err != nil {
		return Label{}, err
	} else if !ok {
		return Label{}, ErrNotFound
	}
	return s.store.CreateLabel(ctx, Label{Account: accountID, Name: name, Color: color, Keyword: LabelKeyword(name)})
}

// UpdateLabel renames a label and changes its color, "" keeping the one
// it has. Both are this device's: the label's keyword stays what it was,
// which is what other mail clients see, so that no message on the server
// changes. ErrLabelName, ErrLabelColor and ErrLabelExists as CreateLabel.
func (s *Service) UpdateLabel(ctx context.Context, accountID string, id LabelID, name string, color LabelColor) (Label, error) {
	name, err := CleanLabelName(name)
	if err != nil {
		return Label{}, err
	}
	if color != "" && !color.Valid() {
		return Label{}, ErrLabelColor
	}
	return s.store.UpdateLabel(ctx, Label{ID: id, Account: accountID, Name: name, Color: color})
}

// label returns one of an account's labels, ErrNotFound for one it has
// not.
func (s *Service) label(ctx context.Context, accountID string, id LabelID) (Label, error) {
	labels, err := s.store.Labels(ctx, accountID)
	if err != nil {
		return Label{}, err
	}
	for _, l := range labels {
		if l.ID == id {
			return l, nil
		}
	}
	return Label{}, ErrNotFound
}

// SetLabel puts a label on messages of its account, or takes it off: a
// list's selection, or a conversation. In a folder that keeps the label's
// keyword the cache shows the change first, and one connection then
// changes the keyword on the folder's messages with one silent UID STORE
// (+FLAGS or -FLAGS); what the server refuses comes back off the cache.
// A folder that keeps no such keyword, as its PERMANENTFLAGS say when it
// is opened, keeps the label on this device instead, by LabelKey, and no
// server hears of it. Taking a label off takes it off both. Only the
// keyword changes: nothing is moved, deleted or expunged. A folder whose
// UIDVALIDITY is no longer the cache's is not touched, as its UIDs would
// name other messages; when a folder fails, those before it keep their
// change.
func (s *Service) SetLabel(ctx context.Context, accountID string, msgs []MessageRef, id LabelID, on bool) error {
	return s.setLabel(ctx, accountID, msgs, id, on, true)
}

// setLabel is SetLabel with the queue or without it. The rules and their
// undo keep their own log of what the server has not made yet, so they
// ask for every change to be made now or to fail.
func (s *Service) setLabel(ctx context.Context, accountID string, msgs []MessageRef, id LabelID, on bool, queue bool) error {
	groups := byFolder(msgs, "")
	if len(groups) == 0 {
		return nil
	}
	label, err := s.label(ctx, accountID, id)
	if err != nil {
		return err
	}
	var c *imapSession
	defer func() {
		if c != nil {
			c.close()
		}
	}()
	// The keyword change of a folder is written before the server is
	// touched. When the server cannot be reached, the groups not yet
	// tried wait in the queue too, so that the cache and the queue agree
	// with what the user asked.
	var waiting []int64
	deferred := false
	for _, g := range groups {
		folder, ok, err := s.store.Folder(ctx, accountID, g.folder)
		if err != nil {
			s.dropOperations(ctx, waiting)
			return err
		}
		if !ok {
			s.dropOperations(ctx, waiting)
			return fmt.Errorf("mail: folder %s is not cached: %w", g.folder, ErrNotFound)
		}
		server := !folder.KeywordsKnown() || folder.KeepsKeyword(label.Keyword)
		if on && !server {
			if _, err := s.store.SetLabel(ctx, accountID, id, g.folder, g.uids, true, true); err != nil {
				s.dropOperations(ctx, waiting)
				return err
			}
			continue
		}
		synced, err := s.store.SetLabel(ctx, accountID, id, g.folder, g.uids, on, false)
		if err != nil {
			s.dropOperations(ctx, waiting)
			return err
		}
		var local []UID
		// The rows come back even when the change failed because ctx ended.
		undo := func() {
			bg := context.WithoutCancel(ctx)
			_, _ = s.store.SetLabel(bg, accountID, id, g.folder, synced, !on, false)
			_, _ = s.store.SetLabel(bg, accountID, id, g.folder, local, true, true)
		}
		if !on {
			if local, err = s.store.SetLabel(ctx, accountID, id, g.folder, g.uids, false, true); err != nil {
				undo()
				s.dropOperations(ctx, waiting)
				return err
			}
			// A label this device alone kept needs no server.
			if !server && len(synced) == 0 {
				continue
			}
		}
		var ids []int64
		if queue && server && len(synced) > 0 {
			ops := make([]PendingOperation, 0, len(synced))
			for _, uid := range synced {
				ops = append(ops, PendingOperation{
					Kind: OpLabel, Folder: g.folder, UID: uid,
					Label: id, On: on, Validity: folder.UIDValidity,
				})
			}
			if ids, err = s.enqueueOperations(ctx, accountID, ops); err != nil {
				undo()
				s.dropOperations(ctx, waiting)
				return err
			}
		}
		if deferred {
			waiting = append(waiting, ids...)
			continue
		}
		if c == nil {
			if c, err = s.labelSession(ctx, accountID); err != nil {
				if queue && retryable(err) {
					// The folder keeps the change in the cache, and its
					// keyword waits for the server.
					waiting = append(waiting, ids...)
					deferred = true
					continue
				}
				undo()
				s.dropOperations(ctx, append(waiting, ids...))
				return err
			}
		}
		opened, err := s.openToChange(ctx, c, accountID, folder)
		if err != nil {
			if queue && retryable(err) {
				waiting = append(waiting, ids...)
				deferred = true
				continue
			}
			undo()
			s.dropOperations(ctx, append(waiting, ids...))
			return err
		}
		if on && !opened.KeepsKeyword(label.Keyword) {
			undo()
			s.dropOperations(ctx, ids)
			if _, err := s.store.SetLabel(ctx, accountID, id, g.folder, g.uids, true, true); err != nil {
				s.dropOperations(ctx, waiting)
				return err
			}
			continue
		}
		if err := storeKeyword(c, g.folder, g.uids, label.Keyword, on); err != nil {
			if queue && retryable(err) {
				waiting = append(waiting, ids...)
				deferred = true
				continue
			}
			undo()
			s.dropOperations(ctx, append(waiting, ids...))
			return err
		}
		s.dropOperations(ctx, ids)
	}
	s.requeueOperations(ctx, accountID, waiting)
	return nil
}

// labelSession connects to an account's server for a change of labels.
func (s *Service) labelSession(ctx context.Context, accountID string) (*imapSession, error) {
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.dialIMAP(ctx, a, password)
}

// openToChange selects a cached folder read-write, after checking that its
// UIDVALIDITY is still the cache's, and records the flags it keeps, as
// its PERMANENTFLAGS say. It returns the folder with them.
func (s *Service) openToChange(ctx context.Context, c *imapSession, accountID string, folder Folder) (Folder, error) {
	selected, err := c.client.Select(folder.Name, nil).Wait()
	if err != nil {
		return folder, fmt.Errorf("imap: select %s: %w", folder.Name, err)
	}
	if folder.UIDValidity != 0 && selected.UIDValidity != folder.UIDValidity {
		return folder, fmt.Errorf("%w (%s)", errValidityChanged, folder.Name)
	}
	folder.PermanentFlags = permanentFlags(selected)
	return folder, s.store.SetPermanentFlags(ctx, accountID, folder.Name, folder.PermanentFlags)
}

// permanentFlags is what a SELECT said of the flags the folder keeps.
// The IMAP library reads a PERMANENTFLAGS that is missing as one that is
// empty: either is taken to keep no keyword, as a folder only opened
// read-only does.
func permanentFlags(selected *imap.SelectData) []string {
	out := make([]string, 0, len(selected.PermanentFlags))
	for _, f := range selected.PermanentFlags {
		out = append(out, string(f))
	}
	return out
}

// storeKeyword adds a keyword to messages of the selected folder, or
// takes it off, with one silent STORE.
func storeKeyword(c *imapSession, folder string, uids []UID, keyword string, on bool) error {
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	op := imap.StoreFlagsAdd
	if !on {
		op = imap.StoreFlagsDel
	}
	if err := c.client.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{imap.Flag(keyword)}}, nil).Close(); err != nil {
		return fmt.Errorf("imap: store %s: %w", folder, err)
	}
	return nil
}

// DeleteLabel deletes a label, which the window has the user confirm
// first. Its keyword comes off every message that has it on the server:
// each folder the server lists is searched for it (UID SEARCH KEYWORD)
// and those messages lose it in one silent STORE, and the cached messages
// of the folder lose the label. Then the label goes, and with it the
// label kept on this device. No message is moved or deleted. A failure
// leaves the label as it is on the messages not reached yet; once some
// lost it on the server the error is ErrLabelPartlyRemoved, and deleting
// the label again finishes. The server is not asked when none of the
// account's folders can have the keyword: each is known to keep none.
func (s *Service) DeleteLabel(ctx context.Context, accountID string, id LabelID) error {
	label, err := s.label(ctx, accountID, id)
	if err != nil {
		return err
	}
	folders, err := s.store.Folders(ctx, accountID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(folders, func(f Folder) bool { return !f.KeywordsKnown() || f.KeepsKeyword(label.Keyword) }) {
		if err := s.removeKeyword(ctx, accountID, label); err != nil {
			return err
		}
	}
	return s.store.DeleteLabel(ctx, accountID, id, s.now())
}

// removeKeyword takes a label's keyword off every message of the server
// that has it, and the label off the cached messages of each folder it
// did.
func (s *Service) removeKeyword(ctx context.Context, accountID string, label Label) error {
	c, err := s.labelSession(ctx, accountID)
	if err != nil {
		return err
	}
	defer c.close()
	list, err := s.listFolders(c)
	if err != nil {
		return err
	}
	refs, err := s.store.LabelRefs(ctx, accountID, label.ID)
	if err != nil {
		return err
	}
	removed := false
	fail := func(err error) error {
		if removed {
			return fmt.Errorf("%w: %w", ErrLabelPartlyRemoved, err)
		}
		return err
	}
	for _, f := range list {
		selected, err := c.client.Select(f.Name, nil).Wait()
		if err != nil {
			return fail(fmt.Errorf("imap: select %s: %w", f.Name, err))
		}
		if err := s.store.SetPermanentFlags(ctx, accountID, f.Name, permanentFlags(selected)); err != nil && !errors.Is(err, ErrNotFound) {
			return fail(err)
		}
		found, err := c.client.UIDSearch(&imap.SearchCriteria{Flag: []imap.Flag{imap.Flag(label.Keyword)}}, nil).Wait()
		if err != nil {
			return fail(fmt.Errorf("imap: search %s: %w", f.Name, err))
		}
		if uids := found.AllUIDs(); len(uids) > 0 {
			var set []UID
			for _, uid := range uids {
				set = append(set, UID(uid))
			}
			if err := storeKeyword(c, f.Name, set, label.Keyword, false); err != nil {
				return fail(err)
			}
			removed = true
		}
		// The server's folder has the keyword nowhere now: neither have the
		// cached messages of it.
		var cached []UID
		for _, r := range refs {
			if r.Folder == f.Name {
				cached = append(cached, r.UID)
			}
		}
		if _, err := s.store.SetLabel(ctx, accountID, label.ID, f.Name, cached, false, false); err != nil && !errors.Is(err, ErrNotFound) {
			return fail(err)
		}
	}
	return nil
}
