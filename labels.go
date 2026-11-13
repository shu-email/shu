package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Labels in the window. The sidebar has a section of the labels of the
// account on screen, each of which opens its list (mail.LabelScope) as a
// folder's entry opens the folder: its color, its name, its unread count,
// and a mark on one kept on this device only. Labels go on the rows chosen
// and come off them from the toolbar's checklist, the list's menu and, in
// a conversation, a message's own menu; rows and the reading pane show them
// as chips. A label is made, renamed and recolored in a dialog, and is
// deleted, which keeps every message, only once the user said so.

// labelDeleteTimeout bounds the deletion of a label, which looks for its
// keyword in every folder of the server.
const labelDeleteTimeout = 2 * time.Minute

// rowLabels is how many chips a row of the list shows before it says how
// many more there are, so that they stay on its line.
const rowLabels = 2

// labelState is what the window has of labels.
type labelState struct {
	// of are each account's labels, by its ID, as last read, with their
	// unread counts as the window shows them. read is the read each came
	// from, of reads so far: of two reads of an account, the one begun
	// last is kept, whichever ends first.
	of    map[string][]mail.Label
	read  map[string]uint64
	reads uint64
	// open is whether the sidebar's section shows them, and picker
	// whether the toolbar's checklist does.
	open   bool
	picker bool
	form   labelForm
	del    labelDeletion
	// note says that a label just put on messages is kept on this device
	// only, until it is dismissed or retires.
	note      string
	noteTimer *time.Timer
}

func newLabelState() labelState {
	return labelState{of: map[string][]mail.Label{}, read: map[string]uint64{}, open: true}
}

// labelEntryPrefix begins the ID of a label's entry in the sidebar, which
// a.folder holds while its list shows. No folder's name has a NUL.
const labelEntryPrefix = "\x00label/"

// labelEntry is the sidebar's entry of a label's list.
func labelEntry(id mail.LabelID) string {
	return labelEntryPrefix + strconv.FormatInt(int64(id), 10)
}

// labelOf is the label whose list an entry of the sidebar is, or 0.
func labelOf(entry string) mail.LabelID {
	rest, ok := strings.CutPrefix(entry, labelEntryPrefix)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return mail.LabelID(id)
}

