package store

import (
	"strings"
)

// listQuery builds the SQL and arguments for a cached list read. Search
// with three or more characters uses the trigram FTS index; shorter
// queries, which a trigram cannot answer, fall back to LIKE.
func listQuery(accountID, folder, query string, limit int) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT ` + summaryColumns + `
		FROM messages m JOIN folders f ON f.id = m.folder_id
		WHERE f.account_id = ? AND m.deleted = 0`)
	args := []any{accountID}
	switch folder {
	case "":
		// The whole account.
	case "starred":
		b.WriteString(` AND m.flagged = 1`)
	default:
		b.WriteString(` AND f.name = ?`)
		args = append(args, folder)
	}
	q := strings.TrimSpace(query)
	switch {
	case q == "":
	case len([]rune(q)) >= 3:
		b.WriteString(` AND m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`)
		args = append(args, ftsQuery(q))
	default:
		like := "%" + likeEscape(q) + "%"
		b.WriteString(` AND (m.subject LIKE ? ESCAPE '\' OR m.from_name LIKE ? ESCAPE '\'
			OR m.from_email LIKE ? ESCAPE '\' OR m.body_text LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	b.WriteString(` ORDER BY m.date DESC, m.id DESC LIMIT ?`)
	args = append(args, limit)
	return b.String(), args
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
