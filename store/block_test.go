package store

import (
	"context"
	"testing"
	"time"

	"shu/mail"
)

// savedUnsubscribe is what the store kept of leaving one sender's list,
// read through the list the engine reads.
func savedUnsubscribe(t *testing.T, s *Store, accountID, email string) (mail.UnsubscribeRecord, bool) {
	t.Helper()
	list, err := s.Unsubscribes(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.Email == email {
			return r, true
		}
	}
	return mail.UnsubscribeRecord{}, false
}

// Blocked senders are this device's: one account's each, newest first,
// and a second block of the same sender changes where its mail goes
// rather than adding a row.
func TestBlockedSendersRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	addAccount(t, s, "a2")
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

	blocks := []mail.BlockedSender{
		{AccountID: "a1", Value: "deals@shop.example", Folder: "Junk", At: at},
		{AccountID: "a1", Value: "shop.example", Domain: true, Folder: "Trash", At: at.Add(time.Minute)},
		{AccountID: "a2", Value: "deals@shop.example", Folder: "Junk", At: at},
	}
	for _, b := range blocks {
		if err := s.SaveBlockedSender(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.BlockedSenders(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Value != "shop.example" || !got[0].Domain || got[1].Value != "deals@shop.example" {
		t.Fatalf("a1's blocks = %+v", got)
	}
	if !got[1].At.Equal(at) || got[1].Folder != "Junk" {
		t.Errorf("block = %+v", got[1])
	}

	changed := blocks[0]
	changed.Folder = "Trash"
	changed.At = at.Add(2 * time.Minute)
	if err := s.SaveBlockedSender(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if got, err = s.BlockedSenders(ctx, "a1"); err != nil || len(got) != 2 {
		t.Fatalf("after changing a block: %+v, %v", got, err)
	}
	if got[0].Value != "deals@shop.example" || got[0].Folder != "Trash" || !got[0].At.Equal(changed.At) {
		t.Errorf("the changed block = %+v", got[0])
	}

	if err := s.RemoveBlockedSender(ctx, "a1", "deals@shop.example", false); err != nil {
		t.Fatal(err)
	}
	if got, err = s.BlockedSenders(ctx, "a1"); err != nil || len(got) != 1 || !got[0].Domain {
		t.Errorf("after removing a block: %+v, %v", got, err)
	}
	// The other account keeps its own block of the same address.
	if got, err = s.BlockedSenders(ctx, "a2"); err != nil || len(got) != 1 || got[0].Value != "deals@shop.example" {
		t.Errorf("a2's blocks = %+v, %v", got, err)
	}
}

// What a list left keeps: the sender, the method, the destination and the
// time, one record per sender, replaced when it is left again.
func TestUnsubscribesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	addAccount(t, s, "a1")
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	first := mail.UnsubscribeRecord{
		AccountID: "a1", Email: "news@shop.example",
		Method: mail.UnsubscribeEmail, Destination: "leave@shop.example", At: at,
	}
	second := mail.UnsubscribeRecord{
		AccountID: "a1", Email: "deals@other.example",
		Method: mail.UnsubscribeOneClick, Destination: "https://other.example/unsub", At: at.Add(time.Minute),
	}
	for _, r := range []mail.UnsubscribeRecord{first, second} {
		if err := s.SaveUnsubscribe(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	sameRecord := func(got, want mail.UnsubscribeRecord) bool {
		return got.AccountID == want.AccountID && got.Email == want.Email &&
			got.Method == want.Method && got.Destination == want.Destination && got.At.Equal(want.At)
	}
	got, ok := savedUnsubscribe(t, s, "a1", "news@shop.example")
	if !ok || !sameRecord(got, first) {
		t.Fatalf("record = %+v, %v", got, ok)
	}
	if _, ok := savedUnsubscribe(t, s, "a1", "nobody@example.com"); ok {
		t.Error("a sender never left")
	}
	list, err := s.Unsubscribes(ctx, "a1")
	if err != nil || len(list) != 2 || list[0].Email != "deals@other.example" {
		t.Fatalf("unsubscribes = %+v, %v", list, err)
	}

	again := first
	again.Method = mail.UnsubscribeOneClick
	again.At = at.Add(2 * time.Minute)
	if err := s.SaveUnsubscribe(ctx, again); err != nil {
		t.Fatal(err)
	}
	if got, ok := savedUnsubscribe(t, s, "a1", "news@shop.example"); !ok || !sameRecord(got, again) {
		t.Errorf("the replaced record = %+v, %v", got, ok)
	}
	if list, err := s.Unsubscribes(ctx, "a1"); err != nil || len(list) != 2 {
		t.Errorf("one sender left twice made %d records, %v", len(list), err)
	}
}
