package app

import (
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The window's ways into the mail. A folder is where a message sits on a
// server; the window asks what mail needs of the user instead, and the
// folders a server has stay behind the list rather than before it. What
// the user filed earlier is not lost with them: a rule, a label, a search
// or a smart mailbox still names a folder, and sync still reads them.

// navEntry is one way into the mail, which the sidebar lists instead of
// an account's folders.
type navEntry struct {
	ID    string
	Label string
	Icon  *ui.SVG
}

// navEntries are the sidebar's entries: the stream first, then the lists
// that answer a question of their own, then the two places mail goes and
// comes back from. A folder the server does not have is left out.
func (a *app) navEntries() []navEntry {
	entries := []navEntry{
		{ID: allInboxes, Label: locales.Stream(), Icon: sparklesIcon},
		// Starred is no folder of the server: it is the one mark a user
		// puts on mail, and the stream is otherwise the only way in.
		{ID: "starred", Label: locales.Starred(), Icon: starIcon},
		{ID: followUpFolder, Label: locales.FollowUp(), Icon: replyIcon},
		{ID: remindersFolder, Label: locales.Reminders(), Icon: clockIcon},
		{ID: draftsFolder, Label: locales.Drafts(), Icon: draftIcon},
	}
	if name := a.folderBySpecial(mail.SpecialSent); name != "" {
		entries = append(entries, navEntry{ID: name, Label: locales.Sent(), Icon: sentIcon})
	}
	// Archive is where mail goes when it is done with: the one folder a
	// user still looks at, because "done" is not "gone".
	if name := a.folderBySpecial(mail.SpecialArchive); name != "" {
		entries = append(entries, navEntry{ID: name, Label: locales.Archive(), Icon: archiveIcon})
	}
	if name := a.folderBySpecial(mail.SpecialJunk); name != "" {
		entries = append(entries, navEntry{ID: name, Label: locales.Junk(), Icon: spamIcon})
	}
	if name := a.folderBySpecial(mail.SpecialTrash); name != "" {
		entries = append(entries, navEntry{ID: name, Label: locales.Trash(), Icon: trashIcon})
	}
	return entries
}

// navUnread is what an entry counts: the mail it holds that the user has
// not read. Sent, Trash and Drafts count nothing, as a number there says
// nothing about what it holds.
func (a *app) navUnread(id string) int {
	switch id {
	case allInboxes:
		return a.unifiedUnread()
	case followUpFolder:
		return a.followUps
	case remindersFolder:
		return len(a.reminders)
	}
	return 0
}
