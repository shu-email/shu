package main

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The address book is a window of its own, opened from the sidebar's menu
// beside the mail: the contacts in the engine's order, or those a search
// finds, each with its name and address and the ways to make it a
// favorite, rename it and delete it, and a form to add one by hand. It
// reads the book a page at a time as the list nears the end of what it
// read, and from the top again after each change, so that a change shows
// at once where it puts the contact, as it does in the next suggestion.
// Nothing of it leaves the device: the engine reads and writes the book
// in the local database alone.

// contactsAhead is how near the end of the contacts read the list comes
// before the next page is read.
const contactsAhead = 10

// focusAddContact is the button that opens the form of a contact added by
// hand, among the controls addressBook.focus names; the buttons of a row
// are named after its contact (contactFocus).
const focusAddContact = "add"

// addressBook is the address book's view.
type addressBook struct {
	open   bool
	search string
	// contacts are the contacts read, best first, and more whether others
	// follow them. reading is a read under way, and reads counts the
	// reads from the top: a page of an earlier read is dropped, as is an
	// answer to a search typed over since.
	contacts []mail.Contact
	more     bool
	reading  bool
	reads    uint64
	readErr  string
	list     ui.ListState
	// The form of a contact added by hand, and why the engine refused
	// the last one.
	adding      bool
	name, email string
	addErr      string
	// renaming is the contact whose name is being edited, by its key,
	// and newName the name typed for it.
	renaming string
	newName  string
	// deleting is the contact the question before deleting is about,
	// asked while asking is set.
	deleting mail.Contact
	asking   bool
	// busy is an edit under way, and err why the last one failed.
	busy bool
	err  string
	// focus is the control the keyboard is on, among the add button and
	// the buttons of the rows, which an edit moves it back to.
	focus string
}

// contactFocus names a button of a contact's row among the controls
// addressBook.focus names.
func contactFocus(button string, ct mail.Contact) string {
	return button + "/" + mail.ContactKey(ct.Email)
}

// contactsWindowOptions is the address book's window: its own, so that the
// mail stays readable beside it, and a contact can be looked up, named or
// added while the message it is about is on screen.
func (a *app) contactsWindowOptions() mygo.WindowOptions {
	return mygo.WindowOptions{
		Title:           locales.Contacts(),
		Width:           640,
		Height:          680,
		MinWidth:        460,
		MinHeight:       420,
		TitleBarStyle:   mygo.TitleBarHiddenInset,
		BackgroundColor: "light-dark(#ffffff, #18181b)",
		Content:         ui.View(a.contactsView),
	}
}

// openContacts shows the address book, read anew. One window serves every
// opening: a second call brings the open one forward.
func (a *app) openContacts() {
	if a.book.open && a.contactsWin != nil {
		a.contactsWin.Show()
		a.contactsWin.Focus()
		a.contactsWin.Invalidate()
		return
	}
	a.book = addressBook{open: true, reads: a.book.reads}
	a.readContacts(0)
	if a.newWindow == nil {
		// Tests run the view without a window: the book shows in the
		// main view.
		return
	}
	win := a.newWindow(a.contactsWindowOptions())
	if win == nil {
		return
	}
	a.contactsWin = win
	win.OnClosed(func() {
		// Closed by the user, not by closeContacts, which has already
		// gone back to the mail.
		if a.contactsWin == win {
			a.contactsWin = nil
			a.closeContacts()
		}
	})
}

// closeContacts closes the address book, or leaves its view in the main
// window. Reads and edits still under way change nothing once they answer.
func (a *app) closeContacts() {
	a.book = addressBook{reads: a.book.reads + 1}
	if win := a.contactsWin; win != nil {
		a.contactsWin = nil
		win.Close()
	}
}

// readContacts reads the book from the top, the contacts the search finds,
// as many as keep and a page at least: after an edit, as many as the list
// showed, so that it keeps its length while the change moves a contact.
func (a *app) readContacts(keep int) {
	b := &a.book
	b.reads++
	seq, search, limit := b.reads, b.search, max(keep, mail.DefaultPageSize)
	b.reading = true
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		page, err := a.svc.Contacts(ctx, search, 0, limit)
		a.post(func() {
			if !b.open || b.reads != seq {
				return
			}
			b.reading = false
			if err != nil {
				b.readErr = locales.ContactsReadFailed(err.Error())
				return
			}
			b.readErr = ""
			b.contacts, b.more = page.Contacts, page.More
		})
	})
}