// findLabel is one of an account's labels as the window has them, or nil.
func (a *app) findLabel(account string, id mail.LabelID) *mail.Label {
	list := a.labels.of[account]
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

// shownLabel is the label whose list shows, or nil.
func (a *app) shownLabel() *mail.Label {
	if id := labelOf(a.folder); id != 0 {
		return a.findLabel(a.active, id)
	}
	return nil
}

// labelPlace orders an account's labels as they are listed; a label the
// window does not know goes last.
func (a *app) labelPlace(account string, id mail.LabelID) int {
	if i := slices.IndexFunc(a.labels.of[account], func(l mail.Label) bool { return l.ID == id }); i >= 0 {
		return i
	}
	return len(a.labels.of[account])
}

// loadLabels reads accounts' labels from the cache, with their unread
// counts.
func (a *app) loadLabels(ids ...string) {
	for _, id := range ids {
		if a.account(id) == nil {
			continue
		}
		a.labels.reads++
		seq, accountID := a.labels.reads, id
		a.run(func() {
			ctx, cancel := a.ctx()
			defer cancel()
			list, err := a.svc.Labels(ctx, accountID)
			a.post(func() {
				if err != nil || a.account(accountID) == nil || seq < a.labels.read[accountID] {
					return
				}
				a.labels.read[accountID] = seq
				a.labels.of[accountID] = list
				a.labelsRead(accountID)
			})
		})
	}
}

// labelAccounts are the accounts whose labels the window shows: the one
// on screen, and in a list of several accounts each of them, whose rows
// show their own.
func (a *app) labelAccounts() []string {
	if !a.spansAccounts() {
		return []string{a.active}
	}
	ids := make([]string, 0, len(a.accounts))
	for _, acc := range a.accounts {
		ids = append(ids, acc.ID)
	}
	return ids
}

// labelsRead follows an account's labels as read: the list of a label
// that is gone gives way to the inbox.
func (a *app) labelsRead(accountID string) {
	if accountID == a.active && labelOf(a.folder) != 0 && a.shownLabel() == nil {
		a.folder = ""
		a.ensureFolder()
		a.selectFolder()
	}
}

// labelUnread changes the unread counts of a message's labels by n, as
// the window shows them until they are read again, when its folder is one
// their lists show.
func (a *app) labelUnread(m mail.Summary, n int) {
	if len(m.Labels) == 0 {
		return
	}
	for _, f := range a.foldersFor(m.Account) {
		if f.Name == m.Folder && !f.InLabelScope() {
			return
		}
	}
	list := a.labels.of[m.Account]
	for i := range list {
		if m.HasLabel(list[i].ID) {
			list[i].Unread = max(0, list[i].Unread+n)
		}
	}
}

// keptOnServer reports whether an account's folder keeps a label's
// keyword, as the window knows its folders: one not opened to change yet
// is taken to, as the engine first tries the server there.
func (a *app) keptOnServer(account, folder, keyword string) bool {
	for _, f := range a.foldersFor(account) {
		if f.Name == folder {
			return !f.KeywordsKnown() || f.KeepsKeyword(keyword)
		}
	}
	return true
}

// labelInk is a color of the palette as the window draws it: ink for a
// label's name over a tint of it, and dot for its swatch. Each color has
// a shade for each appearance, darker in the light one and lighter in the
// dark one, so that the names read on either background.
type labelInk struct{ ink, dot ui.Color }

// labelInks are the shades of the palette: the light appearance's, then
// the dark's.
var labelInks = map[mail.LabelColor][2]labelInk{
	mail.LabelRed:    {{ui.Hex("#b91c1c"), ui.Hex("#ef4444")}, {ui.Hex("#fca5a5"), ui.Hex("#f87171")}},
	mail.LabelOrange: {{ui.Hex("#9a3412"), ui.Hex("#f97316")}, {ui.Hex("#fdba74"), ui.Hex("#fb923c")}},
	mail.LabelYellow: {{ui.Hex("#854d0e"), ui.Hex("#eab308")}, {ui.Hex("#fde047"), ui.Hex("#facc15")}},
	mail.LabelGreen:  {{ui.Hex("#166534"), ui.Hex("#22c55e")}, {ui.Hex("#86efac"), ui.Hex("#4ade80")}},
	mail.LabelTeal:   {{ui.Hex("#0f766e"), ui.Hex("#14b8a6")}, {ui.Hex("#5eead4"), ui.Hex("#2dd4bf")}},
	mail.LabelBlue:   {{ui.Hex("#1d4ed8"), ui.Hex("#3b82f6")}, {ui.Hex("#93c5fd"), ui.Hex("#60a5fa")}},
	mail.LabelPurple: {{ui.Hex("#7e22ce"), ui.Hex("#a855f7")}, {ui.Hex("#d8b4fe"), ui.Hex("#c084fc")}},
	mail.LabelPink:   {{ui.Hex("#be185d"), ui.Hex("#ec4899")}, {ui.Hex("#f9a8d4"), ui.Hex("#f472b6")}},
}

// noInk is how a color the palette does not have is drawn, as one a newer
// version of the app wrote.
var noInk = [2]labelInk{{ui.Hex("#52525b"), ui.Hex("#a1a1aa")}, {ui.Hex("#d4d4d8"), ui.Hex("#a1a1aa")}}

// inkOf is how a label's color is drawn in an appearance.
func inkOf(color mail.LabelColor, dark bool) labelInk {
	shades, ok := labelInks[color]
	if !ok {
		shades = noInk
	}
	if dark {
		return shades[1]
	}
	return shades[0]
}

// chipTint is the background of a chip of a color: a tint of its dot.
func chipTint(s labelInk) ui.Color { return s.dot.Alpha(0.16) }

// colorName is a color of the palette in the user's words.
func colorName(color mail.LabelColor) string {
	switch color {
	case mail.LabelRed:
		return locales.ColorRed()
	case mail.LabelOrange:
		return locales.ColorOrange()
	case mail.LabelYellow:
		return locales.ColorYellow()
	case mail.LabelGreen:
		return locales.ColorGreen()
	case mail.LabelTeal:
		return locales.ColorTeal()
	case mail.LabelBlue:
		return locales.ColorBlue()
	case mail.LabelPurple:
		return locales.ColorPurple()
	case mail.LabelPink:
		return locales.ColorPink()
	}
	return string(color)
}

// labelChip draws a label as a chip: its dot and its name, in its color
// over a tint of it, and the mark of a label kept on this device only. On
// the row chosen it takes the row's colors, as the tint would not show on
// the accent.
func labelChip(c *ui.Context, t *ui.Theme, l mail.Label, local, chosen bool, size float32) ui.Element {
	return chipOf(c, t, l, local, chosen, size, nil)
}

// chipOf is labelChip with what after draws at its end, in its ink.
func chipOf(c *ui.Context, t *ui.Theme, l mail.Label, local, chosen bool, size float32, after func(ink ui.Color)) ui.Element {
	s := inkOf(l.Color, t.Dark)
	ink, tint, line := s.ink, chipTint(s), s.dot.Alpha(0.4)
	if chosen {
		ink, tint, line = t.AccentText, t.AccentText.Alpha(0.16), t.AccentText.Alpha(0.45)
	}
	name := l.Name
	if local {
		name = locales.LabelOnDevice(l.Name)
	}
	end := float32(7)
	if after != nil {
		end = 2
	}
	// Short of room, a chip shrinks and its name ends in an ellipsis.
	return ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).Padding(1, end, 1, 6).Radius(999).
		Background(tint).Border(1, line).Label(name).Children(func() {
		dot := ui.Box(c).Size(size-4, size-4).Radius((size - 4) / 2).Shrink(0).Background(s.dot)
		if chosen {
			dot.Border(1, t.AccentText)
		}
		ui.Text(c, l.Name).FontSize(size).TextColor(ink).SingleLine().MinWidth(0).MaxWidth(9 * size)
		if local {
			ui.Icon(c, cloudOffIcon).FontSize(size - 1).TextColor(ink).Shrink(0)
		}
		if after != nil {
			after(ink)
		}
	})
}

