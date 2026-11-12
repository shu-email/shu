package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The address book is the people the user corresponds with: kept on this
// device from the mail the cache takes in and the mail the user sends,
// and edited by the user. It suggests recipients as they are typed. No
// part of it leaves the device: no request to a mail server or a model
// carries it.

// Contact is someone in the address book.
type Contact struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	// Favorite contacts are suggested before all others.
	Favorite bool `json:"favorite,omitempty"`
	// Edited reports that the name is the one the user gave: mail never
	// changes it.
	Edited bool `json:"edited,omitempty"`
	// Added reports that the user added the address by hand.
	Added bool `json:"added,omitempty"`
	// Sent counts the messages the user wrote to the address and Received
	// those it wrote to the user, each message once; LastSent and
	// LastReceived are the dates of the newest.
	Sent         int       `json:"sent,omitempty"`
	Received     int       `json:"received,omitempty"`
	LastSent     time.Time `json:"lastSent,omitzero"`
	LastReceived time.Time `json:"lastReceived,omitzero"`
}

// Address is the contact as a recipient. Its String is the text a
// recipient field takes for it, which reads back as this address.
func (c Contact) Address() Address { return Address{Name: c.Name, Email: c.Email} }

// ContactPage is a page of the address book.
type ContactPage struct {
	Contacts []Contact `json:"contacts"`
	// More reports that contacts follow: the next page starts at this
	// one's offset plus its length.
	More bool `json:"more"`
}

// ContactQuery is a read of the address book: the contacts each of Words
// begins a term of (see ContactTerms), or every contact when there are
// none, but those whose address Exclude holds, best first, from Offset,
// at most Limit. Deleted contacts and the user's own addresses are never
// in it.
type ContactQuery struct {
	Words   []string
	Exclude []string
	Offset  int
	Limit   int
}

// Errors of the address book the window explains.
var (
	// ErrContactAddress is an address to add that is not one.
	ErrContactAddress = errors.New("mail: the contact's address is not valid")
	// ErrContactExists is an address the address book has already.
	ErrContactExists = errors.New("mail: the address book has that address already")
	// ErrContactOwn is one of the user's own addresses, which is no
	// contact.
	ErrContactOwn = errors.New("mail: the address is one of the user's own")
)

// NameSource is where a contact's name came from. A name from a source
// above another's replaces it; of two from the same source, the newer.
type NameSource int

// The sources of a contact's name, the weakest first.
const (
	NameNone NameSource = iota
	// NameFromSender is the name an address gives itself on its mail.
	NameFromSender
	// NameWritten is the name the user wrote for it on mail to it.
	NameWritten
	// NameEdited is the name the user gave it in the address book, which
	// mail never changes.
	NameEdited
)

// ContactEvent is what one message says of one address: that the user
// wrote to it, or that it wrote to the user, under a name, at a date.
type ContactEvent struct {
	Email string
	Name  string
	Sent  bool
	At    time.Time
}

// ContactState is a contact as a Store keeps it, with what decides its
// name and its rank. Stores change it only through its methods, so that
// they all keep the same rules.
//
// A contact ranks by the messages that made it one: the favorites first,
// then those the user wrote to or added, by the messages the user wrote
// to them, then those that only wrote to the user, by their messages.
// Each message counts by its date (contactWeight): the score of the
// messages of a month ago is half that of today's, so that one who is
// written to often and lately comes first, and newsletters and one-off
// senders sink below anyone the user wrote to.
type ContactState struct {
	Email string
	Name  string
	// NameFrom is where the name came from, and NameAt the date of the
	// message it came on.
	NameFrom NameSource
	NameAt   time.Time
	Favorite bool
	Added    bool
	// Deleted is a contact the user deleted. Its address alone is kept,
	// so that the mail it sends does not bring it back; a message the
	// user writes to it does, as the user asked for the address. ForgotAt
	// is when it was deleted: mail dated before never counts for it again.
	Deleted  bool
	ForgotAt time.Time
	// The messages the user wrote to the address and those it wrote, how
	// many, the newest date, and their weights added up.
	Sent          int
	LastSent      time.Time
	SentScore     float64
	Received      int
	LastReceived  time.Time
	ReceivedScore float64
}

