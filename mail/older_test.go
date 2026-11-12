package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// numbered is message i of a long folder: dated i minutes into 2026, from
// one of seven senders; every tenth answers the one before it.
func numbered(i int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: Sender %d <sender%d@example.com>\r\n", i%7, i%7)
	b.WriteString("To: Ann <ann@example.com>\r\n")
	fmt.Fprintf(&b, "Subject: Message %d\r\n", i)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC).Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <n%d@example.com>\r\n", i)
	if i%10 == 0 {
		fmt.Fprintf(&b, "In-Reply-To: <n%d@example.com>\r\nReferences: <n%d@example.com>\r\n", i-1, i-1)
	}
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&b, "The body of message %d.\r\n", i)
	return []byte(b.String())
}

// fill appends messages from-to to a mailbox: every third seen, every
// eleventh flagged, the others unread.
func fill(t *testing.T, f *fakeIMAP, mailbox string, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		var flags []imap.Flag
		if i%3 == 0 {
			flags = append(flags, imap.FlagSeen)
		}
		if i%11 == 0 {
			flags = append(flags, imap.FlagFlagged)
		}
		raw := numbered(i)
		if _, err := f.user.Append(mailbox, appendLiteral{bytes.NewReader(raw)}, &imap.AppendOptions{Time: time.Unix(1_700_000_000, 0), Flags: flags}); err != nil {
			t.Fatal(err)
		}
	}
}

