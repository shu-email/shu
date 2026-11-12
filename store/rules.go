package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"shu/mail"
)

// Rules are three tables (migration v13): rules, in their order; rule_log,
// the rules' matches; and expected_arrivals, the messages this app is
// moving into a folder.

// ruleColumns are the fields of a rule every read takes.
const ruleColumns = `id, name, paused, match_mode, stop, conditions, actions`

func ruleRow(r []any) mail.Rule {
	rule := mail.Rule{
		ID: mail.RuleID(number(r[0])), Name: text(r[1]), Paused: truth(r[2]),
		Match: mail.MatchMode(text(r[3])), Stop: truth(r[4]),
	}
	_ = json.Unmarshal([]byte(text(r[5])), &rule.Conditions)
	_ = json.Unmarshal([]byte(text(r[6])), &rule.Actions)
	return rule
}

// Rules returns the rules in their order.
func (s *Store) Rules(ctx context.Context) ([]mail.Rule, error) {
	rows, err := s.query(ctx, `SELECT `+ruleColumns+` FROM rules ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Rule, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, ruleRow(r))
	}
	return out, nil
}

// accountOf is the column of the account a rule names, null for none.
func accountOf(r mail.Rule) any {
	if a := r.Account(); a != "" {
		return a
	}
	return nil
}

// SaveRule writes a new rule after the others, or a rule there is.
func (s *Store) SaveRule(ctx context.Context, r mail.Rule) (mail.Rule, error) {
	conditions, actions := jsonText(r.Conditions), jsonText(r.Actions)
	if r.ID == 0 {
		rows, err := s.query(ctx, `INSERT INTO rules (account_id, position, name, paused, match_mode, stop, conditions, actions)
			VALUES (?, (SELECT COALESCE(MAX(position), -1) + 1 FROM rules), ?, ?, ?, ?, ?, ?) RETURNING id`,
			accountOf(r), r.Name, boolInt(r.Paused), string(r.Match), boolInt(r.Stop), conditions, actions)
		if err != nil {
			return mail.Rule{}, err
		}
		r.ID = mail.RuleID(number(rows.Values[0][0]))
		return r, nil
	}
	res, err := s.exec(ctx, `UPDATE rules SET account_id = ?, name = ?, paused = ?, match_mode = ?, stop = ?,
		conditions = ?, actions = ? WHERE id = ?`,
		accountOf(r), r.Name, boolInt(r.Paused), string(r.Match), boolInt(r.Stop), conditions, actions, int64(r.ID))
	if err != nil {
		return mail.Rule{}, err
	}
	if res.Changes == 0 {
		return mail.Rule{}, mail.ErrNotFound
	}
	return r, nil
}

// MoveRule puts a rule at a place of the order, numbering them all anew.
func (s *Store) MoveRule(ctx context.Context, id mail.RuleID, index int) error {
	rows, err := s.query(ctx, `SELECT id FROM rules ORDER BY position, id`)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(rows.Values))
	for _, r := range rows.Values {
		ids = append(ids, number(r[0]))
	}
	at := slices.Index(ids, int64(id))
	if at < 0 {
		return mail.ErrNotFound
	}
	ids = slices.Delete(ids, at, at+1)
	ids = slices.Insert(ids, min(max(index, 0), len(ids)), int64(id))
	return s.transaction(ctx, []stmtType{stmt(`UPDATE rules SET position = j.key FROM json_each(?) AS j
		WHERE rules.id = j.value`, jsonText(ids))})
}

// DeleteRule removes a rule; its entries of the log stay.
func (s *Store) DeleteRule(ctx context.Context, id mail.RuleID) error {
	res, err := s.exec(ctx, `DELETE FROM rules WHERE id = ?`, int64(id))
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return mail.ErrNotFound
	}
	return nil
}

// activityColumns are the fields of an entry of the log every read takes.
const activityColumns = `id, at, rule_id, rule_name, manual, account_id, folder, uidvalidity, uid,
	message_id, msg_key, subject, from_name, from_email, actions, changes, quiet, state, error`

func activityRow(r []any) mail.RuleActivity {
	e := mail.RuleActivity{
		ID: mail.ActivityID(number(r[0])), At: unixUTC(r[1]), Rule: mail.RuleID(number(r[2])), RuleName: text(r[3]),
		Manual: truth(r[4]), Account: text(r[5]), Folder: text(r[6]), UIDValidity: uint32(number(r[7])),
		UID: mail.UID(number(r[8])), MessageID: text(r[9]), Key: text(r[10]), Subject: text(r[11]),
		From: mail.Address{Name: text(r[12]), Email: text(r[13])}, Quiet: truth(r[16]),
		State: mail.ActivityState(text(r[17])), Error: text(r[18]),
	}
	_ = json.Unmarshal([]byte(text(r[14])), &e.Actions)
	_ = json.Unmarshal([]byte(text(r[15])), &e.Changes)
	return e
}

// AddRuleActivity writes entries and keeps the log to its bound, in one
// transaction.
func (s *Store) AddRuleActivity(ctx context.Context, entries []mail.RuleActivity) ([]mail.RuleActivity, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	stmts := make([]stmtType, 0, len(entries)+1)
	for _, e := range entries {
		stmts = append(stmts, stmt(`INSERT INTO rule_log (at, rule_id, rule_name, manual, account_id, folder,
				uidvalidity, uid, message_id, msg_key, subject, from_name, from_email, actions, changes, quiet, state, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dateColumn(e.At), int64(e.Rule), e.RuleName, boolInt(e.Manual), e.Account, e.Folder,
			int64(e.UIDValidity), int64(e.UID), e.MessageID, e.Key, e.Subject, e.From.Name, e.From.Email,
			jsonText(e.Actions), jsonText(e.Changes), boolInt(e.Quiet), string(e.State), e.Error))
	}
	stmts = append(stmts, stmt(`DELETE FROM rule_log WHERE state <> ? AND id NOT IN (
		SELECT id FROM rule_log ORDER BY id DESC LIMIT ?)`, string(mail.ActivityPending), mail.MaxRuleActivity))
	results, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(entries)
	for i := range out {
		out[i].ID = mail.ActivityID(results[i].LastInsertID)
	}
	return out, nil
}

