package main

import (
	"context"
	"errors"
	"sort"
	"strings"

	"shu/mail"
)

// fakeService is the UI tests' mail engine. It answers at once and
// records what the window asked it to do.
type fakeService struct {
	accounts []mail.Account
	active   string
	folders  map[string][]mail.Folder
	messages map[string][]mail.Summary
	bodies   map[string]mail.Message
	pending  []mail.PendingSend

	testErr    error
	flagErr    error
	moveErr    error
	sendErr    error
	sendWarn   error
	retryErr   error
	password   map[string]string
	sent       []mail.Outgoing
	moved      []string
	flagCalls  []string
	attachment mail.Attachment
}

func newFakeService() *fakeService {
	return &fakeService{
		folders:  map[string][]mail.Folder{},
		messages: map[string][]mail.Summary{},
		bodies:   map[string]mail.Message{},
		password: map[string]string{},
	}
}

func listKey(accountID, folder string) string { return accountID + "/" + folder }

func bodyKey(accountID, folder string, uid mail.UID) string {
	return accountID + "/" + folder + "/" + itoa(uid)
}

// addAccount stores an account with its folders and headers.
func (f *fakeService) addAccount(a mail.Account, password string, folders []mail.Folder, msgs []mail.Summary) {
	f.accounts = append(f.accounts, a)
	f.password[a.ID] = password
	f.folders[a.ID] = append([]mail.Folder(nil), folders...)
	if f.active == "" {
		f.active = a.ID
	}
	for _, m := range msgs {
		folder := m.Folder
		if folder == "" {
			folder = "INBOX"
		}
		m.Folder = folder
		key := listKey(a.ID, folder)
		f.messages[key] = append(f.messages[key], m)
	}
	for key := range f.messages {
		sortSummaries(f.messages[key])
	}
}

func sortSummaries(list []mail.Summary) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })
}

func (f *fakeService) Accounts(ctx context.Context) ([]mail.Account, string, error) {
	return append([]mail.Account(nil), f.accounts...), f.active, nil
}

func (f *fakeService) AddAccount(ctx context.Context, a mail.Account, password string) error {
	f.accounts = append(f.accounts, a)
	f.password[a.ID] = password
	if f.active == "" {
		f.active = a.ID
	}
	if f.folders[a.ID] == nil {
		f.folders[a.ID] = []mail.Folder{
			{Name: "INBOX", SpecialUse: mail.SpecialInbox},
			{Name: "Sent", SpecialUse: mail.SpecialSent},
		}
	}
	return nil
}

func (f *fakeService) UpdateAccount(ctx context.Context, a mail.Account, password string) error {
	for i := range f.accounts {
		if f.accounts[i].ID == a.ID {
			f.accounts[i] = a
			if password != "" {
				f.password[a.ID] = password
			}
			return nil
		}
	}
	return mail.ErrNotFound
}

func (f *fakeService) RemoveAccount(ctx context.Context, id string) error {
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts = append(f.accounts[:i], f.accounts[i+1:]...)
			break
		}
	}
	if f.active == id {
		f.active = ""
		if len(f.accounts) > 0 {
			f.active = f.accounts[0].ID
		}
	}
	return nil
}

func (f *fakeService) SetActive(ctx context.Context, id string) error {
	f.active = id
	return nil
}

func (f *fakeService) TestConnection(ctx context.Context, a mail.Account, password string) error {
	return f.testErr
}

func (f *fakeService) CachedFolders(ctx context.Context, accountID string) ([]mail.Folder, error) {
	return append([]mail.Folder(nil), f.folders[accountID]...), nil
}

func (f *fakeService) CachedMessages(ctx context.Context, accountID, folder, query string, limit int) ([]mail.Summary, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []mail.Summary
	if folder == "starred" {
		for _, fl := range f.folders[accountID] {
			for _, m := range f.messages[listKey(accountID, fl.Name)] {
				if m.Flagged && f.matches(accountID, m, q) {
					out = append(out, m)
				}
			}
		}
	} else {
		for _, m := range f.messages[listKey(accountID, folder)] {
			if f.matches(accountID, m, q) {
				out = append(out, m)
			}
		}
	}
	sortSummaries(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeService) matches(accountID string, m mail.Summary, q string) bool {
	if q == "" {
		return true
	}
	text := strings.ToLower(m.Subject + " " + m.From.Name + " " + m.From.Email + " " + m.Preview)
	if b, ok := f.bodies[bodyKey(accountID, m.Folder, m.UID)]; ok {
		text += " " + strings.ToLower(b.BodyText)
	}
	return strings.Contains(text, q)
}

func (f *fakeService) CachedMessage(ctx context.Context, accountID, folder string, uid mail.UID) (mail.FetchResult, bool, error) {
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		return mail.FetchResult{Message: b}, true, nil
	}
	if s, ok := f.find(accountID, folder, uid); ok {
		return mail.FetchResult{Message: mail.Message{Summary: s}}, false, nil
	}
	return mail.FetchResult{}, false, nil
}

