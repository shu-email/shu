package main

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Sender authentication and the advisories around it (issue: phishing).
// Every check here reads cached headers only, and every warning is
// advisory: nothing is moved, flagged or deleted because of it.

// ownDomains are the domains the user receives mail at: those of the
// accounts' addresses and of the identities they send from.
func (a *app) ownDomains() []string {
	var out []string
	seen := map[string]bool{}
	add := func(email string) {
		d := mail.AddressDomain(email)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, acc := range a.accounts {
		add(acc.Email)
		for _, id := range a.aliases[acc.ID] {
			add(id.Email)
		}
	}
	return out
}

// ownAddress reports whether an address is one the user receives and
// sends from: an account's or an identity's. Leaving a list and blocking
// a sender are not offered for the user's own mail.
func (a *app) ownAddress(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" {
		return false
	}
	for _, acc := range a.accounts {
		if strings.EqualFold(acc.Email, email) {
			return true
		}
		for _, id := range a.aliases[acc.ID] {
			if strings.EqualFold(id.Email, email) {
				return true
			}
		}
	}
	return false
}

// securityNotice draws what the cached headers say about the message's
// sender: its authentication verdict, a sender outside the user's
// domains, and a Reply-To that leaves them. Unknown is never drawn as
// safe, and no warning relies on color alone.
func (a *app) securityNotice(c *ui.Context, t *ui.Theme, m mail.Summary, msg *mail.Message) {
	if msg == nil {
		return
	}
	tr := msg.Trust()
	replyMismatch := m.ReplyToMismatch()
	external := mail.ExternalSender(m.From, a.ownDomains())
	key := summaryKey(m)
	open := a.securityOpen[key]

	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Children(func() {
			a.securityChip(c, securityVerdictLabel(tr), securityVerdictIcon(tr), securityVerdictColor(t, tr))
			if external {
				a.securityChip(c, locales.SecurityExternal(), shieldQuestionIcon, t.Warning)
			}
			if replyMismatch {
				a.securityChip(c, locales.SecurityReplyMismatch(), alertIcon, t.Warning)
			}
			label := locales.SecurityShowDetails()
			if open {
				label = locales.SecurityHideDetails()
			}
			ui.ButtonBase(c).Label(label).OnClick(func() { a.securityOpen[key] = !open }).Children(func() {
				ui.Text(c, label).FontSize(11).TextColor(t.Accent)
			})
		})
		if !open {
			return
		}
		ui.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(t.Surface).
			Border(1, t.Border).Children(func() {
			line := func(text string) {
				ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).MaxLines(6)
			}
			line(securityVerdictDetail(tr))
			if external {
				line(locales.SecurityExternalDetail(m.From.Email))
			}
			if replyMismatch {
				reply := make([]string, 0, len(m.ReplyTo))
				for _, addr := range m.ReplyTo {
					reply = append(reply, addr.Email)
				}
				line(locales.SecurityReplyMismatchDetail(mail.AddressDomain(m.From.Email), strings.Join(reply, ", ")))
			}
			if len(tr.Checks) > 0 {
				line(locales.SecurityChecks(authChecks(tr)))
			}
			if list, _ := msg.Unsubscribe(); list != "" {
				line(locales.SecurityUnsubscribe(strings.Join(strings.Split(list, "\n"), ", ")))
			}
			for _, evidence := range tr.Evidence {
				ui.Text(c, evidence).FontSize(10).TextColor(t.TextMuted).
					Selectable().MaxLines(4)
			}
			if rec, ok := a.lists[unsubKey(m.Account, m.From.Email)]; ok {
				line(locales.UnsubscribedOn(a.longTime(c, rec.At)))
			}
			// Leaving a list and blocking a sender are the user's choices,
			// each confirmed with the exact destination first.
			offer := offerOf(msg)
			if !a.ownAddress(m.From.Email) && (!offer.Empty() || m.From.Email != "") {
				ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
					if !offer.Empty() {
						ui.Button(c, locales.Unsubscribe()).OnClick(func() { a.openUnsubscribe(m, offer) })
					}
					if m.From.Email != "" {
						ui.Button(c, locales.BlockSender()).OnClick(func() { a.openBlockSender(m) })
					}
				})
			}
		})
	})
}

// securityChip is one label with its icon: state is written, never shown
// by color alone.
func (a *app) securityChip(c *ui.Context, label string, icon *ui.SVG, color ui.Color) {
	ui.Row(c).Shrink(0).Gap(5).Padding(3, 8).Radius(20).AlignItems(ui.Center).
		Background(color.Alpha(0.10)).Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(color)
		ui.Text(c, label).FontSize(11).Bold().TextColor(color)
	})
}

// authChecks says each method's result, in the order the server wrote
// them, for a user who wants the details.
func authChecks(tr mail.Trust) string {
	parts := make([]string, 0, len(tr.Checks))
	for _, c := range tr.Checks {
		parts = append(parts, strings.ToUpper(c.Method)+" "+string(c.Result))
	}
	return strings.Join(parts, " · ")
}

func securityVerdictLabel(tr mail.Trust) string {
	switch tr.Verdict {
	case mail.AuthPassed:
		return locales.SecurityPassed()
	case mail.AuthFailed:
		return locales.SecurityFailed()
	default:
		return locales.SecurityAbsent()
	}
}

func securityVerdictIcon(tr mail.Trust) *ui.SVG {
	switch tr.Verdict {
	case mail.AuthPassed:
		return shieldCheckIcon
	case mail.AuthFailed:
		return spamIcon
	default:
		return shieldQuestionIcon
	}
}

func securityVerdictColor(t *ui.Theme, tr mail.Trust) ui.Color {
	switch tr.Verdict {
	case mail.AuthPassed:
		return t.Success
	case mail.AuthFailed:
		return t.Danger
	default:
		return t.Warning
	}
}

func securityVerdictDetail(tr mail.Trust) string {
	switch tr.Verdict {
	case mail.AuthPassed:
		return locales.SecurityPassedDetail()
	case mail.AuthFailed:
		return locales.SecurityFailedDetail()
	default:
		return locales.SecurityAbsentDetail()
	}
}

// askOpenLink holds a link the HTML view asked for until the user
// confirms its real destination.
func (a *app) askOpenLink(url string) {
	a.linkURL = url
	a.linkOpen = true
}

// linkDialog asks before a link leaves the isolated view: the user sees
// exactly where it goes, and no page can start a navigation by itself.
func (a *app) linkDialog(c *ui.Context) {
	if a.linkURL == "" {
		return
	}
	if ui.AlertDialog(c, &a.linkOpen, locales.SecurityLinkTitle(),
		locales.SecurityLinkMessage(a.linkURL), locales.Cancel(), locales.SecurityLinkOpen()) == 1 {
		a.services.OpenURL(a.linkURL)
	}
	if !a.linkOpen {
		a.linkURL = ""
	}
}

// attachmentWarning marks an attachment whose name or type is commonly
// used to hide a program. It is advice, not a block: the file is still
// saved as usual.
func attachmentWarning(att mail.Attachment) (string, *ui.SVG) {
	switch mail.RiskOf(att) {
	case mail.AttachmentDisguised:
		return locales.SecurityAttachmentDisguised(), alertIcon
	case mail.AttachmentTypeMismatch:
		return locales.SecurityAttachmentTypeMismatch(), alertIcon
	case mail.AttachmentExecutable:
		return locales.SecurityAttachmentExecutable(), spamIcon
	default:
		return "", nil
	}
}
