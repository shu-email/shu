package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"shu/mail"
)

// criterion is a term a search looks for in some parts of a message.
type criterion struct {
	term   string
	fields mail.Field
}

// searchCriteria are the terms of a search, each with the parts it is
// looked for in.
func searchCriteria(q mail.Search) []criterion {
	var out []criterion
	add := func(terms []string, fields mail.Field) {
		for _, term := range terms {
			if term = strings.TrimSpace(term); term != "" {
				out = append(out, criterion{term, fields})
			}
		}
	}
	add(q.Text, mail.FieldAny)
	add(q.From, mail.FieldFrom)
	add(q.To, mail.FieldTo)
	add(q.Subject, mail.FieldSubject)
	add(q.Body, mail.FieldBody)
	return out
}

// queryCriteria is the search of the reads that take a query, as it was
// before searches had parts: the text whole, as one phrase, in the
// subject, the sender or the body.
func queryCriteria(query string) []criterion {
	if q := strings.TrimSpace(query); q != "" {
		return []criterion{{q, mail.FieldSubject | mail.FieldFrom | mail.FieldBody}}
	}
	return nil
}

// fieldColumns are the columns of messages, and of its full-text index,
// that hold each part of a message.
var fieldColumns = []struct {
	field   mail.Field
	columns []string
}{
	{mail.FieldSubject, []string{"subject"}},
	{mail.FieldFrom, []string{"from_name", "from_email"}},
	{mail.FieldTo, []string{"recipients"}},
	{mail.FieldBody, []string{"body_text", "preview"}},
}

func columnsOf(fields mail.Field) []string {
	var out []string
	for _, fc := range fieldColumns {
		if fields.Has(fc.field) {
			out = append(out, fc.columns...)
		}
	}
	return out
}

// listPlan is a list read put in the terms of the SQL that answers it.
type listPlan struct {
	// folders are the scope's, and starred narrows them to the flagged
	// messages.
	folders []int64
	starred bool
	// in are the folders the search's In names, nil when it names none.
	in       []int64
	criteria []criterion
	search   mail.Search
	after    cursor
	limit    int
}

// plan reads what a list read needs to know of the folders, with the
// terms of its search.
func (s *Store) plan(ctx context.Context, q mail.ListQuery, criteria []criterion) (*listPlan, error) {
	if err := q.Scope.Check(); err != nil {
		return nil, err
	}
	after, err := parseCursor(q.After)
	if err != nil {
		return nil, err
	}
	p := &listPlan{starred: q.Scope.Starred, criteria: criteria, search: q.Search, after: after, limit: q.Limit}
	if p.limit <= 0 {
		p.limit = mail.DefaultPageSize
	}
	query, args := `SELECT id, name, COALESCE(special_use, '') FROM folders`, []any(nil)
	if len(q.Scope.Accounts) > 0 {
		query += ` WHERE account_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonText(q.Scope.Accounts))
	}
	rows, err := s.query(ctx, query+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	in := strings.TrimSpace(q.Search.In)
	if in != "" {
		p.in = []int64{}
	}
	for _, r := range rows.Values {
		id, f := number(r[0]), mail.Folder{Name: text(r[1]), SpecialUse: text(r[2])}
		if (q.Scope.Folder == "" || f.Name == q.Scope.Folder) && (q.Scope.Special == "" || f.SpecialUse == q.Scope.Special) {
			p.folders = append(p.folders, id)
		}
		if in != "" && f.NamedBy(in) {
			p.in = append(p.in, id)
		}
	}
	return p, nil
}

// inScope is the condition that a message m is in the scope. drive lets
// the read of a single folder go through that folder's index; any other
// read of several walks the index of all messages newest first, and
// leaves those of other folders out as it goes.
func (p *listPlan) inScope(m string, folders []int64, drive bool) (string, []any) {
	cond, args := `+`+m+`.folder_id IN (SELECT value FROM json_each(?))`, []any{jsonText(folders)}
	if drive && len(folders) == 1 {
		cond, args = m+`.folder_id = ?`, []any{folders[0]}
	}
	if p.starred {
		cond += ` AND ` + m + `.flagged = 1`
	}
	return cond, args
}

// matchSQL is the condition that a message m matches the search, "" for
// none. Terms of three characters or more go to the full-text index, in
// one query; shorter ones, which a trigram cannot answer, are compared
// with LIKE. Unless drive is set, what the index finds is only looked up
// as the rows go by in the order of another index: sorting all it finds
// costs more than reading past what it does not.
func (p *listPlan) matchSQL(m string, drive bool) (string, []any) {
	var conds, fts []string
	var args []any
	for _, c := range p.criteria {
		if len([]rune(c.term)) >= 3 {
			fts = append(fts, ftsTerm(c))
		}
	}
	if len(fts) > 0 {
		id := `+` + m + `.id`
		if drive {
			id = m + `.id`
		}
		conds = append(conds, id+` IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`)
		args = append(args, strings.Join(fts, " AND "))
	}
	for _, c := range p.criteria {
		if len([]rune(c.term)) >= 3 {
			continue
		}
		like := "%" + likeEscape(c.term) + "%"
		var either []string
		for _, column := range columnsOf(c.fields) {
			either = append(either, m+`.`+column+` LIKE ? ESCAPE '\'`)
			args = append(args, like)
		}
		conds = append(conds, "("+strings.Join(either, " OR ")+")")
	}
	q := p.search
	if !q.Since.IsZero() {
		conds = append(conds, m+`.date >= ?`)
		args = append(args, q.Since.Unix())
	}
	if !q.Before.IsZero() {
		conds = append(conds, m+`.date < ?`)
		args = append(args, q.Before.Unix())
	}
	if q.Unread {
		conds = append(conds, m+`.seen = 0`)
	}
	if q.Starred {
		conds = append(conds, m+`.flagged = 1`)
	}
	if q.Attachment {
		conds = append(conds, m+`.has_attachments = 1`)
	}
	if p.in != nil {
		conds = append(conds, `+`+m+`.folder_id IN (SELECT value FROM json_each(?))`)
		args = append(args, jsonText(p.in))
	}
	return strings.Join(conds, " AND "), args
}