// readMoreContacts reads the page after the contacts read, as the list
// nears their end. A contact that moved up meanwhile, and so comes again,
// stays where it was read first.
func (a *app) readMoreContacts() {
	b := &a.book
	if b.reading || !b.more || b.readErr != "" {
		return
	}
	b.reading = true
	seq, search, offset := b.reads, b.search, len(b.contacts)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		page, err := a.svc.Contacts(ctx, search, offset, mail.DefaultPageSize)
		a.post(func() {
			if !b.open || b.reads != seq {
				return
			}
			b.reading = false
			if err != nil {
				b.readErr = locales.ContactsReadFailed(err.Error())
				return
			}
			for _, ct := range page.Contacts {
				key := mail.ContactKey(ct.Email)
				if !slices.ContainsFunc(b.contacts, func(have mail.Contact) bool { return mail.ContactKey(have.Email) == key }) {
					b.contacts = append(b.contacts, ct)
				}
			}
			b.more = page.More
		})
	})
}

// searchContacts reads the book for what the search field holds, from the
// top.
func (a *app) searchContacts() {
	a.book.list.ScrollTo(0, ui.Start)
	a.readContacts(0)
}

// openAddContact opens the form of a contact added by hand.
func (a *app) openAddContact() {
	b := &a.book
	b.adding, b.name, b.email, b.addErr, b.renaming = true, "", "", "", ""
}

// cancelAddContact closes the form, the keyboard going back to the button
// that opened it.
func (a *app) cancelAddContact() {
	b := &a.book
	b.adding, b.addErr, b.focus = false, "", focusAddContact
}

// addContact adds the contact of the form. The engine checks the address,
// and what it refuses is said in the user's words.
func (a *app) addContact() {
	b := &a.book
	if !b.adding || b.busy || strings.TrimSpace(b.email) == "" {
		return
	}
	name, email := b.name, b.email
	b.busy, b.addErr = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, err := a.svc.AddContact(ctx, name, email)
		a.post(func() {
			if !b.open {
				return
			}
			b.busy = false
			if err != nil {
				b.addErr = contactErrorText(err, email)
				return
			}
			b.adding, b.name, b.email, b.focus = false, "", "", focusAddContact
			a.readContacts(len(b.contacts) + 1)
		})
	})
}

// contactErrorText says why a contact was not added.
func contactErrorText(err error, email string) string {
	email = strings.TrimSpace(email)
	switch {
	case errors.Is(err, mail.ErrContactAddress):
		return locales.ContactNotAddress()
	case errors.Is(err, mail.ErrContactExists):
		return locales.ContactExists(email)
	case errors.Is(err, mail.ErrContactOwn):
		return locales.ContactOwn(email)
	}
	return locales.ContactAddFailed(err.Error())
}

// editContact runs an edit of a contact in a job and reads the book again
// once it is in, so that it shows where the edit puts the contact; the
// keyboard then goes to the control focus names. One edit runs at a time.
func (a *app) editContact(edit func(ctx context.Context) error, focus string) {
	b := &a.book
	if b.busy {
		return
	}
	b.busy, b.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := edit(ctx)
		a.post(func() {
			if !b.open {
				return
			}
			b.busy = false
			if err != nil {
				b.err = locales.ContactEditFailed(err.Error())
				return
			}
			b.renaming, b.focus = "", focus
			a.readContacts(len(b.contacts))
		})
	})
}

// favoriteContact makes a contact a favorite, which comes first, or not.
func (a *app) favoriteContact(ct mail.Contact, on bool) {
	a.editContact(func(ctx context.Context) error {
		_, err := a.svc.FavoriteContact(ctx, ct.Email, on)
		return err
	}, contactFocus("star", ct))
}

// startRename opens the name of a contact for editing, in its row.
func (a *app) startRename(ct mail.Contact) {
	b := &a.book
	b.renaming, b.newName, b.err, b.adding = mail.ContactKey(ct.Email), ct.Name, "", false
}

// saveRename gives a contact the name typed, which mail does not change;
// an empty one leaves the naming to its mail again.
func (a *app) saveRename(ct mail.Contact) {
	name := a.book.newName
	a.editContact(func(ctx context.Context) error {
		_, err := a.svc.RenameContact(ctx, ct.Email, name)
		return err
	}, contactFocus("rename", ct))
}

// cancelRename leaves a contact's name as it was.
func (a *app) cancelRename(ct mail.Contact) {
	a.book.renaming, a.book.focus = "", contactFocus("rename", ct)
}

