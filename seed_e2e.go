//go:build e2e && !mygo_nodownload

package main

// With SHU_SEED=1 the dev build starts with an account on local IMAP and
// SMTP servers that run inside the app and keep their mail in memory
// (internal/fakemail), so that a check of the window never needs a real
// mailbox. Each launch removes the account and adds it again: the
// servers start empty and on new ports, and the cache starts from the
// same mailbox every time.
//
// It is linked only with the e2e tag, and not into what `mygo build`
// makes, whose production builds always carry mygo_nodownload, even
// when GOFLAGS adds e2e. At run time it also refuses a production build.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"shu/internal/fakemail"
	"shu/mail"
)

// seedAccountID is the seeded account's ID, fixed so a launch finds the
// one the last launch added.
const seedAccountID = "seed"

func init() { seedDev = seed }

func seed(ctx context.Context, svc *mail.Service) error {
	if os.Getenv("SHU_SEED") != "1" {
		return nil
	}
	if !mygo.IsDev() {
		return errors.New("SHU_SEED is for development builds only")
	}
	cert, pool, err := fakemail.Cert()
	if err != nil {
		return err
	}
	imapServer, err := fakemail.StartIMAP(fakemail.IMAPOptions{
		Cert: cert, Username: fakemail.SeedUser, Password: fakemail.SeedPassword,
		Mailboxes: fakemail.SeedMailboxes, Logger: log.Default(),
	})
	if err != nil {
		return err
	}
	if err := fakemail.Seed(imapServer, time.Now()); err != nil {
		return err
	}
	// The SMTP server delivers nothing; what is sent lands as files, for
	// a check to read back.
	sentDir := filepath.Join(seedTempDir(), "shu-seed", "sent")
	if err := os.RemoveAll(sentDir); err != nil {
		return err
	}
	if err := os.MkdirAll(sentDir, 0o700); err != nil {
		return err
	}
	var sent atomic.Int64
	smtpServer, err := fakemail.StartSMTP(fakemail.SMTPOptions{
		Cert: cert, Username: fakemail.SeedUser, Password: fakemail.SeedPassword, Logger: log.Default(),
		OnMessage: func(raw []byte) {
			name := filepath.Join(sentDir, fmt.Sprintf("%03d.eml", sent.Add(1)))
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				log.Printf("seed: keep a sent message: %v", err)
			}
		},
	})
	if err != nil {
		return err
	}
	// The engine trusts the local servers' certificate and no other, so
	// a seeded run reaches no real mail server, whatever else the dev
	// build holds.
	svc.SetTLSConfig(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})

	imapServerAddr, err := seedServer(imapServer.Addr)
	if err != nil {
		return err
	}
	smtpServerAddr, err := seedServer(smtpServer.Addr)
	if err != nil {
		return err
	}
	if err := svc.RemoveAccount(ctx, seedAccountID); err != nil {
		return err
	}
	account := mail.Account{
		ID: seedAccountID, Name: fakemail.SeedName, Email: fakemail.SeedAddress, Username: fakemail.SeedUser,
		IMAP: imapServerAddr, SMTP: smtpServerAddr,
	}
	if err := svc.AddAccount(ctx, account, fakemail.SeedPassword); err != nil {
		return err
	}
	if err := svc.SetActive(ctx, seedAccountID); err != nil {
		return err
	}
	log.Printf("seed: account %s on IMAP %s and SMTP %s; sent mail goes to %s",
		fakemail.SeedAddress, imapServer.Addr, smtpServer.Addr, sentDir)
	return nil
}

// seedServer is a local server's address as an account's server.
func seedServer(addr string) (mail.Server, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return mail.Server{}, err
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return mail.Server{}, err
	}
	return mail.Server{Host: host, Port: n, TLS: mail.TLSStartTLS}, nil
}

// seedTempDir is a directory only the user can write to: the runtime
// directory on Linux, the per-user temporary one on macOS.
func seedTempDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}
