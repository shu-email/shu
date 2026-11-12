package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// The address book is three tables (migration v11): contacts, a row of
// each address, by the rules of mail.ContactState; contact_terms, the
// terms each live contact is found by, which a suggestion reads by
// prefix; and contact_messages, the key of every message counted, so that
// none counts twice. A write of them reads the rows it changes first, so
// it holds contactMu over the reads and the transaction.

// contactColumns are what a read of a contact takes, of contacts c.
const contactColumns = `c.address, c.name, c.name_from, c.name_at, c.favorite, c.added, c.deleted, c.forgot_at,
	c.sent, c.sent_at, c.sent_score, c.received, c.received_at, c.received_score`

func contactRow(r []any) mail.ContactState {
	return mail.ContactState{
		Email: text(r[0]), Name: text(r[1]), NameFrom: mail.NameSource(number(r[2])), NameAt: unixUTC(r[3]),
		Favorite: truth(r[4]), Added: truth(r[5]), Deleted: truth(r[6]), ForgotAt: unixUTC(r[7]),
		Sent: int(number(r[8])), LastSent: unixUTC(r[9]), SentScore: realNumber(r[10]),
		Received: int(number(r[11])), LastReceived: unixUTC(r[12]), ReceivedScore: realNumber(r[13]),
	}
}

// contactValues are the columns of a contact, its key first, as
// upsertContacts takes them.
func contactValues(key string, c *mail.ContactState) []any {
	return []any{
		key, c.Email, c.Name, int(c.NameFrom), dateColumn(c.NameAt), boolInt(c.Favorite), boolInt(c.Added),
		boolInt(c.Deleted), dateColumn(c.ForgotAt), c.Sent, dateColumn(c.LastSent), c.SentScore,
		c.Received, dateColumn(c.LastReceived), c.ReceivedScore,
	}
}

// contactRank is the order of the address book (see mail.ContactState),
// the one of the index contacts_rank: the favorites first, then the
// contacts the user wrote to or added, by the score of what the user
// wrote them, then those that only wrote to the user, by theirs. What
// they wrote breaks a tie of the first, and the key what is left.
const contactRank = `c.favorite DESC, (c.sent > 0 OR c.added) DESC,
	(CASE WHEN c.sent > 0 OR c.added THEN c.sent_score ELSE c.received_score END) DESC,
	c.received_score DESC, c.key`

// Contacts reads the address book (see mail.ContactQuery).
func (s *Store) Contacts(ctx context.Context, q mail.ContactQuery) ([]mail.Contact, error) {
	own, err := s.ownKeys(ctx)
	if err != nil {
		return nil, err
	}
	query, args := contactQuery(q, own)
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Contact, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, contactRow(r).Contact())
	}
	return out, nil
}

// contactQuery is the statement of a read of the address book, which
// leaves out the user's own addresses. The first word, the longest and so
// the rarest, picks the contacts from the range of the term index it
// begins; each other word must begin a term of theirs too.
func contactQuery(q mail.ContactQuery, own map[string]bool) (string, []any) {
	excluded := make([]string, 0, len(own)+len(q.Exclude))
	for key := range own {
		excluded = append(excluded, key)
	}
	for _, email := range q.Exclude {
		excluded = append(excluded, mail.ContactKey(email))
	}
	words := slices.Clone(q.Words)
	slices.SortStableFunc(words, func(a, b string) int { return len(b) - len(a) })
	from, where := `contacts c`, []string{`c.deleted = 0`, `c.key NOT IN (SELECT value FROM json_each(?))`}
	var args, more []any
	if len(words) > 0 {
		bounds, bargs := termRange("term", words[0])
		from = `(SELECT DISTINCT key FROM contact_terms WHERE ` + bounds + `) t CROSS JOIN contacts c ON c.key = t.key`
		args = append(args, bargs...)
		for _, word := range words[1:] {
			bounds, bargs := termRange("x.term", word)
			where = append(where, `EXISTS (SELECT 1 FROM contact_terms x WHERE x.key = c.key AND `+bounds+`)`)
			more = append(more, bargs...)
		}
	}
	args = append(append(args, jsonText(excluded)), more...)
	limit := q.Limit
	if limit <= 0 {
		limit = -1
	}
	args = append(args, limit, max(q.Offset, 0))
	return `SELECT ` + contactColumns + ` FROM ` + from + `
		WHERE ` + strings.Join(where, ` AND `) + `
		ORDER BY ` + contactRank + ` LIMIT ? OFFSET ?`, args
}