// cachedUIDs is the UIDs the cache holds of a folder, oldest first.
func cachedUIDs(store *memStore, accountID, folder string) []UID {
	store.mu.Lock()
	defer store.mu.Unlock()
	var out []UID
	for uid := range store.messages[msgKey(accountID, folder)] {
		out = append(out, uid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uidRange(from, to UID) []UID {
	var out []UID
	for uid := from; uid <= to; uid++ {
		out = append(out, uid)
	}
	return out
}

// olderService is a service with the account synced and its first-sync
// window set.
func olderService(t *testing.T, f *fakeIMAP, window int) (*Service, *memStore, Account) {
	t.Helper()
	ctx := context.Background()
	svc, store, a := newIMAPService(t, f, "secret")
	set, err := svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.SyncWindow = window
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	return svc, store, a
}

// Fetching older mail pages back from the window a sync cached to the
// folder's first message, a part at a time, newest first. It reads the
// folder read-only and marks nothing; the sync cursor and the watermark of
// announced mail stay, and none of the older mail is announced. The
// server's answer is read as ranges where it has ESEARCH, as a list where
// it does not.
func TestFetchOlderPagesBackToTheOldest(t *testing.T) {
	for _, esearch := range []bool{false, true} {
		t.Run(fmt.Sprintf("esearch=%v", esearch), func(t *testing.T) {
			ctx := context.Background()
			caps := allCaps()
			if esearch {
				caps[imap.CapESearch] = struct{}{}
			}
			f := startFakeIMAP(t, caps)
			f.create(t, "INBOX", "Archive")
			fill(t, f, "INBOX", 1, 130)
			fill(t, f, "Archive", 1, 5)
			svc, store, a := olderService(t, f, 50)
			for _, folder := range []string{"INBOX", "Archive"} {
				if err := svc.SyncFolder(ctx, a.ID, folder, 0); err != nil {
					t.Fatal(err)
				}
			}
			if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, uidRange(81, 130)) {
				t.Fatalf("the first sync cached %v", got)
			}
			if cache, err := svc.FolderCache(ctx, a.ID, "INBOX"); err != nil || cache != (FolderCache{Cached: 50, Total: 130}) {
				t.Fatalf("the cache of the inbox = %+v, %v", cache, err)
			}
			// A folder the first sync read whole has nothing older.
			if cache, _ := svc.FolderCache(ctx, a.ID, "Archive"); !cache.Complete || cache.Cached != 5 {
				t.Errorf("the cache of the small folder = %+v", cache)
			}
			if got, err := svc.FetchOlder(ctx, a.ID, "Archive", 10); err != nil || got != (OlderResult{}) {
				t.Errorf("older mail of the small folder = %+v, %v", got, err)
			}
			// The watcher has looked at the inbox: what is there is not news.
			if got, err := store.TakeNew(ctx, a.ID, "INBOX"); err != nil || len(got) != 0 {
				t.Fatalf("the first look announced %v, %v", got, err)
			}
			cursor, _, _ := store.Folder(ctx, a.ID, "INBOX")
			mark := store.marks[msgKey(a.ID, "INBOX")]
			flags := serverFlags(t, f, "INBOX")
			selects := f.selectCount()

			for _, step := range []struct {
				want   OlderResult
				cached []UID
			}{
				{OlderResult{Fetched: 30, More: true}, uidRange(51, 130)},
				{OlderResult{Fetched: 30, More: true}, uidRange(21, 130)},
				{OlderResult{Fetched: 20}, uidRange(1, 130)},
				{OlderResult{}, uidRange(1, 130)},
			} {
				got, err := svc.FetchOlder(ctx, a.ID, "INBOX", 30)
				if err != nil {
					t.Fatal(err)
				}
				if got != step.want {
					t.Errorf("FetchOlder = %+v, want %+v", got, step.want)
				}
				if cached := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(cached, step.cached) {
					t.Errorf("cached after fetching older mail: %d messages, %v...", len(cached), cached[:min(3, len(cached))])
				}
				now, _, _ := store.Folder(ctx, a.ID, "INBOX")
				if now.LastUID != cursor.LastUID || now.UIDValidity != cursor.UIDValidity || now.Total != cursor.Total {
					t.Errorf("the sync cursor moved: %+v, was %+v", now, cursor)
				}
				if store.marks[msgKey(a.ID, "INBOX")] != mark {
					t.Errorf("the watermark moved: %+v, was %+v", store.marks[msgKey(a.ID, "INBOX")], mark)
				}
				if news, err := store.TakeNew(ctx, a.ID, "INBOX"); err != nil || len(news) != 0 {
					t.Errorf("older mail was announced: %v, %v", subjects(news), err)
				}
			}
			if cache, _ := svc.FolderCache(ctx, a.ID, "INBOX"); cache != (FolderCache{Cached: 130, Total: 130, Complete: true}) {
				t.Errorf("the cache of the inbox at the end = %+v", cache)
			}
			if got := serverFlags(t, f, "INBOX"); !reflect.DeepEqual(got, flags) {
				t.Errorf("fetching older mail changed flags on the server:\n got %v\nwant %v", got, flags)
			}
			for _, sel := range f.selectsSince(selects) {
				if sel.mailbox == "INBOX" && !sel.readOnly {
					t.Error("fetching older mail opened the inbox read-write")
				}
			}
			// They are fetched as a sync fetches them: header, flags,
			// preview and the headers that thread them.
			old, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 10)
			if old.Subject != "Message 10" || old.InReplyTo != "n9@example.com" || !strings.Contains(old.Preview, "message 10") ||
				old.Seen || old.From.Email != "sender3@example.com" {
				t.Errorf("an older message = %+v", old.Summary)
			}
			if seen, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 33); !seen.Seen || !seen.Flagged {
				t.Errorf("an older message lost its flags: %+v", seen.Summary)
			}
			convs, err := svc.ConversationPage(ctx, ListQuery{Scope: AccountScope(a.ID, "INBOX"), Limit: 200})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range convs.Conversations {
				if c.Latest.UID == 10 && c.Count != 2 {
					t.Errorf("an older reply is in a conversation of %d", c.Count)
				}
			}
			if len(convs.Conversations) != 130-13 {
				t.Errorf("130 messages, 13 of them replies, are %d conversations", len(convs.Conversations))
			}
		})
	}
}

// expungeElsewhere removes messages of a mailbox as another mail client
// would.
func expungeElsewhere(t *testing.T, f *fakeIMAP, mailbox string, uids ...UID) {
	t.Helper()
	c := otherClient(t, f)
	defer c.Close()
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		t.Fatal(err)
	}
	var set imap.UIDSet
	for _, uid := range uids {
		set.AddNum(imap.UID(uid))
	}
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		t.Fatal(err)
	}
}

