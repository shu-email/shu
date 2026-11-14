package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"

	"shu/internal/sqlitetest"
	"shu/mail"
)

var labelBase = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// labelled is a message of the tests of labels, minutes after labelBase,
// unread, with keywords as a fetch read them (nil for none read).
func labelled(uid mail.UID, id, subject string, minutes int, keywords ...string) mail.Message {
	return mail.Message{
		Summary: mail.Summary{UID: uid, MessageID: id, Subject: subject, Date: labelBase.Add(time.Duration(minutes) * time.Minute),
			From: mail.Address{Name: "Maya", Email: "maya@example.org"}},
		Keywords: keywords,
	}
}

// labelNames writes the labels of a message by name, "(here)" after
// those kept on this device only.
func labelNames(t *testing.T, s *Store, accountID string, m mail.Summary) []string {
	t.Helper()
	labels, err := s.Labels(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[mail.LabelID]string{}
	for _, l := range labels {
		names[l.ID] = l.Name
	}
	out := []string{}
	for _, l := range m.Labels {
		name := names[l.ID]
		if l.Local {
			name += " (here)"
		}
		out = append(out, name)
	}
	return out
}

func labelsOn(t *testing.T, s *Store, accountID, folder string, uid mail.UID) []string {
	t.Helper()
	m, ok, err := s.GetMessage(context.Background(), accountID, folder, uid)
	if err != nil || !ok {
		t.Fatalf("message %s/%d: %v, %v", folder, uid, ok, err)
	}
	return labelNames(t, s, accountID, m.Summary)
}

func labelOf(t *testing.T, s *Store, accountID, name string) mail.Label {
	t.Helper()
	labels, err := s.Labels(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range labels {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no label %q among %+v", name, labels)
	return mail.Label{}
}

func namesOf(labels []mail.Label) []string {
	out := []string{}
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

// Labels are an account's, in the order they were made, by a name one
// whatever its case and a keyword one whatever its case; a rename keeps
// the keyword; they go with their account, from its messages too.
func TestLabelsOfAnAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	addFolders(t, s, "a1", "INBOX")
	work, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if work.ID == 0 || work.Color != mail.LabelColorAt(0) || work.Local || work.Unread != 0 {
		t.Errorf("made %+v", work)
	}
	for _, name := range []string{"WORK", "work"} {
		if _, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: name, Keyword: mail.LabelKeyword(name)}); !errors.Is(err, mail.ErrLabelExists) {
			t.Errorf("%s = %v", name, err)
		}
	}
	argh, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Ärger", Keyword: mail.LabelKeyword("Ärger"), Color: mail.LabelPink})
	if err != nil || argh.Color != mail.LabelPink {
		t.Fatalf("Ärger = %+v, %v", argh, err)
	}
	if _, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "ÄRGER", Keyword: mail.LabelKeyword("ÄRGER")}); !errors.Is(err, mail.ErrLabelExists) {
		t.Errorf("ÄRGER = %v", err)
	}
	// Another account has labels of its own, of the same names.
	other, err := s.CreateLabel(ctx, mail.Label{Account: "a2", Name: "Work", Keyword: "Work"})
	if err != nil || other.ID == work.ID {
		t.Fatalf("a2's Work = %+v, %v", other, err)
	}
	renamed, err := s.UpdateLabel(ctx, mail.Label{ID: work.ID, Account: "a1", Name: "Clients"})
	if err != nil || renamed.Keyword != "Work" || renamed.Color != work.Color || renamed.Name != "Clients" {
		t.Fatalf("renamed = %+v, %v", renamed, err)
	}
	newWork, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil || newWork.Keyword != "Work-2" || newWork.Color != mail.LabelColorAt(2) {
		t.Fatalf("the new Work = %+v, %v", newWork, err)
	}
	if _, err := s.UpdateLabel(ctx, mail.Label{ID: newWork.ID, Account: "a1", Name: "clients"}); !errors.Is(err, mail.ErrLabelExists) {
		t.Errorf("a rename to another's name = %v", err)
	}
	if _, err := s.UpdateLabel(ctx, mail.Label{ID: other.ID, Account: "a1", Name: "Mine"}); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("renaming another account's label = %v", err)
	}
	if got, _ := s.Labels(ctx, "a1"); !slices.Equal(namesOf(got), []string{"Clients", "Ärger", "Work"}) {
		t.Errorf("a1's labels = %v", namesOf(got))
	}
	if err := s.DeleteLabel(ctx, "a1", other.ID, labelBase); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("deleting another account's label = %v", err)
	}
	if _, err := s.SetLabel(ctx, "a1", other.ID, "INBOX", []mail.UID{1}, true, false); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("setting another account's label = %v", err)
	}
	if _, err := s.SetLabel(ctx, "a1", work.ID, "Nowhere", []mail.UID{1}, true, false); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("setting a label in a folder not there = %v", err)
	}
	if _, err := s.LabelRefs(ctx, "a2", work.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Errorf("another account's label's messages = %v", err)
	}

	// The account goes with its labels and theirs of its messages.
	syncMessages(t, s, "a1", "INBOX", labelled(1, "one@x", "One", 1, "Work"))
	if _, err := s.SetLabel(ctx, "a1", argh.ID, "INBOX", []mail.UID{1}, true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAccount(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"labels WHERE account_id = 'a1'", "message_labels", "local_labels"} {
		if n := countRows(t, s, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%d rows of %s outlived the account", n, table)
		}
	}
	if got, _ := s.Labels(ctx, "a2"); !slices.Equal(namesOf(got), []string{"Work"}) {
		t.Errorf("a2's labels = %v", namesOf(got))
	}
}