// shownLabels are the labels of a message, or of a conversation, that the
// window knows, each with whether it is kept on this device only.
func (a *app) shownLabels(account string, labels []mail.MessageLabel) []mail.MessageLabel {
	out := make([]mail.MessageLabel, 0, len(labels))
	for _, ml := range labels {
		if a.findLabel(account, ml.ID) != nil {
			out = append(out, ml)
		}
	}
	return out
}

// convLabels are the labels of a conversation's row: those any of its
// messages in the list has, kept on this device only when the label is.
func (a *app) convLabels(conv mail.Conversation) []mail.MessageLabel {
	out := make([]mail.MessageLabel, 0, len(conv.Labels))
	for _, id := range conv.Labels {
		if l := a.findLabel(conv.Account, id); l != nil {
			out = append(out, mail.MessageLabel{ID: id, Local: l.Local})
		}
	}
	return out
}

// rowLabelsOf are the labels a row shows: its message's, or its
// conversation's.
func (a *app) rowLabelsOf(i int) []mail.MessageLabel {
	if conv := a.convAt(i); conv != nil {
		return a.convLabels(*conv)
	}
	if i < 0 || i >= len(a.all) {
		return nil
	}
	return a.shownLabels(a.all[i].Account, a.all[i].Labels)
}

// labelNames names labels in words, as assistive technology reads them.
func (a *app) labelNames(account string, labels []mail.MessageLabel) string {
	names := make([]string, 0, len(labels))
	for _, ml := range labels {
		if l := a.findLabel(account, ml.ID); l != nil {
			name := l.Name
			if ml.Local {
				name = locales.LabelOnDevice(l.Name)
			}
			names = append(names, name)
		}
	}
	return strings.Join(names, locales.ListSeparator())
}

// labelChips draws labels as chips on one line, the first limit of them,
// then how many more there are; the caller bounds the line's width.
func (a *app) labelChips(c *ui.Context, t *ui.Theme, account string, labels []mail.MessageLabel, limit int, chosen bool) ui.Element {
	return ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).Children(func() {
		for i, ml := range labels {
			if limit > 0 && i == limit {
				more := len(labels) - limit
				color := t.TextMuted
				if chosen {
					color = t.AccentText
				}
				ui.Text(c, "+"+strconv.Itoa(more)).FontSize(10).Bold().TextColor(color).Shrink(0).
					Label(locales.MoreLabels(more))
				return
			}
			if l := a.findLabel(account, ml.ID); l != nil {
				labelChip(c, t, *l, ml.Local, chosen, 10)
			}
		}
	})
}

// messageLabels draws, in the reading pane, the labels of the message on
// show, each with a button that takes it off.
func (a *app) messageLabels(c *ui.Context, t *ui.Theme, m mail.Summary) {
	labels := a.shownLabels(m.Account, m.Labels)
	if len(labels) == 0 || m.DraftID != "" {
		return
	}
	ui.Row(c).Wrap().Gap(6).AlignItems(ui.Center).Label(locales.MessageLabels()).Children(func() {
		for _, ml := range labels {
			l := *a.findLabel(m.Account, ml.ID)
			ui.Row(c.Key(ml.ID)).Children(func() {
				chipOf(c, t, l, ml.Local, false, 11, func(ink ui.Color) {
					remove := locales.RemoveLabel(l.Name)
					b := ui.ButtonBase(c).Label(remove).Tooltip(remove).Padding(2).Radius(999).
						OnClick(func() { a.setLabel(oneMessage(m), l, false) })
					if b.Hovered() {
						b.Background(ink.Alpha(0.18))
					}
					b.Children(func() { ui.Icon(c, closeIcon).FontSize(10).TextColor(ink) })
				})
			})
		}
	})
}

// labelsSection is the sidebar's section of the labels of the account on
// screen, once they are read, and the button that makes one.
func (a *app) labelsSection(c *ui.Context, t *ui.Theme) {
	list, known := a.labels.of[a.active]
	if !known {
		return
	}
	ui.SidebarSection(c, locales.Labels(), &a.labels.open, func() {
		for _, l := range list {
			a.labelItem(c, t, l)
		}
		a.newLabelButton(c, t)
	})
}

// labelItem is a label's entry in the sidebar: its swatch where a
// folder's icon is, its name, the mark of one kept on this device only and
// its unread count, and its menu.
func (a *app) labelItem(c *ui.Context, t *ui.Theme, l mail.Label) {
	id := labelEntry(l.ID)
	dot := inkOf(l.Color, t.Dark).dot
	ring := ui.Transparent
	if a.folder == id {
		ring = t.AccentText
	}
	icon, gap := float32(16), t.Space(2)
	item := ui.SidebarItem(c, id, nil, l.Name).PaddingStart(gap + icon + gap).
		Draw(func(p *ui.Painter, r ui.Rect) {
			const d = 10
			x, y := r.X+gap+(icon-d)/2, r.Y+(r.H-d)/2
			p.Fill(ui.Rect{X: x - 1.5, Y: y - 1.5, W: d + 3, H: d + 3}, ring, (d+3)/2)
			p.Fill(ui.Rect{X: x, Y: y, W: d, H: d}, dot, d/2)
		}).
		PopoverMenu(func(m *ui.Menu) { a.labelActions(m, l) })
	if l.Local {
		item.Description(locales.LabelLocalDetail(l.Name))
	}
	item.Children(func() {
		if l.Local {
			// In the item's own color, faded, which reads on the accent of
			// the item chosen too.
			ui.Icon(c, cloudOffIcon).FontSize(13).Opacity(0.6).Shrink(0).
				Label(locales.LabelLocal()).Tooltip(locales.LabelLocalDetail(l.Name))
		}
		switch {
		case a.labels.del.busy && a.labels.del.label.ID == l.ID:
			ui.Spinner(c).Size(12, 12)
		case l.Unread > 0:
			ui.Badge(c, strconv.Itoa(l.Unread)).Background(t.Accent).TextColor(t.AccentText)
		}
	})
}