// askDelete asks before deleting a contact.
func (a *app) askDelete(ct mail.Contact) {
	a.book.deleting, a.book.asking = ct, true
}

// deleteContact deletes a contact. The keyboard goes to the contact after
// it, or before it when it was the last, and to the add button when it
// was the only one.
func (a *app) deleteContact(ct mail.Contact) {
	b := &a.book
	focus := focusAddContact
	key := mail.ContactKey(ct.Email)
	if i := slices.IndexFunc(b.contacts, func(have mail.Contact) bool { return mail.ContactKey(have.Email) == key }); i >= 0 {
		switch {
		case i+1 < len(b.contacts):
			focus = contactFocus("delete", b.contacts[i+1])
		case i > 0:
			focus = contactFocus("delete", b.contacts[i-1])
		}
	}
	a.editContact(func(ctx context.Context) error { return a.svc.DeleteContact(ctx, ct.Email) }, focus)
}

// contactsView is the address book: what it is, its search, the form of a
// contact added by hand, and the contacts, best first.
func (a *app) contactsView(c *ui.Context) {
	t := c.Theme()
	b := &a.book
	bar := c.TitleBar()
	top := max(bar.Bottom, 52)
	c.OnShortcut(0, ui.KeyEscape, a.closeContacts)
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Height(top).Shrink(0).AlignItems(ui.Center).Gap(10).
			Padding(0, max(bar.Right, 0)+16, 0, max(bar.Left, 0)+16).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
			ui.Text(c, locales.Contacts()).FontSize(17).Bold()
			ui.Spacer(c)
			ui.Button(c, locales.Done()).OnClick(a.closeContacts)
		})
		ui.Column(c).Grow(1).MinHeight(0).MaxWidth(760).Padding(16, 32, 0, 32).Gap(12).Children(func() {
			ui.Text(c, locales.ContactsDetail()).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.SearchField(c, &b.search).Grow(1).Placeholder(locales.SearchContacts()).
					Label(locales.SearchContacts()).AutoFocus().OnChange(a.searchContacts)
				ui.Button(c, "").Label(locales.AddContact()).FocusBind(&b.focus, focusAddContact).
					OnClick(a.openAddContact).Children(func() {
					ui.Icon(c, plusIcon).FontSize(14)
					ui.Text(c, locales.AddContact())
				})
			})
			if b.adding {
				a.addContactForm(c, t)
			}
			for _, text := range []string{b.err, b.readErr} {
				if text == "" {
					continue
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
					// A status, so that a screen reader says why.
					ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
				})
			}
			a.contactList(c, t)
		})
	})
	who := contactName(b.deleting)
	if ui.AlertDialog(c, &b.asking, locales.DeleteContactTitle(who), locales.DeleteContactMessage(b.deleting.Email),
		locales.Cancel(), locales.DeleteContactButton()) == 1 {
		a.deleteContact(b.deleting)
	}
}

// addContactForm is the form of a contact added by hand: the name, which
// is the user's and which mail does not change, and the address, which
// the engine checks. Enter adds, Escape closes it.
func (a *app) addContactForm(c *ui.Context, t *ui.Theme) {
	b := &a.book
	ui.Column(c).Gap(10).Padding(12).Radius(8).Border(1, t.Accent.Alpha(0.6)).Children(func() {
		var email ui.Element
		ui.Row(c).Gap(10).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				// The captions name the inputs, so a click on one focuses
				// its field.
				caption := ui.Text(c, locales.ContactName()).FontSize(11).Bold().TextColor(t.TextMuted)
				ui.TextInput(c, &b.name).Placeholder(locales.ContactNamePlaceholder()).LabelledBy(caption).
					FillWidth().AutoFocus().OnSubmit(a.addContact).OnShortcut(0, ui.KeyEscape, a.cancelAddContact)
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				caption := ui.Text(c, locales.ContactEmail()).FontSize(11).Bold().TextColor(t.TextMuted)
				email = ui.TextInput(c, &b.email).Placeholder("name@example.com").LabelledBy(caption).
					FillWidth().Invalid(b.addErr != "").OnSubmit(a.addContact).OnShortcut(0, ui.KeyEscape, a.cancelAddContact)
			})
		})
		if b.addErr != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, alertIcon).FontSize(14).TextColor(t.Warning)
				email.ErrorMessage(ui.Text(c, b.addErr).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus))
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if b.busy {
				ui.Spinner(c).Size(14, 14)
			}
			ui.Spacer(c)
			ui.Button(c, locales.Cancel()).Label(locales.ContactAddCancel()).OnClick(a.cancelAddContact)
			ui.PrimaryButton(c, locales.ContactAdd()).Disabled(strings.TrimSpace(b.email) == "").OnClick(a.addContact)
		})
	})
}

