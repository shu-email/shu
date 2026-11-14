package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// Remote images and attachment previews. Remote pictures stay blocked
// until the user allows them for one message or for its sender; the
// per-sender choice is kept by the engine. A preview reads the attachment
// once, and shows it without saving it anywhere.

// attachmentPreview is the open preview.
type attachmentPreview struct {
	open  bool
	name  string
	kind  string // "image", "text", "pdf" or "other"
	text  string
	image ui.ImageSource
	// path is the file Open in the default app opens, written when the
	// preview opens, and data the bytes for it and Save as. A PDF is also
	// shown from path.
	path string
	data []byte
}

// loadImagePolicy reads the senders whose images the user allowed.
func (a *app) loadImagePolicy() {
	g := a.gen
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		policy, err := a.svc.RemoteImages(ctx)
		a.post(func() {
			if a.stale(g) || err != nil {
				return
			}
			if policy == nil {
				policy = map[string]bool{}
			}
			a.images = policy
			a.imagesLoaded = true
		})
	})
}

// imageAllowed reports whether the message's remote images may load.
func (a *app) imageAllowed(m mail.Summary) bool {
	if a.images[mail.RemoteImageKey(m.From)] {
		return true
	}
	return a.imagesShown[summaryKey(m)]
}

// imageNotice says that a message's remote images are blocked, and offers
// to load them once or for the sender; once they show, it offers to block
// them again.
func (a *app) imageNotice(c *ui.Context, t *ui.Theme, m mail.Summary, msg *mail.Message) {
	if msg == nil || strings.TrimSpace(msg.BodyHTML) == "" || len(msg.RemoteImages()) == 0 {
		return
	}
	if a.imageAllowed(m) {
		ui.Row(c).Wrap().Gap(8).AlignItems(ui.Center).Padding(8, 12).Radius(8).
			Background(t.Surface).Children(func() {
			ui.Icon(c, eyeIcon).FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, locales.ImagesShownDetail()).FontSize(11).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			ui.ButtonBase(c).Label(locales.BlockImages()).OnClick(func() { a.setSenderImages(m, false) }).
				Children(func() { ui.Text(c, locales.BlockImages()).FontSize(11).TextColor(t.Accent) })
		})
		return
	}
	ui.Column(c).Gap(6).Padding(10, 12).Radius(8).
		Background(t.Warning.Alpha(0.10)).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, cloudOffIcon).FontSize(14).TextColor(t.Warning)
			ui.Text(c, locales.ImagesBlocked()).FontSize(12).Bold().Grow(1).MinWidth(0)
		})
		ui.Text(c, locales.ImagesBlockedDetail()).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Wrap().Gap(8).Children(func() {
			ui.Button(c, locales.LoadImages()).OnClick(func() { a.showImages(m) })
			ui.Button(c, locales.LoadImagesAlways()).OnClick(func() { a.setSenderImages(m, true) })
		})
	})
}

// showImages loads the pictures of one message, this time only.
func (a *app) showImages(m mail.Summary) {
	a.imagesShown[summaryKey(m)] = true
	a.refreshImages(m)
}

// setSenderImages allows or blocks a sender's pictures for good.
func (a *app) setSenderImages(m mail.Summary, on bool) {
	g := a.gen
	key, email := summaryKey(m), mail.RemoteImageKey(m.From)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		err := a.svc.SetRemoteImages(ctx, m.From, on)
		a.post(func() {
			if a.stale(g) {
				return
			}
			if err != nil {
				a.err = locales.ImagesFailed(failureText(err))
				return
			}
			if on {
				a.images[email] = true
				a.imagesShown[key] = true
			} else {
				delete(a.images, email)
				delete(a.imagesShown, key)
			}
			a.refreshImages(m)
		})
	})
}

