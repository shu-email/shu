package mail

import "github.com/emersion/go-imap/v2"

// Capabilities is what an IMAP server offers a connection, read once
// when it is up. Every path that can do without something reads it here
// rather than asking the server again, so that the degradations are in
// one place.
//
// IMAP4rev2 makes some of these mandatory, and go-imap's CapSet already
// reads them as advertised by a server that names it. Keyword support is
// not here: a server keeps keywords by folder, and Folder.PermanentFlags
// says whether one does (Folder.KeywordsKnown).
type Capabilities struct {
	// IDLE lets a watcher wait for the server to push new mail instead
	// of asking again and again.
	IDLE bool
	// Move moves messages with one command that also names their new
	// UIDs; without it a move is a copy and an expunge.
	Move bool
	// UIDPlus names the messages a copy or an append made, and expunges
	// only the messages it names.
	UIDPlus bool
	// ListExtended asks the server which folders the user subscribed to.
	ListExtended bool
	// ListStatus gives each folder's counts in the LIST itself.
	ListStatus bool
	// SpecialUse names a folder's role, as \Sent or \Trash.
	SpecialUse bool
	// UTF8 names mailboxes and messages in UTF-8 without a charset or an
	// ENVELOPE decode. No path reads it yet.
	UTF8 bool
	// Sort sorts a search on the server. Conversations are the cache's,
	// so no path reads it yet.
	Sort bool
	// Thread threads a search on the server. Conversations are the
	// cache's, so no path reads it yet.
	Thread bool
	// ESearch answers a search with ranges rather than every UID.
	ESearch bool
}

// capabilitiesOf reads a server's advertised set as one value.
func capabilitiesOf(caps imap.CapSet) Capabilities {
	return Capabilities{
		IDLE:         caps.Has(imap.CapIdle),
		Move:         caps.Has(imap.CapMove),
		UIDPlus:      caps.Has(imap.CapUIDPlus),
		ListExtended: caps.Has(imap.CapListExtended),
		ListStatus:   caps.Has(imap.CapListStatus),
		SpecialUse:   caps.Has(imap.CapSpecialUse),
		UTF8:         caps.Has(imap.CapUTF8Accept),
		Sort:         caps.Has(imap.CapSort),
		// go-imap names SORT's companion without a constant.
		Thread:  caps.Has(imap.Cap("THREAD")),
		ESearch: caps.Has(imap.CapESearch),
	}
}
