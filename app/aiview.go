package app

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// aiJob is one AI request as a window shows it: running, failed or done.
// Every job starts from a click and can be cancelled while it runs.
type aiJob struct {
	busy   bool
	cancel context.CancelFunc
	model  string
	text   string
	cached bool
	// err is the failure in the user's language. setup marks a failure
	// the AI settings fix, which the notice links to; canRetry marks one
	// that a second try may still fix without a change there.
	err      string
	setup    bool
	canRetry bool
	copied   bool
	// sent rebuilds the payload of a request whose input the window does
	// not keep, as a conversation's, for "What was sent".
	sent func(context.Context) (ai.Payload, error)
}

// aiReview is the payload of a request shown for inspection. With send
// set it asks for approval before the first use of a capability; without
// it, it shows what a finished request sent.
type aiReview struct {
	open    bool
	payload ai.Payload
	send    func()
}

// triageState is the suggestions for the list on screen. They live only
// in the window: nothing about them is written to the mailbox.
type triageState struct {
	aiJob
	count int
	byKey map[string]ai.Suggestion
}

// aiLanguage names the language answers should be written in: the one the
// app shows, which is the one the user reads.
func aiLanguage() string {
	switch i18n.Locale() {
	case "zh-Hans":
		return "Simplified Chinese"
	case "en":
		return "English"
	}
	return ""
}

// aiErrorText explains a failure in the user's language. It is built from
// the kind of the error alone, so no mail text or key can appear in it.
func aiErrorText(err error) (text string, setup bool) {
	var e *ai.Error
	if !errors.As(err, &e) {
		return locales.AiErrOther(), false
	}
	switch e.Kind {
	case ai.KindDisabled:
		return locales.AiOff(), true
	case ai.KindFeatureOff:
		return locales.AiFeatureOff(), true
	case ai.KindNoKey:
		return locales.AiNoKey(), true
	case ai.KindNoModel:
		return locales.AiNoModel(), true
	case ai.KindInsecure:
		return locales.AiErrInsecure(), true
	case ai.KindBadConfig:
		return locales.AiErrConfig(), true
	case ai.KindInvalidKey:
		return locales.AiErrInvalidKey(), true
	case ai.KindProviderOff:
		return locales.AiErrProviderOff(), true
	case ai.KindKeyEndpoint:
		return locales.AiErrKeyEndpoint(), true
	case ai.KindUnsupportedModel:
		return locales.AiErrModel(), true
	case ai.KindRateLimit:
		return locales.AiErrRateLimit(), false
	case ai.KindTimeout:
		return locales.AiErrTimeout(), false
	case ai.KindUnreachable:
		return locales.AiErrUnreachable(), false
	case ai.KindServer:
		return locales.AiErrServer(e.Status), false
	case ai.KindBadResponse:
		return locales.AiErrBadResponse(), false
	case ai.KindEmpty:
		return locales.AiErrEmpty(), false
	case ai.KindKeychain:
		return locales.AiErrKeychain(), false
	case ai.KindCLINotInstalled:
		return locales.AiErrToolNotInstalled(), true
	case ai.KindCLIBadPath:
		return locales.AiErrToolBadPath(), true
	case ai.KindCLISignIn:
		return locales.AiErrToolSignIn(), true
	case ai.KindCLIFailed:
		return locales.AiErrToolFailed(), false
	case ai.KindCLIOutput:
		return locales.AiErrToolOutput(), false
	case ai.KindCLIOutdated:
		return locales.AiErrToolOutdated(), true
	}
	return locales.AiErrOther(), false
}

func aiFailed(err error) *aiJob {
	text, setup := aiErrorText(err)
	// A sign-in check that did not answer is read as signed out: the tool
	// may be signed in already, or sign in elsewhere, so the notice keeps
	// the retry it offers for failures the settings do not fix.
	return &aiJob{err: text, setup: setup, canRetry: setup && ai.KindOf(err) == ai.KindCLISignIn}
}

// aiBegin is the front half of every AI action. Without any network call
// it checks that the capability is set up, and the first time a capability
// is used it shows the exact payload and waits for the user's approval.
// Only then does run start the request.
func (a *app) aiBegin(f ai.Feature, show func(*aiReview), fail func(error),
	payload func(context.Context) (ai.Payload, error), run func(model string)) {
	if a.ai == nil {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.ai.Check(ctx, f)
		var cfg ai.Config
		if err == nil {
			cfg, err = a.ai.Config(ctx)
		}
		var p ai.Payload
		if err == nil && !cfg.HasReviewed(f) {
			p, err = payload(ctx)
		}
		a.post(func() {
			if err != nil {
				fail(err)
				return
			}
			model := cfg.ModelFor(f)
			if cfg.HasReviewed(f) {
				run(model)
				return
			}
			show(&aiReview{open: true, payload: p, send: func() {
				a.run(func() {
					ctx, cancel := a.ctx()
					defer cancel()
					_ = a.ai.MarkReviewed(ctx, f)
				})
				run(model)
			}})
		})
	})
}