// termRange is the condition that a column of terms begins with prefix,
// as a range of its index.
func termRange(column, prefix string) (string, []any) {
	if end, ok := prefixEnd(prefix); ok {
		return column + ` >= ? AND ` + column + ` < ?`, []any{prefix, end}
	}
	return column + ` >= ?`, []any{prefix}
}

// prefixEnd is the least text above all text that begins with prefix:
// prefix with its last character raised by one. Text compares as its
// UTF-8 bytes, which keep the order of the characters. ok is false when
// there is none.
func prefixEnd(prefix string) (string, bool) {
	for prefix != "" {
		r, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
		if r == unicode.MaxRune || (r == utf8.RuneError && size == 1) {
			continue
		}
		next := r + 1
		if next >= 0xD800 && next <= 0xDFFF {
			// Surrogates are no characters of UTF-8.
			next = 0xE000
		}
		return prefix + string(next), true
	}
	return "", false
}

// ownKeys are the user's own addresses as contact keys: those of the
// accounts and of their identities, which are no contacts.
func (s *Store) ownKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.query(ctx, `SELECT email FROM accounts UNION ALL SELECT email FROM identities`)
	if err != nil {
		return nil, err
	}
	own := make(map[string]bool, len(rows.Values))
	for _, r := range rows.Values {
		own[mail.ContactKey(text(r[0]))] = true
	}
	return own, nil
}

func ownFunc(own map[string]bool) func(string) bool {
	return func(email string) bool { return own[mail.ContactKey(email)] }
}

// countedMessage is what a message says in the address book, under the
// key it counts by.
type countedMessage struct {
	key    string
	events []mail.ContactEvent
}

// contactBook is what one write of the address book reads and changes:
// the contacts it touches, as they were and as it leaves them, and the
// keys of the messages counted, those it read and those it adds.
type contactBook struct {
	was      map[string]mail.ContactState
	contacts map[string]*mail.ContactState
	changed  map[string]bool
	counted  map[string]bool
	fresh    []string
}

func newContactBook() *contactBook {
	return &contactBook{
		was: map[string]mail.ContactState{}, contacts: map[string]*mail.ContactState{},
		changed: map[string]bool{}, counted: map[string]bool{},
	}
}

// contact is the contact of a key, a new one when the book has none.
func (b *contactBook) contact(key string) *mail.ContactState {
	c := b.contacts[key]
	if c == nil {
		c = &mail.ContactState{}
		b.contacts[key] = c
	}
	return c
}

// count counts a message that says something of someone, once ever.
func (b *contactBook) count(m countedMessage) {
	if len(m.events) == 0 || b.counted[m.key] {
		return
	}
	b.counted[m.key] = true
	b.fresh = append(b.fresh, m.key)
	for _, e := range m.events {
		key := mail.ContactKey(e.Email)
		if b.contact(key).Apply(e) {
			b.changed[key] = true
		}
	}
}

// contactChunk is how many rows one statement of a write of the address
// book carries at most.
const contactChunk = 2000

