package mail

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The Thread-Index of the Outlook fixtures: the conversation's head, and
// the head with one and two replies' blocks.
const (
	outlookHead   = "AdpPK5wxfA5aHZOyT2qOPBXXqStOYA=="
	outlookReply1 = "AdpPK5wxfA5aHZOyT2qOPBXXqStOYAAASxwg"
	outlookReply2 = "AdpPK5wxfA5aHZOyT2qOPBXXqStOYAAASxwgAAEabkg="
)

func TestThreadKeys(t *testing.T) {
	long := strings.Repeat("a", 300) + "@example.com"
	for _, tc := range []struct {
		name                 string
		messageID, inReplyTo string
		references           []string
		want                 []string
	}{
		{"own id only", "<a@example.com>", "", nil, []string{"a@example.com"}},
		{"brackets and space go", "  < a@example.com >  ", "", nil, []string{"a@example.com"}},
		{"the domain compares without case", "Local@Example.COM", "", nil, []string{"Local@example.com"}},
		{"the local part keeps its case", "ABC@example.com", "abc@example.com", nil,
			[]string{"ABC@example.com", "abc@example.com"}},
		{"reply and references", "c@x.org", "<b@x.org>", []string{"a@x.org", "b@x.org"},
			[]string{"c@x.org", "b@x.org", "a@x.org"}},
		{"an id repeated counts once", "a@x.org", "a@X.ORG", []string{"<a@x.org>", "a@x.org"}, []string{"a@x.org"}},
		{"malformed ids are left out", "no-at-sign", "<>",
			[]string{"", "@example.com", "garbage@", "a b@example.com", "a<b@example.com", long, "ok@example.com"},
			[]string{"ok@example.com"}},
		{"no headers, no keys", "", "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ThreadKeys(tc.messageID, tc.inReplyTo, tc.references, ""); !slices.Equal(got, tc.want) {
				t.Errorf("keys = %q, want %q", got, tc.want)
			}
		})
	}
}

// A long References keeps its first entry, where the conversation began,
// and its last ones, the nearest parents.
func TestThreadKeysCapReferences(t *testing.T) {
	var refs []string
	for i := range 150 {
		refs = append(refs, fmt.Sprintf("r%d@example.com", i))
	}
	keys := ThreadKeys("own@example.com", "", refs, "")
	if len(keys) != 1+maxReferences {
		t.Fatalf("keys = %d, want %d", len(keys), 1+maxReferences)
	}
	for i, ref := range refs {
		want := i == 0 || i >= len(refs)-(maxReferences-1)
		if got := slices.Contains(keys, ref); got != want {
			t.Errorf("reference %d kept = %v, want %v", i, got, want)
		}
	}
}

func TestThreadIndexKey(t *testing.T) {
	key := ThreadKeys("", "", nil, outlookHead)
	if len(key) != 1 || !strings.HasPrefix(key[0], "thread-index:") || strings.Contains(key[0], "@") {
		t.Fatalf("key = %q", key)
	}
	// Every reply extends the head; the conversation is the head.
	for _, value := range []string{outlookReply1, outlookReply2, " AdpPK5wxfA5aHZOyT2qO\r\n PBXXqStOYA== ", strings.TrimRight(outlookHead, "=")} {
		if got := ThreadKeys("", "", nil, value); !slices.Equal(got, key) {
			t.Errorf("Thread-Index %q gives %q, want %q", value, got, key)
		}
	}
	for _, value := range []string{
		"not base64 at all!",
		"AdpPK5wxfA5aHZOyT2qOPBXXqStO",             // 21 bytes: too short
		"AdpPK5wxfA5aHZOyT2qOPBXXqStOYAA=",         // 23 bytes: no whole reply block
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",         // a head of zeros
		strings.Repeat("A", maxThreadIndex+4) + "", // absurdly long
	} {
		if got := ThreadKeys("", "", nil, value); len(got) != 0 {
			t.Errorf("Thread-Index %q gives %q, want none", value, got)
		}
	}
	if cleanThreadIndex("garbage") != "" || cleanThreadIndex(" "+outlookReply1+"\r\n") != outlookReply1 {
		t.Error("cleanThreadIndex keeps garbage or loses a valid value")
	}
}

func TestMsgIDs(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  []string
	}{
		{"<a@b.org> (a comment) <c@d.org>", []string{"a@b.org", "c@d.org"}},
		{"<<<>>> <no-at> <x@y.org> <x@Y.ORG>", []string{"x@y.org"}},
		{"<a <b@c.org>", []string{"b@c.org"}},
		{"words only, no ids", nil},
		{"<unterminated@x.org", nil},
	} {
		if got := msgIDs(tc.value); !slices.Equal(got, tc.want) {
			t.Errorf("msgIDs(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestParseThreadingHeaders(t *testing.T) {
	broken, err := ParseMessage(readFixture(t, "threads/broken-references.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if broken.InReplyTo != "broken-root.4400@example.com" {
		t.Errorf("in reply to = %q", broken.InReplyTo)
	}
	if !slices.Equal(broken.References, []string{"broken-root.4400@example.com"}) {
		t.Errorf("references = %q, want the one valid id once", broken.References)
	}
	outlook, err := ParseMessage(readFixture(t, "threads/outlook-3.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if outlook.ThreadIndex != outlookReply2 || len(outlook.References) != 0 || !strings.HasPrefix(outlook.InReplyTo, "BY5PR11MB4056") {
		t.Errorf("outlook reply = index %q, references %q, in reply to %q", outlook.ThreadIndex, outlook.References, outlook.InReplyTo)
	}
	anonymous, err := ParseMessage(readFixture(t, "threads/no-message-id.eml"))
	if err != nil {
		t.Fatal(err)
	}
	if keys := ThreadKeys(anonymous.MessageID, anonymous.InReplyTo, anonymous.References, anonymous.ThreadIndex); len(keys) != 0 {
		t.Errorf("a message without headers has keys %q", keys)
	}
}
