package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// threadPlan puts the messages of one write in conversations. It starts
// from the conversations the cache maps their keys to and those of the
// rows the write replaces, and records what the write must change besides
// its rows: the conversations it begins, those it merges, the keys it is
// the first to see.
type threadPlan struct {
	account string
	// refs is each key's conversation, as the cache has it and as the
	// write makes it; stored is the cache's alone.
	refs   map[string]int64
	stored map[string]int64
	// merged maps a conversation that became part of another to it. A
	// merge always goes into the lowest ID, so the root of each set is
	// its lowest member.
	merged  map[int64]int64
	next    int64
	created []int64
}

func newThreadPlan(account string, stored map[string]int64, next int64) *threadPlan {
	refs := make(map[string]int64, len(stored))
	for key, id := range stored {
		refs[key] = id
	}
	return &threadPlan{account: account, refs: refs, stored: stored, merged: map[int64]int64{}, next: next}
}

// planThreads starts the plan of a write whose messages have these keys.
// The caller holds threadMu until the write is done, so that what it read
// is still so when it writes.
func (s *Store) planThreads(ctx context.Context, accountID string, keys [][]string) (*threadPlan, error) {
	var all []string
	for _, list := range keys {
		all = append(all, list...)
	}
	stored := map[string]int64{}
	if len(all) > 0 {
		rows, err := s.query(ctx, `SELECT key, thread_id FROM thread_refs
			WHERE account_id = ? AND key IN (SELECT value FROM json_each(?))`, accountID, jsonText(all))
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			stored[text(r[0])] = number(r[1])
		}
	}
	// The sequence of an AUTOINCREMENT table only grows, even when the
	// conversation that had the highest ID was merged into another.
	rows, err := s.query(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'threads'), 0)`)
	if err != nil {
		return nil, err
	}
	return newThreadPlan(accountID, stored, number(rows.Values[0][0])), nil
}

// find returns the conversation a conversation is now part of.
func (p *threadPlan) find(id int64) int64 {
	for {
		to, ok := p.merged[id]
		if !ok {
			return id
		}
		id = to
	}
}

// assign returns the conversation of a message with these keys, which was
// in conversation current before (0 for none). Without a conversation to
// join it begins one; with several it merges them into the lowest.
func (p *threadPlan) assign(keys []string, current int64) int64 {
	var found []int64
	if current != 0 {
		found = append(found, p.find(current))
	}
	for _, key := range keys {
		if id, ok := p.refs[key]; ok {
			found = append(found, p.find(id))
		}
	}
	var to int64
	if len(found) == 0 {
		p.next++
		to = p.next
		p.created = append(p.created, to)
	} else {
		to = slices.Min(found)
		for _, id := range found {
			if id != to {
				p.merged[id] = to
			}
		}
	}
	for _, key := range keys {
		p.refs[key] = to
	}
	return to
}

// before is what the write does ahead of its rows: the conversations it
// merged give their messages and keys to the one they joined, and the
// ones it began are made.
func (p *threadPlan) before() []stmtType {
	var stmts []stmtType
	gone := make([]int64, 0, len(p.merged))
	for id := range p.merged {
		if !slices.Contains(p.created, id) {
			gone = append(gone, id)
		}
	}
	slices.Sort(gone)
	for _, id := range gone {
		to := p.find(id)
		stmts = append(stmts,
			stmt(`UPDATE messages SET thread_id = ? WHERE thread_id = ?`, to, id),
			stmt(`UPDATE thread_refs SET thread_id = ? WHERE thread_id = ?`, to, id),
			stmt(`DELETE FROM threads WHERE id = ?`, id))
	}
	for _, id := range p.created {
		if p.find(id) == id {
			stmts = append(stmts, stmt(`INSERT INTO threads (id, account_id) VALUES (?, ?)`, id, p.account))
		}
	}
	return stmts
}

// after is what the write does once its rows are written: it records the
// keys the cache had not seen. Those it had are renumbered by before.
func (p *threadPlan) after() []stmtType {
	var rows [][]any
	for _, key := range sortedKeys(p.refs) {
		if _, ok := p.stored[key]; !ok {
			rows = append(rows, []any{p.account, key, p.find(p.refs[key])})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return []stmtType{insertRefs(rows)}
}

// insertRefs records keys, each an [account, key, conversation] row.
func insertRefs(rows [][]any) stmtType {
	// SQLite needs the WHERE to tell the upsert from a join.
	return stmt(`INSERT INTO thread_refs (account_id, key, thread_id)
		SELECT value ->> 0, value ->> 1, value ->> 2 FROM json_each(?) WHERE true
		ON CONFLICT (account_id, key) DO UPDATE SET thread_id = excluded.thread_id`, jsonText(rows))
}

// cachedThread is what a write reads of a row it replaces.
type cachedThread struct {
	thread      int64
	refs        string
	threadIndex string
}

// cachedThreads reads the conversations and threading headers of a
// folder's rows.
func (s *Store) cachedThreads(ctx context.Context, folderID int64, uids []mail.UID) (map[mail.UID]cachedThread, error) {
	out := map[mail.UID]cachedThread{}
	if len(uids) == 0 {
		return out, nil
	}
	rows, err := s.query(ctx, `SELECT server_uid, thread_id, COALESCE(references_json, ''), COALESCE(thread_index, '')
		FROM messages WHERE folder_id = ? AND server_uid IN (SELECT value FROM json_each(?))`,
		folderID, jsonText(uids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		out[mail.UID(number(r[0]))] = cachedThread{thread: number(r[1]), refs: text(r[2]), threadIndex: text(r[3])}
	}
	return out, nil
}

// backfillThreads puts the messages of a cache from before conversations
// in them, from what it holds of their headers: the Message-ID and
// In-Reply-To of each, and the References of those whose body was
// fetched. Reading the rows in the order they were written numbers the
// conversations the same way every time.
func backfillThreads(ctx context.Context, s *Store) ([]sqlite.Statement, error) {
	rows, err := s.query(ctx, `SELECT m.id, f.account_id, COALESCE(m.message_id, ''),
		COALESCE(m.in_reply_to, ''), COALESCE(m.references_json, '')
		FROM messages m JOIN folders f ON f.id = m.folder_id ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, nil
	}
	plans := map[string]*threadPlan{}
	var next int64
	type placed struct {
		id     int64
		plan   *threadPlan
		thread int64
	}
	all := make([]placed, 0, len(rows.Values))
	for _, r := range rows.Values {
		account := text(r[1])
		p := plans[account]
		if p == nil {
			p = newThreadPlan(account, nil, 0)
			plans[account] = p
		}
		p.next = next
		keys := mail.ThreadKeys(text(r[2]), text(r[3]), decodeStrings(text(r[4])), "")
		all = append(all, placed{number(r[0]), p, p.assign(keys, 0)})
		next = p.next
	}
	var threads, messages, refs [][]any
	for _, account := range sortedKeys(plans) {
		p := plans[account]
		for _, id := range p.created {
			if p.find(id) == id {
				threads = append(threads, []any{id, account})
			}
		}
		for _, key := range sortedKeys(p.refs) {
			refs = append(refs, []any{account, key, p.find(p.refs[key])})
		}
	}
	for _, m := range all {
		messages = append(messages, []any{m.id, m.plan.find(m.thread)})
	}
	stmts := []sqlite.Statement{
		stmt(`INSERT INTO threads (id, account_id) SELECT value ->> 0, value ->> 1 FROM json_each(?)`, jsonText(threads)),
		stmt(`UPDATE messages SET thread_id = j.value ->> 1 FROM json_each(?) AS j
			WHERE messages.id = j.value ->> 0`, jsonText(messages)),
	}
	if len(refs) > 0 {
		stmts = append(stmts, insertRefs(refs))
	}
	return stmts, nil
}

// jsonText encodes a statement's list argument, which json_each reads.
func jsonText(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