// RuleActivity reads entries of the log, newest first.
func (s *Store) RuleActivity(ctx context.Context, q mail.ActivityQuery) (mail.ActivityPage, error) {
	var where []string
	var args []any
	narrow := func(cond string, arg any) {
		where = append(where, cond)
		args = append(args, arg)
	}
	if q.ID != 0 {
		narrow("id = ?", int64(q.ID))
	}
	if q.Rule != 0 {
		narrow("rule_id = ?", int64(q.Rule))
	}
	if q.Account != "" {
		narrow("account_id = ?", q.Account)
	}
	if q.State != "" {
		narrow("state = ?", string(q.State))
	}
	if q.Folder != "" {
		narrow("folder = ?", q.Folder)
	}
	if q.UIDValidity != 0 {
		narrow("uidvalidity = ?", int64(q.UIDValidity))
	}
	if len(q.UIDs) > 0 {
		narrow("uid IN (SELECT value FROM json_each(?))", jsonText(q.UIDs))
	}
	filter := ""
	if len(where) > 0 {
		filter = " WHERE " + strings.Join(where, " AND ")
	}
	count, err := s.query(ctx, `SELECT COUNT(*) FROM rule_log`+filter, args...)
	if err != nil {
		return mail.ActivityPage{}, err
	}
	limit := int64(q.Limit)
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.query(ctx, `SELECT `+activityColumns+` FROM rule_log`+filter+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, max(q.Offset, 0))...)
	if err != nil {
		return mail.ActivityPage{}, err
	}
	out := mail.ActivityPage{Entries: make([]mail.RuleActivity, 0, len(rows.Values)), Total: int(number(count.Values[0][0]))}
	for _, r := range rows.Values {
		out.Entries = append(out.Entries, activityRow(r))
	}
	return out, nil
}

// UpdateRuleActivity writes the state, changes and error of entries.
func (s *Store) UpdateRuleActivity(ctx context.Context, entries []mail.RuleActivity) error {
	stmts := make([]stmtType, 0, len(entries))
	for _, e := range entries {
		stmts = append(stmts, stmt(`UPDATE rule_log SET state = ?, changes = ?, error = ? WHERE id = ?`,
			string(e.State), jsonText(e.Changes), e.Error, int64(e.ID)))
	}
	return s.transaction(ctx, stmts)
}

// MessageHeaders reads cached messages of a folder with their labels and
// header lines, oldest first.
func (s *Store) MessageHeaders(ctx context.Context, accountID, folder string, uids []mail.UID) ([]mail.Message, error) {
	if uids != nil && len(uids) == 0 {
		return nil, nil
	}
	filter, args := "", []any{accountID, folder}
	if uids != nil {
		filter, args = ` AND m.server_uid IN (SELECT value FROM json_each(?))`, append(args, jsonText(uids))
	}
	rows, err := s.query(ctx, `SELECT `+summaryColumns+`, COALESCE(m.raw_headers, '')
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND f.name = ? AND m.deleted = 0`+filter+`
		ORDER BY m.server_uid`, args...)
	if err != nil {
		return nil, err
	}
	heads := make([][]any, 0, len(rows.Values))
	for _, r := range rows.Values {
		heads = append(heads, r[:summaryWidth])
	}
	summaries, err := s.summaries(ctx, heads)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Message, 0, len(summaries))
	for i, sum := range summaries {
		out = append(out, mail.Message{Summary: sum, RawHeaders: text(rows.Values[i][summaryWidth])})
	}
	return out, nil
}

// ExpectArrivals notes messages moving into a folder, keeping the newest
// notes to the bound.
func (s *Store) ExpectArrivals(ctx context.Context, accountID, folder string, keys []string) error {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil || len(keys) == 0 {
		return err
	}
	return s.transaction(ctx, []stmtType{
		stmt(`INSERT INTO expected_arrivals (folder_id, msg_key) SELECT ?, value FROM json_each(?)`, folderID, jsonText(keys)),
		stmt(`DELETE FROM expected_arrivals WHERE id NOT IN (
			SELECT id FROM expected_arrivals ORDER BY id DESC LIMIT ?)`, mail.MaxExpectedArrivals),
	})
}

// TakeArrivals takes back the notes of messages of keys moving into a
// folder, in one step, and returns the keys there were notes of.
func (s *Store) TakeArrivals(ctx context.Context, accountID, folder string, keys []string) ([]string, error) {
	folderID, err := s.folderID(ctx, accountID, folder)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	rows, err := s.query(ctx, `DELETE FROM expected_arrivals WHERE folder_id = ? AND msg_key IN (SELECT value FROM json_each(?))
		RETURNING msg_key`, folderID, jsonText(keys))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rows.Values {
		if key := text(r[0]); !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	return out, nil
}
