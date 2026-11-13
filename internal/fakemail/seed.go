package fakemail

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
)

// The seeded mailbox, which the dev build's seeded account reads.
const (
	SeedUser     = "dev"
	SeedPassword = "dev"
	SeedName     = "Dev Seed"
	SeedAddress  = "dev@example.com"
)

// SeedMailboxes are the seeded mailbox's folders. The server does not
// advertise special uses; Shu finds them by these names.
var SeedMailboxes = []string{"INBOX", "Archive", "Drafts", "Sent", "Junk", "Trash"}

// SeedFiller is how many plain messages, older than the rest, fill the
// inbox past the default first-sync window (mail.DefaultSyncWindow), so
// that the list pages and older mail has to be fetched on demand.
const SeedFiller = 230

// Seed fills a server started with SeedMailboxes with a mailbox that
// covers what a check of the window walks through: a conversation within
// the inbox and Sent, one across the inbox and Archive, HTML with remote
// images, attachments, text in Chinese, a long message, flags, a draft
// another client wrote, junk, trash and enough mail for several pages.
// Dates count back from now so the list shows today, yesterday and
// older; everything else is fixed.
func Seed(s *IMAP, now time.Time) error {
	ago := func(d time.Duration) time.Time { return now.Add(-d).Truncate(time.Minute) }
	me := SeedName + " <" + SeedAddress + ">"
	mia := "Mia Chen <mia@example.com>"
	leo := "Leo Park <leo@example.com>"
	wang := "王小明 <xiaoming@example.com>"

	type seeded struct {
		folder string
		flags  []imap.Flag
		raw    []byte
	}
	var all []seeded
	put := func(folder string, raw []byte, flags ...imap.Flag) {
		all = append(all, seeded{folder, flags, raw})
	}

	// A conversation of three: Mia asks, the user answers from Sent, Leo
	// follows up.
	put("INBOX", plain(header{
		from: mia, to: me, subject: "Q3 roadmap review", date: ago(50 * time.Hour), id: "roadmap-1",
	}, "Hi,\n\nCould you look at the Q3 roadmap before Thursday? The open\nquestions are in the second section.\n\nMia"), imap.FlagSeen)
	put("Sent", plain(header{
		from: me, to: mia, subject: "Re: Q3 roadmap review", date: ago(30 * time.Hour), id: "roadmap-2",
		inReplyTo: "roadmap-1", refs: []string{"roadmap-1"},
	}, "Looks good. Two notes on the timeline inline.\n\n> Could you look at the Q3 roadmap before Thursday?"), imap.FlagSeen)
	put("INBOX", plain(header{
		from: leo, to: me, cc: mia, subject: "Re: Q3 roadmap review", date: ago(3 * time.Hour), id: "roadmap-3",
		inReplyTo: "roadmap-2", refs: []string{"roadmap-1", "roadmap-2"},
	}, "Agreed with the notes. I moved the launch a week out."))

	// A conversation across folders: the original was archived, the
	// reply is in the inbox.
	put("Archive", plain(header{
		from: me, to: leo, subject: "Venue for the offsite", date: ago(9 * 24 * time.Hour), id: "offsite-1",
	}, "Leo, do we have a venue yet?"), imap.FlagSeen)
	put("INBOX", plain(header{
		from: leo, to: me, subject: "Re: Venue for the offsite", date: ago(40 * time.Minute), id: "offsite-2",
		inReplyTo: "offsite-1", refs: []string{"offsite-1"},
	}, "Booked the room on the third floor."), imap.FlagFlagged)

	// HTML whose images are remote, which the reading pane blocks.
	put("INBOX", alternative(header{
		from: "Acme Weekly <news@acme.example.com>", to: me, subject: "This week at Acme", date: ago(5 * time.Hour), id: "news-1",
	}, "This week at Acme: the new dashboard is live.",
		`<html><body><h1>This week at Acme</h1>`+
			`<p><img src="https://images.example.com/hero.png" alt="Hero" width="600" height="200"></p>`+
			`<p>The new dashboard is live. <a href="https://acme.example.com/blog">Read more</a>.</p>`+
			`</body></html>`))

	// Attachments.
	put("INBOX", attached(header{
		from: mia, to: me, subject: "Numbers for the board", date: ago(26 * time.Hour), id: "board-1",
	}, "The numbers and my notes are attached.", []attachment{
		{name: "q3-numbers.csv", typ: "text/csv", data: "month,revenue\nJuly,120\nAugust,135\nSeptember,150\n"},
		{name: "notes.txt", typ: "text/plain", data: "Revenue is up 25% over the quarter.\n"},
	}), imap.FlagSeen)

	// Chinese, to check text that is not Latin and its search.
	put("INBOX", plain(header{
		from: wang, to: me, subject: "周五团队聚餐安排", date: ago(7 * time.Hour), id: "dinner-1",
	}, "大家好，\n\n周五晚上七点在公司楼下的餐厅聚餐，请回复是否参加。\n\n小明"))

	// A meeting invitation, to walk the RSVP card.
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VEVENT\r\nUID:offsite-plan@example.com\r\nSEQUENCE:0\r\n" +
		"SUMMARY:Offsite planning\r\nDTSTART:20261120T100000Z\r\nDTEND:20261120T110000Z\r\n" +
		"LOCATION:Room 4\r\nORGANIZER;CN=Leo Park:mailto:leo@example.com\r\n" +
		"ATTENDEE;CN=Dev Seed;PARTSTAT=NEEDS-ACTION:mailto:dev@example.com\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	put("INBOX", invite(header{
		from: leo, to: me, subject: "Invitation: Offsite planning", date: ago(90 * time.Minute), id: "invite-1",
	}, "Here is the invitation for the offsite planning.", ics))

	// A long message, to scroll the reading pane.
	var long strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&long, "%d. A paragraph of the incident report, long enough to wrap across the width of the reading pane at the window's minimum size.\n\n", i)
	}
	put("INBOX", plain(header{
		from: leo, to: me, subject: "Incident report: sync outage", date: ago(4 * 24 * time.Hour), id: "incident-1",
	}, long.String()), imap.FlagSeen, imap.FlagFlagged)

	// A draft another mail client saved.
	put("Drafts", plain(header{
		from: me, to: mia, subject: "Draft: travel plans", date: ago(2 * time.Hour), id: "draft-1",
	}, "Mia, I land on Sunday evening and"), imap.FlagDraft, imap.FlagSeen)

	put("Junk", plain(header{
		from: "Prize Desk <winner@prizes.example.net>", to: me, subject: "You have won a prize", date: ago(20 * time.Hour), id: "junk-1",
	}, "Click here to claim it."))
	put("Trash", plain(header{
		from: "Calendar <calendar@example.com>", to: me, subject: "Cancelled: weekly sync", date: ago(6 * 24 * time.Hour), id: "trash-1",
	}, "This event was cancelled."), imap.FlagSeen)

	senders := []string{
		"Build Bot <ci@example.com>", "Docs Team <docs@example.com>", mia, leo, "Billing <billing@example.com>",
	}
	for i := 1; i <= SeedFiller; i++ {
		var flags []imap.Flag
		if i%4 != 0 {
			flags = append(flags, imap.FlagSeen)
		}
		put("INBOX", plain(header{
			from: senders[i%len(senders)], to: me, subject: fmt.Sprintf("Status update #%d", i),
			date: ago(time.Duration(10*24+i*6) * time.Hour), id: fmt.Sprintf("filler-%d", i),
		}, fmt.Sprintf("Status update number %d. Nothing needs your attention.", i)), flags...)
	}

	// Each folder's UIDs go up with the date, as on a server that got the
	// mail as it was sent.
	sort.SliceStable(all, func(i, j int) bool { return dateOf(all[i].raw).Before(dateOf(all[j].raw)) })
	for _, m := range all {
		if err := s.Append(m.folder, m.raw, &imap.AppendOptions{Flags: m.flags, Time: dateOf(m.raw)}); err != nil {
			return err
		}
	}
	return nil
}

