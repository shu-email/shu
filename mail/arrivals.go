package mail

import (
	"bytes"
	"cmp"
	"context"
	"net/mail"
	"net/textproto"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// Mail that arrives. A sync of a folder synced before fetches the messages
// above its cursor: those arrived since, and are the mail the rules sort
// (rules.go) before the watcher may announce any of it. The first sync of
// a folder, FetchOlder, and a sync after the server gave the folder
// another UIDVALIDITY bring none: what they fetch was there before. Nor is
// a message this app moved into the folder one that arrived: an undo, the
// user's or a rule's, would have it sorted away again (expectArrivals). A
// message another mail client moves in looks like one that arrived, and
// is sorted as one.

// maxHeaderLine is the most bytes of a header's value RawHeaders keeps.
const maxHeaderLine = 998

// writeSync writes a sync's batch and, when sorts, sorts the mail that
// arrived with it. Sorting never fails the sync: the mail is in the cache
// by then, and the rules' actions are applied in the background, where a
// server out of reach leaves them pending until a later sync, which this
// one may be.
func (s *Service) writeSync(ctx context.Context, accountID string, batch SyncBatch, sorts bool) error {
	logged, err := s.writeArrivals(ctx, accountID, batch, sorts)
	if err != nil {
		return err
	}
	if logged || s.hasPending(ctx, accountID) {
		s.applyLater(accountID)
	}
	return nil
}

// writeArrivals writes a batch and sorts what arrived with it in one step,
// under arrivalMu, which the watcher takes the mail to announce under
// (takeNew): no message is announced before the rules said whether to,
// and none is sorted twice. A block only queues the moves it plans; the
// account's worker makes them, which needs no arrival lock, so a server
// out of reach delays neither this check nor what it announces. It
// reports whether it logged actions to apply.
func (s *Service) writeArrivals(ctx context.Context, accountID string, batch SyncBatch, sorts bool) (bool, error) {
	s.arrivalMu.Lock()
	defer s.arrivalMu.Unlock()
	apply, queued, err := s.writeArrivalsLocked(ctx, accountID, batch, sorts)
	if err != nil {
		return false, err
	}
	if queued {
		s.wakeQueue(accountID)
	}
	return apply, nil
}

// writeArrivalsLocked is writeArrivals under arrivalMu. It reports whether
// a block queued moves for the account's worker.
func (s *Service) writeArrivalsLocked(ctx context.Context, accountID string, batch SyncBatch, sorts bool) (bool, bool, error) {
	before, known, err := s.store.Folder(ctx, accountID, batch.Folder.Name)
	if err != nil {
		return false, false, err
	}
	// Only a sync writes SyncedAt: a folder without it was never synced,
	// and one synced empty has all it gets next arrive.
	same := known && !batch.Reset && !before.SyncedAt.IsZero() && before.UIDValidity == batch.Folder.UIDValidity
	if same && batch.Folder.LastUID < before.LastUID {
		// A sync that began before another wrote must not take the cursor
		// back: the mail between would arrive twice.
		batch.Folder.LastUID = before.LastUID
	}
	if err := s.store.ApplySync(ctx, accountID, batch); err != nil {
		return false, false, err
	}
	if !same {
		delete(s.movedIn, accountID+"\x00"+batch.Folder.Name)
		delete(s.heldBack, accountID+"\x00"+batch.Folder.Name)
	}
	if !sorts || !same {
		return false, false, nil
	}
	var arrived []UID
	for _, m := range batch.Messages {
		if m.UID > before.LastUID {
			arrived = append(arrived, m.UID)
		}
	}
	if len(arrived) == 0 {
		return false, false, nil
	}
	apply, queued := s.sortArrivals(ctx, accountID, before, arrived)
	return apply, queued, nil
}

// sortArrivals runs the rules over the messages that arrived in a folder
// and logs each match, its actions to be applied in the background. It
// reads the cache alone. It reports whether it logged actions to apply,
// and whether a block queued moves for the account's worker. The caller
// holds arrivalMu.
func (s *Service) sortArrivals(ctx context.Context, accountID string, folder Folder, uids []UID) (bool, bool) {
	msgs, err := s.store.MessageHeaders(ctx, accountID, folder.Name, uids)
	if err != nil || len(msgs) == 0 {
		return false, false
	}
	msgs = s.skipMovedIn(ctx, accountID, folder, msgs)
	// A block files its sender's mail before the rules see it: the rules
	// sort the mail that stays. It holds back only what arrives in the
	// inbox; mail in another folder is what the user or another mail client
	// put there, and moving it again would undo their choice. The moves
	// themselves are made by the account's worker.
	var queued bool
	if folder.IsInbox() {
		msgs, queued = s.planBlocked(ctx, accountID, folder, msgs)
	}
	rules, err := s.sortingRules(ctx)
	if err != nil {
		return false, queued
	}
	rules = slices.DeleteFunc(rules, func(r Rule) bool { return !r.watches(accountID, folder) })
	if len(rules) == 0 || len(msgs) == 0 {
		return false, queued
	}
	slices.SortFunc(msgs, func(a, b Message) int { return cmp.Compare(a.UID, b.UID) })
	var entries []RuleActivity
	at := s.now()
	for _, m := range msgs {
		for _, r := range sortMessage(rules, m, folder) {
			entries = append(entries, newActivity(r, m, folder.UIDValidity, at))
		}
	}
	if len(entries) == 0 {
		return false, queued
	}
	saved, err := s.store.AddRuleActivity(ctx, entries)
	return err == nil && slices.ContainsFunc(saved, func(e RuleActivity) bool { return e.State == ActivityPending }), queued
}

// skipMovedIn leaves out of the messages that arrived in a folder those
// this app moved there, and of the inbox notes them, for the watcher to
// announce none of them either.
func (s *Service) skipMovedIn(ctx context.Context, accountID string, folder Folder, msgs []Message) []Message {
	keys := make([]string, 0, len(msgs))
	for _, m := range msgs {
		keys = append(keys, LabelKey(m.Summary))
	}
	moved, err := s.store.TakeArrivals(ctx, accountID, folder.Name, keys)
	if err != nil || len(moved) == 0 {
		return msgs
	}
	note := accountID + "\x00" + folder.Name
	byKey := make(map[string]bool, len(moved))
	for _, key := range moved {
		byKey[key] = true
	}
	return slices.DeleteFunc(msgs, func(m Message) bool {
		if !byKey[LabelKey(m.Summary)] {
			return false
		}
		if folder.IsInbox() {
			if s.movedIn[note] == nil {
				s.movedIn[note] = map[UID]bool{}
			}
			s.movedIn[note][m.UID] = true
		}
		return true
	})
}

// takeNew returns the mail of a folder to announce: what the store hands
// out once (TakeNew), less the messages a rule keeps quiet and those this
// app moved there. It holds arrivalMu, so that what a sync wrote was
// sorted first.
func (s *Service) takeNew(ctx context.Context, accountID, folder string) ([]Summary, error) {
	s.arrivalMu.Lock()
	defer s.arrivalMu.Unlock()
	fresh, err := s.store.TakeNew(ctx, accountID, folder)
	if err != nil {
		return nil, err
	}
	note := accountID + "\x00" + folder
	moved, held := s.movedIn[note], s.heldBack[note]
	delete(s.movedIn, note)
	delete(s.heldBack, note)
	if len(fresh) == 0 {
		return fresh, nil
	}
	quiet := map[UID]bool{}
	if f, ok, err := s.store.Folder(ctx, accountID, folder); err == nil && ok {
		uids := make([]UID, 0, len(fresh))
		for _, m := range fresh {
			uids = append(uids, m.UID)
		}
		page, err := s.store.RuleActivity(ctx, ActivityQuery{Account: accountID, Folder: folder, UIDValidity: f.UIDValidity, UIDs: uids})
		if err == nil {
			for _, e := range page.Entries {
				quiet[e.UID] = quiet[e.UID] || e.Quiet
			}
		}
	}
	return slices.DeleteFunc(fresh, func(m Summary) bool {
		return quiet[m.UID] || moved[m.UID] || held[m.UID]
	}), nil
}

// arrivalsCount reports whether the mail that arrives in a folder is
// sorted or announced: the account's inbox, and a folder a rule watches.
func (s *Service) arrivalsCount(ctx context.Context, accountID, folder string) bool {
	f, ok, err := s.store.Folder(ctx, accountID, folder)
	if err != nil || !ok {
		return false
	}
	if f.IsInbox() {
		return true
	}
	rules, err := s.sortingRules(ctx)
	return err == nil && slices.ContainsFunc(rules, func(r Rule) bool { return r.watches(accountID, f) })
}

// expectArrivals notes the messages of the folder a connection selected
// that this app is about to move into dest, when what arrives there counts
// (arrivalsCount): there they are no mail that arrived. The note is made
// before the move, as the watcher may see the message arrive before the
// move's answer comes back, and by each message's key, which its envelope
// gives as a sync will. It returns what takes the note back, for a move
// that failed. A message the note misses is sorted as one that arrived.
func (s *Service) expectArrivals(ctx context.Context, c *imapSession, accountID string, uids []UID, dest string) (forget func()) {
	forget = func() {}
	if len(uids) == 0 || !s.arrivalsCount(ctx, accountID, dest) {
		return forget
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	fetched, err := c.client.Fetch(set, &imap.FetchOptions{UID: true, Envelope: true, InternalDate: true}).Collect()
	if err != nil {
		return forget
	}
	asked := make(map[UID]bool, len(uids))
	for _, uid := range uids {
		asked[uid] = true
	}
	var keys []string
	for _, buf := range fetched {
		if buf.Envelope != nil && asked[UID(buf.UID)] {
			keys = append(keys, LabelKey(summaryFromFetch(buf, false).Summary))
		}
	}
	if len(keys) == 0 || s.store.ExpectArrivals(ctx, accountID, dest, keys) != nil {
		return forget
	}
	return func() { _, _ = s.store.TakeArrivals(context.WithoutCancel(ctx), accountID, dest, keys) }
}

// ruleHeaders returns the names of the headers the enabled rules read that
// sort the mail of an account: of a folder of it, or of every folder when
// folder is nil.
func (s *Service) ruleHeaders(ctx context.Context, accountID string, folder *Folder) []string {
	rules, err := s.sortingRules(ctx)
	if err != nil {
		return nil
	}
	var names []string
	for _, r := range rules {
		if a := r.Account(); (a != "" && a != accountID) || (folder != nil && !r.watches(accountID, *folder)) {
			continue
		}
		for _, h := range r.headers() {
			if !slices.Contains(names, h) {
				names = append(names, h)
			}
		}
	}
	return names
}

// addRuleHeaders fetches the headers the rules that watch a folder read,
// of the messages a sync found arrived there, above the folder's cursor,
// and keeps them with each message (RawHeaders), so that the rules read
// them from the cache. A sync with no such rule asks nothing more of the
// server, and one whose fetch fails goes on without them: the mail
// arrives all the same. An answer the IMAP library took for an update
// comes without the headers (see fetchSummaries), and is asked for once
// more.
func (s *Service) addRuleHeaders(ctx context.Context, c *imapSession, accountID string, folder Folder, messages []Message) {
	names := s.ruleHeaders(ctx, accountID, &folder)
	if len(names) == 0 {
		return
	}
	want := map[UID]bool{}
	for _, m := range messages {
		if m.UID > folder.LastUID {
			want[m.UID] = true
		}
	}
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: names, Peek: true}
	got := map[UID][]byte{}
	for try := 0; try < 2 && len(got) < len(want); try++ {
		var set imap.UIDSet
		for uid := range want {
			if got[uid] == nil {
				set.AddNum(imap.UID(uid))
			}
		}
		cmd := c.client.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
		for {
			data := cmd.Next()
			if data == nil {
				break
			}
			buf, err := data.Collect()
			if err != nil {
				break
			}
			if raw := buf.FindBodySection(section); raw != nil && want[UID(buf.UID)] {
				got[UID(buf.UID)] = raw
			}
		}
		if cmd.Close() != nil {
			return
		}
	}
	for i := range messages {
		if raw, ok := got[messages[i].UID]; ok {
			messages[i].SetRawHeaders(addHeaderLines(messages[i].RawHeaders, raw, names))
		}
	}
}

// withRuleHeaders adds to the header lines a message keeps (RawHeaders) the
// headers the rules of its account read, from the message as fetched
// whole, so that opening a message keeps what its arrival fetched and
// gives it what rules made since read.
func (s *Service) withRuleHeaders(ctx context.Context, accountID, kept string, raw []byte) string {
	var names []string
	for _, name := range s.ruleHeaders(ctx, accountID, nil) {
		if len(headerValues(kept, name)) == 0 {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return kept
	}
	return kept + headerLines(raw, names)
}

// addHeaderLines adds to the header lines a message keeps those of a
// header block it does not have yet: a sync's message arrived with the
// security and threading headers, and the rules add theirs.
func addHeaderLines(kept string, raw []byte, names []string) string {
	var add []string
	for _, name := range names {
		if len(headerValues(kept, name)) == 0 {
			add = append(add, name)
		}
	}
	if len(add) == 0 {
		return kept
	}
	return kept + headerLines(raw, add)
}

// headerLines writes the values the named headers have in a block of
// header lines, each on a line of its own as RawHeaders keeps them:
// unfolded, in valid UTF-8, and at most maxHeaderLine bytes.
func headerLines(raw []byte, names []string) string {
	block := append(slices.Clone(raw), "\r\n\r\n"...)
	msg, err := mail.ReadMessage(bytes.NewReader(block))
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, name := range names {
		key := textproto.CanonicalMIMEHeaderKey(name)
		for _, value := range msg.Header[key] {
			value = strings.ToValidUTF8(strings.Map(func(r rune) rune {
				if r == '\r' || r == '\n' {
					return ' '
				}
				return r
			}, strings.TrimSpace(value)), "\uFFFD")
			if len(value) > maxHeaderLine {
				cut := maxHeaderLine
				for cut > 0 && !utf8.RuneStart(value[cut]) {
					cut--
				}
				value = value[:cut]
			}
			b.WriteString(key + ": " + value + "\r\n")
		}
	}
	return b.String()
}