// aiInspect shows what a request sent, for a result already on screen.
func (a *app) aiInspect(show func(*aiReview), payload func(context.Context) (ai.Payload, error)) {
	if a.ai == nil {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		p, err := payload(ctx)
		a.post(func() {
			if err == nil {
				show(&aiReview{open: true, payload: p})
			}
		})
	})
}

// aiLanguageTag names the language the app shows in, which a translation
// is written into unless the settings choose another.
func aiLanguageTag() string {
	switch i18n.Locale() {
	case "zh-Hans":
		return "zh-Hans"
	}
	return "en"
}

// translation is one message's translation as the window holds it: its
// text takes the place of the message in the reading pane until the user
// asks for the original back.
type translation struct {
	aiJob
	// key names the message it is of, target the language it was written
	// into, and source the language the model said the message was in.
	key    string
	target string
	source string
	// html is the message's HTML with its text translated, "" when the
	// message has none or the translation is of its plain text.
	html string
	// htmlWasOn reports that the message was shown as HTML before it was
	// translated, which is what the original comes back as.
	htmlWasOn bool
	// original reports that the message itself, not the translation, is
	// what the reading pane draws. The translation stays at hand.
	original bool
	// retry asks again for the same message, after a failure.
	retry func()
}

// translationShows reports whether a message's translation, and not the
// message itself, is what the reading pane draws.
func (a *app) translationShows(key string) bool {
	t := a.translations[key]
	return t != nil && !t.busy && t.err == "" && !t.original
}

// translatedText is a message's translated text, "" while none shows.
func (a *app) translatedText(key string) string {
	if !a.translationShows(key) {
		return ""
	}
	return a.translations[key].text
}

// translatedHTML is a message's translated HTML, "" while none shows or
// the translation is of its plain text.
func (a *app) translatedHTML(key string) string {
	if !a.translationShows(key) {
		return ""
	}
	return a.translations[key].html
}

// translationLanguage is the name a target language is shown by.
func translationLanguage(tag string) string { return ai.LanguageLabel(tag) }

// translate translates the open message, or in conversation view the
// newest message of the conversation. It is the Translate button.
func (a *app) translate() {
	if a.threaded() {
		if m, msg := a.newestShown(); m != nil && msg != nil {
			a.translateMessage(*m, msg)
		}
		return
	}
	m, msg := a.current()
	if m == nil || msg == nil {
		return
	}
	a.translateMessage(*m, msg)
}

// translateMessage asks for one message in the language the settings
// choose. Its HTML, when it has one, is translated block by block so that
// the links, tables and emphasis it carries stay where they are. Like
// every capability, its first use shows exactly what would be sent and
// waits for the user's approval.
func (a *app) translateMessage(m mail.Summary, msg *mail.Message) {
	if a.ai == nil || m.DraftID != "" {
		return
	}
	key := summaryKey(m)
	if job := a.translations[key]; job != nil && job.busy {
		return
	}
	in := a.aiMessage(m, msg.BodyText)
	html := a.aiHTMLMessage(m, msg.BodyHTML)
	g := a.gen
	// The target language is a setting, which the window reads before it
	// builds a request, so the review names the language it will use.
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		target := aiLanguageTag()
		if cfg, err := a.ai.Config(ctx); err == nil {
			target = cfg.TranslateTarget(target)
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			a.beginTranslation(g, key, in, html, target)
		})
	})
}

// aiHTMLMessage is the HTML of a message the AI layer may read, or an
// empty one when the message has none.
func (a *app) aiHTMLMessage(m mail.Summary, body string) ai.HTMLMessage {
	if strings.TrimSpace(body) == "" {
		return ai.HTMLMessage{}
	}
	return ai.HTMLMessage{
		Scope: m.Account, Subject: m.Subject,
		FromName: m.From.Name, FromEmail: m.From.Email, Date: m.Date,
		HTML: body,
	}
}