// Gaps in the UIDs, of messages the server no longer has and of messages
// a sync left out, are no matter: older mail is what the server has and
// the cache lacks, and what a sync left out comes first. A fetch of many
// is made in parts.
func TestFetchOlderFillsGaps(t *testing.T) {
	defer func(n int) { fetchChunk = n }(fetchChunk)
	fetchChunk = 7
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 60)
	expungeElsewhere(t, f, "INBOX", append(uidRange(5, 14), uidRange(30, 33)...)...)
	svc, store, a := olderService(t, f, 50)
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 20); err != nil {
		t.Fatal(err)
	}
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, uidRange(41, 60)) {
		t.Fatalf("the first sync cached %v", got)
	}
	// Thirty arrive, and a sync that keeps ten leaves twenty out.
	fill(t, f, "INBOX", 61, 90)
	acc, password, err := svc.account(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.dialIMAP(ctx, acc, password)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := c.client.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.syncSelected(ctx, c, a.ID, "INBOX", selected.UIDValidity, permanentFlags(selected), syncSpan{first: 20, refresh: 20, keep: 10}); err != nil {
		t.Fatal(err)
	}
	c.close()
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, append(uidRange(41, 60), uidRange(81, 90)...)) {
		t.Fatalf("the sync that kept ten cached %v", got)
	}
	if cache, _ := svc.FolderCache(ctx, a.ID, "INBOX"); cache.Complete {
		t.Error("a sync that left mail out made the cache complete")
	}
	// The gap the sync left comes first, newest first.
	got, err := svc.FetchOlder(ctx, a.ID, "INBOX", 15)
	if err != nil || got != (OlderResult{Fetched: 15, More: true}) {
		t.Fatalf("FetchOlder = %+v, %v", got, err)
	}
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, append(uidRange(41, 60), uidRange(66, 90)...)) {
		t.Errorf("after the first fetch the cache holds %v", got)
	}
	// Then the rest of it, and what is older, past the server's gaps.
	got, err = svc.FetchOlder(ctx, a.ID, "INBOX", 100)
	if err != nil || got != (OlderResult{Fetched: 5 + 40 - 14}) {
		t.Fatalf("FetchOlder = %+v, %v", got, err)
	}
	server := append(append(uidRange(1, 4), uidRange(15, 29)...), uidRange(34, 90)...)
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, server) {
		t.Errorf("the cache holds %v, want what the server has: %v", got, server)
	}
	if cache, _ := svc.FolderCache(ctx, a.ID, "INBOX"); !cache.Complete {
		t.Error("the cache of every message of the folder is not complete")
	}
	// Every part came with its previews.
	for _, uid := range []UID{1, 15, 40, 66} {
		if m, _, _ := store.GetMessage(ctx, a.ID, "INBOX", uid); !strings.Contains(m.Preview, fmt.Sprintf("message %d.", uid)) {
			t.Errorf("message %d has the preview %q", uid, m.Preview)
		}
	}
}

// A folder the server gave a new UIDVALIDITY is synced anew instead, as a
// sync would, and so is a folder never synced: their lists start again
// from the top.
func TestFetchOlderSyncsAResetFolder(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive")
	fill(t, f, "INBOX", 1, 70)
	fill(t, f, "Archive", 1, 3)
	svc, store, a := olderService(t, f, 50)
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	before, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if err := f.user.Delete("INBOX"); err != nil {
		t.Fatal(err)
	}
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 201, 203)
	got, err := svc.FetchOlder(ctx, a.ID, "INBOX", 30)
	if err != nil || got != (OlderResult{Fetched: 3, Reset: true}) {
		t.Fatalf("FetchOlder of a reset folder = %+v, %v", got, err)
	}
	after, _, _ := store.Folder(ctx, a.ID, "INBOX")
	if after.UIDValidity == before.UIDValidity || after.LastUID != 3 || !after.Complete {
		t.Errorf("the folder after the reset = %+v, was %+v", after, before)
	}
	msgs, _ := svc.CachedMessages(ctx, a.ID, "INBOX", "", 50)
	if len(msgs) != 3 || msgs[0].Subject != "Message 203" {
		t.Errorf("the cache after the reset = %v", subjects(msgs))
	}
	// Never synced: it is, from its newest messages.
	got, err = svc.FetchOlder(ctx, a.ID, "Archive", 30)
	if err != nil || got != (OlderResult{Fetched: 3, Reset: true}) {
		t.Errorf("FetchOlder of a folder never synced = %+v, %v", got, err)
	}
}