// Contact is the contact as the address book shows it.
func (c ContactState) Contact() Contact {
	return Contact{
		Email: c.Email, Name: c.Name, Favorite: c.Favorite, Edited: c.NameFrom == NameEdited, Added: c.Added,
		Sent: c.Sent, Received: c.Received, LastSent: c.LastSent, LastReceived: c.LastReceived,
	}
}

// Apply counts a message for the contact, and reports whether it did.
// Mail dated before the contact was deleted never counts, and a deleted
// contact takes only a message the user wrote to it, which brings it
// back. The name changes when the message's comes from a source above
// the current one, or from the same and is newer; an edited name stays.
func (c *ContactState) Apply(e ContactEvent) bool {
	at := e.At.UTC().Truncate(time.Second)
	if at.Before(c.ForgotAt) || (c.Deleted && !e.Sent) {
		return false
	}
	c.Deleted = false
	source := NameFromSender
	if e.Sent {
		source = NameWritten
		c.Sent++
		c.SentScore += contactWeight(at)
		if at.After(c.LastSent) {
			c.LastSent = at
		}
		// The address as the user wrote it.
		c.Email = e.Email
	} else {
		c.Received++
		c.ReceivedScore += contactWeight(at)
		if at.After(c.LastReceived) {
			c.LastReceived = at
		}
	}
	if c.Email == "" {
		c.Email = e.Email
	}
	if e.Name != "" && c.NameFrom != NameEdited &&
		(source > c.NameFrom || (source == c.NameFrom && !at.Before(c.NameAt))) {
		c.Name, c.NameFrom, c.NameAt = e.Name, source, at
	}
	return true
}

// Add makes the contact one the user added, under the user's name for
// it. A deleted contact comes back so, with nothing of what was forgotten.
func (c *ContactState) Add(name, email string) {
	c.Email, c.Added, c.Deleted = email, true, false
	c.Rename(name)
}

// Rename gives the contact the user's name for it, which mail does not
// change. An empty name leaves the naming to the mail again.
func (c *ContactState) Rename(name string) {
	c.Name, c.NameFrom, c.NameAt = name, NameEdited, time.Time{}
	if name == "" {
		c.NameFrom = NameNone
	}
}

// Forget deletes the contact at a time: all but its address goes, its
// name, its favorite and the counts of its mail, and mail dated before
// that time never counts for it again.
func (c *ContactState) Forget(at time.Time) {
	forgot := at.UTC().Truncate(time.Second)
	if forgot.Before(c.ForgotAt) {
		forgot = c.ForgotAt
	}
	*c = ContactState{Email: c.Email, Deleted: true, ForgotAt: forgot}
}

// The weight of a message halves every contactHalfLife: one of a month
// ago counts half as much as one of today, one of a year ago a
// four-thousandth. As every weight halves at the same pace, sums of
// weights keep their order as time passes, so a contact's score is the
// sum of its messages' weights, written once and never brought up to
// date. Weights are counted from contactEpoch, which keeps the sums of
// this century far within a float64.
const contactHalfLife = 30 * 24 * time.Hour

var contactEpoch = time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)

// contactWeight is the weight of a message of a date.
func contactWeight(at time.Time) float64 {
	return math.Exp2(min(float64(at.Sub(contactEpoch))/float64(contactHalfLife), 900))
}

// ContactKey is what the address book knows an address by: the address
// in lower case, as an address is one contact whatever its case.
func ContactKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// ContactMessageKey is what a message counts by in the address book, so
// that it counts once in whatever folders it is, however often a sync
// writes it, and when it was sent from here and then synced from Sent:
// its Message-ID, or, for one without, a digest of its sender, date and
// subject.
func ContactMessageKey(messageID, from string, date time.Time, subject string) string {
	if id := msgIDKey(messageID); id != "" {
		return id
	}
	sum := sha256.Sum256([]byte(strings.ToLower(from) + "\x00" + strconv.FormatInt(date.Unix(), 10) + "\x00" + subject))
	// No Message-ID key is without an @.
	return "digest:" + hex.EncodeToString(sum[:16])
}

// MessageContacts is what a message the cache takes in says of the people
// on it: when it is from one of the user's own addresses, that the user
// wrote to each of its recipients; otherwise that its sender wrote to the
// user, unless the sender takes no replies (NoReply). own reports whether
// an address is the user's. A date after now counts as now: a message
// can say any date, and one in the future would outrank every other.
func MessageContacts(m Message, own func(email string) bool, now time.Time) []ContactEvent {
	at := contactTime(m.Date, now)
	from, ok := contactEmail(m.From.Email)
	if ok && own(from) {
		return recipientEvents(at, own, m.To, m.Cc)
	}
	if !ok || NoReply(from) {
		return nil
	}
	return []ContactEvent{{Email: from, Name: mailName(m.From.Name, from), At: at}}
}

