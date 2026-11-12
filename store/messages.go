package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"shu/mail"
)

// summaryColumns are the message list fields, of messages m in folders f,
// and the message's row, which its labels are read by.
const summaryColumns = `m.server_uid, f.name, COALESCE(m.message_id, ''), COALESCE(m.in_reply_to, ''),
	COALESCE(m.references_json, ''), COALESCE(m.from_name, ''), COALESCE(m.from_email, ''),
	COALESCE(m.to_json, ''), COALESCE(m.cc_json, ''), COALESCE(m.reply_to_json, ''),
	COALESCE(m.subject, ''), COALESCE(m.preview, ''), m.date, m.size,
	m.seen, m.flagged, m.has_attachments, COALESCE(m.thread_index, ''), m.thread_id, f.account_id,
	m.id, f.permanent_flags`

// summaryWidth is how many columns summaryColumns has, and summaryDate
// and summaryID the places of the date and the row among them.
const (
	summaryWidth = 22
	summaryDate  = 12
	summaryID    = 20
)

// summaries reads rows of summaryColumns, with their labels.
func (s *Store) summaries(ctx context.Context, rows [][]any) ([]mail.Summary, error) {
	out := make([]mail.Summary, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, summaryRow(r))
		ids = append(ids, number(r[summaryID]))
	}
	return out, s.withLabels(ctx, out, ids)
}

func summaryRow(r []any) mail.Summary {
	return mail.Summary{
		LabelsLocal:    labelsLocal(r[21]),
		Account:        text(r[19]),
		UID:            mail.UID(number(r[0])),
		Folder:         text(r[1]),
		MessageID:      text(r[2]),
		InReplyTo:      text(r[3]),
		References:     decodeStrings(text(r[4])),
		From:           mail.Address{Name: text(r[5]), Email: text(r[6])},
		To:             decodeAddresses(text(r[7])),
		Cc:             decodeAddresses(text(r[8])),
		ReplyTo:        decodeAddresses(text(r[9])),
		Subject:        text(r[10]),
		Preview:        text(r[11]),
		Date:           unix(r[12]),
		Size:           number(r[13]),
		Seen:           truth(r[14]),
		Flagged:        truth(r[15]),
		HasAttachments: truth(r[16]),
		ThreadIndex:    text(r[17]),
		Thread:         mail.ThreadID(number(r[18])),
	}
}

// labelsLocal reports whether a folder's PERMANENTFLAGS column says it
// keeps no new keyword.
func labelsLocal(v any) bool {
	f := mail.Folder{PermanentFlags: decodeFlags(v)}
	return f.KeywordsKnown() && !f.KeepsNewKeywords()
}

func encodeJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if string(b) == "null" || string(b) == "[]" {
		return ""
	}
	return string(b)
}

