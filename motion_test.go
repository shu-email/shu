package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// restoreMotion brings back the window's motion that TestMain stilled.
var restoreMotion func()

// stillMotion makes every transition of the window instant, and returns
// what brings them back.
func stillMotion() func() {
	all := []*ui.ElementTransition{&sidebarMotion, &paneMotion, &readingMotion, &bannerMotion, &listMotion}
	saved := make([]ui.ElementTransition, len(all))
	for i, tr := range all {
		saved[i] = *tr
		tr.Animation, tr.Duration = nil, -1
	}
	return func() {
		for i, tr := range all {
			*tr = saved[i]
		}
	}
}

// withMotion turns the window's motion on for the test.
func withMotion(t *testing.T) {
	restoreMotion()
	t.Cleanup(func() { restoreMotion = stillMotion() })
}

func findText(t *testing.T, tt *ui.Tester, s string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(s)
	if !ok {
		t.Fatalf("%q not shown: %q", s, tt.Texts())
	}
	return r
}

// The sidebar folds away and the list slides into its place over the
// spring, not at once, and back as the sidebar opens again.
func TestSidebarMotion(t *testing.T) {
	withMotion(t)
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	const row = "[shu-email/shu] Pull request #128"
	start := findText(t, tt, row).X
	click(t, tt, locales.HideSidebar())
	tt.Advance(80 * time.Millisecond)
	if mid := findText(t, tt, row).X; !(mid < start && mid > start-200) {
		t.Errorf("list at x=%v mid-way, from %v: it did not slide", mid, start)
	}
	tt.Advance(time.Second)
	if tt.HasText(locales.Mailboxes()) {
		t.Errorf("sidebar still shows once folded: %q", tt.Texts())
	}
	end := findText(t, tt, row).X
	if end >= start-200 {
		t.Errorf("list at x=%v with the sidebar hidden, from %v", end, start)
	}
	click(t, tt, locales.ShowSidebar())
	tt.Advance(80 * time.Millisecond)
	if mid := findText(t, tt, row).X; !(mid > end && mid < start) {
		t.Errorf("list at x=%v mid-way back, between %v and %v", mid, end, start)
	}
	tt.Advance(time.Second)
	if x := findText(t, tt, row).X; x != start {
		t.Errorf("list at x=%v after the sidebar came back, want %v", x, start)
	}
	if !tt.HasText(locales.Mailboxes()) {
		t.Errorf("sidebar missing: %q", tt.Texts())
	}
}

// The Undo banner opens over its motion, takes clicks once open, and folds
// away after Undo; the list keeps working through it.
func TestBannerMotion(t *testing.T) {
	withMotion(t)
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	click(t, tt, locales.ArchiveMessage())
	if a.undo == nil {
		t.Fatal("no undo offered")
	}
	if tt.Click(locales.Undo()) == nil {
		t.Fatal("Undo took a click before its banner opened")
	}
	tt.Advance(300 * time.Millisecond)
	click(t, tt, locales.Undo())
	tt.Advance(300 * time.Millisecond)
	if a.undo != nil || tt.HasText(locales.Undo()) {
		t.Errorf("the banner stays after Undo: %q", tt.Texts())
	}
	click(t, tt, "[shu-email/shu] Pull request #128")
	tt.Advance(300 * time.Millisecond)
	if m, _ := a.current(); m == nil || m.Subject != "[shu-email/shu] Pull request #128" {
		t.Errorf("chosen = %+v", m)
	}
}
