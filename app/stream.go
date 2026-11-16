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
// of the user, in the words the user would use. It is drawn from the rows
// on screen, so it counts the newest page rather than the whole inbox,
// and says so by following the rows as they are read.
func (a *app) streamBand(c *ui.Context, t *ui.Theme) {
	if !a.streaming() || a.loading || len(a.all) == 0 {
		return
	}
	n := a.streamCounts()
	ui.Row(c.Key("stream-band")).Shrink(0).Padding(6, 12, 6, 12).Gap(6).
		AlignItems(ui.Center).Wrap().BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
			a.streamPill(c, t, locales.StreamNeeds(n.needs), t.Accent, t.AccentText)
			if n.codes > 0 {
				a.streamPill(c, t, locales.StreamCodes(n.codes), t.Surface, t.Text)
			}
			if n.news > 0 {
				a.streamPill(c, t, locales.StreamNews(n.news), t.Surface, t.TextMuted)
			}
		})
}

// streamPill is one count of the band.
func (a *app) streamPill(c *ui.Context, t *ui.Theme, label string, bg, fg ui.Color) {
	ui.Text(c, label).FontSize(11).TextColor(fg).SingleLine().
		Padding(2, 8).Radius(t.Radius).Background(bg)
}

// rowExtracts are the things a row shows of its message without the
// reading pane: a code to copy, an amount, a date it asks something by.
func rowExtracts(m mail.Summary) []mail.Extract {
	return mail.Extracted(m.Preview)
}

// extractChips are a row's extracts, each a button that puts its value on
// the clipboard. A chip is what makes a code usable without opening the
// message it came in, which is most of what those messages are for.
func (a *app) extractChips(c *ui.Context, t *ui.Theme, m mail.Summary, chosen bool, key string) {
	extracts := rowExtracts(m)
	if len(extracts) == 0 {
		return
	}
	fg, bg := t.TextMuted, t.Surface
	icon := copyIcon
	if chosen {
		fg, bg = t.AccentText, t.AccentText.Alpha(0.16)
	}
	ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
		for i, e := range extracts {
			value := e.Copy
			ui.ButtonBase(c.Key(fmt.Sprintf("%s/%d", key, i))).Label(extractLabel(e)).
				Tooltip(locales.CopyExtract()).Padding(1, 5).Radius(t.Radius).
				Background(bg).OnClick(func() {
				a.copyExtract(key, value)
			}).Children(func() {
				ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon).FontSize(9)
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