// beginTranslation runs a translation of one message into one language.
func (a *app) beginTranslation(g uint64, key string, in ai.Message, html ai.HTMLMessage, target string) {
	retry := func() { a.beginTranslation(a.gen, key, in, html, target) }
	payload := func(ctx context.Context) (ai.Payload, error) {
		if html.HTML != "" {
			return a.ai.TranslateHTMLPayload(ctx, html, target)
		}
		return a.ai.TranslatePayload(ctx, in, target)
	}
	a.aiBegin(ai.FeatureTranslate,
		func(r *aiReview) { a.review = r },
		func(err error) {
			if !a.stale(g) {
				a.translations[key] = &translation{aiJob: *aiFailed(err), key: key, target: target, retry: retry}
			}
		},
		payload,
		func(model string) {
			if a.stale(g) {
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			job := &translation{aiJob: aiJob{busy: true, cancel: cancel, model: model}, key: key, target: target}
			job.sent = payload
			job.htmlWasOn = a.htmlShown(key)
			a.translations[key] = job
			a.run(func() {
				defer cancel()
				result, translated, err := a.translateResult(ctx, in, html, target)
				a.post(func() {
					if a.translations[key] != job {
						return
					}
					switch {
					case ai.KindOf(err) == ai.KindCancelled:
						delete(a.translations, key)
					case err != nil:
						a.translations[key] = &translation{aiJob: *aiFailed(err), key: key, target: target, retry: retry}
					default:
						job.busy = false
						job.text, job.model, job.cached = result.Text, result.Model, result.Cached
						job.source, job.html = result.Source, translated
						if translated == "" && html.HTML != "" {
							// The HTML request fell back to plain text, so
							// that is what was sent.
							job.sent = func(ctx context.Context) (ai.Payload, error) {
								return a.ai.TranslatePayload(ctx, in, target)
							}
						}
						a.showTranslation(key)
					}
				})
			})
		})
}

// translateResult asks for the translation of a message, as HTML when it
// has it. A model that answers the HTML request with something that cannot
// be read still translates: the second request sends the plain text.
func (a *app) translateResult(ctx context.Context, in ai.Message, html ai.HTMLMessage, target string) (ai.Translation, string, error) {
	if html.HTML != "" {
		result, err := a.ai.TranslateHTML(ctx, html, target)
		if err == nil {
			return result.Translation, result.HTML, nil
		}
		if ai.KindOf(err) != ai.KindBadResponse {
			return ai.Translation{}, "", err
		}
	}
	result, err := a.ai.Translate(ctx, in, target)
	return result, "", err
}

// htmlShown reports whether a message is shown as HTML now, which is what
// the original comes back as after a translation.
func (a *app) htmlShown(key string) bool {
	if a.threaded() {
		cv := a.conv
		return cv != nil && cv.html == key
	}
	return a.htmlOn
}

// showTranslation puts a finished translation on screen, taking the web
// view when it has HTML to show.
func (a *app) showTranslation(key string) {
	job := a.translations[key]
	if job == nil {
		return
	}
	if job.html != "" {
		a.claimTranslationHTML(key)
	} else {
		if cv := a.conv; cv != nil && cv.html == key {
			cv.html = ""
		}
		if !a.threaded() {
			a.htmlOn = false
		}
	}
	a.htmlKey = ""
	a.syncHTMLView()
}

// claimTranslationHTML shows a message's translated HTML in the window's
// one web view. In conversation view only one message holds it, so the
// newest claim takes it from the one before, whose translation shows as
// text until it is claimed again.
func (a *app) claimTranslationHTML(key string) {
	if a.threaded() {
		if cv := a.conv; cv != nil && cv.html == key {
			cv.html = ""
		}
		a.translationHTMLKey = key
	} else {
		a.htmlOn = false
	}
	a.htmlKey = ""
}

// releaseTranslationHTML gives the web view back to what held it before a
// translation did.
func (a *app) releaseTranslationHTML(key string) {
	if a.translationHTMLKey == key {
		a.translationHTMLKey = ""
	}
	a.htmlKey = ""
}

// toggleTranslation switches the reading pane between a message and its
// translation, and puts the original back as HTML when that is how it was
// read.
func (a *app) toggleTranslation(key string) {
	tr := a.translations[key]
	if tr == nil {
		return
	}
	tr.original = !tr.original
	switch {
	case tr.original:
		a.releaseTranslationHTML(key)
		if tr.htmlWasOn {
			if a.threaded() {
				if cv := a.conv; cv != nil {
					cv.html = key
				}
			} else {
				a.htmlOn = true
			}
		}
	case tr.html != "":
		a.claimTranslationHTML(key)
	}
	a.htmlKey = ""
	a.syncHTMLView()
}

// dismissTranslation drops a translation, cancelling it while it runs, and
// gives the reading pane back to the message.
func (a *app) dismissTranslation(job *translation) {
	if job.cancel != nil {
		job.cancel()
	}
	delete(a.translations, job.key)
	a.releaseTranslationHTML(job.key)
	if job.htmlWasOn {
		if a.threaded() {
			if cv := a.conv; cv != nil {
				cv.html = job.key
			}
		} else {
			a.htmlOn = true
		}
	}
	a.syncHTMLView()
}

// translationStrip draws what a translation is, above the text it
// replaced: its progress, its failure, or the language it came from with
// the controls around it.
func (a *app) translationStrip(c *ui.Context, t *ui.Theme, job *translation) {
	dismiss := func() { a.dismissTranslation(job) }
	switch {
	case job.busy:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(8).Background(t.Accent.Alpha(0.06)).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.Translating(job.model)).FontSize(12).TextColor(t.TextMuted).Grow(1)
			ui.Button(c, locales.Cancel()).OnClick(dismiss)
		})
	case job.err != "":
		bannerColumn(c, "banner/translation/"+job.key, t.Accent.Alpha(0.07)).Children(func() {
			a.aiNotice(c, t, &job.aiJob, locales.AiDismissTranslation(), job.retry, dismiss)
		})
	default:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 10).Radius(8).Background(t.Accent.Alpha(0.06)).Children(func() {
			ui.Icon(c, languagesIcon).FontSize(14).TextColor(t.Accent)
			claim := locales.TranslatedInto(translationLanguage(job.target))
			if job.source != "" {
				claim = locales.TranslatedFrom(job.source)
			}
			ui.Text(c, claim).FontSize(12).Grow(1).MinWidth(0).SingleLine()
			ui.Badge(c, job.model).Background(t.Surface).TextColor(t.TextMuted)
			if job.cached {
				ui.Text(c, locales.AiCachedNote()).FontSize(11).TextColor(t.TextMuted)
			}
			label := locales.ShowOriginal()
			if job.original {
				label = locales.ShowTranslation()
			}
			ui.ButtonBase(c).Label(label).OnClick(func() { a.toggleTranslation(job.key) }).Children(func() {
				ui.Text(c, label).FontSize(11).TextColor(t.Accent)
			})
			copyLabel := locales.AiCopy()
			if job.copied {
				copyLabel = locales.AiCopied()
			}
			ui.Button(c, "").Label(copyLabel).Tooltip(copyLabel).Padding(4, 6).
				OnClick(func() {
					a.services.WriteClipboard(job.text)
					job.copied = true
				}).Children(func() { ui.Icon(c, copyIcon).FontSize(14) })
			ui.ButtonBase(c).Label(locales.AiWhatWasSent()).OnClick(func() { a.inspectJob(&job.aiJob) }).Children(func() {
				ui.Text(c, locales.AiWhatWasSent()).FontSize(11).TextColor(t.Accent)
			})
			a.iconButton(c, closeIcon, locales.AiDismissTranslation(), locales.AiDismissTranslation()).OnClick(dismiss)
		})
	}
}

