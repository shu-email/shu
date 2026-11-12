package store

import (
	"context"

	"shu/mail"
)

// ListConversations reads the conversations with a message in a scope (a
// folder, "starred", or "" for the whole account), the one whose newest
// such message is newest first. A row is made of the conversation's
// messages in the scope: the newest, how many there are, and whether any
// is unread, flagged or has attachments. A query keeps the conversations
// any message of which matches, in any folder, and names those messages.
func (s *Store) ListConversations(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Conversation, error) {
	page, err := s.conversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope(accountID, folder), Limit: limit}, queryCriteria(query))
	return page.Conversations, err
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
	return s.summaries(ctx, rows.Values)
}