// statements are what the write changed: the contacts whole, the terms of
// those whose name, address or deletion changed, and the messages it
// counted.
func (b *contactBook) statements() []stmtType {
	var rows, terms [][]any
	var reindex []string
	for _, key := range sortedKeys(b.changed) {
		c := b.contacts[key]
		rows = append(rows, contactValues(key, c))
		if was, ok := b.was[key]; ok && was.Name == c.Name && was.Email == c.Email && was.Deleted == c.Deleted {
			continue
		}
		reindex = append(reindex, key)
		if !c.Deleted {
			for _, term := range mail.ContactTerms(c.Name, c.Email) {
				terms = append(terms, []any{term, key})
			}
		}
	}
	var stmts []stmtType
	for chunk := range slices.Chunk(rows, contactChunk) {
		stmts = append(stmts, stmt(`INSERT INTO contacts (key, address, name, name_from, name_at, favorite, added, deleted,
				forgot_at, sent, sent_at, sent_score, received, received_at, received_score)
			SELECT value ->> 0, value ->> 1, value ->> 2, value ->> 3, value ->> 4, value ->> 5, value ->> 6, value ->> 7,
				value ->> 8, value ->> 9, value ->> 10, value ->> 11, value ->> 12, value ->> 13, value ->> 14
			FROM json_each(?) WHERE true
			ON CONFLICT (key) DO UPDATE SET address = excluded.address, name = excluded.name,
				name_from = excluded.name_from, name_at = excluded.name_at, favorite = excluded.favorite,
				added = excluded.added, deleted = excluded.deleted, forgot_at = excluded.forgot_at,
				sent = excluded.sent, sent_at = excluded.sent_at, sent_score = excluded.sent_score,
				received = excluded.received, received_at = excluded.received_at, received_score = excluded.received_score`,
			jsonText(chunk)))
	}
	for chunk := range slices.Chunk(reindex, contactChunk) {
		stmts = append(stmts, stmt(`DELETE FROM contact_terms WHERE key IN (SELECT value FROM json_each(?))`, jsonText(chunk)))
	}
	for chunk := range slices.Chunk(terms, contactChunk) {
		stmts = append(stmts, stmt(`INSERT OR IGNORE INTO contact_terms (term, key)
			SELECT value ->> 0, value ->> 1 FROM json_each(?)`, jsonText(chunk)))
	}
	for chunk := range slices.Chunk(b.fresh, contactChunk) {
		stmts = append(stmts, stmt(`INSERT OR IGNORE INTO contact_messages (key) SELECT value FROM json_each(?)`, jsonText(chunk)))
	}
	return stmts
}

// readBook reads what a write of the address book changes: the contacts
// of keys, and which of the messages of counted were counted before.
func (s *Store) readBook(ctx context.Context, keys, counted []string) (*contactBook, error) {
	b := newContactBook()
	if len(counted) > 0 {
		rows, err := s.query(ctx, `SELECT key FROM contact_messages WHERE key IN (SELECT value FROM json_each(?))`, jsonText(counted))
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			b.counted[text(r[0])] = true
		}
	}
	if len(keys) > 0 {
		rows, err := s.query(ctx, `SELECT c.key, `+contactColumns+` FROM contacts c
			WHERE c.key IN (SELECT value FROM json_each(?))`, jsonText(keys))
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			key, c := text(r[0]), contactRow(r[1:])
			b.was[key] = c
			b.contacts[key] = &c
		}
	}
	return b, nil
}

// countMessages is the statements that count messages in the address
// book, those counted before left out. The caller holds contactMu until
// they are written.
func (s *Store) countMessages(ctx context.Context, msgs []countedMessage) ([]stmtType, error) {
	var keys, counted []string
	for _, m := range msgs {
		if len(m.events) == 0 {
			continue
		}
		counted = append(counted, m.key)
		for _, e := range m.events {
			keys = append(keys, mail.ContactKey(e.Email))
		}
	}
	if len(counted) == 0 {
		return nil, nil
	}
	b, err := s.readBook(ctx, keys, counted)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		b.count(m)
	}
	return b.statements(), nil
}

