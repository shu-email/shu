package mail

import "strings"

// specialUse maps an IMAP mailbox attribute to the app's special-use
// name, or "".
func specialUse(attrs []string) string {
	for _, a := range attrs {
		switch strings.ToLower(a) {
		case `\inbox`:
			return SpecialInbox
		case `\sent`:
			return SpecialSent
		case `\drafts`:
			return SpecialDrafts
		case `\trash`:
			return SpecialTrash
		case `\junk`:
			return SpecialJunk
		case `\archive`:
			return SpecialArchive
		case `\flagged`:
			return SpecialFlagged
		}
	}
	return ""
}

// specialUseByName is the fallback for servers that do not send
// special-use attributes.
func specialUseByName(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	// Use the last path segment: "[Gmail]/All Mail" and "INBOX/Sent".
	if i := strings.LastIndexAny(normalized, "/."); i >= 0 {
		normalized = strings.TrimSpace(normalized[i+1:])
	}
	switch normalized {
	case "inbox":
		return SpecialInbox
	case "sent", "sent items", "sent mail", "sent messages":
		return SpecialSent
	case "drafts", "draft":
		return SpecialDrafts
	case "trash", "deleted items", "deleted messages", "bin":
		return SpecialTrash
	case "junk", "spam", "junk e-mail", "bulk mail":
		return SpecialJunk
	case "archive", "all mail":
		return SpecialArchive
	case "flagged", "starred":
		return SpecialFlagged
	}
	return ""
}

// folderSpecialUse combines the server's attributes with the name
// fallback.
func folderSpecialUse(name string, attrs []string) string {
	if su := specialUse(attrs); su != "" {
		return su
	}
	return specialUseByName(name)
}

// hasNoSelect reports whether a mailbox is a container that cannot hold
// messages.
func hasNoSelect(attrs []string) bool {
	for _, a := range attrs {
		if strings.EqualFold(a, `\Noselect`) {
			return true
		}
	}
	return false
}