func (f *fakeService) find(accountID, folder string, uid mail.UID) (mail.Summary, bool) {
	for _, m := range f.messages[listKey(accountID, folder)] {
		if m.UID == uid {
			return m, true
		}
	}
	return mail.Summary{}, false
}

func (f *fakeService) SyncAccount(ctx context.Context, accountID string) ([]mail.Folder, error) {
	return append([]mail.Folder(nil), f.folders[accountID]...), nil
}

func (f *fakeService) SyncFolder(ctx context.Context, accountID, folder string, limit int) error {
	return nil
}

func (f *fakeService) FetchMessage(ctx context.Context, accountID, folder string, uid mail.UID, markSeen bool) (mail.FetchResult, error) {
	s, ok := f.find(accountID, folder, uid)
	if !ok {
		return mail.FetchResult{}, mail.ErrNotFound
	}
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		return mail.FetchResult{Message: b}, nil
	}
	msg := mail.Message{Summary: s, BodyText: "Body of " + s.Subject, BodyCached: true}
	msg.BodyHTML = "<p>Body of <b>" + s.Subject + "</b></p><script>alert(1)</script>"
	if markSeen {
		msg.Seen = true
	}
	f.bodies[bodyKey(accountID, folder, uid)] = msg
	f.markSeen(accountID, folder, uid, msg.Seen)
	return mail.FetchResult{Message: msg}, nil
}

func (f *fakeService) markSeen(accountID, folder string, uid mail.UID, seen bool) {
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID == uid {
			list[i].Seen = seen
			f.messages[key] = list
		}
	}
	f.recount(accountID, folder)
}

// recount keeps the fake folder's unread count in step with its rows.
func (f *fakeService) recount(accountID, folder string) {
	unread := 0
	for _, m := range f.messages[listKey(accountID, folder)] {
		if !m.Seen {
			unread++
		}
	}
	for i := range f.folders[accountID] {
		if f.folders[accountID][i].Name == folder {
			f.folders[accountID][i].Unread = unread
			f.folders[accountID][i].Total = len(f.messages[listKey(accountID, folder)])
		}
	}
}

func (f *fakeService) SetFlag(ctx context.Context, accountID, folder string, uid mail.UID, flag mail.Flag, on bool) error {
	f.flagCalls = append(f.flagCalls, string(flag))
	if f.flagErr != nil {
		return f.flagErr
	}
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		if flag == mail.FlagFlagged {
			list[i].Flagged = on
		}
		if flag == mail.FlagSeen {
			list[i].Seen = on
		}
		f.messages[key] = list
	}
	if flag == mail.FlagSeen {
		f.recount(accountID, folder)
	}
	return nil
}

func (f *fakeService) Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) error {
	f.moved = append(f.moved, folder+"->"+dest)
	if f.moveErr != nil {
		return f.moveErr
	}
	key := listKey(accountID, folder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		m := list[i]
		f.messages[key] = append(list[:i], list[i+1:]...)
		m.Folder = dest
		m.UID = 1000 + uid
		destKey := listKey(accountID, dest)
		f.messages[destKey] = append(f.messages[destKey], m)
		return nil
	}
	return nil
}

func (f *fakeService) Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error) {
	if f.sendErr != nil {
		return mail.SendResult{}, f.sendErr
	}
	f.sent = append(f.sent, out)
	return mail.SendResult{Copied: f.sendWarn == nil, Warning: f.sendWarn, Outbox: mail.PendingSend{ID: "out-1"}}, nil
}

func (f *fakeService) RetryPending(ctx context.Context, id string) (mail.SendResult, error) {
	if f.retryErr != nil {
		return mail.SendResult{}, f.retryErr
	}
	f.pending = nil
	return mail.SendResult{Copied: true}, nil
}

func (f *fakeService) PendingSends(ctx context.Context, accountID string) ([]mail.PendingSend, error) {
	return append([]mail.PendingSend(nil), f.pending...), nil
}

func (f *fakeService) Attachment(ctx context.Context, accountID, folder string, uid mail.UID, part string) (mail.Attachment, []byte, error) {
	if f.attachment.Part == "" {
		return mail.Attachment{}, nil, mail.ErrNotFound
	}
	return f.attachment, []byte("data"), nil
}

func itoa(uid mail.UID) string {
	if uid == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for uid > 0 {
		i--
		b[i] = byte('0' + uid%10)
		uid /= 10
	}
	return string(b[i:])
}

var _ mailService = (*fakeService)(nil)

var errFake = errors.New("fake error")