func decodeStrings(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

func decodeAddresses(s string) []mail.Address {
	if s == "" {
		return nil
	}
	var out []mail.Address
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// recipientsText is what the full-text index holds of the To and Cc of a
// message: each name with its address. Migration v8 writes the same of
// the rows it found.
func recipientsText(to, cc []mail.Address) string {
	var parts []string
	for _, list := range [][]mail.Address{to, cc} {
		for _, a := range list {
			parts = append(parts, strings.TrimSpace(a.Name+" "+a.Email))
		}
	}
	return strings.Join(parts, ", ")
}

// ListMessages reads the cache: the newest headers of a folder, all
// starred messages across folders ("starred"), or search results.
func (s *Store) ListMessages(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Summary, error) {
	page, err := s.messagePage(ctx, mail.ListQuery{Scope: mail.AccountScope(accountID, folder), Limit: limit}, queryCriteria(query))
	return page.Messages, err
}

// GetMessage reads a cached message and its attachments.
func (s *Store) GetMessage(ctx context.Context, accountID, folder string, uid mail.UID) (mail.Message, bool, error) {
	rows, err := s.query(ctx, `SELECT `+summaryColumns+`,
		COALESCE(m.body_text, ''), COALESCE(m.body_html, ''), m.body_truncated,
		COALESCE(m.raw_headers, ''), m.cached_at, m.id
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND f.name = ? AND m.server_uid = ?`,
		accountID, folder, int64(uid))
	if err != nil {
		return mail.Message{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.Message{}, false, nil
	}
	r := rows.Values[0]
	body := r[summaryWidth:]
	summary, err := s.summaries(ctx, [][]any{r[:summaryWidth]})
	if err != nil {
		return mail.Message{}, false, err
	}
	msg := mail.Message{
		Summary:       summary[0],
		BodyText:      text(body[0]),
		BodyHTML:      text(body[1]),
		BodyTruncated: truth(body[2]),
		RawHeaders:    text(body[3]),
		CachedAt:      unix(body[4]),
		BodyCached:    body[4] != nil,
	}
	messageID := number(body[5])
	attachments, err := s.attachments(ctx, messageID)
	if err != nil {
		return mail.Message{}, false, err
	}
	msg.Attachments = attachments
	return msg, true, nil
}

func (s *Store) attachments(ctx context.Context, messageID int64) ([]mail.Attachment, error) {
	rows, err := s.query(ctx, `SELECT part, COALESCE(filename, ''), COALESCE(content_type, ''),
		COALESCE(size, 0), COALESCE(content_hash, ''), COALESCE(cache_path, '')
		FROM attachments WHERE message_id = ? ORDER BY rowid`, messageID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Attachment, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.Attachment{
			Part:        text(r[0]),
			Filename:    text(r[1]),
			ContentType: text(r[2]),
			Size:        number(r[3]),
			ContentHash: text(r[4]),
			CachePath:   text(r[5]),
		})
	}
	return out, nil
}

// SaveMessage stores a fetched message with its body and attachments.
func (s *Store) SaveMessage(ctx context.Context, accountID, folder string, m mail.Message) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return err
	}
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	var id, thread int64
	var wasSeen bool
	rows, err := s.query(ctx, `SELECT id, seen, thread_id FROM messages WHERE folder_id = ? AND server_uid = ?`, folderID, int64(m.UID))
	if err != nil {
		return err
	}
	if len(rows.Values) == 1 {
		id = number(rows.Values[0][0])
		wasSeen = truth(rows.Values[0][1])
		thread = number(rows.Values[0][2])
	}
	keys := mail.ThreadKeys(m.MessageID, m.InReplyTo, m.References, m.ThreadIndex)
	plan, err := s.planThreads(ctx, accountID, [][]string{keys})
	if err != nil {
		return err
	}
	thread = plan.assign(keys, thread)
	stmts := append(plan.before(),
		stmt(`INSERT INTO messages (
				folder_id, server_uid, message_id, in_reply_to, references_json,
				from_name, from_email, to_json, cc_json, reply_to_json, recipients,
				subject, preview, date, size, seen, flagged, has_attachments,
				body_text, body_html, body_truncated, raw_headers, cached_at,
				thread_index, thread_id, msg_key, deleted)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
			ON CONFLICT (folder_id, server_uid) DO UPDATE SET
				message_id=excluded.message_id, in_reply_to=excluded.in_reply_to,
				references_json=excluded.references_json,
				from_name=excluded.from_name, from_email=excluded.from_email,
				to_json=excluded.to_json, cc_json=excluded.cc_json, reply_to_json=excluded.reply_to_json,
				recipients=excluded.recipients,
				subject=excluded.subject, preview=excluded.preview, date=excluded.date, size=excluded.size,
				seen=excluded.seen OR messages.seen,
				flagged=excluded.flagged OR messages.flagged,
				has_attachments=excluded.has_attachments,
				body_text=excluded.body_text, body_html=excluded.body_html,
				body_truncated=excluded.body_truncated, raw_headers=excluded.raw_headers,
				cached_at=excluded.cached_at, thread_index=excluded.thread_index,
				thread_id=excluded.thread_id, msg_key=excluded.msg_key, deleted=0`,
			folderID, int64(m.UID), m.MessageID, m.InReplyTo, encodeJSON(m.References),
			m.From.Name, m.From.Email, encodeJSON(m.To), encodeJSON(m.Cc), encodeJSON(m.ReplyTo), recipientsText(m.To, m.Cc),
			m.Subject, m.Preview, dateColumn(m.Date), m.Size, boolInt(m.Seen), boolInt(m.Flagged),
			boolInt(m.HasAttachments), m.BodyText, m.BodyHTML, boolInt(m.BodyTruncated),
			m.RawHeaders, unixPtr(m.CachedAt), m.ThreadIndex, plan.find(thread), mail.LabelKey(m.Summary)))
	if m.Seen && !wasSeen {
		stmts = append(stmts, stmt(`UPDATE folders SET unread = MAX(0, unread - 1) WHERE id = ?`, folderID))
	}
	if id != 0 {
		stmts = append(stmts, stmt(`DELETE FROM attachments WHERE message_id = ?`, id))
		for _, a := range m.Attachments {
			stmts = append(stmts, stmt(`INSERT INTO attachments
				(message_id, part, filename, content_type, size, content_hash, cache_path)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				id, a.Part, a.Filename, a.ContentType, a.Size, a.ContentHash, a.CachePath))
		}
	}
	// A new row's attachments need its id, which only the insert knows.
	if id == 0 {
		for _, a := range m.Attachments {
			stmts = append(stmts, stmt(`INSERT INTO attachments
				(message_id, part, filename, content_type, size, content_hash, cache_path)
				SELECT id, ?, ?, ?, ?, ?, ? FROM messages WHERE folder_id = ? AND server_uid = ?`,
				a.Part, a.Filename, a.ContentType, a.Size, a.ContentHash, a.CachePath, folderID, int64(m.UID)))
		}
		// A row written before keeps its labels, as it keeps a flag set:
		// a label just put on it may not have reached the server this
		// fetch read.
		labels, err := s.keywordLabels(ctx, accountID, folderID, keywordRows([]mail.Message{m}, nil), time.Time{})
		if err != nil {
			return err
		}
		stmts = append(stmts, labels...)
	}
	stmts = append(stmts, plan.after()...)
	if id != 0 {
		// The body of a row written before, which counted its message.
		return s.transaction(ctx, stmts)
	}
	return s.writeCounting(ctx, stmts, func() ([]stmtType, error) {
		return s.countFolder(ctx, folderID, []mail.Message{m})
	})
}

// ApplySync writes one folder sync batch and advances its cursor, all or
// nothing.
func (s *Store) ApplySync(ctx context.Context, accountID string, b mail.SyncBatch) error {
	folderID, err := s.folderID(ctx, accountID, b.Folder.Name)
	if err != nil {
		return err
	}
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	// A reset folder's rows go: the UIDs now name other messages.
	var uids []mail.UID
	if !b.Reset {
		for _, m := range b.Messages {
			uids = append(uids, m.UID)
		}
		for _, h := range b.Threading {
			uids = append(uids, h.UID)
		}
	}
	cached, err := s.cachedThreads(ctx, folderID, uids)
	if err != nil {
		return err
	}
	keys := make([][]string, 0, len(b.Messages))
	written := map[mail.UID]bool{}
	for _, m := range b.Messages {
		keys = append(keys, mail.ThreadKeys(m.MessageID, m.InReplyTo, m.References, m.ThreadIndex))
		written[m.UID] = true
	}
	// A row read again is threaded again only when the server gave more
	// than the cache has, so that a sync that changed nothing writes
	// nothing for it.
	var again []mail.ThreadHeaders
	for _, h := range b.Threading {
		row, ok := cached[h.UID]
		if !ok || written[h.UID] {
			continue
		}
		refs := encodeJSON(h.References)
		if (refs == "" || refs == row.refs) && (h.ThreadIndex == "" || h.ThreadIndex == row.threadIndex) {
			continue
		}
		again = append(again, h)
		keys = append(keys, mail.ThreadKeys(h.MessageID, h.InReplyTo, h.References, h.ThreadIndex))
	}
	plan, err := s.planThreads(ctx, accountID, keys)
	if err != nil {
		return err
	}
	threads := make([]int64, len(keys))
	for i, m := range b.Messages {
		threads[i] = plan.assign(keys[i], cached[m.UID].thread)
	}
	for i, h := range again {
		j := len(b.Messages) + i
		threads[j] = plan.assign(keys[j], cached[h.UID].thread)
	}

	stmts := make([]stmtType, 0, len(b.Messages)+len(again)+len(b.Flags)+8)
	if b.Reset {
		stmts = append(stmts, stmt(`DELETE FROM messages WHERE folder_id = ?`, folderID))
	}
	stmts = append(stmts, plan.before()...)
	for i, m := range b.Messages {
		stmts = append(stmts, upsertHeader(folderID, m, plan.find(threads[i])))
	}
	for i, h := range again {
		stmts = append(stmts, stmt(`UPDATE messages SET
				references_json = COALESCE(NULLIF(?, ''), references_json),
				thread_index = COALESCE(NULLIF(?, ''), thread_index), thread_id = ?
			WHERE folder_id = ? AND server_uid = ?`,
			encodeJSON(h.References), h.ThreadIndex, plan.find(threads[len(b.Messages)+i]), folderID, int64(h.UID)))
	}
	for _, f := range b.Flags {
		stmts = append(stmts, stmt(`UPDATE messages SET seen = ?, flagged = ?
			WHERE folder_id = ? AND server_uid = ?`,
			boolInt(f.Seen), boolInt(f.Flagged), folderID, int64(f.UID)))
	}
	labels, err := s.keywordLabels(ctx, accountID, folderID, keywordRows(b.Messages, b.Flags), b.Folder.SyncedAt)
	if err != nil {
		return err
	}
	stmts = append(stmts, labels...)
	stmts = append(stmts, stmt(`UPDATE folders SET
			uidvalidity = ?, last_uid = ?, unread = ?, total = ?, synced_at = ?, complete = ?,
			permanent_flags = COALESCE(?, permanent_flags)
			WHERE id = ?`,
		int64(b.Folder.UIDValidity), int64(b.Folder.LastUID), b.Folder.Unread, b.Folder.Total,
		unixPtr(b.Folder.SyncedAt), boolInt(b.Folder.Complete), encodeFlags(b.Folder.PermanentFlags), folderID))
	return s.writeCounting(ctx, append(stmts, plan.after()...), func() ([]stmtType, error) {
		return s.countFolder(ctx, folderID, b.Messages)
	})
}

// upsertHeader writes the header a fetch read of a message, in its
// conversation, with the header lines the rules read (RawHeaders), when
// the fetch got them. A header the fetch did not get keeps what the cache
// had of it, as do header lines the cache had.
func upsertHeader(folderID int64, m mail.Message, thread int64) stmtType {
	return stmt(`INSERT INTO messages (
			folder_id, server_uid, message_id, in_reply_to, references_json,
			from_name, from_email, to_json, cc_json, reply_to_json, recipients,
			subject, preview, date, size, seen, flagged, has_attachments,
			thread_index, thread_id, msg_key, raw_headers, deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), 0)
		ON CONFLICT (folder_id, server_uid) DO UPDATE SET
			message_id=excluded.message_id, in_reply_to=excluded.in_reply_to,
			references_json=COALESCE(NULLIF(excluded.references_json, ''), messages.references_json),
			from_name=excluded.from_name, from_email=excluded.from_email,
			to_json=excluded.to_json, cc_json=excluded.cc_json, reply_to_json=excluded.reply_to_json,
			recipients=excluded.recipients,
			subject=excluded.subject, preview=excluded.preview, date=excluded.date,
			size=excluded.size, seen=excluded.seen, flagged=excluded.flagged,
			has_attachments=excluded.has_attachments,
			thread_index=COALESCE(NULLIF(excluded.thread_index, ''), messages.thread_index),
			thread_id=excluded.thread_id, msg_key=excluded.msg_key,
			raw_headers=COALESCE(messages.raw_headers, excluded.raw_headers), deleted=0`,
		folderID, int64(m.UID), m.MessageID, m.InReplyTo, encodeJSON(m.References),
		m.From.Name, m.From.Email, encodeJSON(m.To), encodeJSON(m.Cc), encodeJSON(m.ReplyTo), recipientsText(m.To, m.Cc),
		m.Subject, m.Preview, dateColumn(m.Date), m.Size, boolInt(m.Seen), boolInt(m.Flagged),
		boolInt(m.HasAttachments), m.ThreadIndex, thread, mail.LabelKey(m.Summary), m.RawHeaders)
}

// ApplyOlder writes messages of a folder that a sync left out, older ones
// fetched on demand, in their conversations, and whether the cache now
// lacks none. The cursor, the counts and the watermark stay as they are:
// this is no new mail. A batch fetched under a UIDVALIDITY the folder no
// longer has names other messages, and is refused whole.
func (s *Store) ApplyOlder(ctx context.Context, accountID string, b mail.OlderBatch) error {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	// Only ApplySync changes a folder's UIDVALIDITY, under threadMu, so
	// what is read here still holds when the batch is written.
	rows, err := s.query(ctx, `SELECT id, uidvalidity FROM folders WHERE account_id = ? AND name = ?`, accountID, b.Folder)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return mail.ErrNotFound
	}
	folderID := number(rows.Values[0][0])
	if uint32(number(rows.Values[0][1])) != b.UIDValidity {
		return mail.ErrFolderReset
	}
	uids := make([]mail.UID, 0, len(b.Messages))
	keys := make([][]string, 0, len(b.Messages))
	for _, m := range b.Messages {
		uids = append(uids, m.UID)
		keys = append(keys, mail.ThreadKeys(m.MessageID, m.InReplyTo, m.References, m.ThreadIndex))
	}
	// A sync may have written some of them meanwhile.
	cached, err := s.cachedThreads(ctx, folderID, uids)
	if err != nil {
		return err
	}
	plan, err := s.planThreads(ctx, accountID, keys)
	if err != nil {
		return err
	}
	threads := make([]int64, len(b.Messages))
	for i, m := range b.Messages {
		threads[i] = plan.assign(keys[i], cached[m.UID].thread)
	}
	stmts := plan.before()
	for i, m := range b.Messages {
		stmts = append(stmts, upsertHeader(folderID, m, plan.find(threads[i])))
	}
	labels, err := s.keywordLabels(ctx, accountID, folderID, keywordRows(b.Messages, nil), time.Time{})
	if err != nil {
		return err
	}
	stmts = append(stmts, labels...)
	stmts = append(stmts, stmt(`UPDATE folders SET complete = ? WHERE id = ?`, boolInt(b.Complete), folderID))
	return s.writeCounting(ctx, append(stmts, plan.after()...), func() ([]stmtType, error) {
		return s.countFolder(ctx, folderID, b.Messages)
	})
}

// SetFlag updates a flag and, for \Seen, adjusts the folder's unread
// count in the same transaction.
func (s *Store) SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return err
	}
	column := ""
	switch flag {
	case mail.FlagSeen:
		column = "seen"
	case mail.FlagFlagged:
		column = "flagged"
	default:
		return fmt.Errorf("store: flag %q is not cached", flag)
	}
	stmts := []stmtType{
		stmt(`UPDATE messages SET `+column+` = ? WHERE folder_id = ? AND server_uid = ?`,
			boolInt(on), folderID, int64(uid)),
	}
	if flag == mail.FlagSeen {
		rows, err := s.query(ctx, `SELECT seen FROM messages WHERE folder_id = ? AND server_uid = ?`, folderID, int64(uid))
		if err != nil {
			return err
		}
		if len(rows.Values) == 1 && truth(rows.Values[0][0]) != on {
			if on {
				stmts = append(stmts, stmt(`UPDATE folders SET unread = MAX(0, unread - 1) WHERE id = ?`, folderID))
			} else {
				stmts = append(stmts, stmt(`UPDATE folders SET unread = unread + 1 WHERE id = ?`, folderID))
			}
		}
	}
	return s.transaction(ctx, stmts)
}

// MarkDeleted hides or restores a message while the server confirms a
// move.
func (s *Store) MarkDeleted(ctx context.Context, accountID, folder string, uid mail.UID, deleted bool) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE messages SET deleted = ? WHERE folder_id = ? AND server_uid = ?`,
		boolInt(deleted), folderID, int64(uid))
	return err
}

// DeleteMessage removes a message once the server confirmed it moved.
func (s *Store) DeleteMessage(ctx context.Context, accountID, folder string, uid mail.UID) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return err
	}
	stmts := []stmtType{stmt(`DELETE FROM messages WHERE folder_id = ? AND server_uid = ?`, folderID, int64(uid))}
	rows, err := s.query(ctx, `SELECT seen FROM messages WHERE folder_id = ? AND server_uid = ?`, folderID, int64(uid))
	if err != nil {
		return err
	}
	if len(rows.Values) == 1 && !truth(rows.Values[0][0]) {
		stmts = append(stmts, stmt(`UPDATE folders SET unread = MAX(0, unread - 1), total = MAX(0, total - 1) WHERE id = ?`, folderID))
	} else {
		stmts = append(stmts, stmt(`UPDATE folders SET total = MAX(0, total - 1) WHERE id = ?`, folderID))
	}
	return s.transaction(ctx, stmts)
}

// SetAttachmentCache records where an attachment's bytes are cached.
func (s *Store) SetAttachmentCache(ctx context.Context, accountID, folder string, uid mail.UID, part, hash, path string) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE attachments SET content_hash = ?, cache_path = ?
		WHERE part = ? AND message_id = (
			SELECT id FROM messages WHERE folder_id = ? AND server_uid = ?)`,
		hash, path, part, folderID, int64(uid))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