// newLabelButton makes a label of the account on screen.
func (a *app) newLabelButton(c *ui.Context, t *ui.Theme) {
	b := ui.ButtonBase(c).Label(locales.NewLabel()).Padding(t.Space(1.5), t.Space(2)).Radius(t.Radius).
		Justify(ui.Start).OnClick(func() { a.openLabelForm(a.active, nil, nil) })
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, plusIcon).FontSize(16).TextColor(t.TextMuted)
			ui.Text(c, locales.NewLabel()).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// labelActions are a label's menu, in the sidebar and in its list's
// header: edit it or delete it.
func (a *app) labelActions(m *ui.Menu, l mail.Label) {
	if m.Item(locales.EditLabelEllipsis()).Chosen() {
		a.openLabelForm(l.Account, &l, nil)
	}
	if m.Item(locales.DeleteLabelEllipsis()).Disabled(a.labels.del.busy).Chosen() {
		a.askDeleteLabel(l)
	}
}

// labelHeaderMenu is the menu of the label whose list shows, in the list's
// header, where the keyboard reaches it.
func (a *app) labelHeaderMenu(c *ui.Context, l mail.Label) {
	label := locales.LabelActions(l.Name)
	ui.Button(c, "").Label(label).Tooltip(label).Padding(barPad, barPad).Radius(barButton).
		Children(func() { ui.Icon(c, moreIcon).FontSize(16) }).
		PopoverMenuButton(func(m *ui.Menu) { a.labelActions(m, l) })
}

// labelTargets are the rows labels go on, or come off: messages, or
// conversations, which stand for their messages in the list's scope.
type labelTargets struct {
	msgs  []mail.Summary
	convs []mail.Conversation
	// account is the rows' account, "" when they are of several, which
	// accounts counts; drafts reports a draft of this device among them.
	account  string
	accounts int
	drafts   bool
}

// labelTargets are the rows chosen, as the toolbar's actions take them.
func (a *app) labelTargets() labelTargets {
	var lt labelTargets
	seen := map[string]bool{}
	of := func(account string) {
		if !seen[account] {
			seen[account] = true
			lt.account = account
			lt.accounts++
		}
	}
	if a.threaded() {
		lt.convs = a.threadTargets()
		for _, conv := range lt.convs {
			of(conv.Account)
		}
	} else {
		lt.msgs = a.targets()
		for _, m := range lt.msgs {
			of(m.Account)
			lt.drafts = lt.drafts || m.DraftID != ""
		}
	}
	if lt.accounts != 1 {
		lt.account = ""
	}
	return lt
}

// oneMessage is a message as the rows labels go on.
func oneMessage(m mail.Summary) labelTargets {
	return labelTargets{msgs: []mail.Summary{m}, account: m.Account, accounts: 1, drafts: m.DraftID != ""}
}

func (lt labelTargets) empty() bool { return len(lt.msgs) == 0 && len(lt.convs) == 0 }

// state is whether the rows have a label: all of them, some or none. A
// conversation has the labels any of its messages in the list has.
func (lt labelTargets) state(id mail.LabelID) ui.CheckState {
	n := 0
	for _, m := range lt.msgs {
		if m.HasLabel(id) {
			n++
		}
	}
	for _, conv := range lt.convs {
		if slices.Contains(conv.Labels, id) {
			n++
		}
	}
	switch {
	case n == 0:
		return ui.CheckOff
	case n == len(lt.msgs)+len(lt.convs):
		return ui.CheckOn
	}
	return ui.CheckMixed
}

// keptHereFor reports whether a label put on rows stays on this device for
// some of them: their folder keeps none of its keyword, as the window
// knows the folders. A conversation is taken to be where its newest
// message is.
func (a *app) keptHereFor(lt labelTargets, l mail.Label) bool {
	return slices.ContainsFunc(lt.msgs, func(m mail.Summary) bool { return !a.keptOnServer(m.Account, m.Folder, l.Keyword) }) ||
		slices.ContainsFunc(lt.convs, func(conv mail.Conversation) bool {
			return !a.keptOnServer(conv.Account, conv.Latest.Folder, l.Keyword)
		})
}

// labelsWhyNot says why the rows chosen take no labels, or "" when they
// take their account's.
func (a *app) labelsWhyNot(lt labelTargets) string {
	switch {
	case lt.empty():
		return locales.LabelsChooseMail()
	case lt.accounts > 1:
		return locales.LabelsSeveralAccounts()
	case lt.drafts || a.draftsList():
		return locales.LabelsNoDrafts()
	}
	return ""
}

