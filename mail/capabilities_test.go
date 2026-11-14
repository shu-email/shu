package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

// capabilitiesOf reads the advertised set once. A server that names
// IMAP4rev2 is read as having the extensions that revision makes
// mandatory, as go-imap's CapSet does; one that names none has nothing
// beyond a plain IMAP4rev1 connection.
func TestCapabilitiesOf(t *testing.T) {
	rev1 := imap.CapSet{imap.CapIMAP4rev1: {}}
	with := func(caps ...imap.Cap) imap.CapSet {
		set := imap.CapSet{imap.CapIMAP4rev1: {}}
		for _, c := range caps {
			set[c] = struct{}{}
		}
		return set
	}
	cases := []struct {
		name string
		caps imap.CapSet
		want Capabilities
	}{
		{"plain", rev1, Capabilities{}},
		{"idle", with(imap.CapIdle), Capabilities{IDLE: true}},
		{"move", with(imap.CapMove), Capabilities{Move: true}},
		{"uidplus", with(imap.CapUIDPlus), Capabilities{UIDPlus: true}},
		{"list", with(imap.CapListExtended, imap.CapListStatus, imap.CapSpecialUse),
			Capabilities{ListExtended: true, ListStatus: true, SpecialUse: true}},
		{"esearch", with(imap.CapESearch), Capabilities{ESearch: true}},
		{"utf8", with(imap.CapUTF8Accept), Capabilities{UTF8: true}},
		{"utf8 only", with(imap.CapUTF8Only), Capabilities{UTF8: true}},
		{"sort", with(imap.CapSort, imap.Cap("THREAD")), Capabilities{Sort: true, Thread: true}},
		// IMAP4rev2 makes IDLE, MOVE, UIDPLUS, LIST-EXTENDED,
		// LIST-STATUS and ESEARCH mandatory. SPECIAL-USE, UTF-8, SORT
		// and THREAD are not part of that set.
		{"imap4rev2", imap.CapSet{imap.CapIMAP4rev2: {}}, Capabilities{
			IDLE: true, Move: true, UIDPlus: true,
			ListExtended: true, ListStatus: true, ESearch: true,
		}},
		{"everything", with(
			imap.CapIdle, imap.CapMove, imap.CapUIDPlus, imap.CapListExtended,
			imap.CapListStatus, imap.CapSpecialUse, imap.CapUTF8Accept,
			imap.CapSort, imap.Cap("THREAD"), imap.CapESearch,
		), Capabilities{
			IDLE: true, Move: true, UIDPlus: true, ListExtended: true,
			ListStatus: true, SpecialUse: true, UTF8: true, Sort: true,
			Thread: true, ESearch: true,
		}},
	}
	for _, c := range cases {
		if got := capabilitiesOf(c.caps); got != c.want {
			t.Errorf("%s: capabilities = %+v, want %+v", c.name, got, c.want)
		}
	}
}