// refreshImages shows the message's HTML with its pictures, in the
// message view or in the conversation's web view.
func (a *app) refreshImages(m mail.Summary) {
	if a.threaded() {
		if cv := a.conv; cv != nil {
			cv.html = summaryKey(m)
		}
	} else {
		a.htmlOn = true
	}
	a.htmlKey = ""
	a.ensureWebView()
	a.syncHTMLView()
}

// previewAttachment reads one attachment and opens the preview it fits.
func (a *app) previewAttachment(m mail.Summary, att mail.Attachment) {
	if m.DraftID != "" {
		return
	}
	g := a.gen
	account, folder, uid, part := m.Account, m.Folder, m.UID, att.Part
	name := att.Filename
	if name == "" {
		name = locales.Attachment()
	}
	kind := previewKind(att)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		_, data, err := a.svc.Attachment(ctx, account, folder, uid, part)
		// Open in the default app opens a file: it is written here, once,
		// beside the bytes, not as the dialog draws. An empty attachment
		// opens nothing, and writes no file.
		var path string
		var pathErr error
		if err == nil && len(data) > 0 {
			path, pathErr = a.svc.TempFile(previewExt(kind, name), data)
		}
		a.post(func() {
			if a.stale(g) {
				// The preview is no longer the one the user wants: its
				// file goes with it.
				if path != "" {
					_ = os.Remove(path)
				}
				return
			}
			if err != nil {
				a.err = locales.PreviewFailed(failureText(err))
				return
			}
			if pathErr != nil {
				a.err = locales.OpenAttachmentFailed(failureText(pathErr))
			}
			p := attachmentPreview{open: true, name: name, data: data, kind: "other", path: path}
			switch kind {
			case "image":
				if src, err := ui.DecodeImage(data); err == nil {
					p.kind, p.image = "image", src
				}
			case "text":
				p.kind, p.text = "text", truncateText(string(data))
			case "pdf":
				p.kind = "pdf"
			}
			if p.kind == "pdf" {
				a.showPreviewPDF(p.path)
			}
			if old := a.preview.path; old != "" && old != p.path {
				_ = os.Remove(old)
			}
			a.preview = p
		})
	})
}

// showPreviewPDF loads a PDF into the preview's own web view.
func (a *app) showPreviewPDF(path string) {
	if path == "" || a.win == nil {
		return
	}
	if a.previewView == nil {
		v, err := a.win.NewWebView(mygo.WebViewOptions{
			Page:            mygo.PageOptions{Untrusted: true},
			BackgroundColor: "light-dark(#ffffff, #18181b)",
		})
		if err != nil {
			return
		}
		a.previewView = v
	}
	_ = a.previewView.Page().LoadFile(path)
}

// closePreview closes the preview and removes its temporary file.
func (a *app) closePreview() {
	if p := a.preview.path; p != "" {
		_ = os.Remove(p)
	}
	a.preview = attachmentPreview{}
}

// attachmentPreviewDialog draws the open preview.
func (a *app) attachmentPreviewDialog(c *ui.Context) {
	p := &a.preview
	if !p.open {
		return
	}
	t := c.Theme()
	modal := ui.Modal(c, &p.open, func() {
		ui.Column(c).Width(760).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, p.name).FontSize(15).Bold().Grow(1).MinWidth(0).SingleLine()
				a.iconButton(c, closeIcon, locales.Cancel(), locales.Cancel()).OnClick(a.closePreview)
			})
			switch p.kind {
			case "image":
				ui.Scroll(c).Height(520).Children(func() {
					ui.Column(c).Center().Children(func() { ui.Image(c, p.image).MaxWidth(720) })
				})
			case "text":
				ui.Scroll(c).Height(520).Children(func() {
					ui.Text(c, p.text).FontSize(12).Selectable().LineHeight(1.5)
				})
			case "pdf":
				// A PDF the file could not be written for shows none of
				// the one opened before it.
				if a.previewView != nil && p.path != "" {
					ui.WebView(c, a.previewView).Height(520)
				} else {
					ui.Text(c, locales.PreviewUnavailable()).FontSize(12).TextColor(t.TextMuted)
				}
			default:
				ui.Text(c, locales.PreviewUnavailable()).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if len(p.data) > 0 {
					ui.Button(c, locales.OpenInDefaultApp()).Disabled(p.path == "").OnClick(func() {
						if p.path != "" {
							_ = mygo.Shell.OpenExternal("file://" + p.path)
						}
					})
				}
				ui.Spacer(c)
				ui.Button(c, locales.SaveAs()).OnClick(func() { a.savePreview(p) })
				ui.PrimaryButton(c, locales.Done()).OnClick(a.closePreview)
			})
		})
	})
	if modal.Dismissed() {
		// Escape or a click outside closed it: its file goes as the
		// dialog's does.
		a.closePreview()
	}
}

