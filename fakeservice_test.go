package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

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
	moveBacks  []string
	flagCalls  []string
	attachment mail.Attachment

	// Drafts. The fake keeps them as the engine does: saving is local,
	// and SyncDrafts is the step that reaches the server, or fails to.
	drafts map[string]mail.Draft
	// draftBase and draftTick give each save a later time than the last,
	// without a clock.
	draftBase time.Time
	draftTick int
	draftUID  mail.UID
	// draftSyncErr is the server being unreachable, draftSaveErr and
	// discardErr the cache failing, adoptErr a draft that cannot be taken
	// over.
	draftSyncErr error
	draftSaveErr error
	discardErr   error
	adoptErr     error
	// onDraftSync runs as a sync begins: what another mail client did to
	// the server's drafts meanwhile. replaced marks the drafts it took
	// over, as the engine remembers them.
	onDraftSync func()
	replaced    map[string]bool
	draftSaves  int
	draftSyncs  int
	discarded   []string
}

func newFakeService() *fakeService {
	return &fakeService{
		folders:   map[string][]mail.Folder{},
		messages:  map[string][]mail.Summary{},
		bodies:    map[string]mail.Message{},
		password:  map[string]string{},
		drafts:    map[string]mail.Draft{},
		replaced:  map[string]bool{},
		draftBase: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC),
		draftUID:  500,
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

func (f *fakeService) Move(ctx context.Context, accountID, folder string, uid mail.UID, dest string) (mail.MoveResult, error) {
	f.moved = append(f.moved, folder+"->"+dest)
	if f.moveErr != nil {
		return mail.MoveResult{}, f.moveErr
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
		f.recount(accountID, folder)
		f.recount(accountID, dest)
		return mail.MoveResult{Folder: dest, DestUID: m.UID}, nil
	}
	return mail.MoveResult{Folder: dest}, nil
}

func (f *fakeService) MoveBack(ctx context.Context, accountID, from, to string, uid mail.UID) error {
	f.moveBacks = append(f.moveBacks, from+"->"+to)
	key := listKey(accountID, from)
	list := f.messages[key]
	for i := range list {
		if list[i].UID != uid {
			continue
		}
		m := list[i]
		f.messages[key] = append(list[:i], list[i+1:]...)
		m.Folder = to
		if m.UID > 1000 {
			m.UID -= 1000
		}
		srcKey := listKey(accountID, to)
		f.messages[srcKey] = append(f.messages[srcKey], m)
		f.recount(accountID, from)
		f.recount(accountID, to)
		return nil
	}
	return nil
}

func (f *fakeService) Send(ctx context.Context, accountID string, out mail.Outgoing) (mail.SendResult, error) {
	if f.sendErr != nil {
		return mail.SendResult{}, f.sendErr
	}
	f.sent = append(f.sent, out)
	// The message is out: its draft goes, here and on the server.
	f.removeDraft(out.DraftID)
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

// serverDrafts is the name of the account's Drafts folder, or "".
func (f *fakeService) serverDrafts(accountID string) string {
	for _, folder := range f.folders[accountID] {
		if folder.SpecialUse == mail.SpecialDrafts {
			return folder.Name
		}
	}
	return ""
}

// removeDraft deletes a draft and the server copy it is linked to.
func (f *fakeService) removeDraft(id string) {
	d, ok := f.drafts[id]
	if !ok {
		return
	}
	delete(f.drafts, id)
	if d.ServerUID == 0 {
		return
	}
	key := listKey(d.AccountID, d.ServerFolder)
	list := f.messages[key]
	for i := range list {
		if list[i].UID == d.ServerUID {
			f.messages[key] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// draftRecipients reads a recipient field as far as it parses.
func draftRecipients(text string) []mail.Address {
	var out []mail.Address
	for _, part := range strings.Split(text, ",") {
		if list, err := mail.ParseAddressList([]string{part}); err == nil {
			out = append(out, list...)
		}
	}
	return out
}

func (f *fakeService) DraftList(ctx context.Context, accountID, query string, limit int) ([]mail.Summary, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var from mail.Address
	for _, a := range f.accounts {
		if a.ID == accountID {
			from = mail.Address{Name: a.Name, Email: a.Email}
		}
	}
	var out []mail.Summary
	uploaded := map[mail.UID]bool{}
	for _, d := range f.drafts {
		if d.AccountID != accountID {
			continue
		}
		uploaded[d.ServerUID] = true
		if q != "" && !strings.Contains(strings.ToLower(d.Subject+" "+d.To+" "+d.Body), q) {
			continue
		}
		out = append(out, mail.Summary{
			DraftID: d.ID, MessageID: d.MessageID, From: from,
			To: draftRecipients(d.To), Cc: draftRecipients(d.Cc),
			Subject: strings.TrimSpace(d.Subject), Preview: strings.Join(strings.Fields(d.Body), " "),
			Date: d.UpdatedAt, Seen: true,
		})
	}
	sortSummaries(out)
	if folder := f.serverDrafts(accountID); folder != "" {
		var server []mail.Summary
		for _, m := range f.messages[listKey(accountID, folder)] {
			if !uploaded[m.UID] && f.matches(accountID, m, q) {
				server = append(server, m)
			}
		}
		sortSummaries(server)
		out = append(out, server...)
	}
	return out, nil
}

func (f *fakeService) Draft(ctx context.Context, id string) (mail.Draft, bool, error) {
	d, ok := f.drafts[id]
	return d, ok, nil
}

func (f *fakeService) SaveDraft(ctx context.Context, d mail.Draft) (mail.Draft, error) {
	f.draftSaves++
	if f.draftSaveErr != nil {
		return mail.Draft{}, f.draftSaveErr
	}
	if old, ok := f.drafts[d.ID]; ok {
		if old.To == d.To && old.Cc == d.Cc && old.Bcc == d.Bcc && old.Subject == d.Subject &&
			old.Body == d.Body && old.InReplyTo == d.InReplyTo {
			return old, nil
		}
		// The content changes; where the copy is, and its revision on
		// the server, do not.
		old.To, old.Cc, old.Bcc, old.Subject, old.Body = d.To, d.Cc, d.Bcc, d.Subject, d.Body
		old.InReplyTo, old.References = d.InReplyTo, d.References
		old.Revision++
		d = old
	} else {
		d.Revision, d.SyncedRevision = 1, 0
		d.ServerFolder, d.ServerUID, d.ServerUIDValidity = "", 0, 0
		d.MessageID = d.ID + "@example.com"
		// A draft another client took over comes back as one of its own,
		// another message than the one it was.
		if d.Conflict = f.replaced[d.ID]; d.Conflict {
			d.MessageID = d.ID + ".kept@example.com"
		}
		delete(f.replaced, d.ID)
	}
	f.draftTick++
	d.UpdatedAt = f.draftBase.Add(time.Duration(f.draftTick) * time.Minute)
	f.drafts[d.ID] = d
	return d, nil
}

func (f *fakeService) SyncDrafts(ctx context.Context, accountID string) (mail.DraftSyncResult, error) {
	f.draftSyncs++
	if f.draftSyncErr != nil {
		return mail.DraftSyncResult{}, f.draftSyncErr
	}
	if f.onDraftSync != nil {
		f.onDraftSync()
	}
	folder := f.serverDrafts(accountID)
	if folder == "" {
		return mail.DraftSyncResult{}, nil
	}
	result := mail.DraftSyncResult{Folder: folder}
	for id, d := range f.drafts {
		if d.AccountID != accountID || !d.Dirty() {
			continue
		}
		f.draftUID++
		d.SyncedRevision = d.Revision
		d.ServerFolder, d.ServerUID, d.ServerUIDValidity = folder, f.draftUID, 1
		f.drafts[id] = d
		result.Uploaded++
	}
	return result, nil
}

func (f *fakeService) DiscardDraft(ctx context.Context, id string) error {
	f.discarded = append(f.discarded, id)
	if f.discardErr != nil {
		return f.discardErr
	}
	f.removeDraft(id)
	return nil
}

func (f *fakeService) AdoptDraft(ctx context.Context, accountID, folder string, uid mail.UID) (mail.Draft, error) {
	if f.adoptErr != nil {
		return mail.Draft{}, f.adoptErr
	}
	for _, d := range f.drafts {
		if d.AccountID == accountID && d.ServerFolder == folder && d.ServerUID == uid {
			return d, nil
		}
	}
	m, ok := f.find(accountID, folder, uid)
	if !ok {
		return mail.Draft{}, mail.ErrNotFound
	}
	body := "Body of " + m.Subject
	if b, ok := f.bodies[bodyKey(accountID, folder, uid)]; ok {
		body = b.BodyText
	}
	f.draftTick++
	d := mail.Draft{
		ID: "adopted-" + itoa(uid), AccountID: accountID,
		To: joinAddresses(m.To), Cc: joinAddresses(m.Cc), Subject: m.Subject, Body: body,
		MessageID: m.MessageID, UpdatedAt: f.draftBase.Add(time.Duration(f.draftTick) * time.Minute),
		Revision: 1, SyncedRevision: 1, ServerFolder: folder, ServerUID: uid, ServerUIDValidity: 1,
	}
	f.drafts[d.ID] = d
	return d, nil
}

func (f *fakeService) DismissDraftConflict(ctx context.Context, id string) error {
	if d, ok := f.drafts[id]; ok {
		d.Conflict = false
		f.drafts[id] = d
	}
	return nil
}

func joinAddresses(list []mail.Address) string {
	parts := make([]string, 0, len(list))
	for _, a := range list {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, ", ")
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
