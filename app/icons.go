package app

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

// The icons are Lucide's (https://lucide.dev, ISC) from lucide-static
// v1.72.0, kept as the SVG files they ship and parsed once at startup.
//
//go:embed icons/*.svg
var iconFS embed.FS

// mustIcon parses icons/<name>.svg.
func mustIcon(name string) *ui.SVG {
	b, err := iconFS.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic("shu: icon " + name + ": " + err.Error())
	}
	return ui.MustParseSVG(b)
}

var (
	inboxIcon          = mustIcon("inbox")
	mailsIcon          = mustIcon("mails")
	starIcon           = mustIcon("star")
	starFilledIcon     = mustIcon("star-filled")
	sentIcon           = mustIcon("send")
	draftIcon          = mustIcon("file-pen")
	archiveIcon        = mustIcon("archive")
	trashIcon          = mustIcon("trash-2")
	spamIcon           = mustIcon("shield-alert")
	tagIcon            = mustIcon("tag")
	composeIcon        = mustIcon("square-pen")
	filterIcon         = mustIcon("filter")
	searchIcon         = mustIcon("search")
	replyIcon          = mustIcon("reply")
	replyAllIcon       = mustIcon("reply-all")
	forwardIcon        = mustIcon("forward")
	moreIcon           = mustIcon("ellipsis")
	paperclipIcon      = mustIcon("paperclip")
	refreshIcon        = mustIcon("refresh-cw")
	chevronDownIcon    = mustIcon("chevron-down")
	chevronLeftIcon    = mustIcon("chevron-left")
	panelLeftIcon      = mustIcon("panel-left")
	downloadIcon       = mustIcon("download")
	settingsIcon       = mustIcon("settings")
	plusIcon           = mustIcon("plus")
	closeIcon          = mustIcon("x")
	checkIcon          = mustIcon("check")
	alertIcon          = mustIcon("circle-alert")
	mailIcon           = mustIcon("mail")
	mailOpenIcon       = mustIcon("mail-open")
	sparklesIcon       = mustIcon("sparkles")
	copyIcon           = mustIcon("copy")
	offlineIcon        = mustIcon("wifi-off")
	conversationsIcon  = mustIcon("messages-square")
	pencilIcon         = mustIcon("pencil")
	cloudOffIcon       = mustIcon("cloud-off")
	listFilterIcon     = mustIcon("list-filter")
	gripVerticalIcon   = mustIcon("grip-vertical")
	arrowUpIcon        = mustIcon("arrow-up")
	arrowDownIcon      = mustIcon("arrow-down")
	clockIcon          = mustIcon("clock")
	shieldQuestionIcon = mustIcon("shield-question")
	shieldCheckIcon    = mustIcon("shield-check")
	shieldIcon         = mustIcon("shield")
	folderInputIcon    = mustIcon("folder-input")
	eyeIcon            = mustIcon("eye")
	globeIcon          = mustIcon("globe")
)
