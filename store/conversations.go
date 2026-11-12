package store

import (
	"context"
	"strings"

	"shu/mail"
)

// ListConversations reads the conversations with a message in a scope (a
// folder, "starred", or "" for the whole account), the one whose newest
// such message is newest first. A row is made of the conversation's
// messages in the scope: the newest, how many there are, and whether any
// is unread, flagged or has attachments. A query keeps the conversations
// any message of which matches, in any folder, and names those messages.
func (s *Store) ListConversations(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Conversation, error) {
	if limit <= 0 {
		limit = 50
	}
	var b strings.Builder
	args := []any{accountID}
	b.WriteString(`WITH scoped AS (
			SELECT m.id, m.thread_id, m.date, m.seen, m.flagged, m.has_attachments
			FROM messages m JOIN folders f ON f.id = m.folder_id
			WHERE f.account_id = ? AND m.deleted = 0`)
	scope, scopeArgs := scopeClause(folder)
	b.WriteString(scope)
	args = append(args, scopeArgs...)
	match, matchArgs := matchClause(query)
	if match != "" {
		b.WriteString(` AND m.thread_id IN (
			SELECT m.thread_id FROM messages m JOIN folders f ON f.id = m.folder_id
			WHERE f.account_id = ? AND m.deleted = 0 AND ` + match + `)`)
		args = append(append(args, accountID), matchArgs...)
	}
	b.WriteString(`),
		ranked AS (
			SELECT id,
				ROW_NUMBER() OVER (PARTITION BY thread_id ORDER BY date DESC, id DESC) AS place,
				COUNT(*) OVER (PARTITION BY thread_id) AS n,
				MAX(seen = 0) OVER (PARTITION BY thread_id) AS unread,
				MAX(flagged) OVER (PARTITION BY thread_id) AS flagged,
				MAX(has_attachments) OVER (PARTITION BY thread_id) AS attached
			FROM scoped)
		SELECT ` + summaryColumns + `, r.n, r.unread, r.flagged, r.attached
		FROM ranked r JOIN messages m ON m.id = r.id JOIN folders f ON f.id = m.folder_id
		WHERE r.place = 1
		ORDER BY m.date DESC, m.id DESC LIMIT ?`)
	args = append(args, limit)
	rows, err := s.query(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Conversation, 0, len(rows.Values))
	index := map[mail.ThreadID]int{}
	for _, r := range rows.Values {
		latest := summaryRow(r[:summaryWidth])
		sums := r[summaryWidth:]
		index[latest.Thread] = len(out)
		out = append(out, mail.Conversation{
			Thread: latest.Thread, Latest: latest, Count: int(number(sums[0])),
			Unread: truth(sums[1]), Flagged: truth(sums[2]), HasAttachments: truth(sums[3]),
		})
	}
	if match == "" || len(out) == 0 {
		return out, nil
	}
	threads := make([]int64, 0, len(out))
	for _, c := range out {
		threads = append(threads, int64(c.Thread))
	}
	hits, err := s.query(ctx, `SELECT m.thread_id, f.name, m.server_uid
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND m.deleted = 0 AND `+match+`
			AND m.thread_id IN (SELECT value FROM json_each(?))
		ORDER BY m.date DESC, m.id DESC`, append(append([]any{accountID}, matchArgs...), jsonText(threads))...)
	if err != nil {
		return nil, err
	}
	for _, r := range hits.Values {
		i := index[mail.ThreadID(number(r[0]))]
		out[i].Matches = append(out[i].Matches, mail.MessageRef{Folder: text(r[1]), UID: mail.UID(number(r[2]))})
	}
	return out, nil
}

// ConversationMessages reads a conversation's messages in a scope, oldest
// first: "" for every folder, which is how the conversation reads, or the
// scope of a list, which is what its row stands for.
func (s *Store) ConversationMessages(ctx context.Context, accountID, folder string, thread mail.ThreadID) ([]mail.Summary, error) {
	scope, scopeArgs := scopeClause(folder)
	rows, err := s.query(ctx, `SELECT `+summaryColumns+`
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND m.thread_id = ? AND m.deleted = 0`+scope+`
		ORDER BY m.date, m.id`, append([]any{accountID, int64(thread)}, scopeArgs...)...)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Summary, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, summaryRow(r))
	}
	return out, nil
}