// A sync writes the labels of the keywords it read: a keyword no label
// has becomes one, Thunderbird's with its name and color, one named as
// another label gets a number; the system's and the reserved ones are
// none; a keyword in another case is the label's; a refresh replaces a
// message's labels, and a folder reset drops them. A body fetch labels a
// new row alone, and older mail as a sync does.
func TestSyncMakesLabelsOfKeywords(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX", "Archive")
	work, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	clients, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "clients", Keyword: "$label2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3, PermanentFlags: []string{`\Seen`, `\*`}},
		Messages: []mail.Message{
			labelled(1, "one@x", "One", 1, "work", "$Forwarded", "Junk", "clients"),
			labelled(2, "two@x", "Two", 2, "$label1", "=E5=B7=A5=E4=BD=9C", "$label1"),
			labelled(3, "three@x", "Three", 3),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Labels(ctx, "a1"); !slices.Equal(namesOf(got), []string{"Work", "clients", "clients 2", "Important", "工作"}) {
		t.Fatalf("labels = %v", namesOf(got))
	}
	if important := labelOf(t, s, "a1", "Important"); important.Color != mail.LabelRed || important.Keyword != "$label1" || important.Unread != 1 {
		t.Errorf("Important = %+v", important)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Work", "clients 2"}) {
		t.Errorf("One has %v", got)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 2); !slices.Equal(got, []string{"Important", "工作"}) {
		t.Errorf("Two has %v", got)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 3); len(got) != 0 {
		t.Errorf("Three has %v", got)
	}
	if f, _, _ := s.Folder(ctx, "a1", "INBOX"); !slices.Equal(f.PermanentFlags, []string{`\Seen`, `\*`}) {
		t.Errorf("the inbox keeps %v", f.PermanentFlags)
	}

	// A refresh replaces the labels of the messages it read, and no
	// others; a batch that says nothing of PERMANENTFLAGS keeps them.
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 3},
		Flags:  []mail.FlagUpdate{{UID: 1, Keywords: []string{"$LABEL2"}}, {UID: 3, Keywords: []string{"WORK"}}, {UID: 2}},
	}); err != nil {
		t.Fatal(err)
	}
	for uid, want := range map[mail.UID][]string{1: {"clients"}, 2: {"Important", "工作"}, 3: {"Work"}} {
		if got := labelsOn(t, s, "a1", "INBOX", uid); !slices.Equal(got, want) {
			t.Errorf("refreshed, %d has %v, want %v", uid, got, want)
		}
	}
	if f, _, _ := s.Folder(ctx, "a1", "INBOX"); !slices.Equal(f.PermanentFlags, []string{`\Seen`, `\*`}) {
		t.Errorf("after a read-only sync the inbox keeps %v", f.PermanentFlags)
	}
	if refs, err := s.LabelRefs(ctx, "a1", clients.ID); err != nil || !slices.Equal(refs, []mail.MessageRef{{Folder: "INBOX", UID: 1}}) {
		t.Errorf("clients' messages = %v, %v", refs, err)
	}

	// A body fetch labels a new row; a row there before keeps its labels.
	fetched := labelled(4, "four@x", "Four", 4, "work")
	fetched.BodyText, fetched.CachedAt = "Body", labelBase
	if err := s.SaveMessage(ctx, "a1", "INBOX", fetched); err != nil {
		t.Fatal(err)
	}
	again := labelled(1, "one@x", "One", 1, "work")
	again.BodyText, again.CachedAt = "Body", labelBase
	if err := s.SaveMessage(ctx, "a1", "INBOX", again); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 4); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("a fetched message has %v", got)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"clients"}) {
		t.Errorf("a message fetched again has %v", got)
	}
	if err := s.ApplyOlder(ctx, "a1", mail.OlderBatch{Folder: "INBOX", UIDValidity: 1,
		Messages: []mail.Message{labelled(0x10, "older@x", "Older", -60, "Work", "New one")}}); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 0x10); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("older mail has %v", got)
	}
	// "New one" is no atom.
	if got, _ := s.Labels(ctx, "a1"); len(got) != 5 {
		t.Errorf("labels = %v", namesOf(got))
	}

	// A reset drops the labels the server kept on the old rows.
	if _, err := s.SetLabel(ctx, "a1", work.ID, "INBOX", []mail.UID{2}, true, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{Reset: true,
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 1},
		Messages: []mail.Message{labelled(1, "two@x", "Two", 2)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Work (here)"}) {
		t.Errorf("after a reset, Two has %v", got)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM message_labels`); n != 0 {
		t.Errorf("%d labels of rows that went", n)
	}
	checkThreadRows(t, s)
}

// A sync writes a message's labels only as they changed: one that read
// the same keywords again writes none, and one that read another writes
// that one alone. A refresh reads the keywords of the whole window every
// time, so writing them all again would rewrite every message's labels
// on every sync.
func TestSyncWritesOnlyChangedLabels(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	if _, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Clients", Keyword: "Clients"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1},
		Messages: []mail.Message{
			labelled(1, "one@x", "One", 1, "Work"),
			labelled(2, "two@x", "Two", 2, "Clients"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE label_writes (ins INTEGER, del INTEGER)`,
		`INSERT INTO label_writes VALUES (0, 0)`,
		`CREATE TRIGGER test_label_ins AFTER INSERT ON message_labels
			BEGIN UPDATE label_writes SET ins = ins + 1; END`,
		`CREATE TRIGGER test_label_del AFTER DELETE ON message_labels
			BEGIN UPDATE label_writes SET del = del + 1; END`,
	} {
		if _, err := s.exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	writes := func() (ins, del int64) {
		t.Helper()
		rows, err := s.query(ctx, `SELECT ins, del FROM label_writes`)
		if err != nil {
			t.Fatal(err)
		}
		return number(rows.Values[0][0]), number(rows.Values[0][1])
	}
	refresh := func(keywords []string) {
		t.Helper()
		if _, err := s.exec(ctx, `UPDATE label_writes SET ins = 0, del = 0`); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
			Folder: mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1},
			Flags:  []mail.FlagUpdate{{UID: 1, Keywords: keywords}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	refresh([]string{"work"})
	if ins, del := writes(); ins != 0 || del != 0 {
		t.Errorf("the same keywords written again: %d inserts, %d deletes", ins, del)
	}
	refresh([]string{"Work", "CLIENTS"})
	if ins, del := writes(); ins != 1 || del != 0 {
		t.Errorf("a label gained wrote %d inserts, %d deletes", ins, del)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Work", "Clients"}) {
		t.Errorf("One has %v", got)
	}
	refresh([]string{"Clients"})
	if ins, del := writes(); ins != 0 || del != 1 {
		t.Errorf("a label lost wrote %d inserts, %d deletes", ins, del)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Clients"}) {
		t.Errorf("One has %v", got)
	}
	// A message read with no keyword loses those it had.
	refresh([]string{})
	if ins, del := writes(); ins != 0 || del != 1 {
		t.Errorf("no keywords wrote %d inserts, %d deletes", ins, del)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); len(got) != 0 {
		t.Errorf("One has %v", got)
	}
	// A reset replaces the folder's rows, and with them their labels: the
	// message carries its keyword again only because its new row is
	// labelled, not because the cache says it had it.
	refresh([]string{"Work"})
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{Reset: true,
		Folder:   mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 2},
		Messages: []mail.Message{labelled(1, "one@x", "One", 1, "Work"), labelled(2, "two@x", "Two", 2, "Clients")},
	}); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("after a reset, One has %v", got)
	}
	// One refresh that takes a label off each of two messages and puts
	// another on it writes those four changes, and takes no label off a
	// message that keeps it.
	if _, err := s.exec(ctx, `UPDATE label_writes SET ins = 0, del = 0`); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
		Folder: mail.Folder{Name: "INBOX", UIDValidity: 2, LastUID: 2},
		Flags: []mail.FlagUpdate{
			{UID: 1, Keywords: []string{"Clients"}},
			{UID: 2, Keywords: []string{"Work"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if ins, del := writes(); ins != 2 || del != 2 {
		t.Errorf("a swap wrote %d inserts, %d deletes; want 2 and 2", ins, del)
	}
	for uid, want := range map[mail.UID]string{1: "Clients", 2: "Work"} {
		if got := labelsOn(t, s, "a1", "INBOX", uid); !slices.Equal(got, []string{want}) {
			t.Errorf("message %d has %v, want %s", uid, got, want)
		}
	}
}

// A refresh that changes labels reads the pairs it writes from its own
// list, one index lookup each, not by pairing the folder's mail with the
// account's labels.
func TestLabelChangesReadTheirPairsByIndex(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for name, query := range map[string]string{"drops": labelDrops, "adds": labelAdds} {
		rows, err := s.query(ctx, `EXPLAIN QUERY PLAN `+query, "[[1,\"Work\"]]", 1, "a1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var steps []string
		for _, r := range rows.Values {
			steps = append(steps, text(r[len(r)-1]))
		}
		plan := strings.Join(steps, "\n")
		at := strings.Index(plan, "SCAN j VIRTUAL TABLE")
		message, label := strings.Index(plan, "SEARCH m USING"), strings.Index(plan, "SEARCH l USING")
		if at < 0 || message < at || label < at {
			t.Errorf("%s does not read its pairs from the list it is given, then look them up:\n%s", name, plan)
		}
		for _, scan := range []string{"SCAN m", "SCAN l", "SCAN message_labels"} {
			if strings.Contains(plan, scan) {
				t.Errorf("%s reads %s whole:\n%s", name, scan, plan)
			}
		}
	}
}

// A sync that began before a label was deleted does not make it again of
// the keyword it read; one that began after does.
func TestSyncAfterADeletedLabel(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addFolders(t, s, "a1", "INBOX")
	at := labelBase.Add(time.Hour)
	sync := func(began time.Time) {
		t.Helper()
		if err := s.ApplySync(ctx, "a1", mail.SyncBatch{
			Folder:   mail.Folder{Name: "INBOX", UIDValidity: 1, LastUID: 1, SyncedAt: began},
			Messages: []mail.Message{labelled(1, "one@x", "One", 1, "Work", "Other")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	sync(at.Add(-time.Minute))
	work := labelOf(t, s, "a1", "Work")
	if err := s.DeleteLabel(ctx, "a1", work.ID, at); err != nil {
		t.Fatal(err)
	}
	for _, began := range []time.Time{at.Add(-time.Second), at} {
		sync(began)
		if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Other"}) {
			t.Errorf("a sync begun at %v made %v", began.Sub(at), got)
		}
	}
	sync(at.Add(time.Second))
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Other", "Work"}) {
		t.Errorf("a sync begun after made %v", got)
	}
	if again := labelOf(t, s, "a1", "Work"); again.ID == work.ID {
		t.Errorf("a label's ID was given again: %d", again.ID)
	}
}

// A label kept on this device is on a message by its Message-ID, or the
// digest of one without, wherever the message is: in two folders at
// once, after a move, after a reset.
func TestLocalLabelsFollowTheMessage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	addFolders(t, s, "a1", "Notes", "Sent", "Old")
	addFolders(t, s, "a2", "INBOX")
	idea := labelled(1, "idea@x", "Idea", 1)
	noID := labelled(2, "", "Scribble", 2)
	syncMessages(t, s, "a1", "Notes", idea, noID, labelled(3, "other@x", "Other", 3))
	syncMessages(t, s, "a1", "Sent", labelled(1, "<idea@X>", "Idea", 1))
	syncMessages(t, s, "a2", "INBOX", labelled(1, "idea@x", "Idea", 1))
	work, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetLabel(ctx, "a1", work.ID, "Notes", []mail.UID{1, 2, 9}, true, true)
	if err != nil || !slices.Equal(changed, []mail.UID{1, 2}) {
		t.Fatalf("changed %v, %v", changed, err)
	}
	if changed, _ := s.SetLabel(ctx, "a1", work.ID, "Notes", []mail.UID{1}, true, true); len(changed) != 0 {
		t.Errorf("set twice, changed %v", changed)
	}
	for _, at := range []mail.MessageRef{{Folder: "Notes", UID: 1}, {Folder: "Notes", UID: 2}, {Folder: "Sent", UID: 1}} {
		if got := labelsOn(t, s, "a1", at.Folder, at.UID); !slices.Equal(got, []string{"Work (here)"}) {
			t.Errorf("%v has %v", at, got)
		}
	}
	if got := labelsOn(t, s, "a1", "Notes", 3); len(got) != 0 {
		t.Errorf("another message has %v", got)
	}
	if m, _, _ := s.GetMessage(ctx, "a2", "INBOX", 1); len(m.Labels) != 0 {
		t.Errorf("another account's copy has %v", m.Labels)
	}
	// Moved: the rows go from Notes and come in Old, under other UIDs.
	for _, uid := range []mail.UID{1, 2} {
		if err := s.DeleteMessage(ctx, "a1", "Notes", uid); err != nil {
			t.Fatal(err)
		}
	}
	idea.UID, noID.UID = 7, 8
	syncMessages(t, s, "a1", "Old", idea, noID)
	for _, uid := range []mail.UID{7, 8} {
		if got := labelsOn(t, s, "a1", "Old", uid); !slices.Equal(got, []string{"Work (here)"}) {
			t.Errorf("moved, Old/%d has %v", uid, got)
		}
	}
	if err := s.ApplySync(ctx, "a1", mail.SyncBatch{Reset: true, Folder: mail.Folder{Name: "Old", UIDValidity: 9, LastUID: 2},
		Messages: []mail.Message{headerOnly(labelled(1, "idea@x", "Idea", 1)), headerOnly(labelled(2, "", "Scribble", 2))}}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []mail.UID{1, 2} {
		if got := labelsOn(t, s, "a1", "Old", uid); !slices.Equal(got, []string{"Work (here)"}) {
			t.Errorf("reset, Old/%d has %v", uid, got)
		}
	}
	// On the server and here, a label is the server's.
	if _, err := s.SetLabel(ctx, "a1", work.ID, "Old", []mail.UID{1}, true, false); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "Old", 1); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("on both, Old/1 has %v", got)
	}
	changed, err = s.SetLabel(ctx, "a1", work.ID, "Old", []mail.UID{1, 2}, false, true)
	if err != nil || !slices.Equal(changed, []mail.UID{1, 2}) {
		t.Errorf("taken off here, changed %v, %v", changed, err)
	}
	if got := labelsOn(t, s, "a1", "Sent", 1); len(got) != 0 {
		t.Errorf("taken off here, the copy in Sent has %v", got)
	}
	if got := labelsOn(t, s, "a1", "Old", 1); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("taken off here, Old/1 has %v", got)
	}
}

// A label's list is its messages, the server's and this device's, in every
// folder but Junk, Trash and Drafts, newest first a page at a time, and
// its count is of the unread ones; each conversation's row carries the
// labels of its messages there, and a folder that keeps no new keyword
// says so of its messages.
func TestLabelPagesAndCounts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	if err := s.SaveFolders(ctx, "a1", []mail.Folder{
		{Name: "INBOX", SpecialUse: mail.SpecialInbox}, {Name: "Notes"}, {Name: "Junk", SpecialUse: mail.SpecialJunk},
		{Name: "Trash", SpecialUse: mail.SpecialTrash}, {Name: "Drafts", SpecialUse: mail.SpecialDrafts},
	}); err != nil {
		t.Fatal(err)
	}
	reply := labelled(2, "reply@x", "Re: Plan", 2, "Work", "Later")
	reply.InReplyTo, reply.Seen = "plan@x", true
	syncMessages(t, s, "a1", "INBOX", labelled(1, "plan@x", "Plan", 1, "Work"), reply, labelled(3, "other@x", "Other", 3))
	syncMessages(t, s, "a1", "Notes", labelled(1, "note@x", "Note", 4), labelled(2, "plan@x", "Plan", 1))
	for _, box := range []string{"Junk", "Trash", "Drafts"} {
		syncMessages(t, s, "a1", box, labelled(1, box+"@x", box, 5, "Work"))
	}
	if err := s.SetPermanentFlags(ctx, "a1", "Notes", []string{`\Seen`}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPermanentFlags(ctx, "a1", "INBOX", []string{`\Seen`, `\*`}); err != nil {
		t.Fatal(err)
	}
	work, later := labelOf(t, s, "a1", "Work"), labelOf(t, s, "a1", "Later")
	// The note, and the copy in Notes of a message the inbox has Work on.
	if _, err := s.SetLabel(ctx, "a1", work.ID, "Notes", []mail.UID{1, 2}, true, true); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "INBOX", 1); !slices.Equal(got, []string{"Work"}) {
		t.Errorf("the inbox's Plan has %v", got)
	}
	if got := labelOf(t, s, "a1", "Work"); got.Unread != 3 || got.Local {
		t.Errorf("Work = %+v", got)
	}
	if got := labelOf(t, s, "a1", "Later"); got.Unread != 0 {
		t.Errorf("Later = %+v", got)
	}
	var got []string
	var after mail.Cursor
	for {
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.LabelScope("a1", work.ID), After: after, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Messages {
			got = append(got, m.Folder+"/"+m.Subject)
			if m.Folder == "Notes" && !m.LabelsLocal || m.Folder == "INBOX" && m.LabelsLocal {
				t.Errorf("%s/%s says its labels are local: %v", m.Folder, m.Subject, m.LabelsLocal)
			}
		}
		if !page.More {
			break
		}
		after = page.Next
	}
	if want := []string{"Notes/Note", "INBOX/Re: Plan", "Notes/Plan", "INBOX/Plan"}; !slices.Equal(got, want) {
		t.Errorf("Work lists %v, want %v", got, want)
	}
	page, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.LabelScope("a1", work.ID)})
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, c := range page.Conversations {
		rows = append(rows, fmt.Sprintf("%s:%d:%v:%v", c.Latest.Subject, c.Count, c.Unread, c.Labels))
	}
	if want := []string{
		"Note:1:true:" + fmt.Sprint([]mail.LabelID{work.ID}),
		"Re: Plan:3:true:" + fmt.Sprint([]mail.LabelID{work.ID, later.ID}),
	}; !slices.Equal(rows, want) {
		t.Errorf("Work's conversations = %v, want %v", rows, want)
	}
	page, err = s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "INBOX")})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range page.Conversations {
		want := map[string][]mail.LabelID{"Other": nil, "Re: Plan": {work.ID, later.ID}}[c.Latest.Subject]
		if !slices.Equal(c.Labels, want) || !slices.Equal(c.Latest.Labels, labelsOf(t, s, c.Latest)) {
			t.Errorf("the inbox's row of %s has %v", c.Latest.Subject, c.Labels)
		}
	}
	if page, _ := s.MessagePage(ctx, mail.ListQuery{Scope: mail.Scope{Accounts: []string{"a2"}, Label: work.ID}}); len(page.Messages) != 0 {
		t.Errorf("Work in another account's scope lists %v", subjectsOf(page.Messages))
	}
	if page, _ := s.MessagePage(ctx, mail.ListQuery{Scope: mail.LabelScope("a1", 999)}); len(page.Messages) != 0 {
		t.Errorf("a label not there lists %v", subjectsOf(page.Messages))
	}
	if _, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.Scope{Folder: "INBOX", Label: work.ID}}); !errors.Is(err, mail.ErrBadScope) {
		t.Errorf("a label in a folder = %v", err)
	}
}

