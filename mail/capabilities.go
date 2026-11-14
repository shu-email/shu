package mail

import (
	"strings"

	"github.com/emersion/go-imap/v2"
)

// Capabilities is what an IMAP server offers a connection, read once
// when it is up. Every path on an established connection that can do
// without something reads it here rather than asking the server again,
// so that the degradations are in one place. Sign-in is the one
// exception: it reads the greeting's set to choose a mechanism, before
// this value exists.
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
	// UTF8 advertises UTF8=ACCEPT. Nothing enables it (ENABLE), so the
	// strings on this connection are not UTF-8 yet, and no path reads it.
	UTF8 bool
	// Sort sorts a search on the server, whether the server names SORT
	// or one of its algorithm forms (SORT=DISPLAY). Conversations are
	// the cache's, so no path reads it yet.
	Sort bool
	// Thread threads a search on the server, from THREAD=REFERENCES or
	// THREAD=ORDEREDSUBJECT. Conversations are the cache's, so no path
	// reads it yet.
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
		Sort:         advertisePrefix(caps, "SORT"),
		Thread:       advertiseThread(caps),
		ESearch:      caps.Has(imap.CapESearch),
	}
}

// advertisePrefix reports whether a server names a capability or one of
// its algorithm forms, as SORT and SORT=DISPLAY.
func advertisePrefix(caps imap.CapSet, name string) bool {
	if caps.Has(imap.Cap(name)) {
		return true
	}
	for c := range caps {
		if strings.HasPrefix(string(c), name+"=") {
			return true
		}
	}
	return false
}

// advertiseThread reports whether a server names a threading algorithm,
// as THREAD=REFERENCES. There is no bare THREAD: RFC 5256 requires one
// algorithm at least.
func advertiseThread(caps imap.CapSet) bool {
	return len(caps.ThreadAlgorithms()) > 0
}
