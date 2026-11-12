package mail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	gomail "github.com/wneessen/go-mail"
)

// The files attached to a draft are the user's data, not a cache: nothing
// may prune them. Each is copied, as it is attached, into the draft file
// store under the data directory, one file per content hash, so that the
// draft does not depend on the original staying where it was and what is
// sent is what was attached. The database lists which draft has which
// file; a file goes once no draft and no unsent message names it.

// The limits on what a draft carries.
const (
	// MaxAttachmentBytes is the most the attachments of one draft may
	// total. A message is built in memory to be sent, and this bounds
	// that memory. It is far above what mail servers take: their answer
	// is the limit that matters, not this.
	MaxAttachmentBytes = 100 << 20
	// AttachmentWarnBytes is the size of a message, as encoded for
	// sending, past which a mail provider may well refuse it. Providers
	// differ and change their limits, so this is one cautious number to
	// warn at, never a reason to refuse.
	AttachmentWarnBytes = 20 << 20
	// DraftAutoUploadLimit is the most attachment bytes a draft may
	// carry and still be uploaded at every pause in the typing. A server
	// copy cannot be edited, only replaced whole, so a heavier draft is
	// uploaded when its window closes and when the account syncs.
	DraftAutoUploadLimit = 1 << 20
)

// Attachment errors the UI explains.
var (
	// ErrAttachDirectory refuses a folder: only files are attached.
	ErrAttachDirectory = errors.New("mail: a folder cannot be attached")
	// ErrAttachTooLarge refuses a file that would take a draft's
	// attachments past MaxAttachmentBytes.
	ErrAttachTooLarge = errors.New("mail: the attachments of one message can total at most 100 MB")
	// ErrAttachmentMissing is a stored file that is gone or is not the
	// bytes that were attached. The message is not sent without it.
	ErrAttachmentMissing = errors.New("mail: an attached file is missing or damaged")
)

// draftFilesFolder is the draft file store's directory under the data
// directory, and incomingPrefix starts the name of a file still being
// copied in.
const (
	draftFilesFolder = "draft-files"
	incomingPrefix   = "incoming-"
)

// EncodedSize is how many bytes n bytes of attachment take in a message:
// base64 in lines of 76 characters, a little over a third more.
func EncodedSize(n int64) int64 {
	encoded := (n + 2) / 3 * 4
	return encoded + (encoded+75)/76*2
}

// AttachmentBytes is the size of the draft's attachments together.
func (d Draft) AttachmentBytes() int64 {
	var n int64
	for _, a := range d.Attachments {
		n += a.Size
	}
	return n
}

// sameAttachment reports whether two attachments are the same file under
// the same name. A draft holds such a file once.
func sameAttachment(a, b Attachment) bool {
	return a.ContentHash == b.ContentHash && a.Filename == b.Filename
}

// sameAttachments reports whether two lists name the same files in the
// same order.
func sameAttachments(a, b []Attachment) bool {
	return slices.EqualFunc(a, b, sameAttachment)
}

// SetDataDir names the directory for what must outlive the cache. The
// draft file store is a directory of its own under it.
func (s *Service) SetDataDir(dir string) { s.dataDir = dir }

// draftFilesDir is the draft file store's directory, or "" where the
// service was given none.
func (s *Service) draftFilesDir() string {
	if s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, draftFilesFolder)
}

// draftFilePath is where the file with a content hash is stored. The hash
// is checked: it may come from an outbox row or from another mail
// client's message, and it names a file.
func (s *Service) draftFilePath(hash string) (string, error) {
	dir := s.draftFilesDir()
	if dir == "" {
		return "", errors.New("mail: there is no directory for the attachments of drafts")
	}
	if len(hash) != sha256.Size*2 || strings.ContainsFunc(hash, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) {
		return "", fmt.Errorf("%w: %q is no content hash", ErrAttachmentMissing, hash)
	}
	return filepath.Join(dir, hash), nil
}