// savePreview writes the previewed bytes where the user chooses.
func (a *app) savePreview(p *attachmentPreview) {
	if a.saveFile == nil || len(p.data) == 0 {
		return
	}
	path, err := a.saveFile(a.win, safeFileName(p.name))
	if err != nil || path == "" {
		return
	}
	if err := os.WriteFile(path, p.data, 0o600); err != nil {
		a.err = locales.SaveFileFailed(err.Error())
	}
}

// saveAllAttachments fetches every attachment and writes it to a chosen
// folder, keeping names apart when two are the same.
func (a *app) saveAllAttachments(m mail.Summary, list []mail.Attachment) {
	if a.pickFolder == nil || m.DraftID != "" || len(list) == 0 {
		return
	}
	g := a.gen
	account, folder, uid := m.Account, m.Folder, m.UID
	items := append([]mail.Attachment(nil), list...)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		dir, err := a.pickFolder(a.win)
		if err != nil {
			return
		}
		if dir == "" {
			a.post(func() {
				if !a.stale(g) {
					a.err = locales.SaveAllNoFolder()
				}
			})
			return
		}
		saved, failed := 0, 0
		used := map[string]bool{}
		for _, att := range items {
			_, data, err := a.svc.Attachment(ctx, account, folder, uid, att.Part)
			if err != nil {
				failed++
				continue
			}
			name := uniqueFileName(safeFileName(att.Filename), used)
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				failed++
				continue
			}
			saved++
		}
		a.post(func() {
			if a.stale(g) {
				return
			}
			if failed > 0 {
				a.err = locales.SaveAllFailed(saved, failed)
			} else {
				a.folderNote = locales.SaveAllDone(saved)
			}
		})
	})
}

// uniqueFileName keeps two attachments of the same name apart.
func uniqueFileName(name string, used map[string]bool) string {
	if name == "" {
		name = "attachment"
	}
	if !used[name] {
		used[name] = true
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func isTextName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".md", ".csv", ".log", ".json", ".xml", ".yaml", ".yml", ".ini":
		return true
	}
	return false
}

func truncateText(s string) string {
	const max = 512 << 10
	if len(s) > max {
		cut := max
		for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		return s[:cut]
	}
	return s
}

// previewKind is the preview an attachment gets: "image", "text", "pdf"
// or, for anything else, "other".
func previewKind(att mail.Attachment) string {
	switch {
	case strings.HasPrefix(att.ContentType, "image/"):
		return "image"
	case strings.HasPrefix(att.ContentType, "text/"), isTextName(att.Filename):
		return "text"
	case att.ContentType == "application/pdf", strings.HasSuffix(strings.ToLower(att.Filename), ".pdf"):
		return "pdf"
	}
	return "other"
}

// previewExt is the extension of the file Open in the default app opens
// for a preview of this kind.
func previewExt(kind, name string) string {
	switch kind {
	case "pdf":
		return ".pdf"
	case "text":
		return ".txt"
	}
	return filepath.Ext(name)
}

// previewable reports whether an attachment has a preview in the app.
func previewable(att mail.Attachment) bool {
	return previewKind(att) != "other"
}
