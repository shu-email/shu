package fakemail

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	gomsgmail "github.com/emersion/go-message/mail"
)

// This profile has its own small wire server because go-imap's server
// cannot advertise or dispatch custom commands. The in-memory user still
// assigns each mailbox's UIDs, including when one message has many labels.
type gmailExtServer struct {
	ln    net.Listener
	user  *imapmemserver.User
	gmail *gmailState
	mu    sync.Mutex
	done  bool
	conns map[net.Conn]struct{}
	next  uint64
	boxes map[string]map[imap.UID]*gmailExtMessage
}

type gmailExtMessage struct {
	id         uint64
	labels     []string
	attachment bool
	unread     bool
}

func newGmailExtServer(ln net.Listener, user *imapmemserver.User, gmail *gmailState, o IMAPOptions) *gmailExtServer {
	return &gmailExtServer{
		ln:   tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{o.Cert}, MinVersion: tls.VersionTLS12}),
		user: user, gmail: gmail, conns: make(map[net.Conn]struct{}),
		next: 1 << 63, boxes: make(map[string]map[imap.UID]*gmailExtMessage),
	}
}

func (s *gmailExtServer) add(folders []string, uids []imap.UID, raw []byte, flags []imap.Flag) {
	m := &gmailExtMessage{unread: !slices.Contains(flags, imap.FlagSeen)}
	if r, err := gomsgmail.CreateReader(bytes.NewReader(raw)); err == nil {
		for {
			part, err := r.NextPart()
			if err != nil {
				break
			}
			if _, ok := part.Header.(*gomsgmail.AttachmentHeader); ok {
				m.attachment = true
				break
			}
		}
		r.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	m.id = s.next
	for i, name := range folders {
		if s.boxes[name] == nil {
			s.boxes[name] = make(map[imap.UID]*gmailExtMessage)
		}
		s.boxes[name][uids[i]] = m
		label := name
		switch name {
		case "INBOX":
			label = `\Inbox`
		case gmailParent + "/" + s.gmail.naming.all:
			label = `\All`
		}
		m.labels = append(m.labels, label)
	}
}

func (s *gmailExtServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.done {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		go s.session(conn)
	}
}

func (s *gmailExtServer) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	err := s.ln.Close()
	for conn := range s.conns {
		conn.Close()
	}
	return err
}

func (s *gmailExtServer) session(conn net.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	s.gmail.opened()
	defer s.gmail.closed()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	in := bufio.NewReader(conn)
	send := func(line string) { fmt.Fprint(conn, line+"\r\n") }
	send("* OK fake Gmail IMAP")
	authed, folder, writable := false, "", false
	for {
		args, err := gmailExtArgs(in, send)
		if err != nil || len(args) < 2 {
			return
		}
		tag, cmd, args := args[0], strings.ToUpper(args[1]), args[2:]
		status := "OK command completed"
		switch cmd {
		case "CAPABILITY":
			send("* CAPABILITY IMAP4rev1 AUTH=XOAUTH2 X-GM-EXT-1")
		case "LOGIN", "AUTHENTICATE":
			authed = !s.gmail.tooMany() && s.auth(cmd, args, in, send)
			if !authed {
				status = "NO authentication failed"
			}
		case "SELECT", "EXAMINE":
			if !authed || len(args) != 1 || !slices.Contains(s.gmail.names(), args[0]) {
				status = "NO mailbox unavailable"
				break
			}
			folder, writable = args[0], cmd == "SELECT"
			send("* FLAGS (\\Seen)")
			send("* OK [UIDVALIDITY 1] UID validity")
			s.mu.Lock()
			count := len(s.boxes[folder])
			s.mu.Unlock()
			send(fmt.Sprintf("* %d EXISTS", count))
		case "UID":
			if !authed || folder == "" || !s.uid(folder, writable, args, send) {
				status = "BAD unknown UID command"
			}
		case "LOGOUT":
			send("* BYE goodbye")
			send(tag + " " + status)
			return
		default:
			status = "BAD unknown command"
		}
		send(tag + " " + status)
	}
}

