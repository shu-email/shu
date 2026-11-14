package app

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
// types: for the entry under the caret, as soon as it holds a letter, a
// list below the field of the contacts it may be, best first, which the
// arrows move through and Enter, Tab or a click takes, writing the
// contact's address in place of the entry. Each field is a combo box to
// assistive technology, as MyGo's own are, pointing at the offer the
// arrows are on. A send that finds entries that are no address stops, and
// the window says what each is, offering the contacts a name may mean.

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
	// tokens are each field's finished entries, shown as chips, and
	// entries the text being typed after them. joined is the field's text
	// they made as the window last wrote it; a field holding another text
	// was set from outside, and is read again (syncField). shown is the
	// text the field's input last showed, which a pending edit replaces.
	tokens  [recipientFieldCount][]string
	entries [recipientFieldCount]string
	joined  [recipientFieldCount]string
	shown   [recipientFieldCount]string
	// picker is the list of contacts the + button opens, for the field it
	// was opened in, and pseq drops an answer for a query typed over.
	picker contactPicker
	pseq   atomic.Uint64
}

// contactPicker is the list of contacts a + button opens: the field they
// go into, the search typed, the contacts the address book answered, and
// the one the arrows are on.
type contactPicker struct {
	field    recipientField
	open     bool
	query    string
	contacts []mail.Contact
	loading  bool
	err      string
	hi       int
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

// recipientEdit replaces the bytes from to to of a field's input with s,
// while the input still shows text, and sets the field's chips to tokens
// when they are not nil. quiet edits offer nothing.
type recipientEdit struct {
	field    recipientField
	text     string
	from, to int
	s        string
	quiet    bool
	tokens   []string
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
// fields whose entries show as chips, before an input for the one being
// typed, which offers contacts for it in a list below the field. A
// comma, Enter or Tab ends the entry and makes a chip of it, Enter takes
// the contact the list is on, and Backspace in an empty input takes the
// last chip out. trailing adds controls after the field.
func (cs *composeState) recipientRow(c *ui.Context, f recipientField, label, placeholder string, trailing func()) ui.Element {
	t := c.Theme()
	cs.syncField(f)
	var in, box, add ui.Element
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		// The caption names the input, so a click on it focuses the field.
		caption := ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(52)
		box = ui.Row(c.Key("recipients")).Grow(1).Wrap().AlignItems(ui.Center).Gap(6).
			Padding(3, 6).Radius(t.Radius).Background(t.Surface).Border(1, t.Border).Cursor(ui.CursorText)
		box.Children(func() {
			for i, token := range cs.rcpt.tokens[f] {
				cs.recipientChip(c, t, f, i, token)
			}
			// The input keeps its identity, whatever chips come before it.
			ui.Row(c.Key("input")).Grow(1).MinWidth(t.Space(12)).Children(func() {
				ph := placeholder
				if len(cs.rcpt.tokens[f]) > 0 {
					ph = ""
				}
				text := &cs.rcpt.entries[f]
				in = ui.TextInput(c, text).Grow(1).Padding(2, 0).Border(0, ui.Transparent).Background(ui.Transparent).
					LabelledBy(caption).Placeholder(ph).
					FocusBind(&cs.focused, focusOf(f)).Role(ui.RoleComboBox).AutoComplete(ui.AutoCompleteList).
					OnChange(func() { cs.recipientChanged(f) }).
					OnTextKey(func(k ui.TextKey) bool { return cs.recipientKey(f, k) })
				if e := cs.rcpt.edit; e != nil && e.field == f && cs.rcpt.shown[f] == e.text {
					cs.rcpt.edit = nil
					cs.rcpt.quiet[f] = e.quiet
					in.ReplaceText(runeAt(e.text, e.from), runeAt(e.text, e.to), e.s)
					if e.tokens != nil {
						cs.rcpt.tokens[f] = e.tokens
					}
					cs.setJoined(f)
					cs.changed()
					if cs.rcpt.checks[f] != nil {
						cs.checkRecipients(false, f)
					}
				}
				_, caret := in.TextAnchorCaret()
				cs.rcpt.carets[f] = caret
				cs.rcpt.shown[f] = cs.rcpt.entries[f]
				problems := len(cs.problemsOf(f)) > 0
				if problems {
					in.Invalid(true)
				}
				entry := cs.rcpt.entries[f]
				o := cs.rcpt.offer
				if o != nil && o.field == f && o.text != entry {
					o = o.narrowed(entry, byteAt(entry, caret))
					cs.rcpt.offer = o
				}
				open := o != nil && o.field == f && o.text == entry && in.Focused() && o.covers(byteAt(entry, caret))
				in.Expanded(open)
				if open {
					// Escape closes the list rather than the window.
					in.OnShortcut(0, ui.KeyEscape, cs.dismissOffer)
					if active := cs.offerList(c, t, box, o); o.hi >= 0 {
						in.ActiveDescendant(active)
					}
				}
				if problems {
					box.Border(1, t.Danger)
				} else if in.Focused() {
					box.Border(1, t.Accent)
				} else if box.Hovered() {
					box.Border(1, t.Border.Mix(t.Text, 0.25))
				}
			})
			add = ui.Button(c.Key("add"), "").Label(locales.AddRecipientTo(label)).Tooltip(locales.AddRecipientTo(label)).
				Padding(2, 3).Radius(t.Radius - 2).Background(ui.Transparent).Shrink(0).TabStop(false).
				OnClick(func() { cs.toggleContactPicker(f) }).
				Children(func() { ui.Icon(c, plusIcon).FontSize(13).TextColor(t.TextMuted) })
		})
		if box.Clicked() {
			in.Focus()
		}
		cs.contactPicker(c, t, f, add)
		if trailing != nil {
			trailing()
		}
	})
	return in
}