// AttachFile copies a file into the draft file store and adds it to a
// draft, which it creates as d says if it is not stored yet; the text of
// a stored draft is left alone. It returns the draft as it is now and
// the attachment the file became. The same file under the same name is
// attached once: attaching it again returns the attachment there is.
func (s *Service) AttachFile(ctx context.Context, d Draft, path string) (Draft, Attachment, error) {
	a, ok, err := s.store.Account(ctx, d.AccountID)
	if err != nil {
		return Draft{}, Attachment{}, err
	}
	if !ok {
		return Draft{}, Attachment{}, ErrNotFound
	}
	if d.ID == "" {
		d.ID = NewDraftID()
	}
	// The room left is read before the copy, so that a file far too
	// large is refused without copying it, and again once the copy is
	// in, as another file may have been attached meanwhile.
	cur, exists, err := s.store.Draft(ctx, d.ID)
	if err != nil {
		return Draft{}, Attachment{}, err
	}
	if exists && cur.PendingDelete {
		return Draft{}, Attachment{}, ErrDraftDiscarded
	}
	att, incoming, err := s.copyIn(ctx, path, MaxAttachmentBytes-cur.AttachmentBytes())
	if err != nil {
		return Draft{}, Attachment{}, err
	}
	// From here the file and the row that names it are made in one step
	// as the sweep sees it: it never finds the file without its row.
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	if err := s.keepIncoming(incoming, att.ContentHash); err != nil {
		return Draft{}, Attachment{}, err
	}
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	cur, exists, err = s.store.Draft(ctx, d.ID)
	if err != nil {
		return Draft{}, Attachment{}, err
	}
	if !exists {
		if cur, err = s.createDraft(ctx, a, d); err != nil {
			return Draft{}, Attachment{}, err
		}
	}
	if cur.PendingDelete {
		return Draft{}, Attachment{}, ErrDraftDiscarded
	}
	for _, have := range cur.Attachments {
		if sameAttachment(have, att) {
			return cur, have, nil
		}
	}
	if cur.AttachmentBytes()+att.Size > MaxAttachmentBytes {
		return Draft{}, Attachment{}, ErrAttachTooLarge
	}
	saved, err := s.store.SetDraftAttachments(ctx, d.ID, append(slices.Clone(cur.Attachments), att), s.now())
	if err != nil {
		return Draft{}, Attachment{}, err
	}
	return saved, att, nil
}

// RemoveAttachment takes a file off a draft. Its bytes stay in the store
// until the next sweep finds nothing naming them. An attachment the draft
// does not have is no error.
func (s *Service) RemoveAttachment(ctx context.Context, id string, att Attachment) (Draft, error) {
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	cur, ok, err := s.store.Draft(ctx, id)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrNotFound
	}
	if cur.PendingDelete {
		return Draft{}, ErrDraftDiscarded
	}
	kept := slices.DeleteFunc(slices.Clone(cur.Attachments), func(have Attachment) bool {
		return sameAttachment(have, att)
	})
	if len(kept) == len(cur.Attachments) {
		return cur, nil
	}
	return s.store.SetDraftAttachments(ctx, id, kept, s.now())
}

// copyIn streams a file into the draft file store under a temporary name,
// hashing it on the way, and describes it. room is the most bytes it may
// have. The file is never held in memory whole.
func (s *Service) copyIn(ctx context.Context, path string, room int64) (Attachment, string, error) {
	dir := s.draftFilesDir()
	if dir == "" {
		return Attachment{}, "", errors.New("mail: there is no directory for the attachments of drafts")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Attachment{}, "", err
	}
	if info.IsDir() {
		return Attachment{}, "", ErrAttachDirectory
	}
	if !info.Mode().IsRegular() {
		// A device or a pipe has no end to copy to.
		return Attachment{}, "", &fs.PathError{Op: "attach", Path: path, Err: errors.New("not a regular file")}
	}
	if info.Size() > room {
		return Attachment{}, "", ErrAttachTooLarge
	}
	src, err := os.Open(path)
	if err != nil {
		return Attachment{}, "", err
	}
	defer src.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, "", err
	}
	tmp, err := os.CreateTemp(dir, incomingPrefix+"*")
	if err != nil {
		return Attachment{}, "", err
	}
	incoming := tmp.Name()
	s.fileMu.Lock()
	s.incoming[filepath.Base(incoming)] = true
	s.fileMu.Unlock()
	fail := func(err error) (Attachment, string, error) {
		tmp.Close()
		s.dropIncoming(incoming)
		return Attachment{}, "", err
	}
	var (
		hash = sha256.New()
		head []byte
		size int64
		buf  = make([]byte, 256<<10)
	)
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			// The size was checked above, but a file may grow while it
			// is read.
			if size += int64(n); size > room {
				return fail(ErrAttachTooLarge)
			}
			if len(head) < sniffLen {
				head = append(head, buf[:min(n, sniffLen-len(head))]...)
			}
			hash.Write(buf[:n])
			if _, err := tmp.Write(buf[:n]); err != nil {
				return fail(err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
	}
	// The bytes are on the disk before the file gets its name: a row
	// must never name a file a crash left empty.
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	name := cleanFilename(filepath.Base(path))
	return Attachment{
		Filename:    name,
		ContentType: detectContentType(name, head),
		Size:        size,
		ContentHash: hex.EncodeToString(hash.Sum(nil)),
	}, incoming, nil
}

