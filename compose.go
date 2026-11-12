package main

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// composeState is one compose window.
type composeState struct {
	app *app

	to      string
	cc      string
	subject string
	body    string

	inReplyTo  string
	references []string

	// source is the message being replied to, which an AI draft reads;
	// nil for a new message or a forward. guidance is what the user asks
	// the draft to say, job the draft in progress and review its payload
	// under inspection.
	source   *ai.Message
	guidance string
	job      *aiJob
	review   *aiReview

	sending bool
	sent    bool
	err     string
	note    string
	closed  bool

	win    *mygo.Window
	ctx    context.Context
	cancel context.CancelFunc
}

// openReply opens a reply to a message, which an AI draft may read.
func (a *app) openReply(m mail.Summary, body string, all bool) {
	cs := a.openCompose(replyDraft(a, m, body, all, false))
	if strings.TrimSpace(body) != "" {
		source := a.aiMessage(m, body)
		cs.source = &source
	}
}

// openCompose opens a compose window for a draft, or an empty one.
func (a *app) openCompose(out *mail.Outgoing) *composeState {
	cs := &composeState{app: a}
	if out != nil {
		cs.to = addressesText(out.To)
		cs.cc = addressesText(out.Cc)
		cs.subject = out.Subject
		cs.body = out.Text
		cs.inReplyTo = out.InReplyTo
		cs.references = append([]string(nil), out.References...)
	}
	cs.ctx, cs.cancel = context.WithCancel(context.Background())
	a.composes = append(a.composes, cs)
	if a.newWindow == nil {
		return cs
	}
	title := locales.ComposeTitle()
	lower := strings.ToLower(strings.TrimSpace(cs.subject))
	switch {
	case strings.HasPrefix(lower, "fwd:"):
		title = locales.ForwardTitle()
	case strings.HasPrefix(lower, "re:"):
		title = locales.ReplyTitle()
	}
	win := a.newWindow(mygo.WindowOptions{
		Title:           title,
		Width:           720,
		Height:          620,
		MinWidth:        520,
		MinHeight:       420,
		BackgroundColor: "light-dark(#ffffff, #18181b)",
		Content:         ui.View(cs.view),
	})
	if win != nil {
		cs.win = win
		win.OnClosed(cs.close)
	}
	return cs
}

// addressesText joins addresses for a text field.
func addressesText(list []mail.Address) string {
	if len(list) == 0 {
		return ""
	}
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.String())
	}
	return strings.Join(out, ", ")
}

// view draws the compose window.
func (cs *composeState) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Background).Padding(18, 20).Gap(10).
		OnShortcut(ui.Cmd, ui.KeyEnter, cs.send).
		OnShortcut(0, ui.KeyEscape, cs.close).Children(func() {
		cs.row(c, locales.To(), "name@example.com", &cs.to)
		cs.row(c, locales.Cc(), "", &cs.cc)
		cs.row(c, locales.Subject(), "", &cs.subject)
		if cs.source != nil && cs.app.ai != nil {
			cs.draftRow(c, t)
		}
		ui.Divider(c)
		ui.TextArea(c, &cs.body).Grow(1).MinHeight(220).Label(locales.Body()).
			Placeholder(locales.ComposeBodyPlaceholder())
		if cs.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, cs.err).FontSize(12).TextColor(t.Text).MaxLines(3).Grow(1)
			})
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			key := "Ctrl+Enter"
			if runtime.GOOS == "darwin" {
				key = "⌘↩"
			}
			ui.Text(c, locales.SendHint(key)).FontSize(11).TextColor(t.TextMuted)
			if cs.note != "" {
				ui.Text(c, cs.note).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Button(c, locales.Cancel()).OnClick(func() { cs.close() })
			label := locales.Send()
			if cs.sending {
				label = locales.Sending()
			}
			ui.PrimaryButton(c, label).Label(locales.Send()).Disabled(cs.sending).
				OnClick(func() { cs.send() })
		})
	})
	aiReviewModal(c, &cs.review)
}

// row is a compact compose field with its label inline, as mail apps
// show recipients and the subject.
func (cs *composeState) row(c *ui.Context, label, placeholder string, value *string) {
	t := c.Theme()
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(52)
		ui.TextInput(c, value).Grow(1).Label(label).Placeholder(placeholder)
	})
}

// send validates and sends the draft. The window stays open with an
// inline error when the server refuses.
func (cs *composeState) send() {
	if cs.sending || cs.closed {
		return
	}
	to, err := parseRecipients(cs.to)
	if err != nil {
		cs.err = locales.SendFailed(err.Error())
		return
	}
	if len(to) == 0 {
		cs.err = locales.SendFailed(locales.NoRecipients())
		return
	}
	cc, err := parseRecipients(cs.cc)
	if err != nil {
		cs.err = locales.SendFailed(err.Error())
		return
	}
	draft := mail.Outgoing{
		To: to, Cc: cc, Subject: strings.TrimSpace(cs.subject), Text: cs.body,
		InReplyTo: cs.inReplyTo, References: cs.references,
	}
	cs.sending = true
	cs.err = ""
	cs.note = ""
	accountID := cs.app.active
	app := cs.app
	app.run(func() {
		ctx, cancel := context.WithTimeout(cs.ctx, 60*time.Second)
		defer cancel()
		result, err := app.svc.Send(ctx, accountID, draft)
		app.post(func() {
			cs.sending = false
			if err != nil {
				cs.err = locales.SendFailed(err.Error())
				app.loadPending(app.gen)
				// The failure belongs to this window, which the mail
				// window's update does not redraw.
				cs.redraw()
				return
			}
			cs.sent = true
			if result.Warning != nil {
				cs.note = locales.SentCopyWarning()
				app.err = cs.note
			}
			app.loadMessages(app.gen)
			app.loadPending(app.gen)
			cs.close()
		})
	})
}

// parseRecipients parses one text field of addresses.
func parseRecipients(s string) ([]mail.Address, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return mail.ParseAddressList([]string{s})
}

// close cancels an in-flight send and closes the window.
func (cs *composeState) close() {
	if cs.closed {
		return
	}
	cs.closed = true
	cs.cancel()
	if cs.win != nil {
		cs.win.Close()
	}
}