// labelsOffered reports whether the toolbar's checklist opens over the
// rows chosen: to label them, or to say why rows of several accounts
// take no labels together.
func (a *app) labelsOffered(lt labelTargets) bool {
	return a.labelsWhyNot(lt) == "" || lt.accounts > 1
}

// openLabelPicker opens the toolbar's checklist, as L does.
func (a *app) openLabelPicker() {
	if a.labelsOffered(a.labelTargets()) {
		a.labels.picker = true
	}
}

// labelButton is the toolbar's button that opens the checklist of labels
// of the rows chosen.
func (a *app) labelButton(c *ui.Context, lt labelTargets) ui.Element {
	on := a.labelsOffered(lt)
	b := ui.Button(c, "").Label(locales.ChangeLabels()).Tooltip(locales.ChangeLabelsTip()).Disabled(!on).
		Padding(barPad, barPad).Radius(barButton).HasPopup(ui.PopupDialog).Expanded(a.labels.picker)
	if on {
		b = b.OnClick(func() { a.labels.picker = !a.labels.picker })
	}
	return b.Children(func() { ui.Icon(c, tagIcon).FontSize(16) })
}

// labelPicker is the checklist the toolbar's button opens below it.
func (a *app) labelPicker(c *ui.Context, t *ui.Theme, anchor ui.Element, lt labelTargets) {
	ui.PopoverBase(c, anchor, &a.labels.picker, func(panel ui.Element) {
		panel.Margin(6, 0, 0, 0).Radius(10).Background(t.Background).Border(1, t.Border).
			Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.18)).Label(locales.Labels())
		a.labelChecklist(c, t, lt)
	})
}

// labelChecklist is the account's labels, each checked when every row
// chosen has it, mixed when some do: a click puts it on all of them, or
// takes it off all once they all have it. Below them, a label is made and
// put on the rows.
func (a *app) labelChecklist(c *ui.Context, t *ui.Theme, lt labelTargets) {
	ui.Column(c).Width(264).Padding(10, 6, 8, 6).Gap(2).Children(func() {
		ui.Text(c, locales.Labels()).FontSize(13).Bold().Padding(0, 8, 4, 8)
		if why := a.labelsWhyNot(lt); why != "" {
			ui.Text(c, why).FontSize(12).TextColor(t.TextMuted).Padding(2, 8, 6, 8).MaxLines(5)
			return
		}
		labels := a.labels.of[lt.account]
		if len(labels) == 0 {
			ui.Text(c, locales.NoLabelsYet()).FontSize(12).TextColor(t.TextMuted).Padding(2, 8, 6, 8)
		}
		marked := false
		ui.Column(c).Gap(1).Label(locales.Labels()).Children(func() {
			for i, l := range labels {
				state := lt.state(l.ID)
				// Marked are the labels that stay on this device for these
				// rows, whose folder keeps no such keyword.
				local := l.Local || a.keptHereFor(lt, l)
				marked = marked || local
				box := ui.CheckboxState(c.Key(l.ID), &state, "").Label(l.Name).Padding(5, 8).Radius(6).
					OnClick(func() { a.setLabel(lt, l, state == ui.CheckOn) })
				if box.Hovered() {
					box.Background(t.SurfaceHover)
				}
				if i == 0 {
					box.AutoFocus()
				}
				if local {
					box.Description(locales.LabelLocal())
				}
				dot := inkOf(l.Color, t.Dark).dot
				box.Children(func() {
					ui.Box(c).Size(10, 10).Radius(5).Shrink(0).Background(dot)
					ui.Text(c, l.Name).FontSize(13).SingleLine().Grow(1).MinWidth(0)
					if local {
						ui.Icon(c, cloudOffIcon).FontSize(12).TextColor(t.TextMuted).Shrink(0).
							Tooltip(locales.LabelLocal())
					}
				})
			}
		})
		if marked {
			ui.Row(c).Gap(6).AlignItems(ui.Start).Padding(6, 8, 2, 8).Children(func() {
				ui.Icon(c, cloudOffIcon).FontSize(12).TextColor(t.TextMuted).Shrink(0)
				ui.Text(c, locales.LabelsKeptHereHint()).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0).MaxLines(4)
			})
		}
		ui.Divider(c).Margin(4, 0)
		ui.Button(c, locales.NewLabelEllipsis()).Padding(5, 8).
			OnClick(func() {
				a.labels.picker = false
				a.openLabelForm(lt.account, nil, &lt)
			})
	})
}

// rowMenu is the list's menu of the rows chosen, which a right-click on a
// row opens once it chose the row: their labels, to put on or take off.
func (a *app) rowMenu(m *ui.Menu) {
	lt := a.labelTargets()
	m.Submenu(locales.MoveToFolder(), func(m *ui.Menu) { a.moveFolderMenu(m) })
	m.Submenu(locales.Labels(), func(m *ui.Menu) { a.labelMenuItems(m, lt) })
	a.ruleMenuItem(m, lt)
}