func (s *gmailExtServer) auth(cmd string, args []string, in *bufio.Reader, send func(string)) bool {
	if cmd == "LOGIN" {
		return len(args) == 2 && s.user.Login(args[0], args[1]) == nil
	}
	if len(args) != 1 || !strings.EqualFold(args[0], "XOAUTH2") {
		return false
	}
	send("+ ")
	line, err := in.ReadString('\n')
	if err != nil {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
	fields := strings.Split(string(raw), "\x01")
	if err == nil && len(fields) == 4 {
		user, userOK := strings.CutPrefix(fields[0], "user=")
		token, tokenOK := strings.CutPrefix(fields[1], "auth=Bearer ")
		if userOK && tokenOK && s.user.Login(user, token) == nil {
			return true
		}
	}
	send("+ eyJzdGF0dXMiOiI0MDEifQ==")
	_, _ = in.ReadString('\n')
	return false
}

func gmailExtArgs(in *bufio.Reader, send func(string)) ([]string, error) {
	var args []string
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSuffix(line, "\r\n")
		literal := false
		for line = strings.TrimLeft(line, " "); line != ""; line = strings.TrimLeft(line, " ") {
			switch line[0] {
			case '"':
				prefix, err := strconv.QuotedPrefix(line)
				if err != nil {
					return nil, err
				}
				arg, err := strconv.Unquote(prefix)
				if err != nil {
					return nil, err
				}
				args = append(args, arg)
				line = line[len(prefix):]
			case '{':
				n, err := strconv.Atoi(strings.TrimSuffix(line[1:], "}"))
				if err != nil || n < 0 || n > 1<<20 || !strings.HasSuffix(line, "}") {
					return nil, fmt.Errorf("invalid fake Gmail literal length")
				}
				send("+ continue")
				b := make([]byte, n)
				if _, err := io.ReadFull(in, b); err != nil {
					return nil, err
				}
				args = append(args, string(b))
				line, literal = "", true
			case '(', ')':
				args = append(args, line[:1])
				line = line[1:]
			default:
				n := strings.IndexAny(line, " ()")
				if n < 0 {
					n = len(line)
				}
				args = append(args, line[:n])
				line = line[n:]
			}
		}
		if !literal {
			return args, nil
		}
	}
}

func (s *gmailExtServer) uid(folder string, writable bool, args []string, send func(string)) bool {
	if len(args) < 3 {
		return false
	}
	s.mu.Lock()
	box := s.boxes[folder]
	uids := make([]imap.UID, 0, len(box))
	for uid := range box {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	var replies []string
	cmd := strings.ToUpper(args[0])
	switch cmd {
	case "SEARCH":
		line := "* SEARCH"
		for _, uid := range uids {
			if box[uid].matches(args[1], args[2]) {
				line += fmt.Sprintf(" %d", uid)
			}
		}
		replies = append(replies, line)
	case "FETCH", "STORE":
		items := args[2:]
		if cmd == "STORE" && (!writable || len(items) < 4 || items[1] != "(" || items[len(items)-1] != ")" ||
			(items[0] != "+X-GM-LABELS" && items[0] != "-X-GM-LABELS")) {
			s.mu.Unlock()
			return false
		}
		for seq, uid := range uids {
			if !gmailExtUIDIn(args[1], uid) {
				continue
			}
			m := box[uid]
			if cmd == "STORE" {
				m.setLabels(items[0] == "+X-GM-LABELS", items[2:len(items)-1])
				replies = append(replies, m.fetch(seq+1, uid, []string{"X-GM-LABELS"}))
			} else {
				replies = append(replies, m.fetch(seq+1, uid, items))
			}
		}
	default:
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	for _, line := range replies {
		send(line)
	}
	return true
}

func gmailExtUIDIn(set string, uid imap.UID) bool {
	for part := range strings.SplitSeq(set, ",") {
		start, stop, rangeSet := strings.Cut(part, ":")
		low, err := strconv.ParseUint(start, 10, 32)
		if err != nil {
			continue
		}
		high := low
		if rangeSet {
			high, err = strconv.ParseUint(stop, 10, 32)
			if err != nil {
				continue
			}
		}
		if uint64(uid) >= low && uint64(uid) <= high {
			return true
		}
	}
	return false
}

func (m *gmailExtMessage) fetch(seq int, uid imap.UID, items []string) string {
	parts := []string{fmt.Sprintf("UID %d", uid)}
	for _, item := range items {
		switch strings.ToUpper(item) {
		case "X-GM-LABELS":
			labels := make([]string, 0, len(m.labels))
			for _, label := range m.labels {
				labels = append(labels, strconv.Quote(label))
			}
			parts = append(parts, "X-GM-LABELS ("+strings.Join(labels, " ")+")")
		case "X-GM-MSGID":
			parts = append(parts, fmt.Sprintf("X-GM-MSGID %d", m.id))
		case "X-GM-THRID":
			parts = append(parts, fmt.Sprintf("X-GM-THRID %d", m.id-1))
		}
	}
	return fmt.Sprintf("* %d FETCH (%s)", seq, strings.Join(parts, " "))
}

func (m *gmailExtMessage) setLabels(add bool, labels []string) {
	for _, label := range labels {
		if add && !slices.Contains(m.labels, label) {
			m.labels = append(m.labels, label)
		}
		if !add {
			m.labels = slices.DeleteFunc(m.labels, func(existing string) bool { return existing == label })
		}
	}
}

func (m *gmailExtMessage) matches(key, value string) bool {
	switch strings.ToUpper(key) {
	case "X-GM-LABELS":
		return slices.Contains(m.labels, value)
	case "X-GM-MSGID":
		return value == strconv.FormatUint(m.id, 10)
	case "X-GM-THRID":
		return value == strconv.FormatUint(m.id-1, 10)
	case "X-GM-RAW":
		for _, word := range strings.Fields(value) {
			switch word {
			case "has:attachment":
				if !m.attachment {
					return false
				}
			case "is:unread":
				if !m.unread {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	return false
}