// aiMessage is the part of a message the AI layer may read.
func (a *app) aiMessage(m mail.Summary, body string) ai.Message {
	return ai.Message{
		Scope:     m.Account,
		Subject:   m.Subject,
		FromName:  m.From.Name,
		FromEmail: m.From.Email,
		Date:      m.Date,
		Body:      body,
		Language:  aiLanguage(),
	}
}

// openAISettings shows the AI tab of the settings.
func (a *app) openAISettings() {
	a.openSettings(settingsTabAI)
	if a.win != nil {
		a.win.Focus()
	}
}

// summarize summarizes the open message, or in conversation view the open
// conversation. It is the Summarize button.
func (a *app) summarize() {
	if a.threaded() {
		a.summarizeThread()
		return
	}
	m, msg := a.current()
	// A draft is the user's own unsent text, not mail to summarize.
	if m == nil || msg == nil || a.ai == nil || m.DraftID != "" {
		return
	}
	key := summaryKey(*m)
	if job := a.summaries[key]; job != nil && job.busy {
		return
	}
	in := a.aiMessage(*m, msg.BodyText)
	g := a.gen
	a.aiBegin(ai.FeatureSummary,
		func(r *aiReview) { a.review = r },
		func(err error) {
			if !a.stale(g) {
				a.summaries[key] = aiFailed(err)
			}
		},
		func(ctx context.Context) (ai.Payload, error) { return a.ai.SummaryPayload(ctx, in) },
		func(model string) {
			if a.stale(g) {
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			job := &aiJob{busy: true, cancel: cancel, model: model}
			a.summaries[key] = job
			a.run(func() {
				defer cancel()
				result, err := a.ai.Summarize(ctx, in)
				a.post(func() {
					// A job dismissed or replaced meanwhile is not shown.
					if a.summaries[key] != job {
						return
					}
					switch {
					case ai.KindOf(err) == ai.KindCancelled:
						delete(a.summaries, key)
					case err != nil:
						a.summaries[key] = aiFailed(err)
					default:
						job.busy = false
						job.text, job.model, job.cached = result.Text, result.Model, result.Cached
					}
				})
			})
		})
}

// summarizeThread summarizes the conversation on show as it shows: each
// message once, oldest first. The bodies of the collapsed messages are
// read first, from the cache or else the server, and nothing is marked
// read.
func (a *app) summarizeThread() {
	cv := a.conv
	if a.ai == nil || cv == nil || a.openRow() != a.selected || len(cv.msgs) == 0 {
		return
	}
	key := threadKey(cv.account, cv.thread)
	if job := a.summaries[key]; job != nil && job.busy {
		return
	}
	msgs := append([]mail.Summary(nil), cv.msgs...)
	have := map[string]bool{}
	for _, m := range msgs {
		if _, ok := a.bodies[summaryKey(m)]; ok {
			have[summaryKey(m)] = true
		}
	}
	g, account := a.gen, cv.account
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		fetched := map[string]mail.FetchResult{}
		var err error
		for _, m := range msgs {
			k := summaryKey(m)
			if have[k] {
				continue
			}
			result, cached, e := a.svc.CachedMessage(ctx, m.Account, m.Folder, m.UID)
			if e == nil && !cached {
				result, e = a.svc.FetchMessage(ctx, m.Account, m.Folder, m.UID, false)
			}
			if e != nil {
				err = e
				break
			}
			fetched[k] = result
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			for k, r := range fetched {
				if _, ok := a.bodies[k]; !ok {
					a.bodies[k] = r
				}
			}
			if err != nil {
				a.summaries[key] = &aiJob{err: a.bodyError(account, err)}
				return
			}
			a.summarizeConversation(g, key, a.aiConversation(account, msgs))
		})
	})
}

