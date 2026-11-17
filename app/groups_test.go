package app

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The caption of the group on screen stays at the top of the list as its
// cards scroll under it, and takes the place of the group's own caption
// only once that one has gone.
func TestPinnedCaption(t *testing.T) {
	t.Setenv("SHU_WINDOW", "shell")
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	if got := a.pinnedCaption(tt.Now()); got != "" {
		t.Fatalf("the caption is pinned at the top: %q", got)
	}
	// Many rows of one day, so that the list can scroll within a group.
	for i := 0; i < 40; i++ {
		m := a.all[0]
		m.UID = 1000 + mail.UID(i)
		m.Subject = "Another message"
		m.Date = a.all[0].Date
		a.all = append(a.all, m)
	}
	tt.Frame()
	scrollList(tt, 400)
	tt.Frame()
	first, last := a.list.Visible()
	if first <= 0 || last < 0 {
		t.Fatalf("the list did not scroll: %d..%d", first, last)
	}
	if got := a.pinnedCaption(tt.Now()); got == "" {
		t.Fatalf("no caption is pinned at %d: %q", first, tt.Texts())
	}
	if !tt.HasText(locales.GroupToday()) {
		t.Errorf("the pinned caption does not show: %q", tt.Texts())
	}
}
