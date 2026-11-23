package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

type gmailClient struct {
	service   *Service
	accountID string
}

// gmailExtension uses the capability learned on the account's ordinary IMAP
// connection. The returned client opens its own connection for each operation.
func (s *Service) gmailExtension(accountID string, caps Capabilities) gmailExt {
	if !caps.GmailExt {
		return nil
	}
	return &gmailClient{service: s, accountID: accountID}
}

func gmailUIDSet(uids []UID) (imap.UIDSet, error) {
	var set imap.UIDSet
	for _, uid := range uids {
		if uid == 0 {
			return nil, errors.New("imap: zero is not a Gmail message UID")
		}
		set.AddNum(imap.UID(uid))
	}
	return set, nil
}

func gmailRead[T any](ctx context.Context, client *gmailClient, folder string, uids []UID, items string, result func(gmailFetchData) (T, bool)) (map[UID]T, error) {
	set, err := gmailUIDSet(uids)
	if err != nil {
		return nil, err
	}
	out := make(map[UID]T, len(uids))
	if len(set) == 0 {
		return out, nil
	}
	err = client.run(ctx, folder, false, func(w *gmailWire) error {
		command := "UID FETCH " + set.String() + " (UID " + items + ")"
		return w.command(command, nil, func(line string) error {
			if !gmailIsFetch(line) {
				return nil
			}
			data, err := w.fetch(line)
			if err != nil {
				return err
			}
			value, ok := result(data)
			if ok {
				if !set.Contains(imap.UID(data.uid)) {
					return errGmailResponse
				}
				out[data.uid] = value
			}
			return nil
		}, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("imap: Gmail FETCH: %w", err)
	}
	return out, nil
}

func (g *gmailClient) Labels(ctx context.Context, folder string, uids []UID) (map[UID][]string, error) {
	return gmailRead(ctx, g, folder, uids, "X-GM-LABELS", func(d gmailFetchData) ([]string, bool) {
		return d.labels, d.labels != nil
	})
}

func (g *gmailClient) IDs(ctx context.Context, folder string, uids []UID) (map[UID]GmailID, error) {
	return gmailRead(ctx, g, folder, uids, "X-GM-MSGID X-GM-THRID", func(d gmailFetchData) (GmailID, bool) {
		return d.id, d.id.Message != 0 && d.id.Thread != 0
	})
}

func (g *gmailClient) SetLabels(ctx context.Context, folder string, uids []UID, add, remove []string) error {
	set, err := gmailUIDSet(uids)
	if err != nil || len(set) == 0 || (len(add) == 0 && len(remove) == 0) {
		return err
	}
	return g.run(ctx, folder, true, func(w *gmailWire) error {
		for _, op := range []struct {
			name   string
			labels []string
		}{{"+X-GM-LABELS", add}, {"-X-GM-LABELS", remove}} {
			if len(op.labels) == 0 {
				continue
			}
			labels := make([]string, 0, len(op.labels))
			for _, label := range op.labels {
				labels = append(labels, gmailQuote(gmailLabelEncode(label)))
			}
			command := "UID STORE " + set.String() + " " + op.name + " (" + strings.Join(labels, " ") + ")"
			if err := w.command(command, nil, nil, nil); err != nil {
				return fmt.Errorf("imap: Gmail %s: %w", op.name, err)
			}
		}
		return nil
	})
}

func (g *gmailClient) RawSearch(ctx context.Context, folder, query string) ([]UID, error) {
	var uids []UID
	got := false
	err := g.run(ctx, folder, false, func(w *gmailWire) error {
		return w.command("UID SEARCH X-GM-RAW", []gmailArg{gmailString(query)}, func(line string) error {
			if gmailIsFetch(line) {
				_, err := w.fetch(line)
				return err
			}
			kind, rest, _ := strings.Cut(line, " ")
			if !strings.EqualFold(kind, "SEARCH") {
				return nil
			}
			if got {
				return errGmailResponse
			}
			got = true
			for remaining := strings.TrimSpace(rest); remaining != ""; remaining = strings.TrimLeft(remaining, " ") {
				var value string
				value, remaining, _ = strings.Cut(remaining, " ")
				n, err := gmailNumber(value, 32)
				if err != nil {
					return err
				}
				uids = append(uids, UID(n))
			}
			return nil
		}, nil)
	})
	if err == nil && !got {
		err = errGmailResponse
	}
	if err != nil {
		return nil, fmt.Errorf("imap: Gmail SEARCH: %w", err)
	}
	return uids, nil
}

func (g *gmailClient) run(ctx context.Context, folder string, writable bool, op func(*gmailWire) error) error {
	s := g.service
	a, secret, err := s.account(ctx, g.accountID)
	if err != nil {
		return err
	}
	if a.OAuth != "" {
		secret, err = s.accessToken(ctx, a, "")
		if err != nil {
			return err
		}
	}
	if folder == "" || a.Username == "" {
		return errors.New("imap: Gmail username and mailbox are required")
	}
	run := func(secret string) error {
		return s.runGmailAs(ctx, a, secret, func(w *gmailWire) error {
			command := "EXAMINE"
			if writable {
				command = "SELECT"
			}
			if err := w.command(command, []gmailArg{gmailString(gmailLabelEncode(folder))}, nil, nil); err != nil {
				return fmt.Errorf("imap: Gmail %s: %w", command, err)
			}
			return op(w)
		})
	}
	err = run(secret)
	var refused *loginError
	if a.OAuth == "" || !errors.As(err, &refused) {
		return err
	}
	secret, err = s.accessToken(ctx, a, secret)
	if err != nil {
		return err
	}
	return run(secret)
}

func (s *Service) runGmailAs(ctx context.Context, a Account, secret string, op func(*gmailWire) error) (err error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	raw, err := s.dialNetwork(dialCtx, a.IMAP.Host, a.IMAP.Port)
	if err != nil {
		return fmt.Errorf("imap: connect %s: %w", a.IMAP.Addr(), err)
	}
	defer raw.Close()
	defer whenDone(ctx, func() { raw.Close() })()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err := raw.SetDeadline(time.Now().Add(gmailCommandTimeout)); err != nil {
		return err
	}
	cfg := s.tlsConfigFor(ctx, a.IMAP.Host)
	if cfg.NextProtos == nil && a.IMAP.TLS == TLSImplicit {
		cfg.NextProtos = []string{"imap"}
	}
	w := &gmailWire{}
	startTLS := func() error {
		secure := tls.Client(raw, cfg)
		if err := secure.HandshakeContext(dialCtx); err != nil {
			return fmt.Errorf("imap: Gmail TLS: %w", err)
		}
		w.use(secure)
		return nil
	}
	switch a.IMAP.TLS {
	case TLSImplicit:
		if err := startTLS(); err != nil {
			return err
		}
	case TLSStartTLS:
		w.use(raw)
	default:
		return errors.New("imap: Gmail requires TLS")
	}
	line, err := w.line(gmailResponseLimit)
	if err != nil {
		return fmt.Errorf("imap: Gmail greeting: %w", err)
	}
	greeting := strings.Fields(line)
	if len(greeting) < 2 || greeting[0] != "*" || !strings.EqualFold(greeting[1], "OK") {
		return errGmailResponse
	}
	if a.IMAP.TLS == TLSStartTLS {
		if err := w.command("STARTTLS", nil, nil, nil); err != nil {
			return fmt.Errorf("imap: Gmail STARTTLS: %w", err)
		}
		if w.in.Buffered() != 0 {
			return errGmailResponse
		}
		if err := startTLS(); err != nil {
			return err
		}
	}
	if err := w.signIn(a, secret); err != nil {
		if errors.Is(err, gmailStatus("NO")) {
			return &loginError{err}
		}
		return fmt.Errorf("imap: Gmail login: %w", err)
	}
	caps, err := w.capabilities()
	if err != nil {
		return fmt.Errorf("imap: Gmail capabilities: %w", err)
	}
	if !capabilitiesOf(caps).GmailExt {
		return errors.New("imap: server no longer advertises X-GM-EXT-1")
	}
	return op(w)
}

func (w *gmailWire) capabilities() (imap.CapSet, error) {
	caps := make(imap.CapSet)
	err := w.command("CAPABILITY", nil, func(line string) error {
		kind, rest, _ := strings.Cut(line, " ")
		if strings.EqualFold(kind, "CAPABILITY") {
			for _, c := range strings.Fields(rest) {
				caps[imap.Cap(strings.ToUpper(c))] = struct{}{}
			}
		}
		return nil
	}, nil)
	return caps, err
}

func (w *gmailWire) signIn(a Account, secret string) error {
	if a.OAuth == "" {
		return w.command("LOGIN", []gmailArg{gmailString(a.Username), gmailString(secret)}, nil, nil)
	}
	caps, err := w.capabilities()
	if err != nil {
		return err
	}
	if !caps.Has(imap.AuthCap(xoauth2Mechanism)) {
		return errors.New("the server does not take OAuth tokens (XOAUTH2)")
	}
	x := &xoauth2Client{user: a.Username, token: secret}
	mechanism, payload, err := x.Start()
	if err != nil {
		return err
	}
	n := 0
	err = w.command("AUTHENTICATE "+mechanism, nil, nil, func() (string, error) {
		n++
		switch n {
		case 1:
			return base64.StdEncoding.EncodeToString(payload), nil
		case 2:
			response, err := x.Next(nil)
			return base64.StdEncoding.EncodeToString(response), err
		default:
			return "", errGmailResponse
		}
	})
	if err == nil && n != 1 {
		return errGmailResponse
	}
	return err
}