// aiConversation is the part of an account's conversation the AI layer
// may read: of each message the sender, the date and the plain text.
func (a *app) aiConversation(account string, msgs []mail.Summary) ai.Conversation {
	c := ai.Conversation{Scope: account, Language: aiLanguage()}
	if len(msgs) > 0 {
		c.Subject = msgs[0].Subject
	}
	for _, m := range msgs {
		body := ""
		if b, ok := a.bodies[summaryKey(m)]; ok {
			body = b.Message.BodyText
		}
		c.Messages = append(c.Messages, ai.Message{FromName: m.From.Name, FromEmail: m.From.Email, Date: m.Date, Body: body})
	}
	return c
}

// summarizeConversation asks for the summary of a conversation whose
// text is loaded, after the review of its payload the first time.
func (a *app) summarizeConversation(g uint64, key string, in ai.Conversation) {
	payload := func(ctx context.Context) (ai.Payload, error) { return a.ai.ConversationPayload(ctx, in) }
	a.aiBegin(ai.FeatureSummary,
		func(r *aiReview) { a.review = r },
		func(err error) {
			if !a.stale(g) {
				a.summaries[key] = aiFailed(err)
			}
		},
		payload,
		func(model string) {
			if a.stale(g) {
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			job := &aiJob{busy: true, cancel: cancel, model: model, sent: payload}
			a.summaries[key] = job
			a.run(func() {
				defer cancel()
				result, err := a.ai.SummarizeConversation(ctx, in)
				a.post(func() {
					if a.summaries[key] != job {
						return
					}
					switch {
					case ai.KindOf(err) == ai.KindCancelled:
						delete(a.summaries, key)
					case err != nil:
						a.summaries[key] = aiFailed(err)
					default:
						job.busy = false
						job.text, job.model, job.cached = result.Text, result.Model, result.Cached
					}
				})
			})
		})
}

// inspectJob shows what a finished summary sent.
func (a *app) inspectJob(job *aiJob) {
	if job.sent != nil {
		a.aiInspect(func(r *aiReview) { a.review = r }, job.sent)
		return
	}
	a.inspectSummary()
}

// inspectSummary shows what the summary of the open message sent.
func (a *app) inspectSummary() {
	m, msg := a.current()
	if m == nil || msg == nil {
		return
	}
	in := a.aiMessage(*m, msg.BodyText)
	a.aiInspect(func(r *aiReview) { a.review = r },
		func(ctx context.Context) (ai.Payload, error) { return a.ai.SummaryPayload(ctx, in) })
}

// summaryCard draws the summary of the open message, or its progress or
// failure, above the body.
func (a *app) summaryCard(c *ui.Context, t *ui.Theme, key string, job *aiJob) {
	dismiss := func() {
		if job.cancel != nil {
			job.cancel()
		}
		delete(a.summaries, key)
	}
	ui.Column(c).Padding(12, 14).Gap(8).Radius(10).Border(1, t.Accent.Alpha(0.35)).
		Background(t.Accent.Alpha(0.06)).Children(func() {
		switch {
		case job.busy:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, locales.AiSummarizing(job.model)).FontSize(12).TextColor(t.TextMuted).Grow(1)
				ui.Button(c, locales.Cancel()).OnClick(dismiss)
			})
		case job.err != "":
			a.aiNotice(c, t, job, locales.AiDismissSummary(), a.summarize, dismiss)
		default:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, sparklesIcon).FontSize(14).TextColor(t.Accent)
				ui.Text(c, locales.AiSummary()).FontSize(12).Bold()
				ui.Badge(c, job.model).Background(t.Surface).TextColor(t.TextMuted)
				if job.cached {
					ui.Text(c, locales.AiCachedNote()).FontSize(11).TextColor(t.TextMuted)
				}
				ui.Spacer(c)
				label := locales.AiCopy()
				if job.copied {
					label = locales.AiCopied()
				}
				ui.Button(c, "").Label(label).Tooltip(label).Padding(4, 6).
					OnClick(func() {
						a.services.WriteClipboard(job.text)
						job.copied = true
					}).Children(func() { ui.Icon(c, copyIcon).FontSize(14) })
				a.iconButton(c, closeIcon, locales.AiDismissSummary(), locales.AiDismissSummary()).OnClick(dismiss)
			})
			ui.Text(c, job.text).FontSize(13).LineHeight(1.55).Selectable()
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.AiGenerated(job.model)).FontSize(11).TextColor(t.TextMuted).Grow(1)
				ui.ButtonBase(c).Label(locales.AiWhatWasSent()).OnClick(func() { a.inspectJob(job) }).Children(func() {
					ui.Text(c, locales.AiWhatWasSent()).FontSize(11).TextColor(t.Accent)
				})
			})
		}
	})
}

