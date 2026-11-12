package store

import (
	"strings"
)

// listQuery builds the SQL and arguments for a cached list read.
func listQuery(accountID, folder, query string, limit int) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT ` + summaryColumns + `
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND m.deleted = 0`)
	args := []any{accountID}
	scope, scopeArgs := scopeClause(folder)
	b.WriteString(scope)
	args = append(args, scopeArgs...)
	if match, matchArgs := matchClause(query); match != "" {
		b.WriteString(` AND ` + match)
		args = append(args, matchArgs...)
	}
	b.WriteString(` ORDER BY m.date DESC, m.id DESC LIMIT ?`)
	args = append(args, limit)
	return b.String(), args
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

// matchClause is the condition on a message m that it matches a search,
// or "" for no search. Three or more characters use the trigram FTS
// index; shorter queries, which a trigram cannot answer, fall back to
// LIKE.
func matchClause(query string) (string, []any) {
	q := strings.TrimSpace(query)
	switch {
	case q == "":
		return "", nil
	case len([]rune(q)) >= 3:
		return `m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`, []any{ftsQuery(q)}
	default:
		like := "%" + likeEscape(q) + "%"
		return `(m.subject LIKE ? ESCAPE '\' OR m.from_name LIKE ? ESCAPE '\'
			OR m.from_email LIKE ? ESCAPE '\' OR m.body_text LIKE ? ESCAPE '\')`, []any{like, like, like, like}
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
