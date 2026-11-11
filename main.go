package main

import (
	"fmt"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// message is one message shown in the list and the reading pane.
type message struct {
	id         int
	sender     string
	email      string
	subject    string
	preview    string
	time       string
	unread     bool
	starred    bool
	attachment bool
	attachName string
	attachInfo string
	body       []string
}

// app is the state the window shows.
type app struct {
	mailbox  string // the mailbox chosen in the sidebar
	query    string // the search field's text
	selected int    // the message chosen in the list, -1 for none
	messages []message
	list     ui.ListState
}

func newApp() *app {
	return &app{
		mailbox:  "inbox",
		selected: 0,
		messages: demoMessages(),
	}
}

func (a *app) view(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	// The bar keeps clear of the traffic lights, and is tall enough to
	// hold them and the controls beside them.
	top := max(bar.Bottom, 52)

	ui.Column(c).Fill().Background(t.Background).Children(func() {
		a.titleBar(c, t, bar, top)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c, t)
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			a.messageList(c, t)
			ui.Box(c).Width(1).Shrink(0).Background(t.Border)
			a.readingPane(c, t)
		})
	})
}

// titleBar draws the window's toolbar: the app, the search field, sync and
// compose.
func (a *app) titleBar(c *ui.Context, t *ui.Theme, bar ui.TitleBar, height float32) {
	ui.Row(c).Height(height).Shrink(0).AlignItems(ui.Center).Gap(10).
		Padding(0, max(bar.Right, 0)+14, 0, max(bar.Left, 0)+14).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {
		ui.Box(c).Size(26, 26).Radius(7).Center().
			Gradient(ui.Hex("#5b9cff"), ui.Hex("#2563eb"), 135).
			Children(func() {
				ui.Text(c, "S").FontSize(14).Bold().TextColor(ui.RGB(255, 255, 255))
			})
		ui.Text(c, "Shu").FontSize(15).Bold()
		ui.Spacer(c)
		ui.SearchField(c, &a.query).Width(300).Placeholder("Search mail").Label("Search mail")
		a.iconButton(c, refreshIcon, "Sync", "Sync mail")
		ui.PrimaryButton(c, "").Children(func() {
			ui.Icon(c, composeIcon).FontSize(14)
			ui.Text(c, "Compose")
		}).Label("Compose").Tooltip("New message")
	})
}

// sidebar draws the account and the mailboxes.
func (a *app) sidebar(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(222).Shrink(0).Background(t.Surface).Children(func() {
		// The account, as Mail shows it above its mailboxes.
		ui.Row(c).Padding(14, 14, 6, 14).Gap(9).AlignItems(ui.Center).Children(func() {
			ui.Avatar(c, "Shu Mail", nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, "Shu Mail").FontSize(12).Bold().SingleLine()
				ui.Text(c, "isaced@shu.email").FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			ui.Icon(c, chevronDownIcon).FontSize(12).TextColor(t.TextMuted)
		})
		ui.Sidebar(c, &a.mailbox, func() {
			ui.SidebarSection(c, "Mailboxes", nil, func() {
				ui.SidebarItem(c, "inbox", inboxIcon, "Inbox").Children(func() {
					ui.Badge(c, "3").Background(t.Accent).TextColor(t.AccentText)
				})
				ui.SidebarItem(c, "starred", starIcon, "Starred")
				ui.SidebarItem(c, "sent", sentIcon, "Sent")
				ui.SidebarItem(c, "drafts", draftIcon, "Drafts").Children(func() {
					ui.Badge(c, "2")
				})
				ui.SidebarItem(c, "archive", archiveIcon, "Archive")
				ui.SidebarItem(c, "junk", spamIcon, "Junk")
				ui.SidebarItem(c, "trash", trashIcon, "Trash")
			})
			ui.SidebarSection(c, "Labels", nil, func() {
				ui.SidebarItem(c, "work", tagIcon, "Work")
				ui.SidebarItem(c, "personal", tagIcon, "Personal")
				ui.SidebarItem(c, "receipts", tagIcon, "Receipts")
				ui.SidebarItem(c, "travel", tagIcon, "Travel")
			})
		}).Grow(1).Label("Mailboxes")
	})
}