// A sync that resets the folder while older mail is fetched wins: what
// was fetched under the old UIDVALIDITY is dropped, and the list starts
// again from the top.
func TestFetchOlderLosesToAReset(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 60)
	svc, store, a := olderService(t, f, 50)
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	store.afterUIDs = func() {
		// What a sync of a folder with a new UIDVALIDITY writes.
		if err := store.ApplySync(ctx, a.ID, SyncBatch{Reset: true, Folder: Folder{
			Name: "INBOX", UIDValidity: 999, LastUID: 2, Total: 2, Complete: true,
		}, Messages: []Message{{Summary: Summary{UID: 1, Subject: "New 1"}}, {Summary: Summary{UID: 2, Subject: "New 2"}}}}); err != nil {
			t.Error(err)
		}
	}
	got, err := svc.FetchOlder(ctx, a.ID, "INBOX", 30)
	if err != nil || got != (OlderResult{Reset: true}) {
		t.Fatalf("FetchOlder across a reset = %+v, %v", got, err)
	}
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, []UID{1, 2}) || store.olderWrites != 0 {
		t.Errorf("the cache after the reset holds %v, with %d writes of older mail", got, store.olderWrites)
	}
	if m, _, _ := store.GetMessage(ctx, a.ID, "INBOX", 1); m.Subject != "New 1" {
		t.Errorf("older mail of the old UIDVALIDITY was written over the new: %+v", m.Summary)
	}
}

// What fails says so and changes nothing.
func TestFetchOlderFailures(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 60)
	svc, store, a := olderService(t, f, 50)
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.FetchOlder(ctx, "nobody", "INBOX", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("older mail of no account = %v", err)
	}
	if _, err := svc.FetchOlder(ctx, a.ID, "Nowhere", 10); err == nil {
		t.Error("older mail of a folder the server does not have was fetched")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.FetchOlder(cancelled, a.ID, "INBOX", 10); err == nil {
		t.Error("a cancelled fetch of older mail worked")
	}
	f.stop()
	if _, err := svc.FetchOlder(ctx, a.ID, "INBOX", 10); err == nil {
		t.Error("older mail was fetched from a server that is gone")
	}
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, uidRange(11, 60)) || store.olderWrites != 0 {
		t.Errorf("failed fetches changed the cache: %v, %d writes", got, store.olderWrites)
	}
}

// The first sync of a folder caches the window the settings say, the
// window's and the watcher's, and the settings take no other.
func TestSyncWindowSetting(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX", "Archive", "Lists")
	fill(t, f, "INBOX", 1, 260)
	fill(t, f, "Archive", 1, 60)
	fill(t, f, "Lists", 1, 60)
	svc, store, a := olderService(t, f, 200)
	if set, _ := svc.Settings(ctx); set.SyncWindow != 200 {
		t.Fatalf("the window = %d", set.SyncWindow)
	}
	if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
		t.Fatal(err)
	}
	if got := cachedUIDs(store, a.ID, "INBOX"); !slices.Equal(got, uidRange(61, 260)) {
		t.Errorf("a window of 200 cached %d messages", len(got))
	}
	set, _ := svc.Settings(ctx)
	set.SyncWindow = 50
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Archive", 0); err != nil {
		t.Fatal(err)
	}
	if got := cachedUIDs(store, a.ID, "Archive"); !slices.Equal(got, uidRange(11, 60)) {
		t.Errorf("a window of 50 cached %d messages", len(got))
	}
	// A caller that names a window has it.
	if err := svc.SyncFolder(ctx, a.ID, "Lists", 25); err != nil {
		t.Fatal(err)
	}
	if got := cachedUIDs(store, a.ID, "Lists"); len(got) != 25 {
		t.Errorf("a window of 25 cached %d messages", len(got))
	}
	for _, offered := range SyncWindows {
		set.SyncWindow = offered
		if err := svc.SaveSettings(ctx, set); err != nil {
			t.Fatal(err)
		}
		if got, _ := svc.Settings(ctx); got.SyncWindow != offered {
			t.Errorf("the window %d came back as %d", offered, got.SyncWindow)
		}
	}
	set.SyncWindow = 123
	if err := svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); got.SyncWindow != DefaultSyncWindow {
		t.Errorf("a window nobody offered came back as %d", got.SyncWindow)
	}
	if err := store.SetSetting(ctx, settingSyncWindow, "lots"); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Settings(ctx); got.SyncWindow != DefaultSyncWindow {
		t.Errorf("a stored window that is no number came back as %d", got.SyncWindow)
	}
}

