package main

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The recipient fields offer the contacts of the address book as the user
// types: for the entry under the caret, once it holds two characters, or
// one of a script that writes a name without spaces, a list below the
// field of the contacts it may be, best first, which the arrows move
// through and Enter, Tab or a click takes, writing the contact's address
// in place of the entry. Each field is a combo box to assistive
// technology, as MyGo's own are, pointing at the offer the arrows are on.
// A send that finds entries that are no address stops, and the window
// says what each is, offering the contacts a name may mean.

// recipientField is one of the compose window's fields of recipients.
type recipientField int

const (
	fieldTo recipientField = iota
	fieldCc
	fieldBcc
)

const recipientFieldCount = 3

var recipientFields = [recipientFieldCount]recipientField{fieldTo, fieldCc, fieldBcc}

// recipientState is what the compose window keeps of its fields of
// recipients besides their text.
type recipientState struct {
	// carets is where each field's caret was in its last build, in runes.
	carets [recipientFieldCount]int
	// offer is the list of contacts shown below a field, and edit a change
	// of a field's text that its next build makes.
	offer *offer
	edit  *recipientEdit
	// seq counts the queries for offers: a query that a later one
	// overtook reads nothing, and its answer is dropped. mu has their
	// jobs take turns.
	seq atomic.Uint64
	mu  sync.Mutex
	// checks is what the engine said of each field that has entries a
	// send cannot take; quiet marks a field whose next change is the
	// window's own, which offers nothing.
	checks [recipientFieldCount]*fieldCheck
	quiet  [recipientFieldCount]bool
}

// offer is the contacts offered for an entry of a field: for the field's
// text as it was when they were looked up, and that entry of it, which
// words found. hi is the one the arrows are on, -1 for none, and chose
// says the user moved them there.
type offer struct {
	field    recipientField
	text     string
	entry    mail.RecipientEntry
	words    []string
	contacts []mail.Contact
	hi       int
	chose    bool
}

// covers reports whether a place in the field's text, in bytes, is in the
// entry offered for, between the commas around it.
func (o *offer) covers(at int) bool { return o.entry.Start <= at && at <= o.entry.End }

