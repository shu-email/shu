package store

import (
	"context"

	"shu/mail"
)

// draftColumns are the draft fields every query reads.
const draftColumns = `id, account_id, to_text, cc_text, bcc_text, subject, body,
	in_reply_to, references_json, message_id, updated_at, revision, synced_revision,
	server_folder, server_uid, server_uidvalidity, pending_delete, conflict`

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
	out := make([]mail.Draft, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, draftRow(r))
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
	return draftRow(rows.Values[0]), true, nil
}

// SaveDraft creates a draft as given, at revision 1, or writes the
// content of an existing one and raises its revision. An existing row
// keeps its account, its Message-ID and its sync state: a save made while
// the draft uploads must not undo what the upload records.
func (s *Store) SaveDraft(ctx context.Context, d mail.Draft) (mail.Draft, error) {
	_, err := s.exec(ctx, `INSERT INTO drafts (`+draftColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			to_text=excluded.to_text, cc_text=excluded.cc_text, bcc_text=excluded.bcc_text,
			subject=excluded.subject, body=excluded.body,
			in_reply_to=excluded.in_reply_to, references_json=excluded.references_json,
			updated_at=excluded.updated_at, revision=drafts.revision + 1`,
		d.ID, d.AccountID, d.To, d.Cc, d.Bcc, d.Subject, d.Body,
		d.InReplyTo, encodeJSON(d.References), d.MessageID, d.UpdatedAt.Unix(),
		d.SyncedRevision, d.ServerFolder, int64(d.ServerUID), int64(d.ServerUIDValidity),
		boolInt(d.PendingDelete), boolInt(d.Conflict))
	if err != nil {
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

// DeleteDraft removes a draft's row.
func (s *Store) DeleteDraft(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `DELETE FROM drafts WHERE id = ?`, id)
	return err
}
