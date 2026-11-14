package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/mail"
)

// Labels are three tables (migration v12): labels, an account's;
// message_labels, the labels each cached message has as the server keeps
// them, by its row; and local_labels, those kept on this device only, by
// the mail.LabelKey of the messages, which messages.msg_key holds. A
// message has the labels of both. Their writes hold threadMu, as a sync
// makes labels of the keywords it reads.

// labelSet is an account's labels as a write of them reads them.
type labelSet struct {
	byID map[mail.LabelID]mail.Label
	// names and keywords are the labels by mail.LabelNameKey of their
	// names and by their keywords in lower case; last is the highest
	// position, -1 for none.
	names    map[string]mail.LabelID
	keywords map[string]mail.LabelID
	last     int
}

func (set *labelSet) hasName(key string) bool { _, ok := set.names[key]; return ok }

func (set *labelSet) hasKeyword(lower string) bool { _, ok := set.keywords[lower]; return ok }

// accountLabels reads an account's labels for a write of them.
func (s *Store) accountLabels(ctx context.Context, accountID string) (*labelSet, error) {
	rows, err := s.query(ctx, `SELECT id, name, name_key, color, keyword, position FROM labels WHERE account_id = ?`, accountID)
	if err != nil {
		return nil, err
	}
	set := &labelSet{byID: map[mail.LabelID]mail.Label{}, names: map[string]mail.LabelID{}, keywords: map[string]mail.LabelID{}, last: -1}
	for _, r := range rows.Values {
		l := mail.Label{ID: mail.LabelID(number(r[0])), Account: accountID, Name: text(r[1]),
			Color: mail.LabelColor(text(r[3])), Keyword: text(r[4])}
		set.byID[l.ID] = l
		set.names[text(r[2])] = l.ID
		set.keywords[strings.ToLower(l.Keyword)] = l.ID
		set.last = max(set.last, int(number(r[5])))
	}
	return set, nil
}

// fromKeyword is the statement that makes a label of a keyword found on
// the server, after the account's: named LabelName of it, or with a
// number after that when another label has the name, and with the color
// the keyword says, else the palette's next. The set takes it in.
func (set *labelSet) fromKeyword(accountID, keyword string) stmtType {
	name := mail.FreeLabelName(mail.LabelName(keyword), set.hasName)
	set.last++
	color := mail.KeywordColor(keyword)
	if color == "" {
		color = mail.LabelColorAt(set.last)
	}
	set.names[mail.LabelNameKey(name)] = 0
	set.keywords[strings.ToLower(keyword)] = 0
	return stmt(`INSERT INTO labels (account_id, name, name_key, color, keyword, position) VALUES (?, ?, ?, ?, ?, ?)`,
		accountID, name, mail.LabelNameKey(name), string(color), keyword, set.last)
}