// narrowed is the offer for the field's text typed on since, until the
// address book answers for it: the contacts offered that the entry under
// the caret still finds, as the book finds them (mail.ContactTerms), so
// that the list neither blinks at each key nor offers a contact the text
// no longer finds. A caret in another entry, or text that finds none of
// them, offers nothing meanwhile.
func (o *offer) narrowed(text string, at int) *offer {
	e, ok := entryAt(text, at)
	if !ok || e.Start != o.entry.Start || !worthLookingUp(e.Text) {
		return nil
	}
	words := mail.ContactWords(e.Text)
	var kept []mail.Contact
	for _, ct := range o.contacts {
		if finds(words, ct) {
			kept = append(kept, ct)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	hi, chose := firstChoice(kept, e, o)
	return &offer{field: o.field, text: text, entry: e, words: words, contacts: kept, hi: hi, chose: chose}
}

// finds reports whether each word typed begins a term the address book
// finds a contact by.
func finds(words []string, ct mail.Contact) bool {
	terms := mail.ContactTerms(ct.Name, ct.Email)
	for _, w := range words {
		if !slices.ContainsFunc(terms, func(term string) bool { return strings.HasPrefix(term, w) }) {
			return false
		}
	}
	return true
}

// firstChoice is where the arrows go on contacts offered for an entry: to
// the contact the user moved them to in the last offer, while it is
// offered, else to the first, or to none for an address typed whole,
// which stays as typed unless the user goes to an offer.
func firstChoice(list []mail.Contact, e mail.RecipientEntry, last *offer) (int, bool) {
	if last != nil && last.chose && last.hi >= 0 && last.hi < len(last.contacts) {
		key := mail.ContactKey(last.contacts[last.hi].Email)
		if i := slices.IndexFunc(list, func(ct mail.Contact) bool { return mail.ContactKey(ct.Email) == key }); i >= 0 {
			return i, true
		}
	}
	if e.Valid {
		return -1, false
	}
	return 0, false
}

// recipientEdit replaces the bytes from to to of a field's text with s,
// while the field still holds text. quiet edits offer nothing.
type recipientEdit struct {
	field    recipientField
	text     string
	from, to int
	s        string
	quiet    bool
}

// fieldCheck is what CheckRecipients said of a field's text.
type fieldCheck struct {
	text  string
	check mail.RecipientCheck
}

// fieldText is the text of a field of recipients.
func (cs *composeState) fieldText(f recipientField) *string {
	switch f {
	case fieldCc:
		return &cs.cc
	case fieldBcc:
		return &cs.bcc
	}
	return &cs.to
}

// focusOf is the control that puts the keyboard in a field of recipients.
func focusOf(f recipientField) string {
	switch f {
	case fieldCc:
		return focusCc
	case fieldBcc:
		return focusBcc
	}
	return focusTo
}

// fieldLabel is a field of recipients as the window names it.
func fieldLabel(f recipientField) string {
	switch f {
	case fieldCc:
		return locales.Cc()
	case fieldBcc:
		return locales.Bcc()
	}
	return locales.To()
}

// recipientRow is a field of recipients: a row of the compose window's
// fields whose input is a combo box, which offers contacts for the entry
// being typed in a list below it. trailing adds controls after it.
func (cs *composeState) recipientRow(c *ui.Context, f recipientField, label, placeholder string, trailing func()) ui.Element {
	t := c.Theme()
	value := cs.fieldText(f)
	var in ui.Element
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		// The caption names the input, so a click on it focuses the field.
		caption := ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(52)
		in = ui.TextInput(c, value).Grow(1).LabelledBy(caption).Placeholder(placeholder).
			FocusBind(&cs.focused, focusOf(f)).Role(ui.RoleComboBox).AutoComplete(ui.AutoCompleteList).
			OnChange(func() { cs.recipientChanged(f) }).
			OnTextKey(func(k ui.TextKey) bool { return cs.offerKey(f, k) })
		_, caret := in.TextAnchorCaret()
		if e := cs.rcpt.edit; e != nil && e.field == f {
			cs.rcpt.edit = nil
			if *value == e.text {
				cs.rcpt.quiet[f] = e.quiet
				in.ReplaceText(runeAt(e.text, e.from), runeAt(e.text, e.to), e.s)
				_, caret = in.TextAnchorCaret()
			}
		}
		cs.rcpt.carets[f] = caret
		if len(cs.problemsOf(f)) > 0 {
			in.Invalid(true)
		}
		o := cs.rcpt.offer
		if o != nil && o.field == f && o.text != *value {
			o = o.narrowed(*value, byteAt(*value, caret))
			cs.rcpt.offer = o
		}
		open := o != nil && o.field == f && o.text == *value && in.Focused() && o.covers(byteAt(*value, caret))
		in.Expanded(open)
		if open {
			// Escape closes the list rather than the window.
			in.OnShortcut(0, ui.KeyEscape, cs.dismissOffer)
			if active := cs.offerList(c, t, in, o); o.hi >= 0 {
				in.ActiveDescendant(active)
			}
		}
		if trailing != nil {
			trailing()
		}
	})
	return in
}

// offerList shows the contacts offered below a field: each one's name and
// address, the parts the words typed begin in bold, and the one the arrows
// are on in the accent color, which it returns.
func (cs *composeState) offerList(c *ui.Context, t *ui.Theme, in ui.Element, o *offer) ui.Element {
	var active ui.Element
	ui.Overlay(c, func() {
		panel := ui.Column(c).AttachTo(in, ui.AnchorBottomStart, ui.AnchorTopStart).MinWidth(in.Bounds().W).
			Margin(4, 0, 0, 0).Padding(4).Gap(1).Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.18)).
			Role(ui.RoleListBox).Label(locales.ContactSuggestions()).KeepFocus()
		if panel.PressedOutside() {
			cs.dismissOffer()
		}
		panel.Children(func() {
			for i, ct := range o.contacts {
				if opt := cs.offerRow(c, t, o, i, ct); i == o.hi {
					active = opt
				}
			}
		})
	})
	return active
}