// keepIncoming gives a copied file its place under its content hash. The
// same bytes attached before are there already, and are kept. fileMu is
// held.
func (s *Service) keepIncoming(incoming, hash string) error {
	defer delete(s.incoming, filepath.Base(incoming))
	path, err := s.draftFilePath(hash)
	if err != nil {
		os.Remove(incoming)
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return os.Remove(incoming)
	}
	if err := os.Rename(incoming, path); err != nil {
		os.Remove(incoming)
		return err
	}
	return nil
}

// dropIncoming removes a copy that did not finish.
func (s *Service) dropIncoming(incoming string) {
	os.Remove(incoming)
	s.fileMu.Lock()
	delete(s.incoming, filepath.Base(incoming))
	s.fileMu.Unlock()
}

// storeBytes writes bytes held in memory into the draft file store, as
// those of a draft taken over from another mail client are, and returns
// their content hash. fileMu is held.
func (s *Service) storeBytes(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	path, err := s.draftFilePath(hash)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, incomingPrefix+"*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return hash, nil
}

// checkAttachments reports the first attachment whose file is not in the
// store with the size that was attached.
func (s *Service) checkAttachments(list []Attachment) error {
	for _, a := range list {
		path, err := s.draftFilePath(a.ContentHash)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != a.Size {
			return fmt.Errorf("%w: %s", ErrAttachmentMissing, a.Filename)
		}
	}
	return nil
}

