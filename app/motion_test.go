package app

import (
	"fmt"
	"strings"
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
	all := []*ui.ElementTransition{
		&sidebarMotion, &paneMotion, &readingMotion, &bannerMotion, &listMotion,
		&badgeMotion, &rowEnter,
	}
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

// The rows after an archived one slide up into its room rather than jump;
// the row chosen in its place shows there at once, under its highlight.
func TestRowsSlideAfterArchive(t *testing.T) {
	withMotion(t)
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	const next, after = "[shu-email/shu] Pull request #128", "Re: Contract for Q4 campaign"
	nextAt := findText(t, tt, "GitHub").Y
	afterAt := findText(t, tt, after).Y
	click(t, tt, locales.ArchiveMessage())
	if tt.HasText("I pulled the latest frames into Figma.") {
		t.Fatalf("the archived row stays: %q", tt.Texts())
	}
	tt.Advance(16 * time.Millisecond)
	moving := findText(t, tt, after).Y
	tt.Advance(time.Second)
	settled := findText(t, tt, after).Y
	if !(settled < afterAt && moving > settled+10) {
		t.Errorf("row after: at %v before, %v moving, %v settled: it did not slide", afterAt, moving, settled)
	}
	if a.selectedKey() != "" {
		if m, _ := a.current(); m != nil && m.Subject == next {
			if y := findText(t, tt, "GitHub").Y; y >= nextAt {
				t.Errorf("chosen row at %v, not in the archived one's place", y)
			}
		}
	}
}

// Rows that change because another list shows do not move: a folder
// opened shows its rows where they are.
func TestRowsStillOnFolderChange(t *testing.T) {
	withMotion(t)
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	a.rows.update(listQuery{folder: "elsewhere"}, []string{"x"}, tt.Now())
	if len(a.rows.shift) != 0 || len(a.rows.coming) != 0 {
		t.Errorf("another list moved rows: %v %v", a.rows.shift, a.rows.coming)
	}
}

// A count's badge remembers which way it went, so its digits roll down as
// mail is read and up as it comes.
func TestCountBadgeDirection(t *testing.T) {
	a, _ := sampleApp(t)
	n := 5
	tt := ui.NewTester(func(c *ui.Context) {
		a.countBadge(c, "k", fmt.Sprint(n), n)
	}, 200, 100)
	if a.counts["k"].down {
		t.Error("first count went down")
	}
	n = 3
	tt.Frame()
	if !a.counts["k"].down || !tt.HasText("3") {
		t.Errorf("5 to 3: %+v %q", a.counts["k"], tt.Texts())
	}
	n = 4
	tt.Frame()
	if a.counts["k"].down {
		t.Error("3 to 4 went down")
	}
}

// The toolbar floats over the top of the reading pane, and what is read
// starts below it, at the window's minimum size too.
func TestFloatingToolbar(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1000, 640)
	aboveBar := func(tt *ui.Tester) {
		bar := findText(t, tt, locales.MessageActions())
		subject := findText(t, tt, "Body of Design review — Thursday at 10")
		if subject.Y < bar.Y+bar.H {
			t.Errorf("the message starts at %v, under the toolbar ending at %v", subject.Y, bar.Y+bar.H)
		}
	}
	aboveBar(tt)
	// The pane is narrow at this size, so the moves are in their menu.
	click(t, tt, locales.MoveActions())
	if err := tt.ChooseMenuItem(locales.ArchiveMessage()); err != nil {
		t.Fatal(err)
	}
	if a.undo == nil {
		t.Error("archive from the floating toolbar did nothing")
	}
	// The dark appearance lays the pane out the same way.
	d, _ := sampleMessageApp(t)
	aboveBar(ui.NewTesterWith(d.view, 1000, 640, ui.TesterOptions{Dark: true}))
}

// Where the reading pane is too narrow for the search field beside the
// actions, a button stands for it: the field shows as it is used, with
// Cmd+K too, and goes once left empty. Cmd+, opens the settings.
func TestCompactSearch(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1000, 640)
	tt.Frame()
	if !tt.HasText(locales.SearchMailLabel()) {
		t.Fatalf("no search: %q", tt.Texts())
	}
	click(t, tt, locales.SearchMailLabel())
	tt.Frame()
	if !a.searchOpen || !tt.Focused(locales.SearchMailLabel()) {
		t.Fatalf("the field did not open with the focus: open=%v", a.searchOpen)
	}
	tt.Type("Contract")
	tt.Frame()
	if a.query != "Contract" {
		t.Fatalf("query = %q", a.query)
	}
	click(t, tt, "Clear")
	tt.Frame()
	click(t, tt, "[shu-email/shu] Pull request #128")
	tt.Frame()
	if a.searchOpen {
		t.Error("the empty field stays open after it lost the focus")
	}
	// The actions are all there again.
	for _, s := range []string{locales.MoveActions(), locales.MarkActions(), locales.Forward()} {
		if r, ok := tt.Find(s); !ok || r.W == 0 {
			t.Errorf("%q not shown with the search closed", s)
		}
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if !a.searchOpen {
		t.Error("Cmd+K did not open the search")
	}
	tt.Key(ui.Cmd, ui.KeyComma)
	if !a.showSettings {
		t.Error("Cmd+, did not open the settings")
	}
}

// A narrow reading bar folds its moves, marks and labels into menus, with
// every action still at hand, and shows them again as the pane widens.
func TestCompactFoldsGroups(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1000, 640)
	tt.Frame()
	if !a.readingCompact() {
		t.Fatal("1000x640 did not make the pane narrow")
	}
	for _, s := range []string{locales.MoveActions(), locales.MarkActions()} {
		if r, ok := tt.Find(s); !ok || r.W == 0 || r.X+r.W > 1000 {
			t.Errorf("the %q menu at %v", s, r)
		}
	}
	for _, s := range []string{locales.ArchiveMessage(), locales.JunkMessage(), locales.StarMessage(),
		locales.MarkRead(), locales.ChangeLabels()} {
		if r, ok := tt.Find(s); ok && r.W > 0 {
			t.Errorf("%q shows beside its menu", s)
		}
	}
	click(t, tt, locales.MoveActions())
	got := strings.Join(tt.Menu(), ",")
	for _, s := range []string{locales.ArchiveMessage(), locales.DeleteMessage(), locales.JunkMessage()} {
		if !strings.Contains(got, s) {
			t.Errorf("the move menu holds %q, not %q", got, s)
		}
	}
	if err := tt.ChooseMenuItem(locales.ArchiveMessage()); err != nil {
		t.Fatal(err)
	}
	if a.undo == nil {
		t.Error("archive from the move menu did nothing")
	}

	// The search in use leaves the replies their room and hides the rest.
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if _, ok := tt.Find(locales.MoveActions()); ok {
		t.Error("the move menu stays beside the search in use")
	}
	if _, ok := tt.Find(locales.Forward()); !ok {
		t.Error("the search in use hides the replies")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	b, _ := sampleMessageApp(t)
	wide := ui.NewTester(b.view, 1220, 800)
	wide.Frame()
	for _, s := range []string{locales.ArchiveMessage(), locales.StarMessage(), locales.ChangeLabels(), locales.MarkUnread()} {
		if r, ok := wide.Find(s); !ok || r.W == 0 {
			t.Errorf("%q not shown in a wide pane", s)
		}
	}
}
