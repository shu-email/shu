package mail

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	gmailCommandTimeout = 30 * time.Second
	gmailResponseLimit  = 8 << 20
)

var errGmailResponse = errors.New("imap: malformed Gmail extension response")

// gmailWire runs one command at a time. The caller owns the connection and
// closes it on cancellation or any error, so a partial reply is never reused.
type gmailWire struct {
	conn net.Conn
	in   *bufio.Reader
	tag  int
}

func (w *gmailWire) use(conn net.Conn) {
	w.conn = conn
	w.in = bufio.NewReader(conn)
}

func (w *gmailWire) line(limit int) (string, error) {
	var line strings.Builder
	for {
		part, err := w.in.ReadSlice('\n')
		if line.Len()+len(part) > limit {
			return "", errGmailResponse
		}
		line.Write(part)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return "", err
		}
		s, ok := strings.CutSuffix(line.String(), "\r\n")
		if !ok {
			return "", errGmailResponse
		}
		return s, nil
	}
}

type gmailStatus string

func (s gmailStatus) Error() string { return "imap: Gmail command returned " + string(s) }

var gmailEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func gmailQuote(s string) string { return `"` + gmailEscapes.Replace(s) + `"` }

// gmailArg holds either a fixed protocol fragment or a string supplied by a
// caller. An 8-bit string is sent as a synchronizing literal on IMAP4rev1.
type gmailArg struct {
	text    string
	astring bool
}

func gmailString(s string) gmailArg { return gmailArg{text: s, astring: true} }

func (w *gmailWire) write(s string) error {
	_, err := io.WriteString(w.conn, s+"\r\n")
	return err
}

func (w *gmailWire) command(name string, args []gmailArg, untagged func(string) error, continuation func() (string, error)) error {
	if err := w.conn.SetDeadline(time.Now().Add(gmailCommandTimeout)); err != nil {
		return err
	}
	w.tag++
	tag := "A" + strconv.Itoa(w.tag)
	line := tag + " " + name
	for _, arg := range args {
		if !arg.astring {
			line += " " + arg.text
			continue
		}
		if strings.ContainsRune(arg.text, 0) {
			return errors.New("imap: Gmail command argument contains NUL")
		}
		if strings.IndexFunc(arg.text, func(r rune) bool { return r < 0x20 || r > 0x7e }) == -1 {
			line += " " + gmailQuote(arg.text)
			continue
		}
		if err := w.write(line + " {" + strconv.Itoa(len(arg.text)) + "}"); err != nil {
			return err
		}
		more, err := w.reply(tag, untagged)
		if err != nil {
			return err
		}
		if !more {
			return errGmailResponse
		}
		line = arg.text
	}
	if err := w.write(line); err != nil {
		return err
	}
	for {
		more, err := w.reply(tag, untagged)
		if err != nil || !more {
			return err
		}
		if continuation == nil {
			return errGmailResponse
		}
		answer, err := continuation()
		if err != nil {
			return err
		}
		if err := w.write(answer); err != nil {
			return err
		}
	}
}

// reply returns true at a continuation and false at a tagged completion.
func (w *gmailWire) reply(tag string, untagged func(string) error) (bool, error) {
	for {
		line, err := w.line(gmailResponseLimit)
		if err != nil {
			return false, err
		}
		first, rest, _ := strings.Cut(line, " ")
		switch first {
		case "+":
			return true, nil
		case "*":
			atom, _, _ := strings.Cut(rest, " ")
			if strings.EqualFold(atom, "BYE") {
				return false, io.ErrUnexpectedEOF
			}
			if untagged != nil {
				if err := untagged(rest); err != nil {
					return false, err
				}
			} else if gmailIsFetch(rest) {
				if _, err := w.fetch(rest); err != nil {
					return false, err
				}
			}
		case tag:
			status, _, _ := strings.Cut(rest, " ")
			switch strings.ToUpper(status) {
			case "OK":
				return false, nil
			case "NO", "BAD":
				return false, gmailStatus(strings.ToUpper(status))
			default:
				return false, errGmailResponse
			}
		default:
			return false, errGmailResponse
		}
	}
}

type gmailFetchData struct {
	uid    UID
	labels []string
	id     GmailID
}

func gmailIsFetch(s string) bool {
	_, rest, _ := strings.Cut(s, " ")
	item, _, _ := strings.Cut(rest, " ")
	return strings.EqualFold(item, "FETCH")
}