// ftsTerm is a criterion as a query of the full-text index: its term as
// one phrase, so that nothing the user types is an operator, in the
// columns of its parts.
func ftsTerm(c criterion) string {
	if c.fields == mail.FieldAny {
		return "(" + ftsQuery(c.term) + ")"
	}
	return "({" + strings.Join(columnsOf(c.fields), " ") + "} : " + ftsQuery(c.term) + ")"
}

// scopeClause narrows a read of messages m in folders f to a folder, to
// the starred messages ("starred") or to none ("", the whole account).
func scopeClause(folder string) (string, []any) {
	switch folder {
	case "":
		return "", nil
	case "starred":
		return ` AND m.flagged = 1`, nil
	default:
		return ` AND f.name = ?`, []any{folder}
	}
}

// ftsQuery turns the user's text into one FTS5 phrase, so operators they
// type are not operators the index evaluates.
func ftsQuery(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// likeEscape escapes the LIKE wildcards in the user's text.
func likeEscape(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(q)
}

// messageText is the text of the parts of a message a search looks in.
type messageText struct {
	subject, fromName, fromEmail, recipients, body, preview string
}

// foundIn returns the parts of a message its search's terms are in,
// compared as the index compares them: without regard to case.
func foundIn(t messageText, criteria []criterion) mail.Field {
	var out mail.Field
	for _, c := range criteria {
		term := strings.ToLower(c.term)
		has := func(texts ...string) bool {
			return slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(strings.ToLower(s), term) })
		}
		for _, part := range []struct {
			field mail.Field
			found bool
		}{
			{mail.FieldSubject, c.fields.Has(mail.FieldSubject) && has(t.subject)},
			{mail.FieldFrom, c.fields.Has(mail.FieldFrom) && has(t.fromName, t.fromEmail)},
			{mail.FieldTo, c.fields.Has(mail.FieldTo) && has(t.recipients)},
			{mail.FieldBody, c.fields.Has(mail.FieldBody) && has(t.body, t.preview)},
		} {
			if part.found {
				out |= part.field
			}
		}
	}
	return out
}

// cursor is a mail.Cursor read: the date and the row of the last row of a
// page.
type cursor struct {
	date, id int64
	set      bool
}

func parseCursor(c mail.Cursor) (cursor, error) {
	if c == "" {
		return cursor{}, nil
	}
	date, id, ok := strings.Cut(string(c), ".")
	d, err1 := strconv.ParseInt(date, 10, 64)
	i, err2 := strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return cursor{}, fmt.Errorf("store: a page cursor %q that no page gave", c)
	}
	return cursor{date: d, id: i, set: true}, nil
}

func (c cursor) value() mail.Cursor {
	return mail.Cursor(strconv.FormatInt(c.date, 10) + "." + strconv.FormatInt(c.id, 10))
}