// labelsOf reads a message's labels again, as GetMessage gives them.
func labelsOf(t *testing.T, s *Store, m mail.Summary) []mail.MessageLabel {
	t.Helper()
	got, ok, err := s.GetMessage(context.Background(), m.Account, m.Folder, m.UID)
	if err != nil || !ok {
		t.Fatalf("%s/%d: %v, %v", m.Folder, m.UID, ok, err)
	}
	return got.Labels
}

// A search finds messages by the name of their label, in any case, with
// the other filters and in each account of its scope, and says it found
// them by it; a name no label has finds none.
func TestLabelSearch(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	searchFixture(t, s)
	for _, m := range []struct {
		account, folder string
		uid             mail.UID
		label           string
		local           bool
	}{
		{"a1", "INBOX", 1, "Work", false}, {"a1", "INBOX", 2, "Work", true}, {"a1", "Archive", 1, "Work", false},
		{"a1", "INBOX", 1, "To Do", false}, {"a2", "INBOX", 1, "work", false},
	} {
		l, err := s.CreateLabel(ctx, mail.Label{Account: m.account, Name: m.label, Keyword: mail.LabelKeyword(m.label)})
		if errors.Is(err, mail.ErrLabelExists) {
			l = labelOf(t, s, m.account, m.label)
		} else if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetLabel(ctx, m.account, l.ID, m.folder, []mail.UID{m.uid}, true, m.local); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		typed string
		scope mail.Scope
		want  []string
	}{
		{"label:work", mail.AccountScope("a1", ""), []string{"a1:INBOX/2", "a1:INBOX/1", "a1:Archive/1"}},
		{"label:WORK", mail.Scope{}, []string{"a1:INBOX/2", "a2:INBOX/1", "a1:INBOX/1", "a1:Archive/1"}},
		{"label:work is:unread", mail.AccountScope("a1", ""), []string{"a1:INBOX/1", "a1:Archive/1"}},
		{"label:work budget", mail.Scope{}, []string{"a2:INBOX/1", "a1:INBOX/1"}},
		{`label:work label:"to do"`, mail.Scope{}, []string{"a1:INBOX/1"}},
		{"label:work in:archive", mail.AccountScope("a1", ""), []string{"a1:Archive/1"}},
		{"label:work from:bob", mail.AccountScope("a1", ""), []string{"a1:INBOX/2"}},
		{"label:nothing", mail.Scope{}, nil},
		{"label:work", mail.AccountScope("a1", "Sent"), nil},
	} {
		search := mail.ParseSearch(tc.typed, time.UTC)
		page, err := s.MessagePage(ctx, mail.ListQuery{Scope: tc.scope, Search: search})
		if err != nil {
			t.Fatal(err)
		}
		if got := keysOf(page.Messages); !slices.Equal(got, tc.want) {
			t.Errorf("%s finds %v, want %v", tc.typed, got, tc.want)
		}
		for _, m := range page.Messages {
			if !m.Matched.Has(mail.FieldLabel) {
				t.Errorf("%s: %s/%d does not say it was found by its label: %b", tc.typed, m.Folder, m.UID, m.Matched)
			}
		}
	}
	page, err := s.MessagePage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", ""), Search: mail.ParseSearch("label:work budget", time.UTC)})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Matched != mail.FieldSubject|mail.FieldBody|mail.FieldLabel {
		t.Errorf("found by label and text: %+v, %v", page.Messages, err)
	}
	// A conversation is found by a message of it that has the label.
	conv, err := s.ConversationPage(ctx, mail.ListQuery{Scope: mail.AccountScope("a1", "Sent"), Search: mail.ParseSearch(`label:"TO DO"`, time.UTC)})
	if err != nil || len(conv.Conversations) != 1 {
		t.Fatalf("conversations of To Do = %+v, %v", conv, err)
	}
	if c := conv.Conversations[0]; len(c.Found) != 1 || c.Found[0].MessageRef != (mail.MessageRef{Folder: "INBOX", UID: 1}) || c.Found[0].Fields != mail.FieldLabel {
		t.Errorf("found = %+v", c.Found)
	}
}