type header struct {
	from, to, cc, subject string
	date                  time.Time
	id, inReplyTo         string
	refs                  []string
}

func msgID(id string) string { return "<seed-" + id + "@example.com>" }

func (h header) write(b *strings.Builder) {
	fmt.Fprintf(b, "From: %s\r\nTo: %s\r\n", encodeAddr(h.from), encodeAddr(h.to))
	if h.cc != "" {
		fmt.Fprintf(b, "Cc: %s\r\n", encodeAddr(h.cc))
	}
	fmt.Fprintf(b, "Subject: %s\r\nDate: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\n",
		encodeWord(h.subject), h.date.Format(time.RFC1123Z), msgID(h.id))
	if h.inReplyTo != "" {
		fmt.Fprintf(b, "In-Reply-To: %s\r\n", msgID(h.inReplyTo))
	}
	if len(h.refs) > 0 {
		refs := make([]string, len(h.refs))
		for i, r := range h.refs {
			refs[i] = msgID(r)
		}
		fmt.Fprintf(b, "References: %s\r\n", strings.Join(refs, " "))
	}
}

// encodeWord is s as an RFC 2047 word when it is not ASCII.
func encodeWord(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?b?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
		}
	}
	return s
}

// encodeAddr encodes the display name of "Name <addr>".
func encodeAddr(a string) string {
	name, addr, ok := strings.Cut(a, " <")
	if !ok {
		return a
	}
	return encodeWord(name) + " <" + addr
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func plain(h header, body string) []byte {
	var b strings.Builder
	h.write(&b)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(crlf(body))
	b.WriteString("\r\n")
	return []byte(b.String())
}

func alternative(h header, text, html string) []byte {
	const boundary = "seed-alt"
	var b strings.Builder
	h.write(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, crlf(text))
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n", boundary, html)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// invite builds a message with a plain text body and a calendar part.
func invite(h header, text, ics string) []byte {
	const boundary = "seed-invite"
	var b strings.Builder
	h.write(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, crlf(text))
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n%s\r\n", boundary, ics)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

type attachment struct{ name, typ, data string }

func attached(h header, text string, files []attachment) []byte {
	const boundary = "seed-mixed"
	var b strings.Builder
	h.write(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, crlf(text))
	for _, f := range files {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; name=%q\r\nContent-Disposition: attachment; filename=%q\r\n"+
			"Content-Transfer-Encoding: base64\r\n\r\n%s\r\n",
			boundary, f.typ, f.name, f.name, base64.StdEncoding.EncodeToString([]byte(f.data)))
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// dateOf reads back the Date a builder above wrote, for the message's
// internal date.
func dateOf(raw []byte) time.Time {
	for _, line := range strings.Split(string(raw), "\r\n") {
		if v, ok := strings.CutPrefix(line, "Date: "); ok {
			if t, err := time.Parse(time.RFC1123Z, v); err == nil {
				return t
			}
		}
		if line == "" {
			break
		}
	}
	return time.Time{}
}