// SentContacts is what a message the user sent says: that the user wrote
// to each of its recipients, the blind copies too, under the names the
// user wrote, at a time.
func SentContacts(out Outgoing, at time.Time, own func(email string) bool) []ContactEvent {
	return recipientEvents(at.UTC().Truncate(time.Second), own, out.To, out.Cc, out.Bcc)
}

// recipientEvents says that the user wrote to each address of the lists
// that is not the user's own, once each.
func recipientEvents(at time.Time, own func(string) bool, lists ...[]Address) []ContactEvent {
	var out []ContactEvent
	seen := map[string]bool{}
	for _, list := range lists {
		for _, a := range list {
			email, ok := contactEmail(a.Email)
			if !ok || own(email) || seen[ContactKey(email)] {
				continue
			}
			seen[ContactKey(email)] = true
			out = append(out, ContactEvent{Email: email, Name: mailName(a.Name, email), Sent: true, At: at})
		}
	}
	return out
}

// contactTime is the date a message counts at: its own, in whole seconds,
// but no later than now, and the start of 1970 for one without.
func contactTime(date, now time.Time) time.Time {
	if date.IsZero() || date.Unix() < 0 {
		return time.Unix(0, 0).UTC()
	}
	if !now.IsZero() && date.After(now) {
		date = now
	}
	return date.UTC().Truncate(time.Second)
}

// maxEmailLen is the longest address SMTP carries (RFC 5321).
const maxEmailLen = 254

// contactEmail is an address fit for the address book: one address alone,
// as net/mail reads it, and so as a recipient field takes it.
func contactEmail(email string) (string, bool) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxEmailLen || !utf8.ValidString(email) {
		return "", false
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Name != "" || a.Address != email {
		return "", false
	}
	return email, true
}

// maxContactName is the most of a name the address book keeps.
const maxContactName = 200

// cleanName makes a name fit to show and to write in a recipient field:
// valid text, without control characters, its spaces collapsed, and not
// too long.
func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(name, ""))
	name = strings.Join(strings.Fields(name), " ")
	if runes := []rune(name); len(runes) > maxContactName {
		name = strings.TrimSpace(string(runes[:maxContactName]))
	}
	return name
}

// mailName is the name mail gives an address, when it is a name: one that
// is an address, its own or another, is none.
func mailName(name, email string) string {
	name = cleanName(name)
	bare := strings.Trim(name, `"' <>`)
	if bare == "" || strings.EqualFold(bare, email) {
		return ""
	}
	if a, err := mail.ParseAddress(bare); err == nil && a.Name == "" {
		return ""
	}
	return name
}

// NoReply reports whether an address takes no replies, as its name says:
// noreply@, no-reply@, donotreply@, do_not_reply@, mailer-daemon@, and
// any address of a noreply domain. The mail such an address sends makes
// it no contact, as the user cannot write to it; a message the user
// writes to it does.
func NoReply(email string) bool {
	email = strings.ToLower(email)
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	local = strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' {
			return -1
		}
		return r
	}, local)
	if strings.HasPrefix(local, "noreply") || strings.HasPrefix(local, "donotreply") || local == "mailerdaemon" {
		return true
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "noreply" || label == "no-reply" {
			return true
		}
	}
	return false
}

// maxContactTerms bounds what one contact is found by.
const maxContactTerms = 32