// The watcher's first check caches the settings' window. Older mail then
// fetched is not news: the checks after it announce nothing of it, and
// announce what arrives.
func TestWatchAnnouncesNoOlderMail(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 80)
	r := newWatchRig(t, f, "secret", false)
	set := DefaultSettings()
	set.SyncWindow = 50
	if err := r.svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	r.watch()
	if first := r.checked(); len(first.New) != 0 {
		t.Fatalf("the first check announced %v", subjects(first.New))
	}
	if got := cachedUIDs(r.store, r.account.ID, "INBOX"); !slices.Equal(got, uidRange(31, 80)) {
		t.Fatalf("the first check cached %d messages, want the window of 50", len(got))
	}
	r.timer()
	got, err := r.svc.FetchOlder(ctx, r.account.ID, "INBOX", 40)
	if err != nil || got != (OlderResult{Fetched: 30}) {
		t.Fatalf("FetchOlder = %+v, %v", got, err)
	}
	r.svc.Nudge()
	if ev := r.checked(); len(ev.New) != 0 {
		t.Errorf("the check after older mail announced %v", subjects(ev.New))
	}
	r.timer()
	deliver(t, f, "INBOX", numbered(500))
	if ev := r.checked(); !slices.Equal(subjects(ev.New), []string{"Message 500"}) {
		t.Errorf("new mail announced = %v", subjects(ev.New))
	}
}

// The watcher's first check caches as many of the inbox's newest
// messages as the settings say: here more than the default, and more
// than it refreshes.
func TestWatchFirstCheckCachesTheWindow(t *testing.T) {
	ctx := context.Background()
	f := startFakeIMAP(t, allCaps())
	f.create(t, "INBOX")
	fill(t, f, "INBOX", 1, 260)
	r := newWatchRig(t, f, "secret", false)
	set := DefaultSettings()
	set.SyncWindow = 1000
	if err := r.svc.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	r.watch()
	if first := r.checked(); len(first.New) != 0 {
		t.Fatalf("the first check announced %d messages", len(first.New))
	}
	if got := cachedUIDs(r.store, r.account.ID, "INBOX"); !slices.Equal(got, uidRange(1, 260)) {
		t.Errorf("the first check cached %d messages, want all 260", len(got))
	}
	if cache, err := r.svc.FolderCache(ctx, r.account.ID, "INBOX"); err != nil || !cache.Complete {
		t.Errorf("the inbox read whole = %+v, %v", cache, err)
	}
}