// labelMenuItems are the labels of rows as items of a menu, each checked
// when every row has it and said to be on some when some do: choosing one
// puts it on them all, or takes it off all once they all have it.
func (a *app) labelMenuItems(m *ui.Menu, lt labelTargets) {
	if why := a.labelsWhyNot(lt); why != "" {
		m.Item(why).Disabled(true)
		return
	}
	for _, l := range a.labels.of[lt.account] {
		state := lt.state(l.ID)
		item := m.Item(l.Name).ID(labelEntry(l.ID)).Checked(state == ui.CheckOn)
		if state == ui.CheckMixed {
			item.Trailing(locales.LabelOnSome())
		}
		if l.Local {
			item.Subtitle(locales.LabelLocal())
		}
		if item.Chosen() {
			a.setLabel(lt, l, state != ui.CheckOn)
		}
	}
	if len(a.labels.of[lt.account]) > 0 {
		m.Separator()
	}
	if m.Item(locales.NewLabelEllipsis()).Chosen() {
		a.openLabelForm(lt.account, nil, &lt)
	}
}

// setLabel puts a label on rows, or takes it off: on messages, or on the
// messages conversations stand for in the list's scope, as their other
// actions do. The rows show it at once; once the engine has it the list
// is read again, which shows what it did, and puts back what it could not
// do.
func (a *app) setLabel(lt labelTargets, l mail.Label, on bool) {
	if a.labelsWhyNot(lt) != "" {
		return
	}
	account := lt.account
	shown := a.shownMembers(lt.convs)
	a.showLabel(lt, l, on)
	g, scope := a.gen, a.listScope()
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		msgs, err := lt.msgs, error(nil)
		if lt.convs != nil {
			msgs, err = a.scopeMessages(ctx, scope, lt.convs, shown)
		}
		if err == nil {
			err = a.svc.SetLabel(ctx, account, refsOf(msgs), l.ID, on)
		}
		// The folders say, as the engine opened them, which keep labels.
		var folders []mail.Folder
		if err == nil && on {
			folders, _ = a.svc.CachedFolders(ctx, account)
		}
		a.post(func() {
			if folders != nil {
				a.setAccountFolders(account, folders)
			}
			if a.stale(g) {
				a.loadLabels(account)
				return
			}
			switch {
			case a.signInRequired(account, err):
				a.err = locales.ActionNeedsSignIn()
			case err != nil:
				a.err = locales.LabelChangeFailed(err.Error())
			case on:
				a.noteKeptHere(l, msgs)
			}
			a.readList(g, false, false)
		})
	})
}

// showLabel shows a label put on rows, or taken off, before the engine
// has it: on the list's messages, the rows of conversations, and the
// messages of the conversation open.
func (a *app) showLabel(lt labelTargets, l mail.Label, on bool) {
	keys := map[string]bool{}
	for _, m := range lt.msgs {
		keys[summaryKey(m)] = true
	}
	mark := func(m *mail.Summary) {
		local := !a.keptOnServer(m.Account, m.Folder, l.Keyword)
		m.Labels = slices.DeleteFunc(slices.Clone(m.Labels), func(ml mail.MessageLabel) bool { return ml.ID == l.ID })
		if on {
			m.Labels = append(m.Labels, mail.MessageLabel{ID: l.ID, Local: local})
			slices.SortStableFunc(m.Labels, func(x, y mail.MessageLabel) int {
				return a.labelPlace(m.Account, x.ID) - a.labelPlace(m.Account, y.ID)
			})
		}
	}
	for i := range a.all {
		if keys[summaryKey(a.all[i])] {
			mark(&a.all[i])
		}
	}
	convs := threadSet(lt.convs)
	for i := range a.convs {
		if !convs[refOf(a.convs[i])] {
			continue
		}
		ids := slices.DeleteFunc(slices.Clone(a.convs[i].Labels), func(id mail.LabelID) bool { return id == l.ID })
		if on {
			ids = append(ids, l.ID)
			account := a.convs[i].Account
			slices.SortStableFunc(ids, func(x, y mail.LabelID) int { return a.labelPlace(account, x) - a.labelPlace(account, y) })
		}
		a.convs[i].Labels = ids
	}
	if cv := a.conv; cv != nil {
		whole := convs[cv.ref()]
		for _, m := range cv.msgs {
			if k := summaryKey(m); keys[k] {
				cv.update(k, mark)
			}
		}
		for _, m := range cv.members {
			if k := summaryKey(m); whole && !keys[k] {
				cv.update(k, mark)
			}
		}
		a.syncRow()
	}
}

// noteKeptHere says, once a label went on messages one of whose folders
// keeps no such keyword, that it is kept on this device only there.
func (a *app) noteKeptHere(l mail.Label, msgs []mail.Summary) {
	n := 0
	for _, m := range msgs {
		if !a.keptOnServer(m.Account, m.Folder, l.Keyword) {
			n++
		}
	}
	if n > 0 {
		a.setLabelNote(locales.LabelKeptHereNote(l.Name, n))
	}
}

// setLabelNote shows a note about labels above the list, or clears it,
// and retires it after a while, as the offer to undo a move.
func (a *app) setLabelNote(text string) {
	if a.labels.noteTimer != nil {
		a.labels.noteTimer.Stop()
		a.labels.noteTimer = nil
	}
	a.labels.note = text
	if text == "" || a.after == nil {
		return
	}
	a.labels.noteTimer = a.after(10*time.Second, func() {
		a.post(func() {
			if a.labels.note == text {
				a.labels.note = ""
			}
		})
	})
}