// recipientChip shows one finished entry of a field, by its name or its
// address, with a button that takes it out.
func (cs *composeState) recipientChip(c *ui.Context, t *ui.Theme, f recipientField, i int, token string) {
	label := recipientChipLabel(token)
	chip := ui.Row(c.Key("chip/"+strconv.Itoa(i))).AlignItems(ui.Center).Gap(2).
		Padding(2, 2).PaddingStart(8).Radius(t.Radius).Background(t.SurfacePressed).Shrink(0)
	chip.Label(token)
	chip.Children(func() {
		ui.Text(c, label).FontSize(12).SingleLine()
		ui.Button(c.Key("remove"), "").Label(locales.RemoveRecipient(label)).Tooltip(locales.RemoveRecipient(label)).
			KeepFocus().TabStop(false).Padding(1, 3).Radius(t.Radius - 2).Background(ui.Transparent).
			OnClick(func() { cs.removeRecipient(f, i) }).
			Children(func() { ui.Icon(c, closeIcon).FontSize(10).TextColor(t.TextMuted) })
	})
}

// recipientChipLabel is what a chip shows: the name of the entry's one
// address when it has one, and the entry itself otherwise.
func recipientChipLabel(token string) string {
	if list, err := mail.ParseAddressList([]string{token}); err == nil && len(list) == 1 {
		if list[0].Name != "" {
			return list[0].Name
		}
		return list[0].Email
	}
	return token
}

// removeRecipient takes a chip out of a field, the draft saved without it.
func (cs *composeState) removeRecipient(f recipientField, i int) {
	if i < 0 || i >= len(cs.rcpt.tokens[f]) {
		return
	}
	cs.rcpt.tokens[f] = slices.Delete(cs.rcpt.tokens[f], i, i+1)
	cs.setJoined(f)
	cs.changed()
	if cs.rcpt.checks[f] != nil {
		cs.checkRecipients(false, f)
	}
	cs.redraw()
}

// contactPickerLimit is how many contacts the + button's list holds; the
// search narrows them.
const contactPickerLimit = 50

// toggleContactPicker opens the + button's list of contacts for a field,
// or closes it.
func (cs *composeState) toggleContactPicker(f recipientField) {
	p := &cs.rcpt.picker
	if p.open && p.field == f {
		p.open = false
		cs.focused = focusOf(f)
		return
	}
	p.field, p.open, p.err, p.hi = f, true, "", 0
	p.query, p.contacts = "", nil
	cs.loadContacts()
	cs.redraw()
}