func (w *gmailWire) fetch(line string) (gmailFetchData, error) {
	var out gmailFetchData
	seq, rest, _ := strings.Cut(line, " ")
	item, body, _ := strings.Cut(rest, " ")
	if _, err := gmailNumber(seq, 32); err != nil || !strings.EqualFold(item, "FETCH") {
		return out, errGmailResponse
	}
	p := gmailTokens{wire: w, rest: body, remaining: gmailResponseLimit - len(line)}
	if !p.take('(') {
		return out, errGmailResponse
	}
	for !p.take(')') {
		name, err := p.atom()
		if err != nil {
			return out, err
		}
		switch strings.ToUpper(name) {
		case "UID":
			n, err := p.number(32)
			if err != nil {
				return out, err
			}
			out.uid = UID(n)
		case "X-GM-MSGID", "X-GM-THRID":
			n, err := p.number(64)
			if err != nil {
				return out, err
			}
			if strings.EqualFold(name, "X-GM-MSGID") {
				out.id.Message = n
			} else {
				out.id.Thread = n
			}
		case "X-GM-LABELS":
			if !p.take('(') {
				return out, errGmailResponse
			}
			out.labels = []string{}
			for !p.take(')') {
				encoded, err := p.astring()
				if err != nil {
					return out, err
				}
				label, err := gmailLabelDecode(encoded)
				if err != nil {
					return out, errGmailResponse
				}
				out.labels = append(out.labels, label)
			}
		default:
			if err := p.skip(); err != nil {
				return out, err
			}
		}
	}
	if strings.TrimSpace(p.rest) != "" || (out.id.Message == 0) != (out.id.Thread == 0) {
		return out, errGmailResponse
	}
	return out, nil
}

type gmailTokens struct {
	wire      *gmailWire
	rest      string
	remaining int
}

func (p *gmailTokens) take(b byte) bool {
	p.rest = strings.TrimLeft(p.rest, " ")
	if len(p.rest) == 0 || p.rest[0] != b {
		return false
	}
	p.rest = p.rest[1:]
	return true
}

func (p *gmailTokens) atom() (string, error) {
	p.rest = strings.TrimLeft(p.rest, " ")
	n := strings.IndexAny(p.rest, " ()\"{")
	if n < 0 {
		n = len(p.rest)
	}
	if n == 0 {
		return "", errGmailResponse
	}
	s := p.rest[:n]
	p.rest = p.rest[n:]
	return s, nil
}

func gmailNumber(s string, bits int) (uint64, error) {
	n, err := strconv.ParseUint(s, 10, bits)
	if err != nil || n == 0 {
		return 0, errGmailResponse
	}
	return n, nil
}

func (p *gmailTokens) number(bits int) (uint64, error) {
	s, err := p.atom()
	if err != nil {
		return 0, err
	}
	return gmailNumber(s, bits)
}

func (p *gmailTokens) astring() (string, error) {
	if p.take('"') {
		var out strings.Builder
		for i := 0; i < len(p.rest); i++ {
			b := p.rest[i]
			switch b {
			case '"':
				p.rest = p.rest[i+1:]
				return out.String(), nil
			case '\\':
				i++
				if i >= len(p.rest) || (p.rest[i] != '"' && p.rest[i] != '\\') {
					return "", errGmailResponse
				}
				b = p.rest[i]
			}
			out.WriteByte(b)
		}
		return "", errGmailResponse
	}
	if p.take('{') {
		length, ok := strings.CutSuffix(p.rest, "}")
		n, err := strconv.ParseUint(length, 10, 32)
		if !ok || err != nil || p.remaining < 2 || n > uint64(p.remaining-2) {
			return "", errGmailResponse
		}
		data := make([]byte, int(n))
		if _, err := io.ReadFull(p.wire.in, data); err != nil {
			return "", err
		}
		p.remaining -= len(data)
		p.rest, err = p.wire.line(p.remaining)
		p.remaining -= len(p.rest) + 2
		return string(data), err
	}
	return p.atom()
}

func (p *gmailTokens) skip() error {
	if !p.take('(') {
		_, err := p.astring()
		return err
	}
	for depth := 1; depth > 0; {
		switch {
		case p.take('('):
			depth++
		case p.take(')'):
			depth--
		default:
			if _, err := p.astring(); err != nil {
				return fmt.Errorf("imap: read Gmail FETCH item: %w", err)
			}
		}
	}
	return nil
}
