package app

import (
	"github.com/egoist/mygo/ui"

	"shu/mail"
)

// Sender icons: the picture the reading pane shows in place of the
// initials of a sender's name (mail/avatars.go). The window asks the
// engine once per domain, from the first frame that shows it, keeps the
// decoded bitmaps it has, and draws no icon for one it is waiting for or
// one that had none: the initials show meanwhile.

// maxSenderIcons is how many icons the window keeps decoded. A mailbox
// with more senders than this shows the initials of the ones it forgets,
// and their files stay on disk, so they come back without a request.
const maxSenderIcons = 256

// senderAvatar is the icon of the sender's domain, or nil to draw the
// initials of its name. It asks for one the first time a domain is shown,
// never once per frame: the pending map makes the ask one.
func (a *app) senderAvatar(addr mail.Address) *ui.Bitmap {
	if !a.mailSet.SenderIcons || a.svc == nil {
		return nil
	}
	domain, ok := mail.LogoDomain(addr)
	if !ok {
		return nil
	}
	if icon, ok := a.icons[domain]; ok {
		return icon
	}
	if a.iconPending[domain] || a.iconGone[domain] {
		return nil
	}
	a.iconPending[domain] = true
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		data, err := a.svc.SenderIcon(ctx, addr)
		a.post(func() {
			delete(a.iconPending, domain)
			if a.stale(g) {
				return
			}
			var icon *ui.Bitmap
			if err == nil && len(data) > 0 {
				icon, err = ui.DecodeBitmap(data)
			}
			if err != nil || icon == nil {
				// This run asks no more: a domain that failed is asked
				// again as the app starts next, and one that had no icon
				// after the store's wait.
				a.iconGone[domain] = true
				return
			}
			a.keepIcon(domain, icon)
		})
	})
	return nil
}

// keepIcon remembers a domain's icon, making room by letting the pictures
// go: a released bitmap decodes itself again when it is shown.
func (a *app) keepIcon(domain string, icon *ui.Bitmap) {
	if len(a.icons) >= maxSenderIcons {
		a.forgetIcons()
	}
	a.icons[domain] = icon
}

// forgetIcons drops every icon the window holds.
func (a *app) forgetIcons() {
	for domain, icon := range a.icons {
		icon.ReleasePixels()
		delete(a.icons, domain)
	}
}
