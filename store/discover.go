package store

import (
	"context"

	"shu/mail"
)

// The servers found or entered for a domain (migration v20). A domain
// has one row: a later lookup or the user's own entry replaces it.

// Discovery reads the servers kept for a domain, or reports that there
// are none.
func (s *Store) Discovery(ctx context.Context, domain string) (mail.Discovery, bool, error) {
	rows, err := s.query(ctx, `SELECT imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, source
		FROM discoveries WHERE domain = ?`, domain)
	if err != nil {
		return mail.Discovery{}, false, err
	}
	if len(rows.Values) == 0 {
		return mail.Discovery{}, false, nil
	}
	r := rows.Values[0]
	return mail.Discovery{
		Domain: domain,
		IMAP:   mail.Server{Host: text(r[0]), Port: int(number(r[1])), TLS: mail.TLSMode(text(r[2]))},
		SMTP:   mail.Server{Host: text(r[3]), Port: int(number(r[4])), TLS: mail.TLSMode(text(r[5]))},
		Source: mail.DiscoverySource(text(r[6])),
	}, true, nil
}

// SaveDiscovery keeps a domain's servers, replacing what it had.
func (s *Store) SaveDiscovery(ctx context.Context, d mail.Discovery) error {
	_, err := s.query(ctx, `INSERT INTO discoveries
		(domain, imap_host, imap_port, imap_tls, smtp_host, smtp_port, smtp_tls, source, found_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (domain) DO UPDATE SET
			imap_host = excluded.imap_host, imap_port = excluded.imap_port, imap_tls = excluded.imap_tls,
			smtp_host = excluded.smtp_host, smtp_port = excluded.smtp_port, smtp_tls = excluded.smtp_tls,
			source = excluded.source, found_at = excluded.found_at`,
		d.Domain, d.IMAP.Host, d.IMAP.Port, string(d.IMAP.TLS), d.SMTP.Host, d.SMTP.Port, string(d.SMTP.TLS),
		string(d.Source), s.now().Unix())
	return err
}