// labelNoteBanner shows the note about labels.
func (a *app) labelNoteBanner(c *ui.Context, t *ui.Theme) {
	ui.Row(c.Key("banner/label-note")).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(8, 14).
		Background(t.Accent.Alpha(0.08)).Transition(bannerMotion).Children(func() {
		ui.Icon(c, cloudOffIcon).FontSize(14).TextColor(t.Accent)
		// A status, so that a screen reader says it once as it appears.
		ui.Text(c, a.labels.note).FontSize(12).TextColor(t.Text).Grow(1).MinWidth(0).MaxLines(4).Role(ui.RoleStatus)
		a.iconButton(c, closeIcon, locales.DismissLabelNote(), locales.DismissLabelNote()).
			OnClick(func() { a.setLabelNote("") })
	})
}

// labelForm is the dialog that makes a label, or renames and recolors
// one.
type labelForm struct {
	open    bool
	account string
	// editing is the label being changed, 0 for a new one, and local
	// whether it is kept on this device only.
	editing mail.LabelID
	local   bool
	name    string
	color   mail.LabelColor
	err     string
	busy    bool
	// apply are the rows a label made from their checklist goes on.
	apply *labelTargets
	// seq tells the dialog opened from those before it.
	seq uint64
}

// openLabelForm opens the dialog of a new label of an account, to go on
// apply's rows when there are any, or of a label to change.
func (a *app) openLabelForm(account string, l *mail.Label, apply *labelTargets) {
	if a.account(account) == nil {
		return
	}
	f := labelForm{open: true, account: account, apply: apply, seq: a.labels.form.seq + 1,
		color: mail.LabelColorAt(len(a.labels.of[account]))}
	if l != nil {
		f.editing, f.local, f.name, f.color = l.ID, l.Local, l.Name, l.Color
	}
	a.labels.form = f
}

// saveLabelForm makes the label of the dialog, or changes it. What the
// engine refuses is said in the dialog, which stays open; a label made
// from the checklist goes on its rows.
func (a *app) saveLabelForm() {
	f := &a.labels.form
	if !f.open || f.busy || strings.TrimSpace(f.name) == "" {
		return
	}
	f.busy, f.err = true, ""
	seq, account, editing, name, color, apply := f.seq, f.account, f.editing, f.name, f.color, f.apply
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var (
			l   mail.Label
			err error
		)
		if editing != 0 {
			l, err = a.svc.UpdateLabel(ctx, account, editing, name, color)
		} else {
			l, err = a.svc.CreateLabel(ctx, account, name, color)
		}
		a.post(func() {
			current := f.seq == seq
			if current {
				f.busy = false
			}
			if err != nil {
				if current {
					f.err = labelFormError(err, name)
				}
				return
			}
			a.loadLabels(account)
			if !current || !f.open {
				return
			}
			f.open = false
			if apply != nil {
				a.setLabel(*apply, l, true)
			}
		})
	})
}

// labelFormError says why the engine refused a label's name or color.
func labelFormError(err error, name string) string {
	switch {
	case errors.Is(err, mail.ErrLabelExists):
		return locales.LabelExists(strings.TrimSpace(name))
	case errors.Is(err, mail.ErrLabelName):
		return locales.LabelNameInvalid(mail.MaxLabelName)
	}
	return locales.LabelSaveFailed(err.Error())
}

// labelDialog is the dialog of a label made or changed: its name, its
// color of the palette, and how it will look.
func (a *app) labelDialog(c *ui.Context) {
	f := &a.labels.form
	if !f.open {
		return
	}
	t := c.Theme()
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(380).Gap(14).Children(func() {
			title, save := locales.NewLabel(), locales.CreateLabel()
			if f.editing != 0 {
				title, save = locales.EditLabel(), locales.Save()
			}
			ui.Text(c, title).FontSize(17).Bold()
			var name ui.Element
			ui.Column(c).Gap(4).Children(func() {
				// The caption names the input, so a click on it focuses it.
				caption := ui.Text(c, locales.LabelNameCaption()).FontSize(11).Bold().TextColor(t.TextMuted)
				name = ui.TextInput(c, &f.name).LabelledBy(caption).Placeholder(locales.LabelNamePlaceholder()).
					FillWidth().AutoFocus().MaxLength(mail.MaxLabelName).Invalid(f.err != "").OnSubmit(a.saveLabelForm)
			})
			ui.Column(c).Gap(6).Children(func() {
				caption := ui.Text(c, locales.LabelColorCaption()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.RadioGroup(c, func() {
					for _, color := range mail.LabelColors {
						colorSwatch(c, t, &f.color, color)
					}
				}).Row().Gap(8).LabelledBy(caption)
			})
			if shown := strings.TrimSpace(f.name); shown != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.LabelPreview()).FontSize(11).TextColor(t.TextMuted)
					labelChip(c, t, mail.Label{Name: shown, Color: f.color}, false, false, 11)
				})
			}
			if f.err != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
					name.ErrorMessage(ui.Text(c, f.err).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus))
				})
			}
			if f.editing != 0 && !f.local {
				ui.Text(c, locales.LabelRenameNote()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.busy {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				ui.Button(c, locales.Cancel()).Label(locales.CancelLabelForm()).OnClick(func() { f.open = false })
				ui.PrimaryButton(c, save).Disabled(f.busy || strings.TrimSpace(f.name) == "").OnClick(a.saveLabelForm)
			})
		})
	})
}