// Labels returns an account's labels in their order, each with how many
// unread messages of its list have it and whether it is kept on this
// device only.
func (s *Store) Labels(ctx context.Context, accountID string) ([]mail.Label, error) {
	rows, err := s.query(ctx, `SELECT id, name, color, keyword FROM labels WHERE account_id = ? ORDER BY position, id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Label, 0, len(rows.Values))
	if len(rows.Values) == 0 {
		return out, nil
	}
	folders, err := s.Folders(ctx, accountID)
	if err != nil {
		return nil, err
	}
	unread, err := s.labelUnread(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		l := mail.Label{ID: mail.LabelID(number(r[0])), Account: accountID, Name: text(r[1]),
			Color: mail.LabelColor(text(r[2])), Keyword: text(r[3])}
		l.Local, l.Unread = mail.KeptLocally(folders, l.Keyword), unread[l.ID]
		out = append(out, l)
	}
	return out, nil
}

// labelUnread counts, for each label of an account, the unread messages
// of its list that have it, each once.
func (s *Store) labelUnread(ctx context.Context, accountID string) (map[mail.LabelID]int, error) {
	folders, err := s.labelFolders(ctx, accountID)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `SELECT label_id, COUNT(*) FROM (
			SELECT ml.label_id, m.id FROM labels l
				JOIN message_labels ml ON ml.label_id = l.id
				JOIN messages m ON m.id = ml.message_id
			WHERE l.account_id = ? AND m.seen = 0 AND m.deleted = 0 AND +m.folder_id IN (SELECT value FROM json_each(?))
			UNION
			SELECT ll.label_id, m.id FROM labels l
				JOIN local_labels ll ON ll.label_id = l.id
				JOIN messages m ON m.msg_key = ll.msg_key
			WHERE l.account_id = ? AND m.seen = 0 AND m.deleted = 0 AND +m.folder_id IN (SELECT value FROM json_each(?)))
		GROUP BY label_id`, accountID, jsonText(folders), accountID, jsonText(folders))
	if err != nil {
		return nil, err
	}
	out := make(map[mail.LabelID]int, len(rows.Values))
	for _, r := range rows.Values {
		out[mail.LabelID(number(r[0]))] = int(number(r[1]))
	}
	return out, nil
}

// labelFoldersSQL reads an account's folders. They are only tested for
// membership, so they come in whatever order the account's index finds
// them, with no sort of their own (v26).
const labelFoldersSQL = `SELECT id, COALESCE(special_use, '') FROM folders WHERE account_id = ?`

// labelFolders are the rows of an account's folders that a label's list
// reads (mail.Folder.InLabelScope).
func (s *Store) labelFolders(ctx context.Context, accountID string) ([]int64, error) {
	rows, err := s.query(ctx, labelFoldersSQL, accountID)
	if err != nil {
		return nil, err
	}
	out := []int64{}
	for _, r := range rows.Values {
		if (mail.Folder{SpecialUse: text(r[1])}).InLabelScope() {
			out = append(out, number(r[0]))
		}
	}
	return out, nil
}

// CreateLabel adds a label after the account's others.
func (s *Store) CreateLabel(ctx context.Context, l mail.Label) (mail.Label, error) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	set, err := s.accountLabels(ctx, l.Account)
	if err != nil {
		return mail.Label{}, err
	}
	key := mail.LabelNameKey(l.Name)
	if set.hasName(key) {
		return mail.Label{}, mail.ErrLabelExists
	}
	l.Keyword = mail.FreeKeyword(l.Keyword, set.hasKeyword)
	position := set.last + 1
	if l.Color == "" {
		l.Color = mail.LabelColorAt(position)
	}
	rows, err := s.query(ctx, `INSERT INTO labels (account_id, name, name_key, color, keyword, position)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`, l.Account, l.Name, key, string(l.Color), l.Keyword, position)
	if err != nil {
		return mail.Label{}, err
	}
	folders, err := s.Folders(ctx, l.Account)
	if err != nil {
		return mail.Label{}, err
	}
	l.ID, l.Local, l.Unread = mail.LabelID(number(rows.Values[0][0])), mail.KeptLocally(folders, l.Keyword), 0
	return l, nil
}

// UpdateLabel renames a label and changes its color, keeping its keyword.
func (s *Store) UpdateLabel(ctx context.Context, l mail.Label) (mail.Label, error) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	set, err := s.accountLabels(ctx, l.Account)
	if err != nil {
		return mail.Label{}, err
	}
	was, ok := set.byID[l.ID]
	if !ok {
		return mail.Label{}, mail.ErrNotFound
	}
	key := mail.LabelNameKey(l.Name)
	if other, ok := set.names[key]; ok && other != l.ID {
		return mail.Label{}, mail.ErrLabelExists
	}
	if l.Color == "" {
		l.Color = was.Color
	}
	if _, err := s.exec(ctx, `UPDATE labels SET name = ?, name_key = ?, color = ? WHERE id = ?`,
		l.Name, key, string(l.Color), int64(l.ID)); err != nil {
		return mail.Label{}, err
	}
	labels, err := s.Labels(ctx, l.Account)
	if err != nil {
		return mail.Label{}, err
	}
	for _, have := range labels {
		if have.ID == l.ID {
			return have, nil
		}
	}
	return mail.Label{}, mail.ErrNotFound
}

// DeleteLabel removes a label, from the messages that have it as the
// server keeps it and from those kept here, by cascade, and remembers
// when, for the syncs that began before.
func (s *Store) DeleteLabel(ctx context.Context, accountID string, id mail.LabelID, at time.Time) error {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	rows, err := s.query(ctx, `DELETE FROM labels WHERE id = ? AND account_id = ? RETURNING keyword`, int64(id), accountID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return mail.ErrNotFound
	}
	s.labelsGone[goneKey(accountID, text(rows.Values[0][0]))] = at
	return nil
}

func goneKey(accountID, keyword string) string { return accountID + "\x00" + strings.ToLower(keyword) }

// goneSince reports whether a keyword's label was deleted at or after
// since. A zero since is no time a sync began.
func (s *Store) goneSince(accountID, keyword string, since time.Time) bool {
	at, ok := s.labelsGone[goneKey(accountID, keyword)]
	return ok && !since.IsZero() && !since.After(at)
}

// ownLabel reports ErrNotFound for a label the account has not.
func (s *Store) ownLabel(ctx context.Context, accountID string, id mail.LabelID) error {
	rows, err := s.query(ctx, `SELECT 1 FROM labels WHERE id = ? AND account_id = ?`, int64(id), accountID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return mail.ErrNotFound
	}
	return nil
}

// SetLabel puts a label on cached messages of a folder or takes it off,
// as the server keeps it or, with local, on this device by their keys,
// and returns the UIDs of those it changed.
func (s *Store) SetLabel(ctx context.Context, accountID string, id mail.LabelID, folder string, uids []mail.UID, on, local bool) ([]mail.UID, error) {
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	if err := s.ownLabel(ctx, accountID, id); err != nil {
		return nil, err
	}
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil || len(uids) == 0 {
		return nil, err
	}
	rows, err := s.query(ctx, `SELECT m.server_uid, COALESCE(m.msg_key, ''),
			EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = m.id AND ml.label_id = ?),
			EXISTS (SELECT 1 FROM local_labels ll WHERE ll.label_id = ? AND ll.msg_key = m.msg_key)
		FROM messages m WHERE m.folder_id = ? AND m.server_uid IN (SELECT value FROM json_each(?))
		ORDER BY m.server_uid`, int64(id), int64(id), folderID, jsonText(uids))
	if err != nil {
		return nil, err
	}
	var changed []mail.UID
	keys := []string{}
	seen := map[string]bool{}
	for _, r := range rows.Values {
		uid, key, synced, here := mail.UID(number(r[0])), text(r[1]), truth(r[2]), truth(r[3])
		if !local {
			if synced != on {
				changed = append(changed, uid)
			}
			continue
		}
		if here == on || key == "" {
			continue
		}
		changed = append(changed, uid)
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	var change stmtType
	switch {
	case !local && on:
		change = stmt(`INSERT OR IGNORE INTO message_labels (message_id, label_id)
			SELECT id, ? FROM messages WHERE folder_id = ? AND server_uid IN (SELECT value FROM json_each(?))`,
			int64(id), folderID, jsonText(changed))
	case !local:
		change = stmt(`DELETE FROM message_labels WHERE label_id = ? AND message_id IN (
			SELECT id FROM messages WHERE folder_id = ? AND server_uid IN (SELECT value FROM json_each(?)))`,
			int64(id), folderID, jsonText(changed))
	case on:
		change = stmt(`INSERT OR IGNORE INTO local_labels (label_id, msg_key) SELECT ?, value FROM json_each(?)`,
			int64(id), jsonText(keys))
	default:
		change = stmt(`DELETE FROM local_labels WHERE label_id = ? AND msg_key IN (SELECT value FROM json_each(?))`,
			int64(id), jsonText(keys))
	}
	if err := s.transaction(ctx, []stmtType{change}); err != nil {
		return nil, err
	}
	return changed, nil
}

// LabelRefs lists the cached messages that have a label as the server
// keeps it.
func (s *Store) LabelRefs(ctx context.Context, accountID string, id mail.LabelID) ([]mail.MessageRef, error) {
	if err := s.ownLabel(ctx, accountID, id); err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `SELECT f.name, m.server_uid FROM message_labels ml
		JOIN messages m ON m.id = ml.message_id JOIN folders f ON f.id = m.folder_id
		WHERE ml.label_id = ? ORDER BY f.name, m.server_uid`, int64(id))
	if err != nil {
		return nil, err
	}
	out := make([]mail.MessageRef, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, mail.MessageRef{Folder: text(r[0]), UID: mail.UID(number(r[1]))})
	}
	return out, nil
}

// SetPermanentFlags records what a folder's PERMANENTFLAGS said.
func (s *Store) SetPermanentFlags(ctx context.Context, accountID, folder string, flags []string) error {
	res, err := s.exec(ctx, `UPDATE folders SET permanent_flags = ? WHERE account_id = ? AND name = ?`,
		encodeFlags(flags), accountID, folder)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return mail.ErrNotFound
	}
	return nil
}

// encodeFlags writes PERMANENTFLAGS as JSON, null when they are not known.
func encodeFlags(flags []string) any {
	if flags == nil {
		return nil
	}
	return jsonText(flags)
}

// decodeFlags reads PERMANENTFLAGS written by encodeFlags: nil when not
// known, and never nil when known.
func decodeFlags(v any) []string {
	s := text(v)
	if s == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil || out == nil {
		return nil
	}
	return out
}

// keywordRow is a message of a folder with the keywords a fetch read of
// it.
type keywordRow struct {
	uid      mail.UID
	keywords []string
}

// keywordRows are the messages and flags of a write whose keywords a
// fetch read.
func keywordRows(msgs []mail.Message, flags []mail.FlagUpdate) []keywordRow {
	var out []keywordRow
	for _, m := range msgs {
		if m.Keywords != nil {
			out = append(out, keywordRow{m.UID, m.Keywords})
		}
	}
	for _, f := range flags {
		if f.Keywords != nil {
			out = append(out, keywordRow{f.UID, f.Keywords})
		}
	}
	return out
}

// keywordLabels is the statements that give messages of a folder, once
// their rows are written, the labels of the keywords a fetch read of
// them, as the server keeps them: each loses those it had so and gains
// those of its keywords, a keyword no label of the account has becoming
// one. A keyword whose label was deleted at or after since, when the
// fetch began, was read before the deletion took it off the server: it
// is left out. Only a label a message gains or loses is written, so that
// a sync that read the same keywords again writes nothing.
//
// reset says the folder's rows are replaced whole in this batch: no label
// of theirs is left to compare with, so every label the messages carry is
// written. The caller must say it rather than the write discover it,
// because the statements are made before the transaction that applies
// them runs. The caller holds threadMu.
func (s *Store) keywordLabels(ctx context.Context, accountID string, folderID int64, rows []keywordRow, since time.Time, reset bool) ([]stmtType, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	set, err := s.accountLabels(ctx, accountID)
	if err != nil {
		return nil, err
	}
	// wanted is what the server said of each message by keyword in lower
	// case, a keyword being its label's whatever case it is in, with the
	// keyword as the server wrote it. A message read twice in a batch
	// keeps both readings.
	var uids []mail.UID
	wanted := make(map[mail.UID]map[string]string, len(rows))
	var stmts []stmtType
	for _, r := range rows {
		if _, ok := wanted[r.uid]; !ok {
			uids = append(uids, r.uid)
			wanted[r.uid] = map[string]string{}
		}
		for _, keyword := range r.keywords {
			if !mail.IsLabelKeyword(keyword) || s.goneSince(accountID, keyword, since) {
				continue
			}
			lower := strings.ToLower(keyword)
			if !set.hasKeyword(lower) {
				stmts = append(stmts, set.fromKeyword(accountID, keyword))
			}
			wanted[r.uid][lower] = keyword
		}
	}
	var have map[mail.UID]map[string]string
	if !reset {
		var err error
		if have, err = s.keywordState(ctx, folderID, uids, set); err != nil {
			return nil, err
		}
	}
	var drops, adds [][]any
	for _, uid := range uids {
		for lower, keyword := range have[uid] {
			if _, ok := wanted[uid][lower]; !ok {
				drops = append(drops, []any{int64(uid), keyword})
			}
		}
		for lower, keyword := range wanted[uid] {
			if _, ok := have[uid][lower]; !ok {
				adds = append(adds, []any{int64(uid), keyword})
			}
		}
	}
	if len(drops) > 0 {
		stmts = append(stmts, stmt(labelDrops, jsonText(drops), folderID, accountID))
	}
	if len(adds) > 0 {
		stmts = append(stmts, stmt(labelAdds, jsonText(adds), folderID, accountID))
	}
	return stmts, nil
}

// labelPairs names the message of the folder and the label of the account
// each (uid, keyword) pair of a JSON list stands for, as a query of that
// list, the folder and the account. The joins go from the list itself, one
// index lookup for the message and one for the label, so a change of a few
// labels costs that and not the folder's mail.
const labelPairs = ` FROM json_each(?) j
		CROSS JOIN messages m
		CROSS JOIN labels l
		WHERE m.folder_id = ? AND m.server_uid = j.value ->> 0
			AND l.account_id = ? AND l.keyword = j.value ->> 1 COLLATE NOCASE`

// labelDrops takes the labels in labelPairs' list off their messages, and
// labelAdds puts them on.
const (
	labelDrops = `DELETE FROM message_labels WHERE (message_id, label_id) IN (
		SELECT m.id, l.id` + labelPairs + `)`
	labelAdds = `INSERT OR IGNORE INTO message_labels (message_id, label_id)
		SELECT m.id, l.id` + labelPairs
)

// keywordState reads the keywords the cache has of each message of a
// folder as the server keeps them, by keyword in lower case with the
// keyword the label has. The account's labels, which the caller has just
// read, name the keywords.
func (s *Store) keywordState(ctx context.Context, folderID int64, uids []mail.UID, labels *labelSet) (map[mail.UID]map[string]string, error) {
	out := make(map[mail.UID]map[string]string, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	rows, err := s.query(ctx, `SELECT m.server_uid, ml.label_id FROM messages m
		JOIN message_labels ml ON ml.message_id = m.id
		WHERE m.folder_id = ? AND m.server_uid IN (SELECT value FROM json_each(?))`,
		folderID, jsonText(uids))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		label, ok := labels.byID[mail.LabelID(number(r[1]))]
		if !ok {
			continue
		}
		uid := mail.UID(number(r[0]))
		set := out[uid]
		if set == nil {
			set = map[string]string{}
			out[uid] = set
		}
		set[strings.ToLower(label.Keyword)] = label.Keyword
	}
	return out, nil
}

// withLabels gives each message of list, whose rows are ids, the labels
// it has: those of its row, as the server keeps them, and those kept on
// this device by its key, a label both being the server's, in the order
// of its account's labels.
func (s *Store) withLabels(ctx context.Context, list []mail.Summary, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.query(ctx, `SELECT ml.message_id, l.id, l.position, 0 FROM message_labels ml
			JOIN labels l ON l.id = ml.label_id
			WHERE ml.message_id IN (SELECT value FROM json_each(?))
		UNION ALL
		SELECT m.id, l.id, l.position, 1 FROM messages m JOIN folders f ON f.id = m.folder_id
			JOIN local_labels ll ON ll.msg_key = m.msg_key
			JOIN labels l ON l.id = ll.label_id AND l.account_id = f.account_id
			WHERE m.id IN (SELECT value FROM json_each(?))
		ORDER BY 1, 3, 2, 4`, jsonText(ids), jsonText(ids))
	if err != nil {
		return err
	}
	byRow := map[int64][]mail.MessageLabel{}
	for _, r := range rows.Values {
		id, label := number(r[0]), mail.LabelID(number(r[1]))
		have := byRow[id]
		if n := len(have); n > 0 && have[n-1].ID == label {
			continue
		}
		byRow[id] = append(have, mail.MessageLabel{ID: label, Local: truth(r[3])})
	}
	for i := range list {
		list[i].Labels = byRow[ids[i]]
	}
	return nil
}

// labelledSQL is the condition that a message m has one of labels, as the
// server keeps it or on this device.
func labelledSQL(m string, labels []int64) (string, []any) {
	ids := jsonText(append([]int64{}, labels...))
	return m + `.id IN (SELECT ml.message_id FROM message_labels ml WHERE ml.label_id IN (SELECT value FROM json_each(?))
		UNION SELECT k.id FROM local_labels ll JOIN labels l ON l.id = ll.label_id
			JOIN messages k ON k.msg_key = ll.msg_key
			JOIN folders kf ON kf.id = k.folder_id AND kf.account_id = l.account_id
		WHERE ll.label_id IN (SELECT value FROM json_each(?)))`, []any{ids, ids}
}

// backfillLabelKeys writes the mail.LabelKey of each message of a cache
// from before labels, from the columns a write of it computes it from.
func backfillLabelKeys(ctx context.Context, s *Store) ([]sqlite.Statement, error) {
	var stmts []sqlite.Statement
	for after := int64(0); ; {
		rows, err := s.query(ctx, `SELECT id, COALESCE(message_id, ''), COALESCE(from_email, ''), date, COALESCE(subject, '')
			FROM messages WHERE id > ? ORDER BY id LIMIT ?`, after, backfillChunk)
		if err != nil {
			return nil, err
		}
		keys := make([][]any, 0, len(rows.Values))
		for _, r := range rows.Values {
			after = number(r[0])
			keys = append(keys, []any{after, mail.LabelKey(mail.Summary{
				MessageID: text(r[1]), From: mail.Address{Email: text(r[2])}, Date: unix(r[3]), Subject: text(r[4]),
			})})
		}
		if len(keys) > 0 {
			stmts = append(stmts, stmt(`UPDATE messages SET msg_key = j.value ->> 1 FROM json_each(?) AS j
				WHERE messages.id = j.value ->> 0`, jsonText(keys)))
		}
		if len(rows.Values) < backfillChunk {
			return stmts, nil
		}
	}
}