// countFolder is the statements that count messages written into a
// folder, as MessageContacts reads them: none of the Junk and Drafts
// folders, whose mail is no correspondence.
func (s *Store) countFolder(ctx context.Context, folderID int64, msgs []mail.Message) ([]stmtType, error) {
	if len(msgs) == 0 {
		return nil, nil
	}
	rows, err := s.query(ctx, `SELECT COALESCE(special_use, '') FROM folders WHERE id = ?`, folderID)
	if err != nil || len(rows.Values) == 0 {
		return nil, err
	}
	if special := text(rows.Values[0][0]); special == mail.SpecialJunk || special == mail.SpecialDrafts {
		return nil, nil
	}
	own, err := s.ownKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	counted := make([]countedMessage, 0, len(msgs))
	for _, m := range msgs {
		counted = append(counted, countedMessage{
			key:    mail.ContactMessageKey(m.MessageID, m.From.Email, m.Date, m.Subject),
			events: mail.MessageContacts(m, ownFunc(own), now),
		})
	}
	return s.countMessages(ctx, counted)
}

// countSent is the statements that count the recipients of an outbox
// entry being recorded as sent, To, Cc and Bcc, as the user wrote them,
// at the time it was due. One recorded before counts nothing again.
func (s *Store) countSent(ctx context.Context, id string) ([]stmtType, error) {
	rows, err := s.query(ctx, `SELECT state, envelope_json, created_at, due_at FROM outbox WHERE id = ?`, id)
	if err != nil || len(rows.Values) == 0 || text(rows.Values[0][0]) == mail.StateSent {
		return nil, err
	}
	r := rows.Values[0]
	var env mail.Outgoing
	if err := json.Unmarshal([]byte(text(r[1])), &env); err != nil {
		return nil, err
	}
	own, err := s.ownKeys(ctx)
	if err != nil {
		return nil, err
	}
	at := sentTime(r[2], r[3])
	return s.countMessages(ctx, []countedMessage{{
		key:    mail.ContactMessageKey(env.MessageID, env.From.Email, at, env.Subject),
		events: mail.SentContacts(env, at, ownFunc(own)),
	}})
}

// sentTime is when an outbox entry left: when it was due, for one sent
// with a delay, or else when it was written.
func sentTime(created, due any) time.Time {
	at := unix(created)
	if d := unix(due); d.After(at) {
		at = d
	}
	return at
}

// writeCounting writes stmts and, in the same transaction, what they
// change in the address book, which count works out. The address book is
// worth less than the mail and the outbox: when its part cannot be worked
// out, or fails, stmts are written alone.
func (s *Store) writeCounting(ctx context.Context, stmts []stmtType, count func() ([]stmtType, error)) error {
	s.contactMu.Lock()
	defer s.contactMu.Unlock()
	if more, err := count(); err == nil && len(more) > 0 {
		if s.transaction(ctx, append(slices.Clip(stmts), more...)) == nil {
			return nil
		}
	}
	return s.transaction(ctx, stmts)
}

// AddContact adds an address the user entered, under the user's name for
// it. A deleted contact comes back so.
func (s *Store) AddContact(ctx context.Context, name, email string) (mail.Contact, error) {
	key := mail.ContactKey(email)
	s.contactMu.Lock()
	defer s.contactMu.Unlock()
	own, err := s.ownKeys(ctx)
	if err != nil {
		return mail.Contact{}, err
	}
	if own[key] {
		return mail.Contact{}, mail.ErrContactOwn
	}
	b, err := s.readBook(ctx, []string{key}, nil)
	if err != nil {
		return mail.Contact{}, err
	}
	if was, ok := b.was[key]; ok && !was.Deleted {
		return mail.Contact{}, mail.ErrContactExists
	}
	c := b.contact(key)
	c.Add(name, email)
	b.changed[key] = true
	if err := s.transaction(ctx, b.statements()); err != nil {
		return mail.Contact{}, err
	}
	return c.Contact(), nil
}