// colorSwatch is a color of the palette to choose in the label dialog: a
// round of it, ringed while it is the one chosen.
func colorSwatch(c *ui.Context, t *ui.Theme, chosen *mail.LabelColor, color mail.LabelColor) {
	ring := ui.Transparent
	if *chosen == color {
		ring = t.Text
	}
	name := colorName(color)
	ui.RadioBase(c, chosen, color).Label(name).Tooltip(name).Size(28, 28).Padding(3).Radius(14).
		Border(2, ring).Children(func() {
		ui.Box(c).Fill().Radius(9).Background(inkOf(color, t.Dark).dot)
	})
}

// labelDeletion is the question before a label is deleted, and the one
// after a deletion that took it off some messages only.
type labelDeletion struct {
	asking bool
	label  mail.Label
	busy   bool
	// partly is set once a deletion took the label off some messages
	// but not all: deleting it again finishes.
	partly bool
}

// askDeleteLabel asks before deleting a label.
func (a *app) askDeleteLabel(l mail.Label) {
	d := &a.labels.del
	d.label, d.asking, d.partly = l, true, false
}

// deleteLabel deletes a label the user said to delete: it comes off every
// message, on the server too, and no message moves.
func (a *app) deleteLabel(l mail.Label) {
	d := &a.labels.del
	if d.busy {
		return
	}
	d.busy, d.label = true, l
	a.run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), labelDeleteTimeout)
		defer cancel()
		err := a.svc.DeleteLabel(ctx, l.Account, l.ID)
		a.post(func() {
			d.busy = false
			switch {
			case errors.Is(err, mail.ErrLabelPartlyRemoved):
				d.partly = true
			case a.signInRequired(l.Account, err):
				a.err = locales.ActionNeedsSignIn()
			case err != nil:
				a.err = locales.LabelDeleteFailed(l.Name, err.Error())
			}
			a.loadLabels(l.Account)
			a.readList(a.gen, false, false)
		})
	})
}

// labelAlerts are the questions about deleting a label.
func (a *app) labelAlerts(c *ui.Context) {
	d := &a.labels.del
	message := locales.DeleteLabelMessage()
	if d.label.Local {
		message = locales.DeleteLabelMessageLocal()
	}
	if ui.AlertDialog(c, &d.asking, locales.DeleteLabelTitle(d.label.Name), message,
		locales.Cancel(), locales.DeleteLabelButton()) == 1 {
		a.deleteLabel(d.label)
	}
	if ui.AlertDialog(c, &d.partly, locales.LabelPartlyTitle(d.label.Name), locales.LabelPartlyMessage(),
		locales.NotNow(), locales.FinishDeleting()) == 1 {
		a.deleteLabel(d.label)
	}
}

// labelFilterChoices are the labels the search filters offer, by name:
// the account's on screen, then, in a search of several accounts, the
// others' of other names.
func (a *app) labelFilterChoices() []mail.Label {
	out := slices.Clone(a.labels.of[a.active])
	if !a.spansAccounts() {
		return out
	}
	for _, acc := range a.accounts {
		for _, l := range a.labels.of[acc.ID] {
			key := mail.LabelNameKey(l.Name)
			if !slices.ContainsFunc(out, func(have mail.Label) bool { return mail.LabelNameKey(have.Name) == key }) {
				out = append(out, l)
			}
		}
	}
	return out
}

// labelValueName is the name label: means, as a label of that name in any
// case writes it, else as typed.
func (a *app) labelValueName(values []string) string {
	value := strings.Join(values, " ")
	key := mail.LabelNameKey(strings.TrimSpace(value))
	for _, l := range a.labelFilterChoices() {
		if mail.LabelNameKey(l.Name) == key {
			return l.Name
		}
	}
	return value
}

// labelFilter is the filter of a label: label:, which keeps the messages
// with a label of that name.
func (a *app) labelFilter(c *ui.Context, q searchQuery) {
	choices := a.labelFilterChoices()
	if len(choices) == 0 && len(q.Labels) == 0 {
		return
	}
	has := func(s searchQuery) bool { return len(s.Labels) > 0 }
	chosen := func(l mail.Label) bool {
		return len(q.Labels) == 1 && mail.LabelNameKey(strings.TrimSpace(q.Labels[0])) == mail.LabelNameKey(l.Name)
	}
	label := locales.AnyLabel()
	if len(q.Labels) > 0 {
		label = a.labelValueName(q.Labels)
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		caption := filterCaption(c, locales.FilterLabel())
		ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
			if m.Item(locales.AnyLabel()).Radio(len(q.Labels) == 0).Chosen() {
				a.writeFilter(has, "")
			}
			m.Separator()
			for _, l := range choices {
				if m.Item(l.Name).Radio(chosen(l)).Chosen() {
					a.writeFilter(has, operator("label", l.Name))
				}
			}
		}).LabelledBy(caption)
	})
}