// messageList draws the header, the search field and the messages.
func (a *app) messageList(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Width(384).Shrink(0).MinWidth(0).Background(t.Background).Children(func() {
		ui.Row(c).Height(48).Shrink(0).Padding(0, 14).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Inbox").FontSize(17).Bold()
			ui.Badge(c, fmt.Sprint(a.unread())).Background(t.Accent).TextColor(t.AccentText)
			ui.Spacer(c)
			a.iconButton(c, filterIcon, "Filter", "Filter messages")
			a.iconButton(c, moreIcon, "More", "More actions")
		})
		ui.Row(c).Shrink(0).Gap(8).AlignItems(ui.Center).Padding(10, 14, 8, 14).Children(func() {
			ui.SearchField(c, &a.query).Grow(1).Placeholder("Search Inbox").Label("Search Inbox")
		})
		a.list.Key = func(i int) any { return a.messages[i].id }
		a.list.Label = func(i int) string { return a.messages[i].sender + " " + a.messages[i].subject }
		a.list.Selected = &a.selected
		ui.List(c, &a.list, len(a.messages)).Grow(1).Rows(a.messageRow)
	})
}

// unread counts the messages not read.
func (a *app) unread() int {
	n := 0
	for _, m := range a.messages {
		if m.unread {
			n++
		}
	}
	return n
}

// messageRow builds one row of the message list.
func (a *app) messageRow(row ui.ListRow) {
	c := row.Context
	t := c.Theme()
	m := a.messages[row.Index]
	chosen := row.Selected()

	muted := t.TextMuted
	nameColor := t.Text
	dotColor := t.Accent
	starColor := t.Warning
	if chosen {
		muted = t.AccentText.Alpha(0.78)
		nameColor = t.AccentText
		dotColor = t.AccentText
		starColor = t.AccentText
	}

	ui.Box(c).Padding(10, 14).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Gap(9).AlignItems(ui.Start).Children(func() {
			dot := ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Margin(6, 0, 0, 0)
			if m.unread {
				dot.Background(dotColor)
			}
			ui.Avatar(c, m.sender, nil)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					name := ui.Text(c, m.sender).FontSize(13).SingleLine().Grow(1)
					if m.unread {
						name.Bold()
					}
					name.TextColor(nameColor)
					if m.attachment {
						ui.Icon(c, paperclipIcon).FontSize(11).TextColor(muted)
					}
					if m.starred {
						ui.Icon(c, starIcon).FontSize(11).TextColor(starColor)
					}
					ui.Text(c, m.time).FontSize(11).TextColor(muted).Shrink(0)
				})
				subject := ui.Text(c, m.subject).FontSize(13).SingleLine()
				if m.unread {
					subject.Bold()
				}
				ui.Text(c, m.preview).FontSize(12).SingleLine().TextColor(muted)
			})
		})
	})
}

// readingPane draws the toolbar and the message chosen.
func (a *app) readingPane(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Grow(1).MinWidth(0).Background(t.Background).Children(func() {
		ui.Toolbar(c, func() {
			a.toolButton(c, archiveIcon, "Archive")
			a.toolButton(c, trashIcon, "Delete")
			a.toolButton(c, spamIcon, "Junk")
			a.toolButton(c, flagIcon, "Flag")
			toolDivider(c, t)
			a.toolButton(c, replyIcon, "Reply")
			a.toolButton(c, replyAllIcon, "Reply All")
			a.toolButton(c, forwardIcon, "Forward")
			ui.Spacer(c)
			a.toolButton(c, moreIcon, "More")
		}).Height(48).Padding(6, 12).Label("Message actions").
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border)

		if a.selected < 0 || a.selected >= len(a.messages) {
			ui.Column(c).Fill().Center().Gap(6).Children(func() {
				ui.Icon(c, inboxIcon).FontSize(36).TextColor(t.TextMuted)
				ui.Text(c, "No message selected").FontSize(15).TextColor(t.TextMuted)
			})
			return
		}
		m := a.messages[a.selected]
		ui.Scroll(c.Key(m.id)).Grow(1).Children(func() {
			ui.Column(c).Padding(26, 30).Gap(18).MinWidth(0).Children(func() {
				ui.Text(c, m.subject).FontSize(21).Bold().Balance()
				a.senderHeader(c, t, m)
				if m.attachment {
					a.attachment(c, t, m)
				}
				ui.Divider(c).Margin(0, 0, 0, 0)
				ui.Column(c).Gap(13).MaxWidth(660).Children(func() {
					for _, p := range m.body {
						ui.Text(c, p).FontSize(14).LineHeight(1.6).TextColor(t.Text).Selectable()
					}
				})
				ui.Column(c).Gap(2).Margin(6, 0, 0, 0).Children(func() {
					ui.Text(c, "—").FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, "Maya Chen").FontSize(13).Bold().TextColor(t.TextMuted)
					ui.Text(c, "Product, Shu").FontSize(12).TextColor(t.TextMuted)
				})
			})
		})
	})
}

