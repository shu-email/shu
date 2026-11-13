package mail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// Importing and exporting mail, and backing up account settings. Import
// uploads with APPEND to a folder the user chose; an import is local
// until it does, and never sends anything anywhere but that account.
// Export reads the messages from the server, because the cache may not
// hold their bytes whole and their attachments may live on disk.

// TransferFormat is a file format mail goes in or out as.
type TransferFormat string

// The formats of the first release.
const (
	FormatMbox TransferFormat = "mbox"
	FormatEML  TransferFormat = "eml"
)

// ImportResult is what an import did: the messages the server took, and
// those it refused.
type ImportResult struct {
	Imported int
	Skipped  int
}

// ExportResult is what an export wrote.
type ExportResult struct {
	Messages int
}

// ExportRequest says what to export.
type ExportRequest struct {
	AccountID string
	// Folder is a folder's name; empty with no Refs means every folder.
	Folder string
	// Refs are chosen messages, of any folder of the account, which win
	// over Folder.
	Refs []MessageRef
	// IncludeAttachments keeps an attachment's bytes. Without it the
	// message goes as its text, with its headers.
	IncludeAttachments bool
	// Format is mbox for several messages in one stream, or eml for one.
	Format TransferFormat
}

// ImportMbox appends every message of an mbox stream to a folder, one
// APPEND each, and reports progress as it goes. A message the server
// refuses is counted and skipped; the rest go on. ctx cancels between
// messages, leaving what was appended already.
func (s *Service) ImportMbox(ctx context.Context, accountID, folder string, r io.Reader, progress func(ImportResult)) (ImportResult, error) {
	var out ImportResult
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return out, err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, folder); err != nil {
		return out, err
	} else if !ok {
		return out, ErrNotFound
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return out, err
	}
	defer c.close()
	err = readMbox(r, func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := ParseMessage(raw); err != nil {
			out.Skipped++
			s.reportImport(progress, out)
			return nil
		}
		if err := appendRaw(ctx, c, folder, raw); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			out.Skipped++
		} else {
			out.Imported++
		}
		s.reportImport(progress, out)
		return nil
	})
	return out, err
}

// ImportEML appends one message to a folder.
func (s *Service) ImportEML(ctx context.Context, accountID, folder string, raw []byte) (ImportResult, error) {
	var out ImportResult
	a, password, err := s.account(ctx, accountID)
	if err != nil {
		return out, err
	}
	if _, ok, err := s.store.Folder(ctx, accountID, folder); err != nil {
		return out, err
	} else if !ok {
		return out, ErrNotFound
	}
	if _, err := ParseMessage(raw); err != nil {
		out.Skipped = 1
		return out, nil
	}
	c, err := s.dialIMAP(ctx, a, password)
	if err != nil {
		return out, err
	}
	defer c.close()
	if err := appendRaw(ctx, c, folder, raw); err != nil {
		return out, err
	}
	out.Imported = 1
	return out, nil
}

func (s *Service) reportImport(fn func(ImportResult), result ImportResult) {
	if fn != nil {
		fn(result)
	}
}

// appendRaw puts one message into a folder, unread.
func appendRaw(ctx context.Context, c *imapSession, folder string, raw []byte) error {
	cmd := c.client.Append(folder, int64(len(raw)), &imap.AppendOptions{Time: time.Now()})
	if _, err := cmd.Write(raw); err != nil {
		cmd.Close()
		return fmt.Errorf("imap: append: %w", err)
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("imap: append: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return fmt.Errorf("imap: append: %w", err)
	}
	_ = ctx
	return nil
}

// readMbox calls fn for every message of an mbox stream. A line that
// starts "From " at the beginning of a message separates it from the
// next, and a body line ">From " is unescaped as mboxrd does.
func readMbox(r io.Reader, fn func([]byte) error) error {
	br := bufio.NewReader(r)
	var buf bytes.Buffer
	started := false
	flush := func() error {
		if !started {
			return nil
		}
		raw := bytes.TrimRight(buf.Bytes(), "\r\n")
		buf.Reset()
		started = false
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		return fn(append([]byte(nil), raw...))
	}
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			body := strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "From ") && !started:
				// The separator itself is not part of the message.
				started = true
				continue
			case strings.HasPrefix(line, "From ") && buf.Len() > 0:
				if ferr := flush(); ferr != nil {
					return ferr
				}
				started = true
				continue
			case strings.HasPrefix(body, ">From ") || strings.HasPrefix(body, ">>From "):
				line = strings.TrimPrefix(line, ">")
			}
			buf.WriteString(line)
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
	}
	return flush()
}

