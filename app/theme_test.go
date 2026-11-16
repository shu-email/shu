package app

import (
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The window draws the app's palette rather than MyGo's default, in both
// appearances: the reading surface is paper in light and ink in dark, and
// a chosen row is tinted instead of filled.
func TestThemeIsTheApps(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	readPane := func() color.RGBA {
		img := tt.Image()
		b := img.Bounds()
		// In the reading pane, clear of the message's text.
		return img.RGBAAt(b.Min.X+b.Dx()*9/10, b.Min.Y+b.Dy()*7/10)
	}
	for _, c := range []struct {
		dark bool
		want uint32
	}{
		{false, 0xfcfbf9},
		{true, 0x15161a},
	} {
		tt.SetDark(c.dark)
		tt.Frame()
		got := readPane()
		if rgb := uint32(got.R)<<16 | uint32(got.G)<<8 | uint32(got.B); rgb != c.want {
			t.Errorf("dark=%v: reading pane = %06x, want %06x", c.dark, rgb, c.want)
		}
	}
}