// SweepDraftFiles removes the stored files that nothing names any more:
// no draft still being written and no message still to deliver. It runs
// when a draft is sent or discarded and once as the app starts, which
// also clears what a crash left half copied. A file that is named is
// never removed; one that is not may stay until the next sweep.
func (s *Service) SweepDraftFiles(ctx context.Context) error {
	dir := s.draftFilesDir()
	if dir == "" {
		return nil
	}
	// Nothing gives a stored file a new row while the sweep runs.
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	// A draft another mail client replaced comes back with the next save
	// of its window, attachments included. Reading its files and the
	// rows in one step keeps that save from falling between the two.
	s.draftMu.Lock()
	refs, err := s.store.DraftFileRefs(ctx)
	keep := make(map[string]bool, len(refs))
	for _, list := range s.draftsReplaced {
		for _, a := range list {
			keep[a.ContentHash] = true
		}
	}
	s.draftMu.Unlock()
	if err != nil {
		// Without the whole list, any file may be in use.
		return err
	}
	for _, hash := range refs {
		keep[hash] = true
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || keep[name] || s.incoming[name] {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

// attachmentFile describes a stored attachment to go-mail: the headers of
// its part, which are written here (see attachmentHeader), and a writer
// that streams the file from the store and will not pass off other bytes
// as the ones that were attached.
func (s *Service) attachmentFile(a Attachment) (*gomail.File, error) {
	path, err := s.draftFilePath(a.ContentHash)
	if err != nil {
		return nil, err
	}
	contentType, disposition := attachmentHeader(a.Filename, a.ContentType)
	return &gomail.File{
		Name: a.Filename,
		Header: textproto.MIMEHeader{
			"Content-Type":        {contentType},
			"Content-Disposition": {disposition},
		},
		Writer: func(w io.Writer) (int64, error) {
			f, err := os.Open(path)
			if err != nil {
				return 0, fmt.Errorf("%w: %s", ErrAttachmentMissing, a.Filename)
			}
			defer f.Close()
			hash := sha256.New()
			n, err := io.Copy(w, io.TeeReader(f, hash))
			if err != nil {
				return n, err
			}
			if hex.EncodeToString(hash.Sum(nil)) != a.ContentHash {
				return n, fmt.Errorf("%w: %s", ErrAttachmentMissing, a.Filename)
			}
			return n, nil
		},
	}, nil
}

// sniffLen is how much of a file's start tells its type.
const sniffLen = 512

// detectContentType finds the type to send a file as: by the extension of
// its name, else by its first bytes, else as bytes of no known type.
func detectContentType(name string, head []byte) string {
	if byExt := mime.TypeByExtension(filepath.Ext(name)); byExt != "" {
		return mediaType(byExt)
	}
	if len(head) > 0 {
		return mediaType(http.DetectContentType(head))
	}
	return "application/octet-stream"
}

// mediaType is a content type without its parameters, or the type of
// unknown bytes for one that does not parse. A text attachment is sent
// without a charset: nothing here knows which it is in. A message or a
// multipart type would promise a structure base64 is not allowed to
// wrap, so those go as plain bytes too.
func mediaType(contentType string) string {
	t, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.Contains(t, "/") ||
		strings.HasPrefix(t, "multipart/") || strings.HasPrefix(t, "message/") {
		return "application/octet-stream"
	}
	return t
}

// cleanFilename makes a name safe to write into a header and no more:
// what the user called the file is what the recipient should see.
func cleanFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	if name = strings.TrimSpace(name); name == "" {
		return "attachment"
	}
	return name
}

// attachmentHeader returns the Content-Type and the Content-Disposition
// of an attachment's part. go-mail would write them too, but it replaces
// quotes, colons and more in the name with underscores, and writes a
// name outside ASCII on one line however long, past the 998 bytes a line
// of a message may have. So both are written here, each parameter on a
// folded line of its own.
//
// A plain name goes as a quoted string in both headers. Any other name
// is written twice, in the two forms mail clients read: RFC 2231's in
// the disposition's filename, which is the standard, and RFC 2047's
// encoded words in the type's name, which the standard does not allow
// there but older clients look for.
func attachmentHeader(name, contentType string) (string, string) {
	name = cleanFilename(name)
	contentType = mediaType(contentType)
	if plainFilename(name) {
		return contentType + ";\r\n name=\"" + name + "\"", "attachment;\r\n filename=\"" + name + "\""
	}
	return contentType + ";\r\n name=\"" + encodedWords(name) + "\"",
		"attachment;\r\n " + extendedParam("filename", name)
}

// plainFilename reports a name that can stand in a quoted string as it
// is: printable ASCII with nothing to escape, nothing a careless parser
// would split at and nothing that reads as an encoded word, short enough
// for one line.
func plainFilename(name string) bool {
	if len(name) > 255 || strings.Contains(name, "=?") {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < ' ' || c > '~' || c == '"' || c == '\\' || c == ';' {
			return false
		}
	}
	return true
}

// encodedWords writes a value as RFC 2047 encoded words, one a line, each
// within the 75 characters the RFC allows and none splitting a character.
func encodedWords(value string) string {
	// 42 bytes make 56 characters of base64, 68 with the word around it,
	// which leaves the line room for the parameter's name and quotes.
	const wordBytes = 42
	var words []string
	for len(value) > 0 {
		n := 0
		for n < len(value) {
			_, size := utf8.DecodeRuneInString(value[n:])
			if n+size > wordBytes {
				break
			}
			n += size
		}
		words = append(words, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(value[:n]))+"?=")
		value = value[n:]
	}
	return strings.Join(words, "\r\n ")
}

// extendedParam writes a parameter in RFC 2231's extended form: its value
// in UTF-8, percent-encoded, and continued over as many lines of at most
// 76 characters as it takes, none splitting a character.
func extendedParam(key, value string) string {
	const lineLen = 75 // after the space a folded line starts with
	var pieces []string
	for _, r := range value {
		var piece strings.Builder
		for _, b := range []byte(string(r)) {
			if attrChar(b) {
				piece.WriteByte(b)
			} else {
				fmt.Fprintf(&piece, "%%%02X", b)
			}
		}
		pieces = append(pieces, piece.String())
	}
	if whole := key + "*=UTF-8''" + strings.Join(pieces, ""); len(whole) <= lineLen {
		return whole
	}
	var lines []string
	for len(pieces) > 0 {
		line := fmt.Sprintf("%s*%d*=", key, len(lines))
		if len(lines) == 0 {
			line += "UTF-8''"
		}
		// Every line but the last ends with the semicolon before the
		// next: room is left for it.
		for len(pieces) > 0 && len(line)+len(pieces[0]) < lineLen {
			line += pieces[0]
			pieces = pieces[1:]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, ";\r\n ")
}

// attrChar reports a byte RFC 2231 lets stand for itself in an extended
// value.
func attrChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", b) >= 0
}