// aiNotice draws a failed request: what went wrong, and either the way to
// the settings that fix it or a retry. Its close button is named for what
// it closes, as a window can show several notices at once.
func (a *app) aiNotice(c *ui.Context, t *ui.Theme, job *aiJob, dismissLabel string, retry, dismiss func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		icon, color := alertIcon, t.Warning
		if job.setup {
			icon, color = sparklesIcon, t.Accent
		}
		ui.Icon(c, icon).FontSize(14).TextColor(color)
		ui.Text(c, job.err).FontSize(12).Grow(1).MaxLines(3)
		if job.setup {
			ui.Button(c, locales.AiOpenSettings()).OnClick(func() {
				dismiss()
				a.openAISettings()
			})
		}
		if retry != nil && (!job.setup || job.canRetry) {
			ui.Button(c, locales.Retry()).OnClick(retry)
		}
		a.iconButton(c, closeIcon, dismissLabel, dismissLabel).OnClick(dismiss)
	})
}

// aiReviewModal floats a payload over the window for inspection. It is
// shared by the mail window and the compose windows.
func aiReviewModal(c *ui.Context, slot **aiReview) {
	r := *slot
	if r == nil {
		return
	}
	if !r.open {
		*slot = nil
		return
	}
	t := c.Theme()
	p := r.payload
	provider := string(p.Provider)
	if preset, ok := ai.ProviderByID(p.Provider); ok && preset.Name != "" {
		provider = preset.Name
	} else if p.Provider == ai.ProviderLocal {
		provider = locales.AiProviderLocal()
	} else if p.Provider == ai.ProviderCustom {
		provider = locales.AiProviderCustom()
	}
	ui.Modal(c, &r.open, func() {
		ui.Column(c).Width(560).Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, sparklesIcon).FontSize(16).TextColor(t.Accent)
				ui.Text(c, locales.AiReviewTitle()).FontSize(17).Bold()
			})
			ui.Text(c, locales.AiReviewTarget(provider, p.Model, p.Host)).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, locales.AiReviewText()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Scroll(c).Height(190).Radius(8).Border(1, t.Border).Background(t.Surface).Children(func() {
				ui.Text(c, p.Text).FontSize(12).LineHeight(1.5).Padding(10, 12).Selectable()
			})
			size := locales.AiReviewSize(p.Chars, p.Limit)
			switch {
			case p.Feature == ai.FeatureTriage:
				size = locales.AiReviewTriageSize(p.Messages, p.Chars)
			case p.Feature == ai.FeatureTranslate:
				size = locales.AiReviewTranslateSize(p.Chars, p.Limit)
			case p.Limit == ai.ConversationLimit:
				size = locales.AiReviewConversationSize(p.Messages, p.Chars, p.Limit)
			}
			ui.Text(c, size).FontSize(11).TextColor(t.TextMuted)
			ui.Text(c, locales.AiReviewLeftOut()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Column(c).Gap(2).Children(func() {
				left := func(s string) { ui.Text(c, "· "+s).FontSize(12) }
				if p.Omitted > 0 {
					left(locales.AiReviewEarlier(p.Omitted))
				}
				if p.Quoted > 0 {
					left(locales.AiReviewQuoted(p.Quoted))
				}
				if p.Signature {
					left(locales.AiReviewSignature())
				}
				if p.Links > 0 {
					left(locales.AiReviewLinks(p.Links))
				}
				if p.Truncated {
					left(locales.AiReviewTruncated())
				}
				left(locales.AiReviewAlways())
			})
			ui.Text(c, locales.AiReviewInstruction()).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Text(c, p.Instruction).FontSize(11).TextColor(t.TextMuted).MaxLines(4).Selectable()
			ui.Divider(c)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				if r.send == nil {
					ui.Spacer(c)
					ui.Button(c, locales.Close()).OnClick(func() { r.open = false })
					return
				}
				ui.Text(c, locales.AiReviewOnce()).FontSize(11).TextColor(t.TextMuted).Grow(1)
				ui.Button(c, locales.Cancel()).OnClick(func() { r.open = false })
				ui.PrimaryButton(c, locales.AiReviewSend(p.Model)).OnClick(func() {
					r.open = false
					r.send()
				})
			})
		})
	})
}

