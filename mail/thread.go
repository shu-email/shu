package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ThreadID names a conversation in the cache. It is the cache's own
// number, which the server knows nothing of. When a message turns out to
// belong to two conversations they become one under the lower ID, and the
// other ID is never given to a conversation again.
type ThreadID int64

// Conversation is a row of a conversation list: what the messages of one
// conversation in the list's scope (a folder, the starred messages, every
// folder, or the inbox of every account; see Scope) add up to.
type Conversation struct {
	Thread ThreadID `json:"thread"`
	// Account is the account the conversation is in: all its messages
	// are, and an action on it is the account's.
	Account string `json:"account,omitempty"`
	// Latest is the newest of those messages; the row shows its sender,
	// subject, date and preview.
	Latest Summary `json:"latest"`
	// Count is how many there are.
	Count int `json:"count"`
	// Unread, Flagged and HasAttachments hold when any of them is unread,
	// flagged or has attachments.
	Unread         bool `json:"unread"`
	Flagged        bool `json:"flagged"`
	HasAttachments bool `json:"hasAttachments"`
	// Labels are the labels any of them has, in the order of the
	// account's labels.
	Labels []LabelID `json:"labels,omitempty"`
	// Matches are the messages of the conversation a search found, in
	// any folder, newest first. A list that is no search has none.
	// Found is the same messages with the parts their terms were found
	// in.
	Matches []MessageRef `json:"matches,omitempty"`
	Found   []Match      `json:"found,omitempty"`
}

// MessageRef names a cached message: its folder and its UID there.
type MessageRef struct {
	Folder string `json:"folder"`
	UID    UID    `json:"uid"`
}

// ThreadHeaders are the headers that put a message in a conversation, as
// a sync read them again for a message it had cached: a cache from before
// conversations has no References or Thread-Index for most of its rows.
type ThreadHeaders struct {
	UID         UID
	MessageID   string
	InReplyTo   string
	References  []string
	ThreadIndex string
}

// What a message's headers may add to its conversation.
const (
	// maxMsgIDLen is the longest Message-ID taken. Real ones are far
	// shorter; a longer one is garbage.
	maxMsgIDLen = 250
	// maxReferences is how many References count: the first, which
	// names where the conversation began, and the last ones, the nearest
	// parents. It bounds what one message, forged or broken, can join.
	maxReferences = 100
	// threadIndexHead is the part of an Outlook Thread-Index that names
	// the conversation: a reserved byte, a time and a GUID. Each reply
	// adds a block of threadIndexBlock bytes.
	threadIndexHead  = 22
	threadIndexBlock = 5
	maxThreadIndex   = 4096
	threadIndexKey   = "thread-index:"
)

// ThreadKeys returns the keys a message is put in a conversation by: its
// Message-ID, the ones it answers and refers to, and the part of an
// Outlook Thread-Index that names the conversation. Messages that share a
// key are one conversation, in whatever folder they are. The subject is
// no key: two messages that only share one are not related. What is
// malformed is left out, so a message without valid headers is a
// conversation of its own.
func ThreadKeys(messageID, inReplyTo string, references []string, threadIndex string) []string {
	var keys []string
	seen := map[string]bool{}
	add := func(key string) {
		if key != "" && !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	add(msgIDKey(messageID))
	add(msgIDKey(inReplyTo))
	for _, id := range capReferences(validIDs(references)) {
		add(msgIDKey(id))
	}
	if head, ok := threadIndexHeadOf(threadIndex); ok {
		// No Message-ID key is without an @, so this one cannot equal one.
		add(threadIndexKey + hex.EncodeToString(head))
	}
	return keys
}

// msgIDKey is a Message-ID as conversations compare it: without angle
// brackets and surrounding space, and with its domain in lower case, which
// RFC 5322 leaves case-insensitive while the part before the @ is not.
// It is empty for what is no Message-ID.
func msgIDKey(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(id, "<"), ">"))
	if len(id) > maxMsgIDLen || !utf8.ValidString(id) {
		return ""
	}
	at := strings.LastIndexByte(id, '@')
	if at <= 0 || at == len(id)-1 {
		return ""
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' {
			return ""
		}
	}
	return id[:at] + "@" + strings.ToLower(id[at+1:])
}

// validIDs keeps the well-formed Message-IDs of a list, once each.
func validIDs(ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		key := msgIDKey(id)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, id)
	}
	return out
}

// capReferences keeps the first reference and the last ones of a list
// longer than maxReferences.
func capReferences(ids []string) []string {
	if len(ids) <= maxReferences {
		return ids
	}
	return append([]string{ids[0]}, ids[len(ids)-(maxReferences-1):]...)
}

// msgIDs reads the Message-IDs of a References or In-Reply-To header,
// leniently: each well-formed one in angle brackets, once, whatever else
// the header holds. Mailers write comments, phrases and worse into these
// headers, and one bad entry must not lose the others.
func msgIDs(value string) []string {
	var found []string
	for {
		end := strings.IndexByte(value, '>')
		if end < 0 {
			break
		}
		if open := strings.LastIndexByte(value[:end], '<'); open >= 0 {
			found = append(found, strings.TrimSpace(value[open+1:end]))
		}
		value = value[end+1:]
	}
	return capReferences(validIDs(found))
}

// cleanThreadIndex returns an Outlook Thread-Index header without its
// folding, or "" when it is not one.
func cleanThreadIndex(value string) string {
	value = strings.Join(strings.Fields(value), "")
	if _, ok := threadIndexHeadOf(value); !ok {
		return ""
	}
	return value
}

// threadIndexHeadOf decodes a Thread-Index and returns the bytes that name
// its conversation. It refuses what does not have the header's shape, and
// a head of zeros, which no Outlook writes and a broken mailer might write
// on every message.
func threadIndexHeadOf(value string) ([]byte, bool) {
	value = strings.Join(strings.Fields(value), "")
	if value == "" || len(value) > maxThreadIndex {
		return nil, false
	}
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(value, "=")); err != nil {
			return nil, false
		}
	}
	if len(b) < threadIndexHead || (len(b)-threadIndexHead)%threadIndexBlock != 0 {
		return nil, false
	}
	head := b[:threadIndexHead]
	if bytes.Count(head, []byte{0}) == len(head) {
		return nil, false
	}
	return head, true
}