// senderHeader draws who sent the message, to whom and when.
func (a *app) senderHeader(c *ui.Context, t *ui.Theme, m message) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Avatar(c, m.sender, nil)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, m.sender).FontSize(14).Bold().SingleLine()
			ui.Text(c, m.email).FontSize(12).TextColor(t.TextMuted).SingleLine()
		})
		ui.Column(c).Shrink(0).AlignItems(ui.End).Gap(1).Children(func() {
			ui.Text(c, m.time).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, "to me").FontSize(11).TextColor(t.TextMuted)
		})
	})
}

// attachment draws the card of the message's attachment.
func (a *app) attachment(c *ui.Context, t *ui.Theme, m message) {
	ui.Row(c).MaxWidth(380).Padding(10, 12).Gap(10).AlignItems(ui.Center).Radius(8).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Box(c).Size(32, 32).Radius(7).Center().Background(t.Background).
			Border(1, t.Border).Children(func() {
			ui.Icon(c, paperclipIcon).FontSize(15).TextColor(t.TextMuted)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, m.attachName).FontSize(12).Bold().SingleLine()
			ui.Text(c, m.attachInfo).FontSize(11).TextColor(t.TextMuted)
		})
		ui.Icon(c, downloadIcon).FontSize(15).TextColor(t.TextMuted)
	})
}