// Export writes the requested messages. An mbox stream separates them
// with "From " lines and escapes body lines that would look like one; eml
// is one message, so the request must name one.
func (s *Service) Export(ctx context.Context, req ExportRequest, w io.Writer, progress func(ExportResult)) (ExportResult, error) {
	var out ExportResult
	refs, _, err := s.exportRefs(ctx, req)
	if err != nil {
		return out, err
	}
	if req.Format == FormatEML && len(refs) > 1 {
		return out, errors.New("mail: an eml export holds one message")
	}
	a, password, err := s.account(ctx, req.AccountID)
	if err != nil {
		return out, err
	}
	byFolder := map[string][]MessageRef{}
	var order []string
	for _, r := range refs {
		if _, ok := byFolder[r.Folder]; !ok {
			order = append(order, r.Folder)
		}
		byFolder[r.Folder] = append(byFolder[r.Folder], r)
	}
	for _, name := range order {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		c, err := s.dialIMAP(ctx, a, password)
		if err != nil {
			return out, err
		}
		if _, err := c.client.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
			c.close()
			return out, fmt.Errorf("imap: select %s: %w", name, err)
		}
		for _, r := range byFolder[name] {
			if err := ctx.Err(); err != nil {
				c.close()
				return out, err
			}
			_, raw, err := fetchRaw(c, r.UID)
			if err != nil {
				c.close()
				return out, err
			}
			if !req.IncludeAttachments {
				raw, err = withoutAttachments(raw)
				if err != nil {
					c.close()
					return out, err
				}
			}
			if req.Format == FormatEML {
				if _, err := w.Write(raw); err != nil {
					c.close()
					return out, err
				}
				if !bytes.HasSuffix(raw, []byte("\n")) {
					_, _ = io.WriteString(w, "\n")
				}
			} else if err := writeMboxMessage(w, raw); err != nil {
				c.close()
				return out, err
			}
			out.Messages++
			if progress != nil {
				progress(out)
			}
		}
		c.close()
	}
	return out, nil
}

// exportRefs resolves a request into the messages it names.
func (s *Service) exportRefs(ctx context.Context, req ExportRequest) ([]MessageRef, string, error) {
	if len(req.Refs) > 0 {
		return req.Refs, req.Folder, nil
	}
	if req.Folder != "" {
		uids, err := s.store.FolderUIDs(ctx, req.AccountID, req.Folder)
		if err != nil {
			return nil, "", err
		}
		refs := make([]MessageRef, 0, len(uids))
		for _, uid := range uids {
			if _, ok, err := s.store.GetMessage(ctx, req.AccountID, req.Folder, uid); err != nil {
				return nil, "", err
			} else if ok {
				refs = append(refs, MessageRef{Folder: req.Folder, UID: uid})
			}
		}
		return refs, req.Folder, nil
	}
	folders, err := s.store.Folders(ctx, req.AccountID)
	if err != nil {
		return nil, "", err
	}
	var refs []MessageRef
	for _, f := range folders {
		if f.IsSystem() && (f.SpecialUse == SpecialTrash || f.SpecialUse == SpecialJunk) {
			continue
		}
		uids, err := s.store.FolderUIDs(ctx, req.AccountID, f.Name)
		if err != nil {
			return nil, "", err
		}
		for _, uid := range uids {
			if _, ok, err := s.store.GetMessage(ctx, req.AccountID, f.Name, uid); err != nil {
				return nil, "", err
			} else if ok {
				refs = append(refs, MessageRef{Folder: f.Name, UID: uid})
			}
		}
	}
	return refs, "", nil
}