// offerRow is one contact offered. A click takes it; the arrows move
// among them while the field keeps the keyboard.
func (cs *composeState) offerRow(c *ui.Context, t *ui.Theme, o *offer, i int, ct mail.Contact) ui.Element {
	chosen := i == o.hi
	f := o.field
	opt := ui.Row(c.Key("offer/"+mail.ContactKey(ct.Email))).Gap(10).Padding(6, 10).Radius(t.Radius).
		AlignItems(ui.Center).Role(ui.RoleOption).Checked(chosen).PositionInSet(i+1, len(o.contacts)).
		OnClick(func() { cs.take(f, ct) })
	name, address, star := t.Text, t.TextMuted, t.Warning
	switch {
	case chosen:
		opt.Background(t.Accent)
		name, address, star = t.AccentText, t.AccentText.Alpha(0.85), t.AccentText
	case opt.Hovered():
		opt.Background(t.SurfaceHover)
	}
	opt.Children(func() {
		if ct.Name != "" {
			ui.RichText(c, matchSpans(ct.Name, o.words, false, name, 400)...).FontSize(13).SingleLine().Shrink(0).MaxWidth(260)
		}
		ui.RichText(c, matchSpans(ct.Email, o.words, true, address, 400)...).FontSize(12).SingleLine().Grow(1).MinWidth(0)
		if ct.Favorite {
			ui.Icon(c, starFilledIcon).FontSize(11).TextColor(star)
		}
	})
	return opt
}

// matchSpans writes a name or an address in spans, with the parts the
// words typed begin in bold, so that what matched shows at a glance.
func matchSpans(text string, words []string, address bool, color ui.Color, weight int) []ui.Span {
	bold := make([]bool, len(text))
	for _, start := range wordBegins(text, address) {
		for _, w := range words {
			for i := start; i < start+foldPrefix(text[start:], w); i++ {
				bold[i] = true
			}
		}
	}
	var spans []ui.Span
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && bold[j] == bold[i] {
			j++
		}
		w := weight
		if bold[i] {
			w = 700
		}
		spans = append(spans, ui.Span{Text: text[i:j], Weight: w, Color: color})
		i = j
	}
	return spans
}

// wordBegins are where the address book finds a name or an address by
// what is typed (mail.ContactTerms): at a letter or digit after anything
// else, and at each character of a script that writes a name without
// spaces; in an address, at its start, and in what comes before its @.
func wordBegins(text string, address bool) []int {
	scan := text
	var out []int
	if address {
		out = append(out, 0)
		if at := strings.LastIndexByte(text, '@'); at > 0 {
			scan = text[:at]
		}
	}
	prev := ' '
	for i, r := range scan {
		if wordRune(r) && (!wordRune(prev) || unspaced(r) || unspaced(prev)) {
			out = append(out, i)
		}
		prev = r
	}
	return out
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) }

// unspaced reports a character of a script that writes a name without
// spaces between its parts: Chinese, Japanese and Korean.
func unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// foldPrefix is how many bytes of s a word in lower case begins, whatever
// their case, and 0 when it does not begin s.
func foldPrefix(s, word string) int {
	n := 0
	for _, w := range word {
		r, size := utf8.DecodeRuneInString(s[n:])
		if size == 0 || unicode.ToLower(r) != w {
			return 0
		}
		n += size
	}
	return n
}