// The reads of a label go through its messages, by the indexes of the
// labels, and sort those alone: they read neither the messages whole nor
// the labels of every message.
func TestLabelQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	scopeFixture(t, s)
	work, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []mail.ListQuery{
		{Scope: mail.LabelScope("a1", work.ID)},
		{Scope: mail.AccountScope("a1", ""), Search: mail.Search{Labels: []string{"work"}}},
	} {
		p, err := s.plan(ctx, q, searchCriteria(q.Search))
		if err != nil {
			t.Fatal(err)
		}
		query, args, _ := p.messageSQL()
		plan := explain(t, s, query, args)
		for _, step := range []string{
			"SEARCH m USING INTEGER PRIMARY KEY (rowid=?)",
			"SEARCH ml USING COVERING INDEX message_labels_label (label_id=?)",
			"SEARCH ll USING PRIMARY KEY (label_id=?)",
			"SEARCH k USING INDEX messages_key (msg_key=?)",
		} {
			if !strings.Contains(plan, step) {
				t.Errorf("%+v does not %s:\n%s", q, step, plan)
			}
		}
		for _, scan := range []string{"SCAN m\n", "SCAN ml", "SCAN ll", "SCAN k\n", "USING INDEX messages_date"} {
			if strings.Contains(plan+"\n", scan) {
				t.Errorf("%+v reads every message or every label (%s):\n%s", q, scan, plan)
			}
		}
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM message_labels ml JOIN messages m ON m.id = ml.message_id WHERE ml.label_id = ? AND m.seen = 0`,
	} {
		if plan := explain(t, s, q, []any{int64(work.ID)}); !strings.Contains(plan, "message_labels_label") {
			t.Errorf("an unread count does not read the label's index:\n%s", plan)
		}
	}
}

func explain(t *testing.T, s *Store, query string, args []any) string {
	t.Helper()
	rows, err := s.query(context.Background(), `EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, r := range rows.Values {
		steps = append(steps, text(r[len(r)-1]))
	}
	return strings.Join(steps, "\n")
}

