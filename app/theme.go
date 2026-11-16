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
	paper     = ui.Hex("#fcfbf9")
	paperSoft = ui.Hex("#f4f2ee")
	ink       = ui.Hex("#1c1b19")
	inkSoft   = ui.Hex("#7a756c")
	teal      = ui.Hex("#0f6e63")
	night     = ui.Hex("#15161a")
	nightSoft = ui.Hex("#1e1f24")
	moon      = ui.Hex("#eeede9")
	moonSoft  = ui.Hex("#9b9a93")
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
		t.SurfaceHover = ui.Hex("#26272d")
		t.SurfacePressed = ui.Hex("#2f3037")
		t.Border = ui.Hex("#303138")
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
	t.SurfaceHover = ui.Hex("#ece9e3")
	t.SurfacePressed = ui.Hex("#e2ded7")
	t.Border = ui.Hex("#e4e0d8")
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