// offerKey takes the keys of the list of offers while it shows: the
// arrows move among the contacts, and Enter and Tab take the one they are
// on. The field keeps the other keys, and these while none is chosen, so
// that an address typed whole goes on as it would without the list.
func (cs *composeState) offerKey(f recipientField, k ui.TextKey) bool {
	o := cs.rcpt.offer
	if o == nil || o.field != f || k.Mods != 0 || k.Text() != o.text || !o.covers(byteAt(o.text, cs.rcpt.carets[f])) {
		return false
	}
	switch k.Key {
	case ui.KeyDown:
		o.hi, o.chose = min(o.hi+1, len(o.contacts)-1), true
	case ui.KeyUp:
		o.hi, o.chose = max(o.hi-1, 0), true
	case ui.KeyEnter, ui.KeyTab:
		if o.hi < 0 {
			return false
		}
		cs.take(f, o.contacts[o.hi])
	default:
		return false
	}
	cs.redraw()
	return true
}

// take writes a contact offered in place of the entry it was offered for,
// followed by the comma that ends it, and leaves the caret after both.
func (cs *composeState) take(f recipientField, ct mail.Contact) {
	o := cs.rcpt.offer
	if o == nil || o.field != f {
		return
	}
	from, to := takenSpan(o.text, o.entry)
	cs.rcpt.edit = &recipientEdit{field: f, text: o.text, from: from, to: to, s: ct.Address().String() + ", "}
	cs.dismissOffer()
	cs.redraw()
}

// takenSpan is the part of a field's text a contact taken replaces: the
// entry, without the spaces before it, and the comma and spaces after it,
// which the comma written after the address stands for.
func takenSpan(text string, e mail.RecipientEntry) (from, to int) {
	seg := text[e.Start:e.End]
	from = e.Start + len(seg) - len(strings.TrimLeftFunc(seg, unicode.IsSpace))
	to = e.End
	if to < len(text) && text[to] == ',' {
		to++
		for to < len(text) && text[to] == ' ' {
			to++
		}
	}
	return from, to
}

// dismissOffer closes the list of offers, and drops the query under way.
func (cs *composeState) dismissOffer() {
	cs.rcpt.seq.Add(1)
	cs.rcpt.offer = nil
}

// recipientChanged follows an edit of a field of recipients: the draft
// saves it, a field whose problems show is checked again, and the address
// book is asked what the entry under the caret may be.
func (cs *composeState) recipientChanged(f recipientField) {
	cs.changed()
	if cs.rcpt.checks[f] != nil {
		cs.checkRecipients(false, f)
	}
	if cs.rcpt.quiet[f] {
		cs.rcpt.quiet[f] = false
		cs.dismissOffer()
		return
	}
	cs.lookUp(f)
}

// lookUp asks the address book, in a job, for the contacts the entry
// under a field's caret may be. A query a later one overtook does not
// run, and an answer for text that changed since is dropped.
func (cs *composeState) lookUp(f recipientField) {
	text := *cs.fieldText(f)
	seq := cs.rcpt.seq.Add(1)
	entry, ok := entryAt(text, byteAt(text, cs.rcpt.carets[f]))
	if !ok || !worthLookingUp(entry.Text) {
		cs.rcpt.offer = nil
		return
	}
	exclude := cs.recipientAddresses()
	a := cs.app
	a.run(func() {
		var (
			list []mail.Contact
			err  error
		)
		cs.rcpt.mu.Lock()
		if cs.rcpt.seq.Load() == seq {
			ctx, cancel := a.ctx()
			list, err = a.svc.Suggest(ctx, entry.Text, exclude, 0)
			cancel()
		}
		cs.rcpt.mu.Unlock()
		a.post(func() {
			if cs.rcpt.seq.Load() != seq || cs.closed || *cs.fieldText(f) != text {
				return
			}
			last := cs.rcpt.offer
			if last != nil && (last.field != f || last.entry.Start != entry.Start) {
				last = nil
			}
			cs.rcpt.offer = nil
			if err == nil && len(list) > 0 {
				hi, chose := firstChoice(list, entry, last)
				cs.rcpt.offer = &offer{field: f, text: text, entry: entry, words: mail.ContactWords(entry.Text), contacts: list, hi: hi, chose: chose}
			}
			cs.redraw()
		})
	})
}