// contactList is the contacts read, a row each, reading the next page as
// the list nears their end; while there are none, it says why.
func (a *app) contactList(c *ui.Context, t *ui.Theme) {
	b := &a.book
	b.list.Key = func(i int) any {
		if i >= len(b.contacts) {
			return i
		}
		return mail.ContactKey(b.contacts[i].Email)
	}
	b.list.Label = func(i int) string {
		if i >= len(b.contacts) {
			return ""
		}
		ct := b.contacts[i]
		if ct.Name == "" {
			return ct.Email
		}
		return ct.Name + " " + ct.Email
	}
	ui.List(c, &b.list, len(b.contacts)).Grow(1).Label(locales.Contacts()).
		Rows(func(row ui.ListRow) { a.contactRow(row.Context, t, b.contacts[row.Index]) }).
		Children(func() {
			if len(b.contacts) > 0 || b.reading || b.readErr != "" {
				return
			}
			text := locales.ContactsEmpty()
			if search := strings.TrimSpace(b.search); search != "" {
				text = locales.ContactsNoMatch(search)
			}
			ui.Text(c, text).FontSize(13).TextColor(t.TextMuted).Padding(24, 12)
		})
	if _, last := b.list.Visible(); last >= len(b.contacts)-contactsAhead {
		a.readMoreContacts()
	}
}

// contactRow is a contact: its name and address, and the buttons that make
// it a favorite or not, rename it and delete it, each named for the
// contact, for assistive technology.
func (a *app) contactRow(c *ui.Context, t *ui.Theme, ct mail.Contact) {
	b := &a.book
	who := contactName(ct)
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(8, 8, 8, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		// The name says it already.
		ui.Avatar(c, who, nil).AccessHidden(true)
		if b.renaming == mail.ContactKey(ct.Email) {
			a.renameFields(c, t, ct)
			return
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, who).FontSize(13).Bold().SingleLine()
			if ct.Name != "" {
				ui.Text(c, ct.Email).FontSize(12).TextColor(t.TextMuted).SingleLine()
			}
		})
		fav, star, color := locales.FavoriteContact(who), starIcon, t.TextMuted
		if ct.Favorite {
			fav, star, color = locales.UnfavoriteContact(who), starFilledIcon, t.Warning
		}
		ui.Button(c, "").Label(fav).Tooltip(fav).Padding(6, 8).FocusBind(&b.focus, contactFocus("star", ct)).
			OnClick(func() { a.favoriteContact(ct, !ct.Favorite) }).
			Children(func() { ui.Icon(c, star).FontSize(16).TextColor(color) })
		rename := locales.RenameContact(who)
		ui.Button(c, "").Label(rename).Tooltip(rename).Padding(6, 8).FocusBind(&b.focus, contactFocus("rename", ct)).
			OnClick(func() { a.startRename(ct) }).
			Children(func() { ui.Icon(c, pencilIcon).FontSize(16) })
		del := locales.DeleteContact(who)
		ui.Button(c, "").Label(del).Tooltip(del).Padding(6, 8).FocusBind(&b.focus, contactFocus("delete", ct)).
			OnClick(func() { a.askDelete(ct) }).
			Children(func() { ui.Icon(c, trashIcon).FontSize(16) })
	})
}

// renameFields is the name of a contact being edited, in its row: Enter
// saves it, Escape leaves it as it was.
func (a *app) renameFields(c *ui.Context, t *ui.Theme, ct mail.Contact) {
	b := &a.book
	who := contactName(ct)
	ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &b.newName).Grow(1).Label(locales.ContactNewName(ct.Email)).
				Placeholder(locales.ContactNameFromMail()).AutoFocus().
				OnSubmit(func() { a.saveRename(ct) }).
				OnShortcut(0, ui.KeyEscape, func() { a.cancelRename(ct) })
			ui.Button(c, locales.Cancel()).Label(locales.ContactRenameCancel(who)).OnClick(func() { a.cancelRename(ct) })
			ui.PrimaryButton(c, locales.ContactRenameSave()).OnClick(func() { a.saveRename(ct) })
		})
		ui.Text(c, ct.Email).FontSize(12).TextColor(t.TextMuted).SingleLine()
	})
}
