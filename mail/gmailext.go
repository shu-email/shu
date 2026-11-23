package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf16"
)

// Gmail's IMAP extension, X-GM-EXT-1 (see docs/decisions/gmail.md). The
// engine speaks it over a connection of its own for Gmail's own
// semantics: labels that are not folders, one identity across folders,
// Gmail's thread, and its search syntax.

// gmailExt is what the engine asks of Gmail's extension. A service has
// one only for an account whose server advertises X-GM-EXT-1.
type gmailExt interface {
	// Labels reads the labels of messages of a folder, by UID, as Gmail
	// names them; the system's are \Inbox, \Sent, \Drafts, \Trash,
	// \Spam, \Starred, \Important and \All.
	Labels(ctx context.Context, folder string, uids []UID) (map[UID][]string, error)
	// SetLabels adds and removes labels of messages of a folder, without
	// moving them: one message keeps every label it was not asked to
	// lose.
	SetLabels(ctx context.Context, folder string, uids []UID, add, remove []string) error
	// IDs reads the Gmail identities of messages of a folder.
	IDs(ctx context.Context, folder string, uids []UID) (map[UID]GmailID, error)
	// RawSearch returns the UIDs of a folder that Gmail's own search
	// syntax finds, as the web interface reads it.
	RawSearch(ctx context.Context, folder, query string) ([]UID, error)
}

// GmailID is a message's Gmail identities: one message is the same in
// every folder it has, and belongs to one thread however the folders
// split it.
type GmailID struct {
	// Message is Gmail's message ID, the 64-bit number behind the web
	// interface's hexadecimal ID.
	Message uint64
	// Thread is Gmail's thread ID, which groups the messages Gmail's web
	// interface shows as one conversation.
	Thread uint64
}

// gmailLabelEncode writes a label as X-GM-LABELS carries it: printable
// ASCII as it is, & as "&-", and every other rune in modified UTF-7
// (RFC 3501 §5.1.3), whose base64 alphabet has ',' where the standard one
// has '/'.
func gmailLabelEncode(label string) string {
	var b strings.Builder
	var pending []rune
	flush := func() {
		if len(pending) == 0 {
			return
		}
		units := utf16.Encode(pending)
		raw := make([]byte, 0, len(units)*2)
		for _, u := range units {
			raw = append(raw, byte(u>>8), byte(u))
		}
		b.WriteByte('&')
		b.WriteString(strings.ReplaceAll(base64.RawStdEncoding.EncodeToString(raw), "/", ","))
		b.WriteByte('-')
		pending = pending[:0]
	}
	for _, r := range label {
		switch {
		case r == '&':
			flush()
			b.WriteString("&-")
		case r >= 0x20 && r <= 0x7e:
			flush()
			b.WriteRune(r)
		default:
			pending = append(pending, r)
		}
	}
	flush()
	return b.String()
}

// gmailLabelDecode reads a label as X-GM-LABELS carries it.
func gmailLabelDecode(value string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '&' {
			b.WriteByte(value[i])
			i++
			continue
		}
		// The run of base64 ends at the first '-'; "&-" alone is '&'.
		end := strings.IndexByte(value[i+1:], '-')
		if end < 0 {
			return "", errors.New("mail: a Gmail label's UTF-7 run has no end")
		}
		chunk := value[i+1 : i+1+end]
		i += end + 2
		if chunk == "" {
			b.WriteByte('&')
			continue
		}
		chunk = strings.ReplaceAll(chunk, ",", "/")
		if pad := len(chunk) % 4; pad != 0 {
			chunk += strings.Repeat("=", 4-pad)
		}
		raw, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			return "", err
		}
		if len(raw)%2 != 0 {
			return "", errors.New("mail: a Gmail label's UTF-7 run is not UTF-16")
		}
		units := make([]uint16, 0, len(raw)/2)
		for j := 0; j < len(raw); j += 2 {
			units = append(units, uint16(raw[j])<<8|uint16(raw[j+1]))
		}
		b.WriteString(string(utf16.Decode(units)))
	}
	return b.String(), nil
}