// entryAt is the entry of a field's text that a place in it, in bytes, is
// in: between the commas around it.
func entryAt(text string, at int) (mail.RecipientEntry, bool) {
	for _, e := range mail.RecipientEntries(text) {
		if e.Start <= at && at <= e.End {
			return e, true
		}
	}
	return mail.RecipientEntry{}, false
}

// worthLookingUp reports whether an entry says enough to look it up: two
// characters, or one of a script that writes a name without spaces, as a
// Chinese family name.
func worthLookingUp(text string) bool {
	if len(mail.ContactWords(text)) == 0 {
		return false
	}
	n := 0
	for _, r := range text {
		if unspaced(r) {
			return true
		}
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n >= 2
}

// recipientAddresses are the addresses the fields of recipients hold
// already, which are not offered again.
func (cs *composeState) recipientAddresses() []string {
	var out []string
	for _, f := range recipientFields {
		for _, e := range mail.RecipientEntries(*cs.fieldText(f)) {
			for _, a := range e.Addresses {
				out = append(out, a.Email)
			}
		}
	}
	return out
}

// byteAt is the place in text, in bytes, of a place in runes.
func byteAt(text string, runes int) int {
	for i := range text {
		if runes <= 0 {
			return i
		}
		runes--
	}
	return len(text)
}

// runeAt is the place in text, in runes, of a place in bytes.
func runeAt(text string, at int) int {
	return utf8.RuneCountInString(text[:min(max(at, 0), len(text))])
}

// problemsOf are the entries of a field a send cannot take, as the last
// check of the field said. They may be of text typed over since, while
// the check of the new text runs: an offer then changes nothing.
func (cs *composeState) problemsOf(f recipientField) []mail.RecipientProblem {
	if ch := cs.rcpt.checks[f]; ch != nil {
		return ch.check.Problems
	}
	return nil
}

// hasProblems reports whether any field of recipients has entries a send
// cannot take.
func (cs *composeState) hasProblems() bool {
	for _, f := range recipientFields {
		if len(cs.problemsOf(f)) > 0 {
			return true
		}
	}
	return false
}

// checkRecipients has the engine read fields of recipients as a send
// takes them, in a job, as it reads the address book, and keeps what it
// finds of each: nothing of a field it can send to. A field changed
// meanwhile keeps what it had, its change being checked when there are
// problems to show. reveal puts the keyboard in the first field with
// problems, as a send that stops does.
func (cs *composeState) checkRecipients(reveal bool, fields ...recipientField) {
	texts := make([]string, len(fields))
	for i, f := range fields {
		texts[i] = *cs.fieldText(f)
	}
	a := cs.app
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		checks := make([]mail.RecipientCheck, len(fields))
		var err error
		for i, text := range texts {
			if checks[i], err = a.svc.CheckRecipients(ctx, text); err != nil {
				break
			}
		}
		a.post(func() {
			if cs.closed || cs.sent {
				return
			}
			if err != nil {
				cs.err = locales.RecipientsCheckFailed(err.Error())
				cs.redraw()
				return
			}
			first := -1
			for i, f := range fields {
				if *cs.fieldText(f) != texts[i] {
					continue
				}
				cs.rcpt.checks[f] = nil
				if !checks[i].OK() {
					cs.rcpt.checks[f] = &fieldCheck{text: texts[i], check: checks[i]}
					if first < 0 {
						first = int(f)
					}
				}
			}
			if reveal && first >= 0 {
				cs.focused = focusOf(recipientField(first))
			}
			cs.redraw()
		})
	})
}

// problemsHeight is the most room the problems of the recipients take;
// more of them scroll, so that the fields and the actions stay on screen.
// In a small window they give way, down to the first problem and its
// offers, and the body gives way to them: it cannot go until they are
// fixed.
const (
	problemsHeight    = 260
	problemsMinHeight = 100
	problemsBodyMin   = 32
)

