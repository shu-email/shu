package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"shu/mail"
)

// draftColumns are the draft fields every query reads.
const draftColumns = `id, account_id, to_text, cc_text, bcc_text, subject, body,
	in_reply_to, references_json, message_id, updated_at, revision, synced_revision,
	server_folder, server_uid, server_uidvalidity, pending_delete, conflict, identity`

func draftRow(r []any) mail.Draft {
	return mail.Draft{
		ID:                text(r[0]),
		AccountID:         text(r[1]),
		To:                text(r[2]),
		Cc:                text(r[3]),
		Bcc:               text(r[4]),
		Subject:           text(r[5]),
		Body:              text(r[6]),
		InReplyTo:         text(r[7]),
		References:        decodeStrings(text(r[8])),
		MessageID:         text(r[9]),
		UpdatedAt:         unix(r[10]),
		Revision:          number(r[11]),
		SyncedRevision:    number(r[12]),
		ServerFolder:      text(r[13]),
		ServerUID:         mail.UID(number(r[14])),
		ServerUIDValidity: uint32(number(r[15])),
		PendingDelete:     truth(r[16]),
		Conflict:          truth(r[17]),
		Identity:          text(r[18]),
	}
}

// Drafts returns an account's drafts, the one edited last first. Those
// whose server copy still awaits deletion are among them.
func (s *Store) Drafts(ctx context.Context, accountID string) ([]mail.Draft, error) {
	rows, err := s.query(ctx, `SELECT `+draftColumns+` FROM drafts
		WHERE account_id = ? ORDER BY updated_at DESC, rowid DESC`, accountID)
	if err != nil {
		return nil, err
	}
	attached, err := s.draftAttachments(ctx, `draft_id IN (SELECT id FROM drafts WHERE account_id = ?)`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]mail.Draft, 0, len(rows.Values))
	for _, r := range rows.Values {
		d := draftRow(r)
		d.Attachments = attached[d.ID]
		out = append(out, d)
	}
	return out, nil
}

