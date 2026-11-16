package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The stream is the window's default list: every account's inbox, in the
// order mail arrived, with what each message asks of the user read from
// the cache alone. It is mail the user has not dealt with yet, and the
// band above it says what that comes to, so that the list can be read
// before it is opened. Nothing is reordered and nothing is hidden: a wrong
// reading of a message costs a line in a count, not the message.

// streaming reports whether the list on screen is the stream.
func (a *app) streaming() bool {
	return a.folder == allInboxes && a.query == ""
}

// intent is what a stream row asks of the user.
type intent int

const (
	// intentNeeds is mail that asks something: a code to use, an amount
	// or a date to act on, a question to answer.
	intentNeeds intent = iota
	// intentNews is mail that only tells: it can be read later or never.
	intentNews
)

// intentOf reads one message's intent from the preview the cache keeps of
// it. It is a rule, not a model: no mail leaves the device to be read,
// and a message it reads wrong only counts in the wrong line.
func intentOf(m mail.Summary) intent {
	if len(mail.Extracted(m.Preview)) > 0 || asksQuestion(m.Preview) {
		return intentNeeds
	}
	if m.Seen {
		return intentNews
	}
	return intentNeeds
}

// asksQuestion reports that a preview asks something: mail that asks is
// mail the user has to answer.
func asksQuestion(preview string) bool {
	if strings.ContainsAny(preview, "?？") {
		return true
	}
	for _, w := range []string{"吗", "请问"} {
		if strings.Contains(preview, w) {
			return true
		}
	}
	return false
}

// streamCounts is what the rows read so far come to: those that ask
// something, those that only tell, and those with a code to copy.
type streamCounts struct {
	needs, news, codes int
}

func (a *app) streamCounts() streamCounts {
	var n streamCounts
	for i := range a.all {
		if intentOf(a.all[i]) == intentNeeds {
			n.needs++
		} else {
			n.news++
		}
		if rowHasCode(a.all[i]) {
			n.codes++
		}
	}
	return n
}

// rowHasCode reports that a row shows a code to copy.
func rowHasCode(m mail.Summary) bool {
	for _, e := range mail.Extracted(m.Preview) {
		if e.Kind == mail.KindCode {
			return true
		}
	}
	return false
}

// streamBand is the line above the stream: what the rows read so far ask
// of the user, in the words the user would use. It counts the newest page
// rather than the whole inbox, and follows the rows as they are read. One
// line, in type rather than in boxes: mail is read, so the line says how
// much of it is left to read and leaves the room to the mail.
func (a *app) streamBand(c *ui.Context, t *ui.Theme) {
	if !a.streaming() || a.loading || len(a.all) == 0 {
		return
	}
	n := a.streamCounts()
	ui.Row(c.Key("stream-band")).Shrink(0).Padding(8, 16).Gap(7).
		AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border.Alpha(0.6)).
		Children(func() {
			ui.Text(c, locales.StreamNeeds(n.needs)).FontSize(11).Bold().TextColor(t.Accent).SingleLine()
			if n.codes > 0 {
				a.bandDot(c, t)
				// A code is something to use rather than to read, so its
				// count keeps the text's color where the news' fades.
				ui.Text(c, locales.StreamCodes(n.codes)).FontSize(11).TextColor(t.Text).SingleLine()
			}
			if n.news > 0 {
				a.bandDot(c, t)
				ui.Text(c, locales.StreamNews(n.news)).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
}

// bandDot separates the band's counts.
func (a *app) bandDot(c *ui.Context, t *ui.Theme) {
	ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted.Alpha(0.6))
}

// rowExtracts are the things a row shows of its message without the
// reading pane: a code to copy, an amount, a date it asks something by.
func rowExtracts(m mail.Summary) []mail.Extract {
	return mail.Extracted(m.Preview)
}

// extractChips are a row's extracts, each a button that puts its value on
// the clipboard. A chip is what makes a code usable without opening the
// message it came in, which is most of what those messages are for. It is
// outlined rather than filled: it sits in the preview's line, and the line
// is the message's, not the chip's.
func (a *app) extractChips(c *ui.Context, t *ui.Theme, m mail.Summary, chosen bool, key string) {
	extracts := rowExtracts(m)
	if len(extracts) == 0 {
		return
	}
	fg, line := t.Text, t.Border
	if chosen {
		fg, line = t.Accent, t.Accent.Alpha(0.45)
	}
	ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
		for i, e := range extracts {
			value := e.Copy
			ui.ButtonBase(c.Key(fmt.Sprintf("%s/%d", key, i))).Label(extractLabel(e)).
				Tooltip(locales.CopyExtract()).Padding(1, 6).Radius(999).
				Border(1, line).OnClick(func() {
				a.copyExtract(key, value)
			}).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, copyIcon).FontSize(9).TextColor(fg.Alpha(0.7))
					ui.Text(c, e.Text).FontSize(10).TextColor(fg).SingleLine()
				})
			})
		}
	})
}

// extractLabel is what assistive technology says of a chip.
func extractLabel(e mail.Extract) string {
	switch e.Kind {
	case mail.KindCode:
		return locales.ExtractCode(e.Text)
	case mail.KindAmount:
		return locales.ExtractAmount(e.Text)
	default:
		return locales.ExtractDate(e.Text)
	}
}

// copyExtract puts a value on the clipboard, and says so in the row it
// came from for a moment: a code copied from the list is one the user saw
// happen, and the row goes back to showing the message.
func (a *app) copyExtract(rowKey, value string) {
	a.services.WriteClipboard(value)
	a.copied = rowKey
	if a.copiedTimer != nil {
		a.copiedTimer.Stop()
	}
	a.copiedTimer = time.AfterFunc(copiedFor, func() {
		a.post(func() { a.copied = "" })
	})
	a.post(func() {})
}

// copiedFor is how long a row says its value was copied.
const copiedFor = 1600 * time.Millisecond