// A database of the previous release, v11, gains labels: its messages get
// the keys a label kept here finds them by, the Message-ID or the digest
// of one without, its folders say nothing yet of the keywords they keep,
// and its messages have no labels until a sync reads their keywords.
func TestMigrationAddsLabelsToAnOlderDatabase(t *testing.T) {
	sqlitetest.Require(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shu.db")
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for v := 0; v < 11; v++ {
		for _, q := range migrations[v] {
			if _, err := db.Execute(ctx, q); err != nil {
				t.Fatalf("v%d: %v", v+1, err)
			}
		}
	}
	date := labelBase.Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO accounts (id, name, email, username, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls)
			VALUES ('a1', 'Ann', 'ann@example.com', 'ann', 'imap.example.com', 993, 'tls', 'smtp.example.com', 465, 'tls')`, nil},
		{`INSERT INTO folders (id, account_id, name, special_use, uidvalidity, last_uid) VALUES (1, 'a1', 'INBOX', '\Inbox', 1, 3), (2, 'a1', 'Notes', NULL, 1, 1)`, nil},
		{`INSERT INTO threads (id, account_id) VALUES (1, 'a1'), (2, 'a1'), (3, 'a1')`, nil},
		{`INSERT INTO messages (folder_id, server_uid, message_id, from_email, subject, date, thread_id) VALUES
			(1, 1, 'one@X.example', 'maya@example.org', 'One', ?, 1),
			(1, 2, NULL, 'Bob@Example.org', 'No id', ?, 2),
			(1, 3, '', 'carol@example.org', 'Empty id', 0, 3),
			(2, 1, '<one@x.EXAMPLE>', 'maya@example.org', 'One', ?, 1)`, []any{date, date + 60, date}},
		{"PRAGMA user_version=11", nil},
	} {
		if _, err := db.Execute(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := countRows(t, s, "PRAGMA user_version"); got != int64(SchemaVersion) || SchemaVersion < 12 {
		t.Fatalf("user_version = %d, schema v%d", got, SchemaVersion)
	}
	rows, err := s.query(ctx, `SELECT f.name, m.server_uid, m.msg_key FROM messages m JOIN folders f ON f.id = m.folder_id ORDER BY m.id`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"INBOX/1 " + mail.LabelKey(mail.Summary{MessageID: "one@X.example"}),
		"INBOX/2 " + mail.LabelKey(mail.Summary{From: mail.Address{Email: "Bob@Example.org"}, Subject: "No id", Date: time.Unix(date+60, 0)}),
		"INBOX/3 " + mail.LabelKey(mail.Summary{From: mail.Address{Email: "carol@example.org"}, Subject: "Empty id"}),
		"Notes/1 " + mail.LabelKey(mail.Summary{MessageID: "one@x.example"}),
	}
	var got []string
	for _, r := range rows.Values {
		got = append(got, fmt.Sprintf("%s/%d %s", text(r[0]), number(r[1]), text(r[2])))
	}
	if !slices.Equal(got, want) {
		t.Errorf("keys:\n got %v\nwant %v", got, want)
	}
	if want[0][len("INBOX/1 "):] != want[3][len("Notes/1 "):] {
		t.Error("two copies of a message have two keys")
	}
	// Each key is the one a sync writes of the same message.
	syncMessages(t, s, "a1", "INBOX", mail.Message{Summary: mail.Summary{UID: 2, Subject: "No id",
		From: mail.Address{Email: "Bob@Example.org"}, Date: time.Unix(date+60, 0)}})
	if after := countRows(t, s, `SELECT COUNT(DISTINCT msg_key) FROM messages`); after != 3 {
		t.Errorf("%d keys once the sync wrote the message again, want 3", after)
	}
	for _, f := range []string{"INBOX", "Notes"} {
		if folder, _, _ := s.Folder(ctx, "a1", f); folder.KeywordsKnown() {
			t.Errorf("%s keeps %v", f, folder.PermanentFlags)
		}
	}
	work, err := s.CreateLabel(ctx, mail.Label{Account: "a1", Name: "Work", Keyword: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetLabel(ctx, "a1", work.ID, "INBOX", []mail.UID{1}, true, true); err != nil {
		t.Fatal(err)
	}
	if got := labelsOn(t, s, "a1", "Notes", 1); !slices.Equal(got, []string{"Work (here)"}) {
		t.Errorf("the copy of a message from before labels has %v", got)
	}
	checkThreadRows(t, s)
}