// contactPicker shows the + button's list of contacts below the field it
// adds to, which a search narrows. A click adds a contact, as Enter adds
// the one the arrows are on; the list stays open to add more.
func (cs *composeState) contactPicker(c *ui.Context, t *ui.Theme, f recipientField, anchor ui.Element) {
	p := &cs.rcpt.picker
	if p.field != f {
		return
	}
	var active ui.Element
	panel := ui.PopoverBase(c, anchor, &p.open, func(panel ui.Element) {
		// The search keeps the keyboard while a contact is pressed, as
		// the offers below a field do.
		panel.Width(320).Margin(6, 0, 0, 0).Padding(8).Gap(8).Radius(10).
			Background(t.Background).Border(1, t.Border).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18)).
			Label(locales.ChooseRecipients()).KeepFocus()
		var search ui.Element
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Icon(c, searchIcon).FontSize(14).TextColor(t.TextMuted).Shrink(0)
			search = ui.TextInput(c, &p.query).Grow(1).Label(locales.SearchContacts()).Placeholder(locales.SearchContacts()).
				AutoFocus().OnChange(func() {
				p.hi = 0
				cs.loadContacts()
			})
		})
		// Escape clears the search, then closes the list, and the
		// keyboard goes back to the field it adds to; it never reaches
		// the window.
		search.OnShortcut(0, ui.KeyEscape, func() {
			if p.query != "" {
				p.query = ""
				p.hi = 0
				cs.loadContacts()
				cs.redraw()
				return
			}
			p.open = false
			cs.focused = focusOf(p.field)
		})
		search.OnTextKey(func(k ui.TextKey) bool {
			if k.Mods != 0 {
				return false
			}
			switch k.Key {
			case ui.KeyDown:
				if len(p.contacts) == 0 {
					return false
				}
				p.hi = min(p.hi+1, len(p.contacts)-1)
			case ui.KeyUp:
				if len(p.contacts) == 0 {
					return false
				}
				p.hi = max(p.hi-1, 0)
			case ui.KeyEnter:
				if p.hi < 0 || p.hi >= len(p.contacts) {
					return false
				}
				cs.addRecipientContact(p.contacts[p.hi])
			default:
				return false
			}
			cs.redraw()
			return true
		})
		switch {
		case p.err != "":
			ui.Text(c, p.err).FontSize(12).TextColor(t.Warning).MaxLines(4).Role(ui.RoleStatus)
		case p.loading && len(p.contacts) == 0:
			ui.Text(c, locales.Loading()).FontSize(12).TextColor(t.TextMuted).Padding(4, 8)
		case len(p.contacts) == 0:
			text := locales.NoContactsToAdd()
			if strings.TrimSpace(p.query) != "" {
				text = locales.ContactsNoMatch(strings.TrimSpace(p.query))
			}
			ui.Text(c, text).FontSize(12).TextColor(t.TextMuted).MaxLines(3).Padding(4, 8)
		default:
			words := mail.ContactWords(p.query)
			ui.Scroll(c).MaxHeight(t.Space(72)).Children(func() {
				ui.Column(c).Gap(1).Role(ui.RoleListBox).Label(locales.ChooseRecipients()).Children(func() {
					for i, ct := range p.contacts {
						if opt := cs.contactPickerRow(c, t, p, i, ct, words); i == p.hi {
							active = opt
						}
					}
				})
			})
		}
		if p.hi >= 0 && p.hi < len(p.contacts) {
			search.ActiveDescendant(active)
		}
	})
	// A Tab out of the list closes it, as a combobox's list closes when
	// the keyboard leaves its field; the keyboard stays where it went.
	if p.open && anchor.FocusLeft() {
		p.open = false
	}
	// A press outside or Escape closes the list, and the keyboard goes
	// back to the field it was opened for — unless the press landed on
	// another field of the window, which keeps the focus it took.
	if panel.Dismissed() && !p.open && ui.FocusedValue(c, &cs.focused) == "" {
		cs.focused = focusOf(f)
	}
}

