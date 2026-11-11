package main

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

// The icons are Lucide's (https://lucide.dev, ISC), kept as the SVG files
// they ship and parsed once at startup.
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
	inboxIcon       = mustIcon("inbox")
	starIcon        = mustIcon("star")
	sentIcon        = mustIcon("send")
	draftIcon       = mustIcon("file-pen")
	archiveIcon     = mustIcon("archive")
	trashIcon       = mustIcon("trash-2")
	spamIcon        = mustIcon("shield-alert")
	tagIcon         = mustIcon("tag")
	composeIcon     = mustIcon("square-pen")
	filterIcon      = mustIcon("filter")
	replyIcon       = mustIcon("reply")
	replyAllIcon    = mustIcon("reply-all")
	forwardIcon     = mustIcon("forward")
	moreIcon        = mustIcon("ellipsis")
	paperclipIcon   = mustIcon("paperclip")
	refreshIcon     = mustIcon("refresh-cw")
	flagIcon        = mustIcon("flag")
	chevronDownIcon = mustIcon("chevron-down")
	downloadIcon    = mustIcon("download")
)
