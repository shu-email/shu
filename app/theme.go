package app

import (
	"github.com/egoist/mygo/ui"
)

// The window's look. Mail is something to read, so the surface is paper
// and ink rather than a form: warm neutrals, one deep teal for what is
// chosen and what asks for an answer, and room between things instead of
// lines around them. MyGo's widgets take their colors and metrics from
// the theme, so the app changes it once per frame and every control
// follows, in the system's light and dark appearance alike.

// The palette. It is small on purpose: one accent, warm neutrals, and the
// three colors that say something is wrong, late or done.
var (
	// The page is a shade the cards sit on, and the cards are the brighter
	// of the two: a card is a thing to pick up, and it has to leave the
	// page to say so. The reading pane is a card too, so a message reads
	// on the same surface the list does.
	paper     = ui.Hex("#f6f3ee")
	paperSoft = ui.Hex("#ffffff")
	ink       = ui.Hex("#1b1a18")
	inkSoft   = ui.Hex("#78736b")
	teal      = ui.Hex("#0f6e63")
	night     = ui.Hex("#101115")
	nightSoft = ui.Hex("#1b1c21")
	moon      = ui.Hex("#ecebe7")
	moonSoft  = ui.Hex("#95948d")
	tealNight = ui.Hex("#16a394")
)

// appTheme is the theme of a window in one appearance: MyGo's own, with
// the app's colors, roomier corners and a slightly larger type.
func appTheme(dark bool) *ui.Theme {
	t := ui.LightTheme()
	if dark {
		t = ui.DarkTheme()
	}
	t.Radius = 10
	t.Spacing = 4
	if dark {
		t.Background = night
		t.Surface = nightSoft
		t.SurfaceHover = ui.Hex("#23242a")
		t.SurfacePressed = ui.Hex("#2b2c33")
		t.Border = ui.Hex("#2e2f36")
		t.Text = moon
		t.TextMuted = moonSoft
		t.Accent = tealNight
		t.AccentHover = ui.Hex("#1db3a3")
		t.AccentPressed = teal
		t.AccentText = ui.Hex("#08110f")
		t.Danger = ui.Hex("#f97066")
		t.Warning = ui.Hex("#f0a04b")
		t.Success = ui.Hex("#4ade80")
		t.Selection = ui.RGBA(22, 163, 148, 0.30)
		t.Focus = ui.RGBA(22, 163, 148, 0.60)
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.22)
		return t
	}
	t.Background = paper
	t.Surface = paperSoft
	t.SurfaceHover = ui.Hex("#f4f1ea")
	t.SurfacePressed = ui.Hex("#eae6de")
	t.Border = ui.Hex("#e2ddd3")
	t.Text = ink
	t.TextMuted = inkSoft
	t.Accent = teal
	t.AccentHover = ui.Hex("#0c5a51")
	t.AccentPressed = ui.Hex("#0a4a43")
	t.AccentText = ui.Hex("#ffffff")
	t.Danger = ui.Hex("#b42318")
	t.Warning = ui.Hex("#b45309")
	t.Success = ui.Hex("#15803d")
	t.Selection = ui.RGBA(15, 110, 99, 0.22)
	t.Focus = ui.RGBA(15, 110, 99, 0.50)
	t.Scrollbar = ui.RGBA(0, 0, 0, 0.22)
	return t
}

// useTheme gives this frame the app's theme for the system's appearance.
// Every view that draws a window calls it first; the windows beside the
// mail, the compose window included, are their own frames.
func useTheme(c *ui.Context) *ui.Theme {
	t := appTheme(c.Theme().Dark)
	c.SetTheme(t)
	return t
}