// The unified inbox lists the inboxes of every account, each row with its
// account; starring or archiving a row acts on that account's server and
// cache alone, though the other account has a message of the same UID.
func TestUnifiedInboxActsOnTheRowsAccount(t *testing.T) {
	ctx := context.Background()
	f1 := startFakeIMAP(t, allCaps())
	f1.create(t, "INBOX", "Archive")
	fill(t, f1, "INBOX", 1, 2)
	f2 := startFakeIMAP(t, allCaps())
	f2.create(t, "INBOX", "Archive")
	fill(t, f2, "INBOX", 7, 8)
	svc, store, a1 := newIMAPService(t, f1, "secret")
	a2 := fakeAccount(t, f2.addr)
	a2.ID, a2.Name, a2.Email = "a2", "Work", "ann@work.example"
	if err := svc.AddAccount(ctx, a2, "secret"); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Account{a1, a2} {
		if _, err := svc.SyncAccount(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := svc.SyncFolder(ctx, a.ID, "INBOX", 0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := svc.MessagePage(ctx, ListQuery{Scope: AllInboxes()})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, m := range page.Messages {
		rows = append(rows, fmt.Sprintf("%s:%s/%d:%s", m.Account, m.Folder, m.UID, m.Subject))
	}
	if want := []string{"a2:INBOX/2:Message 8", "a2:INBOX/1:Message 7", "a1:INBOX/2:Message 2", "a1:INBOX/1:Message 1"}; !slices.Equal(rows, want) {
		t.Fatalf("every inbox = %v, want %v", rows, want)
	}
	inboxes, err := svc.Inboxes(ctx)
	if err != nil || len(inboxes) != 2 {
		t.Fatalf("inboxes = %+v, %v", inboxes, err)
	}
	for _, in := range inboxes {
		if in.Name != "INBOX" || in.Unread != 2 || in.Total != 2 {
			t.Errorf("the inbox of %s = %+v", in.AccountID, in.Folder)
		}
	}

	row := page.Messages[1]
	before := serverFlags(t, f1, "INBOX")
	ref := []MessageRef{{Folder: row.Folder, UID: row.UID}}
	if err := svc.SetFlags(ctx, row.Account, ref, FlagFlagged, true); err != nil {
		t.Fatal(err)
	}
	if got := serverFlags(t, f2, "INBOX")[1]; !slices.Contains(got, string(imap.FlagFlagged)) {
		t.Errorf("the row's message on its server has %v", got)
	}
	if got := serverFlags(t, f1, "INBOX"); !reflect.DeepEqual(got, before) {
		t.Errorf("the other account's server changed: %v, was %v", got, before)
	}
	if m, _, _ := store.GetMessage(ctx, a1.ID, "INBOX", 1); m.Flagged {
		t.Error("the other account's cached message was starred")
	}
	if _, err := svc.MoveMessages(ctx, row.Account, ref, "Archive"); err != nil {
		t.Fatal(err)
	}
	if got := serverFlags(t, f2, "INBOX"); len(got) != 1 {
		t.Errorf("the row's inbox holds %v after archiving it", got)
	}
	if got := serverFlags(t, f1, "INBOX"); len(got) != 2 {
		t.Errorf("archiving moved the other account's mail: %v", got)
	}
	page, err = svc.MessagePage(ctx, ListQuery{Scope: AllInboxes()})
	if err != nil || len(page.Messages) != 3 {
		t.Errorf("every inbox after archiving = %+v, %v", page.Messages, err)
	}
	convs, err := svc.ConversationPage(ctx, ListQuery{Scope: AllInboxes()})
	if err != nil || len(convs.Conversations) != 3 || convs.Conversations[0].Account != "a2" {
		t.Errorf("every inbox's conversations = %+v, %v", convs.Conversations, err)
	}
}

// The messages of a conversation in a scope are what its row stands for:
// those of one account's folder of the scope's special use, or its
// starred, or all; an account the scope leaves out has none.
func TestScopedConversation(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := newTestService()
	a := testAccount()
	if err := svc.AddAccount(ctx, a, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFolders(ctx, a.ID, []Folder{{Name: "INBOX", SpecialUse: SpecialInbox}, {Name: "Sent Items", SpecialUse: SpecialSent}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	for _, m := range []struct {
		folder string
		msg    Message
	}{
		{"INBOX", Message{Summary: Summary{UID: 1, MessageID: "root@x", Subject: "Plan", Date: at}}},
		{"Sent Items", Message{Summary: Summary{UID: 1, MessageID: "re@x", InReplyTo: "root@x", Subject: "Re: Plan", Date: at.Add(time.Hour), Flagged: true}}},
		{"INBOX", Message{Summary: Summary{UID: 2, MessageID: "re2@x", InReplyTo: "re@x", Subject: "Re: Plan", Date: at.Add(2 * time.Hour)}}},
	} {
		if err := store.SaveMessage(ctx, a.ID, m.folder, m.msg); err != nil {
			t.Fatal(err)
		}
	}
	convs, err := svc.ConversationPage(ctx, ListQuery{Scope: AllInboxes()})
	if err != nil || len(convs.Conversations) != 1 || convs.Conversations[0].Count != 2 {
		t.Fatalf("every inbox = %+v, %v", convs.Conversations, err)
	}
	thread := convs.Conversations[0].Thread
	for _, tc := range []struct {
		scope Scope
		want  []string
	}{
		{AllInboxes(), []string{"INBOX/1", "INBOX/2"}},
		{Scope{Special: SpecialSent}, []string{"Sent Items/1"}},
		{Scope{Starred: true}, []string{"Sent Items/1"}},
		{AccountScope(a.ID, "INBOX"), []string{"INBOX/1", "INBOX/2"}},
		{Scope{}, []string{"INBOX/1", "Sent Items/1", "INBOX/2"}},
		{Scope{Accounts: []string{"other"}}, nil},
	} {
		msgs, err := svc.ScopedConversation(ctx, a.ID, tc.scope, thread)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range msgs {
			got = append(got, fmt.Sprintf("%s/%d", m.Folder, m.UID))
			if m.Account != a.ID {
				t.Errorf("a message without its account: %+v", m)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("in %+v: %v, want %v", tc.scope, got, tc.want)
		}
	}
	if _, err := svc.ScopedConversation(ctx, a.ID, Scope{Folder: "INBOX", Starred: true}, thread); !errors.Is(err, ErrBadScope) {
		t.Errorf("a scope narrowed twice = %v", err)
	}
}