// RenameContact gives a contact the user's name for it.
func (s *Store) RenameContact(ctx context.Context, email, name string) (mail.Contact, error) {
	return s.editContact(ctx, email, func(c *mail.ContactState) { c.Rename(name) })
}

// FavoriteContact makes a contact a favorite, or not.
func (s *Store) FavoriteContact(ctx context.Context, email string, on bool) (mail.Contact, error) {
	return s.editContact(ctx, email, func(c *mail.ContactState) { c.Favorite = on })
}

// DeleteContact deletes a contact at a time: its row keeps its address
// alone, and it leaves the term index.
func (s *Store) DeleteContact(ctx context.Context, email string, at time.Time) error {
	_, err := s.editContact(ctx, email, func(c *mail.ContactState) { c.Forget(at) })
	return err
}

// editContact changes a contact of the book, one that is there and not
// deleted.
func (s *Store) editContact(ctx context.Context, email string, edit func(*mail.ContactState)) (mail.Contact, error) {
	key := mail.ContactKey(email)
	s.contactMu.Lock()
	defer s.contactMu.Unlock()
	b, err := s.readBook(ctx, []string{key}, nil)
	if err != nil {
		return mail.Contact{}, err
	}
	if was, ok := b.was[key]; !ok || was.Deleted {
		return mail.Contact{}, mail.ErrNotFound
	}
	c := b.contact(key)
	edit(c)
	b.changed[key] = true
	if err := s.transaction(ctx, b.statements()); err != nil {
		return mail.Contact{}, err
	}
	return c.Contact(), nil
}

// backfillChunk is how many messages the backfill reads at once.
const backfillChunk = 5000

// backfillContacts makes the address book of a cache from before it, as
// the writes of what it holds would have: the recipients of the messages
// the outbox sent, and the people of every message cached outside the
// Junk and Drafts folders, each message once.
func backfillContacts(ctx context.Context, s *Store) ([]sqlite.Statement, error) {
	own, err := s.ownKeys(ctx)
	if err != nil {
		return nil, err
	}
	b := newContactBook()
	rows, err := s.query(ctx, `SELECT envelope_json, created_at, due_at FROM outbox WHERE state = ? ORDER BY created_at, rowid`,
		mail.StateSent)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		var env mail.Outgoing
		if json.Unmarshal([]byte(text(r[0])), &env) != nil {
			continue
		}
		at := sentTime(r[1], r[2])
		b.count(countedMessage{
			key:    mail.ContactMessageKey(env.MessageID, env.From.Email, at, env.Subject),
			events: mail.SentContacts(env, at, ownFunc(own)),
		})
	}
	now := s.now()
	for after := int64(0); ; {
		rows, err := s.query(ctx, `SELECT m.id, COALESCE(m.message_id, ''), COALESCE(m.from_name, ''), COALESCE(m.from_email, ''),
				COALESCE(m.to_json, ''), COALESCE(m.cc_json, ''), m.date, COALESCE(m.subject, '')
			FROM messages m JOIN folders f ON f.id = m.folder_id
			WHERE m.id > ? AND COALESCE(f.special_use, '') NOT IN (?, ?)
			ORDER BY m.id LIMIT ?`, after, mail.SpecialJunk, mail.SpecialDrafts, backfillChunk)
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			after = number(r[0])
			m := mail.Message{Summary: mail.Summary{
				MessageID: text(r[1]), From: mail.Address{Name: text(r[2]), Email: text(r[3])},
				To: decodeAddresses(text(r[4])), Cc: decodeAddresses(text(r[5])), Date: unix(r[6]), Subject: text(r[7]),
			}}
			b.count(countedMessage{
				key:    mail.ContactMessageKey(m.MessageID, m.From.Email, m.Date, m.Subject),
				events: mail.MessageContacts(m, ownFunc(own), now),
			})
		}
		if len(rows.Values) < backfillChunk {
			break
		}
	}
	return b.statements(), nil
}