// Draft returns one draft.
func (s *Store) Draft(ctx context.Context, id string) (mail.Draft, bool, error) {
	rows, err := s.query(ctx, `SELECT `+draftColumns+` FROM drafts WHERE id = ?`, id)
	if err != nil {
		return mail.Draft{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.Draft{}, false, nil
	}
	attached, err := s.draftAttachments(ctx, `draft_id = ?`, id)
	if err != nil {
		return mail.Draft{}, false, err
	}
	d := draftRow(rows.Values[0])
	d.Attachments = attached[id]
	return d, true, nil
}

// draftAttachments reads the attachments of the drafts a condition
// chooses, by draft, each list in the order the files were added.
func (s *Store) draftAttachments(ctx context.Context, where string, arg any) (map[string][]mail.Attachment, error) {
	rows, err := s.query(ctx, `SELECT draft_id, filename, content_type, size, content_hash
		FROM draft_attachments WHERE `+where+` ORDER BY draft_id, position`, arg)
	if err != nil {
		return nil, err
	}
	out := map[string][]mail.Attachment{}
	for _, r := range rows.Values {
		id := text(r[0])
		out[id] = append(out[id], mail.Attachment{
			Filename:    text(r[1]),
			ContentType: text(r[2]),
			Size:        number(r[3]),
			ContentHash: text(r[4]),
		})
	}
	return out, nil
}

// attachmentInsert writes one attachment row of a draft. It is one
// statement of a transaction, so guard is part of it: the row is written
// only where the guard holds.
func attachmentInsert(id string, position int, a mail.Attachment, guard string, args ...any) stmtType {
	values := append([]any{id, position, a.Filename, a.ContentType, a.Size, a.ContentHash}, args...)
	return stmt(`INSERT INTO draft_attachments (draft_id, position, filename, content_type, size, content_hash)
		SELECT ?, ?, ?, ?, ?, ? WHERE `+guard, values...)
}

// SaveDraft creates a draft as given, at revision 1 and with its
// attachments, or writes the text of an existing one and raises its
// revision. An existing row keeps its account, its Message-ID, its sync
// state and its attachments: a save made while the draft uploads must not
// undo what the upload records, and a save of the text carries a list of
// attachments that may be out of date.
func (s *Store) SaveDraft(ctx context.Context, d mail.Draft) (mail.Draft, error) {
	stmts := []stmtType{stmt(`INSERT INTO drafts (`+draftColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			to_text=excluded.to_text, cc_text=excluded.cc_text, bcc_text=excluded.bcc_text,
			subject=excluded.subject, body=excluded.body,
			in_reply_to=excluded.in_reply_to, references_json=excluded.references_json,
			identity=excluded.identity,
			updated_at=excluded.updated_at, revision=drafts.revision + 1`,
		d.ID, d.AccountID, d.To, d.Cc, d.Bcc, d.Subject, d.Body,
		d.InReplyTo, encodeJSON(d.References), d.MessageID, d.UpdatedAt.Unix(),
		d.SyncedRevision, d.ServerFolder, int64(d.ServerUID), int64(d.ServerUIDValidity),
		boolInt(d.PendingDelete), boolInt(d.Conflict), d.Identity)}
	for i, a := range d.Attachments {
		// Only the statement above tells a new row from an old one: the
		// row it made is at revision 1, the one it changed is past it.
		stmts = append(stmts, attachmentInsert(d.ID, i, a,
			`(SELECT revision FROM drafts WHERE id = ?) = 1`, d.ID))
	}
	if err := s.transaction(ctx, stmts); err != nil {
		return mail.Draft{}, err
	}
	saved, ok, err := s.Draft(ctx, d.ID)
	if err != nil {
		return mail.Draft{}, err
	}
	if !ok {
		return mail.Draft{}, mail.ErrNotFound
	}
	return saved, nil
}

// SetDraftAttachments replaces the attachments of an existing draft and
// raises its revision, as a change of its text does. It reports
// ErrNotFound when the row is gone.
func (s *Store) SetDraftAttachments(ctx context.Context, id string, list []mail.Attachment, updatedAt time.Time) (mail.Draft, error) {
	stmts := []stmtType{stmt(`DELETE FROM draft_attachments WHERE draft_id = ?`, id)}
	for i, a := range list {
		stmts = append(stmts, attachmentInsert(id, i, a, `EXISTS (SELECT 1 FROM drafts WHERE id = ?)`, id))
	}
	stmts = append(stmts, stmt(`UPDATE drafts SET revision = revision + 1, updated_at = ? WHERE id = ?`,
		updatedAt.Unix(), id))
	if err := s.transaction(ctx, stmts); err != nil {
		return mail.Draft{}, err
	}
	saved, ok, err := s.Draft(ctx, id)
	if err != nil {
		return mail.Draft{}, err
	}
	if !ok {
		return mail.Draft{}, mail.ErrNotFound
	}
	return saved, nil
}

// DraftFileRefs returns the content hashes of the attachment files that
// are still needed: those of the drafts neither discarded nor sent, and
// those of the outbox entries still to deliver. It is what keeps a file
// from being collected, so it fails rather than leave a reference out.
func (s *Store) DraftFileRefs(ctx context.Context) ([]string, error) {
	rows, err := s.query(ctx, `SELECT DISTINCT a.content_hash FROM draft_attachments a
		JOIN drafts d ON d.id = a.draft_id WHERE d.pending_delete = 0`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, text(r[0]))
	}
	rows, err = s.query(ctx, `SELECT id, envelope_json FROM outbox WHERE state NOT IN (?, ?)`,
		mail.StateSent, mail.StateCancelled)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		var envelope mail.Outgoing
		if err := json.Unmarshal([]byte(text(r[1])), &envelope); err != nil {
			return nil, fmt.Errorf("store: outbox envelope %s: %w", text(r[0]), err)
		}
		for _, a := range envelope.Attachments {
			out = append(out, a.ContentHash)
		}
	}
	return out, nil
}

// SetDraftSync records what a sync learned about a draft: its place on
// the server, the revision there, its markers and its Message-ID. It
// never writes the content or the revision, which a save may be changing.
func (s *Store) SetDraftSync(ctx context.Context, d mail.Draft) error {
	res, err := s.exec(ctx, `UPDATE drafts SET
			message_id = ?, synced_revision = ?, server_folder = ?, server_uid = ?,
			server_uidvalidity = ?, pending_delete = ?, conflict = ?
		WHERE id = ?`,
		d.MessageID, d.SyncedRevision, d.ServerFolder, int64(d.ServerUID),
		int64(d.ServerUIDValidity), boolInt(d.PendingDelete), boolInt(d.Conflict), d.ID)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return mail.ErrNotFound
	}
	return nil
}

// DeleteDraft removes a draft's row, and with it the rows of its
// attachments.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM drafts WHERE id = ?`, id)
	return err
}