// contactPickerRow is one contact of the + button's list: its name and
// address, the parts the search begins in bold, and the one the arrows
// are on in the accent color.
func (cs *composeState) contactPickerRow(c *ui.Context, t *ui.Theme, p *contactPicker, i int, ct mail.Contact, words []string) ui.Element {
	chosen := i == p.hi
	row := ui.Row(c.Key("picker/"+mail.ContactKey(ct.Email))).Gap(10).Padding(6, 8).Radius(t.Radius).
		AlignItems(ui.Center).Role(ui.RoleOption).Checked(chosen).PositionInSet(i+1, len(p.contacts)).
		OnClick(func() { cs.addRecipientContact(ct) })
	name, address, star := t.Text, t.TextMuted, t.Warning
	switch {
	case chosen:
		row.Background(t.Accent)
		name, address, star = t.AccentText, t.AccentText.Alpha(0.85), t.AccentText
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		if ct.Name != "" {
			ui.RichText(c, matchSpans(ct.Name, words, false, name, 400)...).FontSize(13).SingleLine().Shrink(0).MaxWidth(180)
		}
		ui.RichText(c, matchSpans(ct.Email, words, true, address, 400)...).FontSize(12).SingleLine().Grow(1).MinWidth(0)
		if ct.Favorite {
			ui.Icon(c, starFilledIcon).FontSize(11).TextColor(star)
		}
	})
	return row
}

// loadContacts asks the address book, in a job, for the contacts the +
// button's list shows: those the search finds, the addresses the fields
// hold already left out. An answer a later query overtook is dropped.
func (cs *composeState) loadContacts() {
	p := &cs.rcpt.picker
	if !p.open {
		return
	}
	field, text := p.field, p.query
	seq := cs.rcpt.pseq.Add(1)
	exclude := cs.recipientAddresses()
	p.loading, p.err = true, ""
	a := cs.app
	a.run(func() {
		ctx, cancel := a.ctx()
		var (
			list []mail.Contact
			err  error
		)
		if strings.TrimSpace(text) == "" {
			// The book answers whole: the addresses already in the fields
			// are left out here. Suggest leaves out its own.
			var page mail.ContactPage
			page, err = a.svc.Contacts(ctx, "", 0, contactPickerLimit+len(exclude))
			excluded := make(map[string]bool, len(exclude))
			for _, email := range exclude {
				excluded[mail.ContactKey(email)] = true
			}
			for _, ct := range page.Contacts {
				if len(list) == contactPickerLimit {
					break
				}
				if !excluded[mail.ContactKey(ct.Email)] {
					list = append(list, ct)
				}
			}
		} else {
			list, err = a.svc.Suggest(ctx, text, exclude, contactPickerLimit)
		}
		cancel()
		a.post(func() {
			p := &cs.rcpt.picker
			if cs.closed || cs.rcpt.pseq.Load() != seq || !p.open || p.field != field || p.query != text {
				return
			}
			p.loading = false
			p.err, p.contacts = "", nil
			if err != nil {
				p.err = locales.ContactsReadFailed(err.Error())
			} else {
				p.contacts = list
			}
			p.hi = min(max(p.hi, 0), len(p.contacts)-1)
			cs.redraw()
		})
	})
}

// addRecipientContact adds a contact of the + button's list to the field
// it was opened for, as a chip, and asks the list again without it.
func (cs *composeState) addRecipientContact(ct mail.Contact) {
	p := &cs.rcpt.picker
	if !p.open {
		return
	}
	f := p.field
	cs.rcpt.tokens[f] = append(cs.rcpt.tokens[f], ct.Address().String())
	cs.setJoined(f)
	cs.changed()
	if cs.rcpt.checks[f] != nil {
		cs.checkRecipients(false, f)
	}
	p.hi = 0
	cs.loadContacts()
	cs.redraw()
}

