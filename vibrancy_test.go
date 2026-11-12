package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// Where the window shows its material, the sidebar lets it through, from
// under the window controls to the bottom, and the panes stay opaque;
// where it shows none, the sidebar draws its own surface. In both
// appearances.
func TestSidebarMaterial(t *testing.T) {
	a, _ := sampleMessageApp(t)
	for _, dark := range []bool{false, true} {
		for _, vibrant := range []bool{false, true} {
			tt := ui.NewTesterWith(a.view, 1000, 640, ui.TesterOptions{Dark: dark})
			tt.SetVibrancy(vibrant)
			tt.Frame()
			img := tt.Image()
			alpha := func(x, y float32) uint8 {
				s := float32(img.Bounds().Dx()) / 1000
				return img.RGBAAt(int(x*s), int(y*s)).A
			}
			for _, y := range []float32{10, 600} {
				if got := alpha(215, y); vibrant != (got == 0) {
					t.Errorf("dark=%v vibrant=%v: sidebar alpha %d at y=%v", dark, vibrant, got, y)
				}
			}
			for _, p := range [][2]float32{{400, 30}, {400, 600}, {800, 600}} {
				if got := alpha(p[0], p[1]); got != 255 {
					t.Errorf("dark=%v vibrant=%v: pane alpha %d at %v", dark, vibrant, got, p)
				}
			}
		}
	}
}