// triageList asks for suggestions on the messages the list shows. It is
// the Triage button.
func (a *app) triageList() {
	if a.ai == nil || len(a.all) == 0 || a.draftsList() || (a.triage != nil && a.triage.busy) {
		return
	}
	items := a.triageItems()
	g := a.gen
	language := aiLanguage()
	a.aiBegin(ai.FeatureTriage,
		func(r *aiReview) { a.review = r },
		func(err error) {
			if !a.stale(g) {
				a.triage = &triageState{aiJob: *aiFailed(err)}
			}
		},
		func(ctx context.Context) (ai.Payload, error) { return a.ai.TriagePayload(ctx, items, language) },
		func(model string) {
			if a.stale(g) {
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			state := &triageState{aiJob: aiJob{busy: true, cancel: cancel, model: model}, count: len(items)}
			a.triage = state
			a.run(func() {
				defer cancel()
				result, err := a.ai.Triage(ctx, items, language)
				a.post(func() {
					if a.triage != state {
						return
					}
					switch {
					case ai.KindOf(err) == ai.KindCancelled:
						a.triage = nil
					case err != nil:
						a.triage = &triageState{aiJob: *aiFailed(err)}
					default:
						state.busy = false
						state.model = result.Model
						state.byKey = make(map[string]ai.Suggestion, len(result.Suggestions))
						for _, s := range result.Suggestions {
							state.byKey[s.ID] = s
						}
					}
				})
			})
		})
}

// triageItems lists the rows on screen for a triage, newest first: a
// conversation is one item, told by its newest message.
func (a *app) triageItems() []ai.TriageItem {
	n := min(len(a.all), ai.TriageLimit)
	items := make([]ai.TriageItem, 0, n)
	for i, m := range a.all[:n] {
		unread := !m.Seen
		if c := a.convAt(i); c != nil {
			unread = c.Unread
		}
		items = append(items, ai.TriageItem{
			ID: a.rowKey(i), FromName: m.From.Name, FromEmail: m.From.Email,
			Subject: m.Subject, Preview: m.Preview, Unread: unread,
		})
	}
	return items
}

// clearTriage drops the suggestions, cancelling a triage in flight.
func (a *app) clearTriage() {
	if a.triage != nil && a.triage.cancel != nil {
		a.triage.cancel()
	}
	a.triage = nil
}

// suggestion is the triage suggestion for the row with a key, if any.
func (a *app) suggestion(key string) (ai.Suggestion, bool) {
	if a.triage == nil || a.triage.byKey == nil {
		return ai.Suggestion{}, false
	}
	s, ok := a.triage.byKey[key]
	return s, ok
}

// selectSuggested chooses the rows of one group, so the user can act on
// them with the toolbar. Choosing is all it does.
func (a *app) selectSuggested(group ai.Group) {
	a.sel.Clear()
	first := -1
	for i := range a.all {
		if s, ok := a.suggestion(a.rowKey(i)); ok && s.Group == group {
			a.sel.Add(a.rowKey(i))
			if first < 0 {
				first = i
			}
		}
	}
	if first >= 0 {
		a.selected = first
		a.details = false
		a.reloadBody()
	}
}

func groupLabel(g ai.Group) string {
	switch g {
	case ai.GroupNeedsYou:
		return locales.AiNeedsYou()
	case ai.GroupCanWait:
		return locales.AiCanWait()
	}
	return locales.AiCanArchive()
}

// triageBanner draws the triage's progress, failure or result under the
// list header.
func (a *app) triageBanner(c *ui.Context, t *ui.Theme) {
	s := a.triage
	bannerColumn(c, "banner/triage", t.Accent.Alpha(0.07)).Children(func() {
		switch {
		case s.busy:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, locales.AiTriaging(s.count, s.model)).FontSize(12).TextColor(t.TextMuted).Grow(1)
				ui.Button(c, locales.Cancel()).OnClick(a.clearTriage)
			})
		case s.err != "":
			a.aiNotice(c, t, &s.aiJob, locales.AiDismissSuggestions(), a.triageList, a.clearTriage)
		default:
			var needs, wait, archive int
			for i := range a.all {
				if sug, ok := a.suggestion(a.rowKey(i)); ok {
					switch sug.Group {
					case ai.GroupNeedsYou:
						needs++
					case ai.GroupCanWait:
						wait++
					case ai.GroupCanArchive:
						archive++
					}
				}
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, sparklesIcon).FontSize(14).TextColor(t.Accent)
				ui.Text(c, locales.AiSuggestions()).FontSize(12).Bold()
				ui.Badge(c, s.model).Background(t.Surface).TextColor(t.TextMuted)
				ui.Spacer(c)
				a.iconButton(c, closeIcon, locales.AiDismissSuggestions(), locales.AiDismissSuggestions()).
					OnClick(a.clearTriage)
			})
			ui.Text(c, locales.AiTriageCounts(needs, wait, archive)).FontSize(12)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.AiTriageNote()).FontSize(11).TextColor(t.TextMuted).Grow(1)
				if archive > 0 {
					ui.Button(c, locales.AiSelectArchive()).OnClick(func() { a.selectSuggested(ai.GroupCanArchive) })
				}
			})
		}
	})
}

// suggestionLine replaces a row's preview with the suggestion and its
// reason, so the row keeps its height.
func (a *app) suggestionLine(c *ui.Context, t *ui.Theme, s ai.Suggestion, chosen bool) {
	color, text := t.TextMuted, t.TextMuted
	switch s.Group {
	case ai.GroupNeedsYou:
		color = t.Warning
	case ai.GroupCanArchive:
		color = t.Success
	}
	if chosen {
		color, text = t.AccentText, t.AccentText.Alpha(0.78)
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, groupLabel(s.Group)).FontSize(10).Bold().TextColor(color).Shrink(0).
			Padding(1, 6).Radius(8).Border(1, color.Alpha(0.6))
		ui.Text(c, s.Reason).FontSize(12).SingleLine().TextColor(text).Grow(1)
	})
}