// syncField reads a field's text into its chips and its input when the
// window was given another text, as a draft opened or a message replied
// to, keeping the field's text as it is.
func (cs *composeState) syncField(f recipientField) {
	text := *cs.fieldText(f)
	if text == cs.rcpt.joined[f] {
		return
	}
	cs.rcpt.tokens[f], cs.rcpt.entries[f] = splitRecipients(text)
	cs.rcpt.joined[f] = text
	cs.rcpt.shown[f] = cs.rcpt.entries[f]
}

// splitRecipients reads a field's text as chips and the entry being
// typed: every entry but one ended by a comma is a chip, and the last,
// while no comma follows it, is the entry in the input.
func splitRecipients(text string) (tokens []string, entry string) {
	entries := mail.RecipientEntries(text)
	if len(entries) == 0 {
		return nil, ""
	}
	last := entries[len(entries)-1]
	for _, e := range entries[:len(entries)-1] {
		tokens = append(tokens, e.Text)
	}
	if last.End < len(text) {
		return append(tokens, last.Text), ""
	}
	return tokens, last.Text
}

// joinRecipients is a field's text as its chips and its input make it.
func joinRecipients(tokens []string, entry string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteString(strings.TrimSpace(token))
		b.WriteString(", ")
	}
	b.WriteString(strings.TrimLeftFunc(entry, unicode.IsSpace))
	return b.String()
}

// setJoined writes a field's text from its chips and its input.
func (cs *composeState) setJoined(f recipientField) {
	text := joinRecipients(cs.rcpt.tokens[f], cs.rcpt.entries[f])
	cs.rcpt.joined[f] = text
	*cs.fieldText(f) = text
}

