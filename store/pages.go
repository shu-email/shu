package store

import (
	"context"
	"slices"

	"shu/mail"
)

// MessagePage reads a page of the messages of a scope a search finds,
// newest first, after a cursor. A message of a search says where its
// terms were found.
func (s *Store) MessagePage(ctx context.Context, q mail.ListQuery) (mail.MessagePage, error) {
	return s.messagePage(ctx, q, searchCriteria(q.Search))
}

func (s *Store) messagePage(ctx context.Context, q mail.ListQuery, criteria []criterion) (mail.MessagePage, error) {
	// A page with nothing after it hands its cursor on, so that asking
	// again does not start over.
	page := mail.MessagePage{Messages: []mail.Summary{}, Next: q.After}
	p, err := s.plan(ctx, q, criteria)
	if err != nil {
		return page, err
	}
	query, args, ok := p.messageSQL()
	if !ok {
		return page, nil
	}
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return page, err
	}
	found := rows.Values
	if len(found) > p.limit {
		found, page.More = found[:p.limit], true
	}
	if page.Messages, err = s.summaries(ctx, found); err != nil {
		return page, err
	}
	ids := make([]int64, 0, len(found))
	for _, r := range found {
		id := number(r[summaryID])
		ids = append(ids, id)
		page.Next = cursor{date: number(r[summaryDate]), id: id, set: true}.value()
	}
	if len(p.labels) > 0 {
		for i := range page.Messages {
			page.Messages[i].Matched |= mail.FieldLabel
		}
	}
	if len(criteria) == 0 || len(ids) == 0 {
		return page, nil
	}
	// The row lacks the body and the recipients as the index has them.
	rows, err = s.query(ctx, `SELECT id, COALESCE(recipients, ''), COALESCE(body_text, '')
		FROM messages WHERE id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	if err != nil {
		return page, err
	}
	more := make(map[int64][2]string, len(rows.Values))
	for _, r := range rows.Values {
		more[number(r[0])] = [2]string{text(r[1]), text(r[2])}
	}
	for i := range page.Messages {
		m := &page.Messages[i]
		m.Matched |= foundIn(messageText{
			subject: m.Subject, fromName: m.From.Name, fromEmail: m.From.Email,
			recipients: more[ids[i]][0], body: more[ids[i]][1], preview: m.Preview,
		}, criteria)
	}
	return page, nil
}

// messageSQL is the query of a page of messages, one row more than the
// page to tell whether there are more; ok is false when the scope has no
// folder the search can find a message in.
func (p *listPlan) messageSQL() (query string, args []any, ok bool) {
	folders := p.folders
	if p.in != nil {
		folders = slices.DeleteFunc(slices.Clone(folders), func(id int64) bool { return !slices.Contains(p.in, id) })
	}
	if len(folders) == 0 {
		return "", nil, false
	}
	where, args := p.inScope("m", folders, true)
	if match, more := p.matchSQL("m", false); match != "" {
		where += ` AND ` + match
		args = append(args, more...)
	}
	if p.after.set {
		where += ` AND (m.date, m.id) < (?, ?)`
		args = append(args, p.after.date, p.after.id)
	}
	return `SELECT ` + summaryColumns + `
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE ` + where + ` AND m.deleted = 0
		ORDER BY m.date DESC, m.id DESC LIMIT ?`, append(args, p.limit+1), true
}

// ConversationPage reads a page of the conversations with a message in a
// scope, the one whose newest such message is newest first, after a
// cursor. A row is made of the conversation's messages in the scope: the
// newest, how many there are, and whether any is unread, flagged or has
// attachments. A search keeps the conversations any message of which, in
// any folder, matches it whole, and names those messages with where its
// terms were found.
func (s *Store) ConversationPage(ctx context.Context, q mail.ListQuery) (mail.ConversationPage, error) {
	return s.conversationPage(ctx, q, searchCriteria(q.Search))
}

func (s *Store) conversationPage(ctx context.Context, q mail.ListQuery, criteria []criterion) (mail.ConversationPage, error) {
	page := mail.ConversationPage{Conversations: []mail.Conversation{}, Next: q.After}
	p, err := s.plan(ctx, q, criteria)
	if err != nil {
		return page, err
	}
	if len(p.folders) == 0 {
		return page, nil
	}
	query, args := p.conversationSQL()
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return page, err
	}
	found := rows.Values
	if len(found) > p.limit {
		found, page.More = found[:p.limit], true
	}
	latest, err := s.summaries(ctx, found)
	if err != nil {
		return page, err
	}
	index := map[mail.ThreadID]int{}
	threads := make([]int64, 0, len(found))
	for i, r := range found {
		index[latest[i].Thread] = len(page.Conversations)
		threads = append(threads, int64(latest[i].Thread))
		page.Conversations = append(page.Conversations, mail.Conversation{Thread: latest[i].Thread, Account: latest[i].Account, Latest: latest[i]})
		page.Next = cursor{date: number(r[summaryDate]), id: number(r[summaryID]), set: true}.value()
	}
	if len(threads) == 0 {
		return page, nil
	}
	counts, err := s.conversationCounts(ctx, p, threads)
	if err != nil {
		return page, err
	}
	for i := range page.Conversations {
		c := &page.Conversations[i]
		n := counts[int64(c.Thread)]
		c.Count, c.Unread, c.Flagged, c.HasAttachments = n.count, n.unread, n.flagged, n.attached
	}
	// The labels of the scope's messages, each once, in the account's
	// order.
	scope, scopeArgs := p.inScope("m", p.folders, false)
	labels, err := s.query(ctx, `SELECT DISTINCT thread_id, label_id, position FROM (
			SELECT m.thread_id, l.id AS label_id, l.position FROM messages m
				JOIN message_labels ml ON ml.message_id = m.id JOIN labels l ON l.id = ml.label_id
			WHERE m.thread_id IN (SELECT value FROM json_each(?)) AND `+scope+` AND m.deleted = 0
			UNION ALL
			SELECT m.thread_id, l.id, l.position FROM messages m JOIN folders f ON f.id = m.folder_id
				JOIN local_labels ll ON ll.msg_key = m.msg_key
				JOIN labels l ON l.id = ll.label_id AND l.account_id = f.account_id
			WHERE m.thread_id IN (SELECT value FROM json_each(?)) AND `+scope+` AND m.deleted = 0)
		ORDER BY thread_id, position, label_id`,
		append(append(append([]any{jsonText(threads)}, scopeArgs...), jsonText(threads)), scopeArgs...)...)
	if err != nil {
		return page, err
	}
	for _, r := range labels.Values {
		c := &page.Conversations[index[mail.ThreadID(number(r[0]))]]
		c.Labels = append(c.Labels, mail.LabelID(number(r[1])))
	}
	match, matchArgs := p.matchSQL("m", false)
	if match == "" {
		return page, nil
	}
	// The messages the search found, in any folder, newest first. Their
	// text is read only when the search has terms to find in it.
	texts := `'', '', '', '', '', ''`
	if len(criteria) > 0 {
		texts = `COALESCE(m.subject, ''), COALESCE(m.from_name, ''), COALESCE(m.from_email, ''),
			COALESCE(m.recipients, ''), COALESCE(m.body_text, ''), COALESCE(m.preview, '')`
	}
	hits, err := s.query(ctx, `SELECT m.thread_id, f.name, m.server_uid, `+texts+`
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE m.thread_id IN (SELECT value FROM json_each(?)) AND m.deleted = 0 AND `+match+`
		ORDER BY m.date DESC, m.id DESC`, append([]any{jsonText(threads)}, matchArgs...)...)
	if err != nil {
		return page, err
	}
	for _, r := range hits.Values {
		c := &page.Conversations[index[mail.ThreadID(number(r[0]))]]
		ref := mail.MessageRef{Folder: text(r[1]), UID: mail.UID(number(r[2]))}
		fields := foundIn(messageText{
			subject: text(r[3]), fromName: text(r[4]), fromEmail: text(r[5]),
			recipients: text(r[6]), body: text(r[7]), preview: text(r[8]),
		}, criteria)
		if len(p.labels) > 0 {
			fields |= mail.FieldLabel
		}
		c.Matches = append(c.Matches, ref)
		c.Found = append(c.Found, mail.Match{MessageRef: ref, Fields: fields})
		if ref.Folder == c.Latest.Folder && ref.UID == c.Latest.UID {
			c.Latest.Matched = fields
		}
	}
	return page, nil
}

// conversationScopeCounts is what a row of a list of conversations says
// of the messages in the list's scope.
type conversationScopeCounts struct {
	count    int
	unread   bool
	flagged  bool
	attached bool
}

// conversationCounts reads the counts and flags of a page's conversations
// from the aggregates the store keeps: a conversation's own for a scope of
// every folder of its account, one row per folder for a scope narrowed to
// folders. A scope narrowed to a flag or a label counts the messages
// themselves, as no aggregate keeps a conversation's flagged or labelled
// messages alone.
func (s *Store) conversationCounts(ctx context.Context, p *listPlan, threads []int64) (map[int64]conversationScopeCounts, error) {
	counts := map[int64]conversationScopeCounts{}
	read := func(query string, args ...any) error {
		rows, err := s.query(ctx, query, args...)
		if err != nil {
			return err
		}
		for _, r := range rows.Values {
			counts[number(r[0])] = conversationScopeCounts{
				count: int(number(r[1])), unread: truth(r[2]), flagged: truth(r[3]), attached: truth(r[4]),
			}
		}
		return nil
	}
	switch {
	case p.whole:
		return counts, read(`SELECT id, message_count, unread_count > 0, starred_count > 0, attachment_count > 0
			FROM threads WHERE id IN (SELECT value FROM json_each(?))`, jsonText(threads))
	case !p.starred && p.label == 0:
		return counts, read(`SELECT thread_id, SUM(message_count), MAX(unread_count > 0),
				MAX(starred_count > 0), MAX(attachment_count > 0)
			FROM thread_folders
			WHERE thread_id IN (SELECT value FROM json_each(?))
				AND folder_id IN (SELECT value FROM json_each(?))
			GROUP BY thread_id`, jsonText(threads), jsonText(p.folders))
	}
	scope, scopeArgs := p.inScope("m", p.folders, false)
	return counts, read(`SELECT m.thread_id, COUNT(*), MAX(m.seen = 0), MAX(m.flagged), MAX(m.has_attachments)
		FROM messages m
		WHERE m.thread_id IN (SELECT value FROM json_each(?)) AND `+scope+` AND m.deleted = 0
		GROUP BY m.thread_id`, append([]any{jsonText(threads)}, scopeArgs...)...)
}

// conversationSQL is the query of the rows of a page of conversations,
// one more than the page. A conversation's row is its newest message in
// the scope: one that no message of the conversation in the scope comes
// after. With a search, its conversation must have a message, in any
// folder, the search finds.
func (p *listPlan) conversationSQL() (string, []any) {
	where, args := p.inScope("m", p.folders, true)
	if p.after.set {
		where += ` AND (m.date, m.id) < (?, ?)`
		args = append(args, p.after.date, p.after.id)
	}
	if match, matchArgs := p.matchSQL("h", true); match != "" {
		where += ` AND +m.thread_id IN (SELECT h.thread_id FROM messages h WHERE h.deleted = 0 AND ` + match + `)`
		args = append(args, matchArgs...)
	}
	newer, newerArgs := p.inScope("n", p.folders, false)
	where += ` AND NOT EXISTS (SELECT 1 FROM messages n WHERE n.thread_id = m.thread_id AND ` + newer + `
		AND n.deleted = 0 AND (n.date, n.id) > (m.date, m.id))`
	args = append(args, newerArgs...)
	return `SELECT ` + summaryColumns + `
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE ` + where + ` AND m.deleted = 0
		ORDER BY m.date DESC, m.id DESC LIMIT ?`, append(args, p.limit+1)
}