// iconButton is a button showing only an icon, named for assistive
// technology and explained by a tooltip.
func (a *app) iconButton(c *ui.Context, svg *ui.SVG, label, tip string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(tip).Padding(6, 8).
		Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolButton is a flat toolbar button showing only an icon.
func (a *app) toolButton(c *ui.Context, svg *ui.SVG, label string) ui.Element {
	return ui.Button(c, "").Label(label).Tooltip(label).
		Children(func() { ui.Icon(c, svg).FontSize(16) })
}

// toolDivider separates groups of toolbar buttons.
func toolDivider(c *ui.Context, t *ui.Theme) {
	ui.Box(c).Width(1).Height(18).Shrink(0).Margin(0, 4).Background(t.Border)
}

// demoMessages is the sample inbox the window shows.
func demoMessages() []message {
	return []message{
		{
			id: 1, sender: "Maya Chen", email: "maya@shu.email", subject: "Design review — Thursday at 10",
			preview: "I pulled the latest frames into Figma and left a few notes on the reading pane.",
			time:    "10:24", unread: true, starred: true, attachment: true,
			attachName: "Reading-pane-notes.pdf", attachInfo: "PDF · 248 KB",
			body: []string{
				"Hi Alex,",
				"I pulled the latest frames into Figma and left a few notes on the reading pane. The three-column layout is feeling much calmer now, and the toolbar finally has room to breathe.",
				"Could we walk through the message list states before Thursday? I want to make sure unread, selected and hover read clearly at a glance, especially at the smaller window size.",
				"Thanks,\nMaya",
			},
		},
		{
			id: 2, sender: "Lena Ortiz", email: "lena@northwind.co", subject: "Re: Contract for Q4 campaign",
			preview: "Attached is the signed copy. Legal had one small question about the renewal clause.",
			time:    "9:58", unread: true,
			body: []string{
				"Hi Alex,",
				"Attached is the signed copy. Legal had one small question about the renewal clause — they read it as a twelve-month term rather than a six-month one. I think the intent was six, but wanted to confirm before we file it.",
				"Happy to jump on a quick call if that is easier.",
				"Best,\nLena",
			},
		},
		{
			id: 3, sender: "GitHub", email: "notifications@github.com", subject: "[shu-email/shu] Pull request #128: Native title bar",
			preview: "isaced requested your review on: Add the custom title bar and window dragging.",
			time:    "9:12", unread: true,
			body: []string{
				"isaced requested your review on shu-email/shu#128.",
				"Add the custom title bar, search field and compose button, and make the empty parts of the bar drag the window.",
				"View it on GitHub: https://github.com/shu-email/shu/pull/128",
			},
		},
		{
			id: 4, sender: "Daniel Park", email: "daniel@shu.email", subject: "Sync engine notes from today",
			preview: "Short version: incremental IMAP fetch is working, and the local index catches up in under a second.",
			time:    "Yesterday", starred: true,
			body: []string{
				"Hey Alex,",
				"Short version: incremental IMAP fetch is working, and the local index catches up in under a second on my test account. I still need to handle the case where a folder is renamed while a fetch is in flight.",
				"I left the details in the doc, including the two approaches I tried. The second one is simpler and seems good enough for v1.",
				"Daniel",
			},
		},
		{
			id: 5, sender: "Figma", email: "team@figma.com", subject: "New comment on Shu — Desktop UI",
			preview: "Maya Chen commented: Should the selected row use the accent color or a softer tint?",
			time:    "Yesterday",
			body: []string{
				"Maya Chen commented on Shu — Desktop UI:",
				"“Should the selected row use the accent color or a softer tint? The accent reads well in the sidebar, but in a list of messages it might be a little loud.”",
				"Reply in Figma to keep the thread together.",
			},
		},
		{
			id: 6, sender: "Sarah Kim", email: "sarah@fieldnotes.studio", subject: "Invoice for September",
			preview: "Here is the invoice for last month, along with the updated statement of work.",
			time:    "Mon",
			body: []string{
				"Hi Alex,",
				"Here is the invoice for last month, along with the updated statement of work for October. The total is unchanged from our estimate.",
				"Let me know if you need anything else from my side.",
				"Sarah",
			},
		},
		{
			id: 7, sender: "Stripe", email: "receipts@stripe.com", subject: "Your receipt from Shu, Inc.",
			preview: "Payment of $24.00 to Shu, Inc. was successful. Thanks for your business.",
			time:    "Mon",
			body: []string{
				"Receipt from Shu, Inc.",
				"Payment of $24.00 was successful. Your next billing date is October 28.",
				"You can view and download this receipt from your billing settings at any time.",
			},
		},
		{
			id: 8, sender: "Product Weekly", email: "hello@productweekly.com", subject: "Issue #214 — The quiet return of desktop apps",
			preview: "This week: native toolkits are back, and what it means for the next generation of mail clients.",
			time:    "Sep 2", attachment: true,
			attachName: "Issue-214.pdf", attachInfo: "PDF · 1.2 MB",
			body: []string{
				"This week: native toolkits are back, and what it means for the next generation of mail clients.",
				"We look at teams choosing small binaries and GPU-rendered interfaces over web wrappers, and at the trade-offs that come with owning more of the stack.",
				"Also inside: a field guide to local-first sync, and three interface details worth stealing.",
			},
		},
		{
			id: 9, sender: "Notion", email: "notify@notion.so", subject: "Your weekly digest",
			preview: "You have 4 pages updated this week in the Shu workspace.",
			time:    "Sep 1",
			body: []string{
				"You have 4 pages updated this week in the Shu workspace.",
				"Roadmap, Meeting notes, Design principles and Launch checklist were all edited by your teammates.",
			},
		},
		{
			id: 10, sender: "Tom Alvarez", email: "tom@alvarez.dev", subject: "Re: Coffee next week?",
			preview: "Tuesday works for me. There is a new place on Valencia that actually has good light.",
			time:    "Aug 30",
			body: []string{
				"Tuesday works for me. There is a new place on Valencia that actually has good light.",
				"Say 9:30 and I will grab a table by the window.",
				"Tom",
			},
		},
	}
}

func main() {
	a := newApp()
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "Shu",
			Width:           1220,
			Height:          800,
			MinWidth:        1000,
			MinHeight:       640,
			StateKey:        "main",
			TitleBarStyle:   mygo.TitleBarHiddenInset,
			BackgroundColor: "light-dark(#ffffff, #18181b)",
			Content:         ui.View(a.view),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