// offerList shows the contacts offered below a field: each one's name and
// address, the parts the words typed begin in bold, and the one the arrows
// are on in the accent color, which it returns.
func (cs *composeState) offerList(c *ui.Context, t *ui.Theme, anchor ui.Element, o *offer) ui.Element {
	var active ui.Element
	ui.Overlay(c, func() {
		panel := ui.Column(c).AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart).MinWidth(anchor.Bounds().W).
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

// recipientKey takes the keys of a field's input: the arrows move among
// the contacts offered while the list shows, and Enter and Tab take the
// one they are on. In an input holding nothing and offering nothing, Down
// opens the address book the + button does. A comma, Enter or Tab ends
// the entry typed and makes a chip of it, and Backspace in an empty input
// takes the last chip out. The input keeps the other keys.
func (cs *composeState) recipientKey(f recipientField, k ui.TextKey) bool {
	o := cs.rcpt.offer
	// The offer list shows only for the field it was made for and the text
	// it was made from; another field's offer asks nothing of this one.
	shown := o != nil && o.field == f && k.Text() == o.text && o.covers(byteAt(o.text, cs.rcpt.carets[f]))
	if shown && k.Mods == 0 {
		switch k.Key {
		case ui.KeyDown:
			o.hi, o.chose = min(o.hi+1, len(o.contacts)-1), true
			cs.redraw()
			return true
		case ui.KeyUp:
			o.hi, o.chose = max(o.hi-1, 0), true
			cs.redraw()
			return true
		case ui.KeyEnter, ui.KeyTab:
			if o.hi >= 0 {
				cs.take(f, o.contacts[o.hi])
				return true
			}
		}
	}
	if k.Mods != 0 {
		return false
	}
	switch {
	case k.Key == ui.KeyDown && cs.rcpt.entries[f] == "" && !shown && !cs.rcpt.picker.open:
		// The + button keeps out of the tab order, so Tab moves between
		// fields; Down in an empty field is how the address book opens.
		cs.toggleContactPicker(f)
		return true
	case k.Key == ui.KeyEnter:
		return cs.commitEntry(f, k)
	case k.Key == ui.KeyBackspace && cs.rcpt.entries[f] == "" && len(cs.rcpt.tokens[f]) > 0:
		cs.rcpt.tokens[f] = cs.rcpt.tokens[f][:len(cs.rcpt.tokens[f])-1]
		k.SetText("")
		cs.setJoined(f)
		cs.changed()
		if cs.rcpt.checks[f] != nil {
			cs.checkRecipients(false, f)
		}
		cs.redraw()
		return true
	case k.Key == ui.KeyTab && strings.TrimSpace(k.Text()) != "":
		// The entry becomes a chip, and the keyboard moves on as Tab does.
		cs.commitEntry(f, k)
		return false
	}
	return false
}

// commitEntry makes chips of the entries typed in a field, and leaves an
// empty input after them.
func (cs *composeState) commitEntry(f recipientField, k ui.TextKey) bool {
	text := strings.TrimSpace(k.Text())
	if text == "" {
		return false
	}
	tokens := cs.rcpt.tokens[f]
	for _, e := range mail.RecipientEntries(text) {
		tokens = append(tokens, e.Text)
	}
	k.SetText("")
	cs.rcpt.tokens[f] = tokens
	cs.rcpt.entries[f] = ""
	cs.rcpt.shown[f] = ""
	cs.setJoined(f)
	cs.dismissOffer()
	cs.changed()
	if cs.rcpt.checks[f] != nil {
		cs.checkRecipients(false, f)
	}
	cs.redraw()
	return true
}

// take makes a chip of a contact offered in place of the entry typed.
func (cs *composeState) take(f recipientField, ct mail.Contact) {
	o := cs.rcpt.offer
	if o == nil || o.field != f {
		return
	}
	old := cs.rcpt.entries[f]
	tokens := append(append([]string(nil), cs.rcpt.tokens[f]...), ct.Address().String())
	cs.rcpt.edit = &recipientEdit{field: f, text: old, from: 0, to: len(old), s: "", quiet: true, tokens: tokens}
	cs.dismissOffer()
	cs.redraw()
}

// dismissOffer closes the list of offers, and drops the query under way.
func (cs *composeState) dismissOffer() {
	cs.rcpt.seq.Add(1)
	cs.rcpt.offer = nil
}

// recipientChanged follows an edit of a field's input: a pasted list is
// split into chips, the draft saves it, a field whose problems show is
// checked again, and the address book is asked what the entry under the
// caret may be.
func (cs *composeState) recipientChanged(f recipientField) {
	if cs.splitPasted(f) {
		return
	}
	cs.setJoined(f)
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

// splitPasted makes chips of the entries a pasted list ended with a comma,
// and leaves the entry after the last comma to type on. It reports whether
// the input held such a list.
func (cs *composeState) splitPasted(f recipientField) bool {
	text := cs.rcpt.entries[f]
	if !strings.Contains(text, ",") {
		return false
	}
	tokens := cs.rcpt.tokens[f]
	cut := 0
	for _, e := range mail.RecipientEntries(text) {
		if e.End >= len(text) {
			break
		}
		tokens = append(tokens, e.Text)
		cut = e.End + 1
	}
	if cut == 0 {
		return false
	}
	// The separators after the last entry are the list's; what follows
	// them is the entry to type on.
	for cut < len(text) && strings.ContainsRune(" \t\r\n,", rune(text[cut])) {
		cut++
	}
	rest := text[cut:]
	cs.rcpt.edit = &recipientEdit{field: f, text: text, from: 0, to: len(text), s: rest, tokens: tokens}
	cs.redraw()
	return true
}

// lookUp asks the address book, in a job, for the contacts the entry
// under a field's caret may be. A query a later one overtook does not
// run, and an answer for text that changed since is dropped.
func (cs *composeState) lookUp(f recipientField) {
	text := cs.rcpt.entries[f]
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
			if cs.rcpt.seq.Load() != seq || cs.closed || cs.rcpt.entries[f] != text {
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

// worthLookingUp reports whether an entry says enough to look it up: a
// word of one character is, as a name or an address begins it.
func worthLookingUp(text string) bool {
	return len(mail.ContactWords(text)) > 0
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
	if from < 0 || from+len(p.Text) > len(ch.text) {
		return
	}
	text := ch.text[:from] + ct.Address().String() + ch.text[from+len(p.Text):]
	tokens, entry := splitRecipients(text)
	old := cs.rcpt.entries[f]
	cs.rcpt.edit = &recipientEdit{field: f, text: old, from: 0, to: len(old), s: entry, quiet: true, tokens: tokens}
	cs.focused = focusOf(f)
	cs.redraw()
}