// recipientProblems says, field by field, what of the recipients a send
// cannot take, and offers the contacts a name may mean. It returns each
// field's messages, which the field names as its error.
func (cs *composeState) recipientProblems(c *ui.Context, t *ui.Theme) [recipientFieldCount][]ui.Element {
	var msgs [recipientFieldCount][]ui.Element
	ui.Scroll(c).MaxHeight(problemsHeight).MinHeight(problemsMinHeight).Children(func() {
		ui.Column(c).Gap(8).Padding(8, 10).Radius(8).Background(t.Warning.Alpha(0.12)).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				ui.Text(c, locales.RecipientsNotSent()).FontSize(12).Bold().Grow(1).MinWidth(0)
			})
			for _, f := range recipientFields {
				problems := cs.problemsOf(f)
				if len(problems) == 0 {
					continue
				}
				ui.Column(c.Key("problems/"+strconv.Itoa(int(f)))).Gap(6).Padding(0, 0, 0, 22).Children(func() {
					ui.Text(c, fieldLabel(f)).FontSize(11).Bold().TextColor(t.TextMuted)
					for _, p := range problems {
						msgs[f] = append(msgs[f], cs.problemLine(c, t, f, p))
					}
				})
			}
		})
	})
	return msgs
}

// problemLine is one entry a send cannot take, in the user's words, and
// the contacts offered in its place, each a button writing it there. It
// returns the text of the message.
func (cs *composeState) problemLine(c *ui.Context, t *ui.Theme, f recipientField, p mail.RecipientProblem) ui.Element {
	var msg ui.Element
	ui.Column(c.Key("problem/" + strconv.Itoa(p.Start))).Gap(4).Children(func() {
		msg = ui.Text(c, problemText(p)).FontSize(12).MaxLines(3)
		if p.Kind != mail.RecipientAmbiguous && p.Kind != mail.RecipientMatch {
			return
		}
		ui.Row(c).Wrap().Gap(6).Children(func() {
			for _, m := range p.Matches {
				address := m.Address().String()
				text := address
				if p.Kind == mail.RecipientMatch {
					text = locales.UseContact(address)
				}
				ui.Button(c.Key("use/"+mail.ContactKey(m.Email)), text).Label(locales.UseContactFor(address, p.Text)).
					Padding(4, 10).OnClick(func() { cs.fix(f, p, m) })
			}
		})
	})
	return msg
}

// problemText says what an entry a send cannot take is.
func problemText(p mail.RecipientProblem) string {
	switch p.Kind {
	case mail.RecipientUnknown:
		return locales.RecipientUnknown(p.Text)
	case mail.RecipientAmbiguous:
		return locales.RecipientAmbiguous(p.Text)
	case mail.RecipientMatch:
		return locales.RecipientMatch(p.Text, contactName(p.Matches[0]))
	}
	return locales.RecipientNotAddress(p.Text)
}

// contactName is what the window calls a contact: its name, or its
// address when it has none.
func contactName(ct mail.Contact) string {
	if ct.Name != "" {
		return ct.Name
	}
	return ct.Email
}

// fix writes a contact in place of an entry a send could not take, the
// spaces around it kept, and puts the keyboard back in the field. Typed
// over since the check, the field has another text, which its own check
// answers for, and the offer changes nothing.
func (cs *composeState) fix(f recipientField, p mail.RecipientProblem, ct mail.Contact) {
	ch := cs.rcpt.checks[f]
	if ch == nil || *cs.fieldText(f) != ch.text || p.End > len(ch.text) {
		return
	}
	seg := ch.text[p.Start:p.End]
	from := p.Start + len(seg) - len(strings.TrimLeftFunc(seg, unicode.IsSpace))
	cs.rcpt.edit = &recipientEdit{field: f, text: ch.text, from: from, to: from + len(p.Text), s: ct.Address().String(), quiet: true}
	cs.focused = focusOf(f)
	cs.redraw()
}
