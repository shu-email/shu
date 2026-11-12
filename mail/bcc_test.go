package mail

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// withoutHeader removes a header, with the lines it is folded on, from the
// header of a raw message.
func withoutHeader(raw, name string) string {
	var out []string
	header, skipping := true, false
	for _, line := range strings.SplitAfter(raw, "\r\n") {
		if header {
			switch {
			case line == "\r\n":
				header = false
			case skipping && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
				continue
			default:
				key, _, found := strings.Cut(line, ":")
				skipping = found && strings.EqualFold(key, name)
				if skipping {
					continue
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "")
}

// Bcc recipients get the message and the other recipients do not see
// them: they are in the envelope (RCPT TO) and nowhere in the message, on
// every way a message leaves: sent at once, sent again from the outbox,
// built again there from its files after a restart, and sent after its
// delay. The copy saved in Sent, and it alone, names them in a Bcc header,
// so that the user sees whom it went to; it is otherwise the message
// sent.
func TestBccIsDeliveredAndNamedOnlyInTheSentCopy(t *testing.T) {
	bcc := []Address{{Name: "Erin", Email: "erin@example.com"}, {Name: "李娜", Email: "li@example.com"}}
	for _, path := range []string{"send", "retry", "rebuilt from the outbox", "after a delay"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			env := newAttachEnv(t, nil)
			out := Outgoing{
				To: []Address{{Name: "Bob", Email: "bob@example.com"}}, Cc: []Address{{Email: "carol@example.com"}},
				Bcc: bcc, Subject: "Plans", Text: "See you.",
			}
			svc := env.svc
			switch path {
			case "send":
				if _, err := svc.Send(ctx, env.a.ID, out); err != nil {
					t.Fatal(err)
				}
			case "retry", "rebuilt from the outbox":
				if path == "rebuilt from the outbox" {
					d, _ := env.attach(t, Draft{
						AccountID: env.a.ID, To: "Bob <bob@example.com>", Cc: "carol@example.com",
						Bcc: "Erin <erin@example.com>, 李娜 <li@example.com>", Subject: "Plans", Body: "See you.",
					}, env.file(t, "plan.txt", []byte("the plan")))
					out = d.outgoing()
					out.DraftID = d.ID
				}
				if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("wrong")); err != nil {
					t.Fatal(err)
				}
				if _, err := svc.Send(ctx, env.a.ID, out); err == nil {
					t.Fatal("a send with a wrong password succeeded")
				}
				if err := env.secrets.Set(PasswordKey(env.a.ID), []byte("secret")); err != nil {
					t.Fatal(err)
				}
				pending, _ := svc.PendingSends(ctx, env.a.ID)
				if len(pending) != 1 || !slices.Equal(pending[0].Envelope.Bcc, bcc) {
					t.Fatalf("outbox = %+v", pending)
				}
				if path == "rebuilt from the outbox" {
					if len(pending[0].Raw) != 0 {
						t.Fatal("the outbox kept the bytes of a message with a file")
					}
					svc = env.restart()
				}
				if _, err := svc.RetryPending(ctx, pending[0].ID); err != nil {
					t.Fatal(err)
				}
			case "after a delay":
				p, err := svc.Schedule(ctx, env.a.ID, out, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.SendScheduled(ctx, p.ID); err != nil {
					t.Fatal(err)
				}
			}

			rcpts := env.smtp.recipients()
			slices.Sort(rcpts)
			if want := []string{"bob@example.com", "carol@example.com", "erin@example.com", "li@example.com"}; !slices.Equal(rcpts, want) {
				t.Errorf("the envelope's recipients = %v, want %v", rcpts, want)
			}
			received := env.smtp.received()
			if len(received) != 1 {
				t.Fatalf("delivered %d messages", len(received))
			}
			data := received[0]
			if slices.Contains(headerNames(data), "bcc") {
				t.Errorf("the message delivered has a Bcc header:\n%s", data)
			}
			for _, a := range bcc {
				if strings.Contains(data, a.Email) {
					t.Errorf("the message delivered names %s:\n%s", a.Email, data)
				}
			}

			sent := env.imap.messages(t, "Sent")
			if len(sent) != 1 {
				t.Fatalf("Sent holds %d messages", len(sent))
			}
			kept := sent[0].Raw
			if named, _ := draftHeaders([]byte(kept)); !slices.Equal(named, bcc) {
				t.Errorf("the Sent copy names %+v as Bcc, want %+v", named, bcc)
			}
			if n := strings.Count(strings.ToLower(kept), "\r\nbcc:"); n != 1 {
				t.Errorf("the Sent copy has %d Bcc headers", n)
			}
			rest := withoutHeader(kept, "Bcc")
			for _, a := range bcc {
				if strings.Contains(rest, a.Email) {
					t.Errorf("the Sent copy names %s outside its Bcc header", a.Email)
				}
			}
			// A retry of a message without files sends it built again, and
			// saves the copy the outbox kept, whose date can differ. SMTP
			// ends the data with a line break the message may not have.
			if path != "retry" && strings.TrimRight(lineBreaks(rest), "\n") != strings.TrimRight(lineBreaks(data), "\n") {
				t.Errorf("the Sent copy is not the message sent:\n%q\n----\n%q", rest, data)
			}
			if m := sent[0].Message; len(m.To) != 1 || len(m.Cc) != 1 || m.Subject != "Plans" {
				t.Errorf("the Sent copy = %+v", m.Summary)
			}
		})
	}
}

// A draft keeps its Bcc, here and in its copy on the server, which
// another mail client opens.
func TestDraftServerCopyKeepsBcc(t *testing.T) {
	ctx := context.Background()
	svc, store, f, a := draftService(t, allCaps(), "INBOX", "Drafts")
	d := mustSaveDraft(t, svc, Draft{
		AccountID: a.ID, To: "bob@example.com", Bcc: "Erin <erin@example.com>, audit@example.com",
		Subject: "Quarterly", Body: "Numbers",
	})
	mustSyncDrafts(t, svc, a.ID)
	copies := f.messages(t, "Drafts")
	if len(copies) != 1 {
		t.Fatalf("server drafts = %d", len(copies))
	}
	bcc, _ := draftHeaders([]byte(copies[0].Raw))
	if want := []Address{{Name: "Erin", Email: "erin@example.com"}, {Email: "audit@example.com"}}; !slices.Equal(bcc, want) {
		t.Errorf("server copy Bcc = %+v, want %+v", bcc, want)
	}
	if got := mustDraft(t, svc, d.ID); got.Bcc != "Erin <erin@example.com>, audit@example.com" {
		t.Errorf("draft Bcc = %q", got.Bcc)
	}
	// The copy alone is enough: taken over by a device that lost the
	// draft, it has them.
	if err := store.DeleteDraft(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncFolder(ctx, a.ID, "Drafts", 50); err != nil {
		t.Fatal(err)
	}
	adopted, err := svc.AdoptDraft(ctx, a.ID, "Drafts", copies[0].UID)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Bcc != d.Bcc {
		t.Errorf("draft taken over has Bcc %q, want %q", adopted.Bcc, d.Bcc)
	}
}