// writeMboxMessage writes one message with its separator, escaping the
// body lines a reader would take for one.
func writeMboxMessage(w io.Writer, raw []byte) error {
	from := "nobody@localhost"
	date := time.Now()
	if msg, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		if addr := msg.Header.Get("From"); addr != "" {
			from = strings.Join(strings.Fields(addr), " ")
		}
		if d, err := msg.Header.Date(); err == nil {
			date = d
		}
	}
	if _, err := fmt.Fprintf(w, "From %s %s\n", mboxFrom(from), date.Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}
	lines := bytes.Split(raw, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("From ")) || bytes.HasPrefix(line, []byte(">From ")) {
			if _, err := io.WriteString(w, ">"); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if i < len(lines)-1 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, "\n\n")
	return err
}

// mboxFrom makes a From line's sender one word, as mbox readers expect.
func mboxFrom(from string) string {
	from = strings.ReplaceAll(from, " ", "_")
	if from == "" {
		return "nobody@localhost"
	}
	return from
}

// withoutAttachments rewrites a message as its headers and its plain text,
// with no part that is a file.
func withoutAttachments(raw []byte) ([]byte, error) {
	m, err := ParseMessage(raw)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	write := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}
	from := m.From.String()
	write("From", from)
	var to, cc []string
	for _, a := range m.To {
		to = append(to, a.String())
	}
	for _, a := range m.Cc {
		cc = append(cc, a.String())
	}
	write("To", strings.Join(to, ", "))
	write("Cc", strings.Join(cc, ", "))
	write("Subject", m.Subject)
	write("Date", m.Date.Format(time.RFC1123Z))
	write("Message-ID", m.MessageID)
	b.WriteString("MIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n")
	b.WriteString(m.BodyText)
	b.WriteString("\n")
	return b.Bytes(), nil
}

// ConfigBackup is the account and compose configuration a new machine can
// start from. It holds no password, token or key: those never leave the
// keychain they are in.
type ConfigBackup struct {
	Version    int             `json:"version"`
	Accounts   []Account       `json:"accounts"`
	Identities []Identity      `json:"identities,omitempty"`
	Compose    ComposeSettings `json:"compose"`
}

// BackupConfig writes the account configuration as JSON, without secrets.
func (s *Service) BackupConfig(ctx context.Context) ([]byte, error) {
	accounts, err := s.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	var identities []Identity
	for _, a := range accounts {
		list, err := s.store.Identities(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		identities = append(identities, list...)
	}
	compose, err := s.ComposeSettings(ctx)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(ConfigBackup{Version: 1, Accounts: accounts, Identities: identities, Compose: compose}, "", "  ")
}

// RestoreConfig adds accounts and identities from a backup, keeping those
// the machine already has. It stores no secret: an account it adds has to
// be given its password or sign-in once.
func (s *Service) RestoreConfig(ctx context.Context, data []byte) (int, error) {
	var backup ConfigBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		return 0, fmt.Errorf("mail: the backup: %w", err)
	}
	if backup.Version != 1 {
		return 0, fmt.Errorf("mail: backup version %d is not supported", backup.Version)
	}
	existing, err := s.store.Accounts(ctx)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, a := range existing {
		have[a.ID] = true
	}
	added := 0
	for _, a := range backup.Accounts {
		if have[a.ID] {
			continue
		}
		if err := s.store.SaveAccount(ctx, a); err != nil {
			return added, err
		}
		added++
	}
	for _, id := range backup.Identities {
		if !have[id.AccountID] && !accountAdded(backup.Accounts, id.AccountID) {
			continue
		}
		if err := s.store.SaveIdentity(ctx, id); err != nil {
			return added, err
		}
	}
	return added, nil
}

func accountAdded(accounts []Account, id string) bool {
	for _, a := range accounts {
		if a.ID == id {
			return true
		}
	}
	return false
}
