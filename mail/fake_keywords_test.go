package mail

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
)

// fakeKeywords is how a mailbox of the fake server keeps keywords, which
// the in-memory server keeps everywhere: its PERMANENTFLAGS name the
// system's flags, the keywords listed and, with any, \*. A STORE of a
// keyword it does not keep, or of any keyword when it refuses, is
// answered NO, as a server that keeps no such keyword may answer.
type fakeKeywords struct {
	listed []string
	any    bool
	refuse bool
}

// keepsNone is a mailbox that keeps no keyword, as Outlook's do.
var keepsNone = fakeKeywords{}

func (k fakeKeywords) permanentFlags() []imap.Flag {
	out := []imap.Flag{imap.FlagSeen, imap.FlagAnswered, imap.FlagFlagged, imap.FlagDeleted, imap.FlagDraft}
	for _, kw := range k.listed {
		out = append(out, imap.Flag(kw))
	}
	if k.any {
		out = append(out, imap.FlagWildcard)
	}
	return out
}

// refuses reports whether a STORE of flags is answered NO.
func (k fakeKeywords) refuses(flags []imap.Flag) bool {
	for _, f := range flags {
		if strings.HasPrefix(string(f), `\`) {
			continue
		}
		if k.refuse || !k.any && !slices.ContainsFunc(k.listed, func(kw string) bool { return strings.EqualFold(kw, string(f)) }) {
			return true
		}
	}
	return false
}

// fakeStore is a STORE the fake server received.
type fakeStore struct {
	mailbox string
	op      imap.StoreFlagsOp
	flags   []imap.Flag
}

func (s *fakeSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.f.mu.Lock()
	s.f.stores = append(s.f.stores, fakeStore{s.selected, flags.Op, slices.Clone(flags.Flags)})
	rule, ruled := s.f.keywords[s.selected]
	s.f.mu.Unlock()
	if ruled && rule.refuses(flags.Flags) {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "This mailbox keeps no such keyword"}
	}
	return s.SessionIMAP4rev2.Store(w, numSet, flags, options)
}

// keepKeywords has a mailbox keep keywords as k says.
func (f *fakeIMAP) keepKeywords(mailbox string, k fakeKeywords) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keywords == nil {
		f.keywords = map[string]fakeKeywords{}
	}
	f.keywords[mailbox] = k
}

// storesSince returns the STOREs the fake server received after the
// first n.
func (f *fakeIMAP) storesSince(n int) []fakeStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.stores[n:])
}

func (f *fakeIMAP) storeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stores)
}

// appendFlagged appends a message that has flags already, as one another
// mail client labelled.
func (f *fakeIMAP) appendFlagged(t *testing.T, mailbox string, raw []byte, flags ...imap.Flag) {
	t.Helper()
	if _, err := f.user.Append(mailbox, appendLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{
		Time: time.Unix(1_700_000_000, 0), Flags: flags,
	}); err != nil {
		t.Fatalf("append %s: %v", mailbox, err)
	}
}

// unflag takes a flag off a message, as another mail client does.
func (f *fakeIMAP) unflag(t *testing.T, mailbox string, uid UID, flag imap.Flag) {
	t.Helper()
	c := f.session(t)
	defer c.close()
	if _, err := c.client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var set imap.UIDSet
	set.AddNum(imap.UID(uid))
	if err := c.client.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: []imap.Flag{flag}}, nil).Close(); err != nil {
		t.Fatalf("unflag %s/%d: %v", mailbox, uid, err)
	}
}

// keywordsOn reads the keywords of a mailbox's messages on the fake
// server, by subject, in lower case and sorted, as the server keeps them.
func keywordsOn(t *testing.T, f *fakeIMAP, mailbox string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, m := range f.messages(t, mailbox) {
		kws := []string{}
		for _, flag := range m.Flags {
			if !strings.HasPrefix(string(flag), `\`) {
				kws = append(kws, strings.ToLower(string(flag)))
			}
		}
		slices.Sort(kws)
		out[m.Subject] = kws
	}
	return out
}