// ContactTerms are what a contact is found by, in lower case: its address,
// the address from each part of what comes before its @, and each word of
// its name. A word begins at a letter or digit after anything else, and
// at each character of the scripts that write names without spaces, so
// that 王小明 is found by 王, 小明 and 明, Jean-Luc by luc, and
// alice.smith@example.com by smith; the domain alone finds nothing, as
// it would find everyone there.
func ContactTerms(name, email string) []string {
	var terms []string
	add := func(term string) {
		if term != "" && len(terms) < maxContactTerms && !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	email = strings.ToLower(email)
	add(email)
	if at := strings.LastIndexByte(email, '@'); at > 0 {
		for _, start := range wordStarts(email[:at]) {
			add(email[start:])
		}
	}
	for _, word := range strings.Fields(strings.ToLower(name)) {
		for _, start := range wordStarts(word) {
			add(word[start:])
		}
	}
	return terms
}

// maxContactWords bounds the words of one query.
const maxContactWords = 8

// ContactWords are the words of text typed to find a contact, in lower
// case, without the quotes, brackets and commas of an address being
// typed: each must begin a term of the contact.
func ContactWords(text string) []string {
	var words []string
	fields := strings.FieldsFunc(strings.ToLower(strings.ToValidUTF8(text, "")), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"<>(),;`, r)
	})
	for _, field := range fields {
		field = strings.TrimLeftFunc(field, func(r rune) bool { return !wordRune(r) })
		if field != "" && len(words) < maxContactWords && !slices.Contains(words, field) {
			words = append(words, field)
		}
	}
	return words
}

// wordStarts are where words begin in text without spaces: at a letter or
// digit after anything else, and at each character of the scripts that
// write names without spaces between their parts, and after one.
func wordStarts(s string) []int {
	var starts []int
	prev := ' '
	for i, r := range s {
		if wordRune(r) && (!wordRune(prev) || unspaced(r) || unspaced(prev)) {
			starts = append(starts, i)
		}
		prev = r
	}
	return starts
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

// unspaced reports whether a character is of a script that writes a name
// without a space between its parts: Chinese, Japanese and Korean.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// DefaultSuggestions is how many contacts Suggest returns unless told.
const DefaultSuggestions = 8

// maxSuggestions bounds the contacts of one call.
const maxSuggestions = 50

// Suggest returns the contacts a recipient being typed may be, best first:
// those each word of typed begins a term of, in their name or address
// (see ContactTerms), at most limit, DefaultSuggestions when it is 0 or
// less. exclude holds the addresses the fields have already, which are
// not offered again. Deleted contacts and the user's own addresses are
// never offered. It reads the address book alone, on this device.
func (s *Service) Suggest(ctx context.Context, typed string, exclude []string, limit int) ([]Contact, error) {
	words := ContactWords(typed)
	if len(words) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultSuggestions
	}
	return s.store.Contacts(ctx, ContactQuery{Words: words, Exclude: exclude, Limit: min(limit, maxSuggestions)})
}

// Contacts reads a page of the address book, best first, from offset, at
// most limit, DefaultPageSize when it is 0 or less: every contact, or
// those a search finds, as Suggest finds them.
func (s *Service) Contacts(ctx context.Context, search string, offset, limit int) (ContactPage, error) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	list, err := s.store.Contacts(ctx, ContactQuery{Words: ContactWords(search), Offset: max(offset, 0), Limit: limit + 1})
	if err != nil {
		return ContactPage{}, err
	}
	page := ContactPage{Contacts: list, More: len(list) > limit}
	if page.More {
		page.Contacts = list[:limit]
	}
	if page.Contacts == nil {
		page.Contacts = []Contact{}
	}
	return page, nil
}

// AddContact adds an address to the address book, under a name which is
// the user's: mail does not change it. A deleted contact comes back so.
func (s *Service) AddContact(ctx context.Context, name, email string) (Contact, error) {
	email, ok := contactEmail(email)
	if !ok {
		return Contact{}, ErrContactAddress
	}
	return s.store.AddContact(ctx, cleanName(name), email)
}

// RenameContact gives a contact the user's name for it, which mail does
// not change. An empty name leaves the naming to the mail again.
func (s *Service) RenameContact(ctx context.Context, email, name string) (Contact, error) {
	return s.store.RenameContact(ctx, email, cleanName(name))
}

// FavoriteContact makes a contact a favorite, which is suggested first,
// or not.
func (s *Service) FavoriteContact(ctx context.Context, email string, on bool) (Contact, error) {
	return s.store.FavoriteContact(ctx, email, on)
}

// DeleteContact takes a contact out of the address book and the
// suggestions at once, and forgets what the book knew of it: its name,
// its favorite and the counts of its mail. Mail it sends later does not
// bring it back, and mail dated before never counts for it again; a
// message the user writes to it brings it back.
func (s *Service) DeleteContact(ctx context.Context, email string) error {
	return s.store.DeleteContact(ctx, email, s.now())
}