// draftWithAI writes a reply into the compose window for the user to
// edit. It never sends: the Send button stays the only way out.
func (cs *composeState) draftWithAI() {
	a := cs.app
	if a.ai == nil || cs.source == nil || cs.closed || (cs.job != nil && cs.job.busy) {
		return
	}
	in, guidance := *cs.source, strings.TrimSpace(cs.guidance)
	in.Language = aiLanguage()
	a.aiBegin(ai.FeatureDraft,
		func(r *aiReview) {
			cs.review = r
			cs.redraw()
		},
		func(err error) {
			cs.job = aiFailed(err)
			cs.redraw()
		},
		func(ctx context.Context) (ai.Payload, error) { return a.ai.DraftPayload(ctx, in, guidance) },
		func(model string) {
			if cs.closed {
				return
			}
			ctx, cancel := context.WithCancel(cs.ctx)
			job := &aiJob{busy: true, cancel: cancel, model: model}
			cs.job = job
			cs.redraw()
			a.run(func() {
				defer cancel()
				result, err := a.ai.Draft(ctx, in, guidance)
				a.post(func() {
					if cs.job != job || cs.closed {
						return
					}
					switch {
					case ai.KindOf(err) == ai.KindCancelled:
						cs.job = nil
					case err != nil:
						cs.job = aiFailed(err)
					default:
						job.busy = false
						job.model = result.Model
						cs.insertDraft(result.Text)
						// The text was not typed, but it is the draft's
						// now and is saved like any edit.
						cs.changed()
					}
					cs.redraw()
				})
			})
		})
}

// redraw builds the compose window again after a change that was made
// from the mail window's update, which redraws only the mail window.
func (cs *composeState) redraw() {
	if cs.win != nil && !cs.closed {
		cs.win.Invalidate()
	}
}

// insertDraft puts the drafted text where the reply is written: above
// whatever the body holds, which for a reply is the quoted original, or
// after the quote of a reply written below it, while the body still
// starts with that quote.
func (cs *composeState) insertDraft(text string) {
	text = strings.TrimSpace(text)
	lead := string([]rune(cs.body)[:cs.writeAt()])
	rest := strings.TrimLeft(strings.TrimPrefix(cs.body, lead), "\n")
	if rest == "" {
		cs.body = lead + text + "\n"
		return
	}
	cs.body = lead + text + "\n\n" + rest
}

// inspectDraft shows what drafting this reply sends.
func (cs *composeState) inspectDraft() {
	if cs.source == nil {
		return
	}
	a := cs.app
	in, guidance := *cs.source, strings.TrimSpace(cs.guidance)
	in.Language = aiLanguage()
	a.aiInspect(func(r *aiReview) {
		cs.review = r
		cs.redraw()
	}, func(ctx context.Context) (ai.Payload, error) { return a.ai.DraftPayload(ctx, in, guidance) })
}

// draftRow draws the AI drafting controls of a reply.
func (cs *composeState) draftRow(c *ui.Context, t *ui.Theme) {
	a := cs.app
	busy := cs.job != nil && cs.job.busy
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		// The icon sits in the column the other rows give their labels.
		ui.Row(c).Width(52).Shrink(0).Children(func() {
			ui.Icon(c, sparklesIcon).FontSize(15).TextColor(t.Accent)
		})
		ui.TextInput(c, &cs.guidance).Grow(1).Label(locales.AiGuidance()).
			Placeholder(locales.AiGuidancePlaceholder()).Disabled(busy)
		if busy {
			ui.Button(c, locales.Cancel()).OnClick(func() {
				if cs.job != nil && cs.job.cancel != nil {
					cs.job.cancel()
				}
				cs.job = nil
			})
		} else {
			ui.Button(c, locales.AiDraft()).OnClick(cs.draftWithAI)
		}
	})
	job := cs.job
	if job == nil {
		return
	}
	dismiss := func() { cs.job = nil }
	switch {
	case job.busy:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.AiDrafting(job.model)).FontSize(12).TextColor(t.TextMuted)
		})
	case job.err != "":
		a.aiNotice(c, t, job, locales.AiDismissDraftNote(), cs.draftWithAI, dismiss)
	default:
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, checkIcon).FontSize(14).TextColor(t.Success)
			ui.Text(c, locales.AiDrafted(job.model)).FontSize(12).TextColor(t.TextMuted).Grow(1)
			ui.ButtonBase(c).Label(locales.AiWhatWasSent()).OnClick(cs.inspectDraft).Children(func() {
				ui.Text(c, locales.AiWhatWasSent()).FontSize(11).TextColor(t.Accent)
			})
		})
	}
}
