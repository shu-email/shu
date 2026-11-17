package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// The stream is read by what asks of the user: a message with a code or a
// question goes above the news, and each keeps the newest first inside its
// group. The cursor the next page is read with is the store's, so ranking
// the rows shown never moves it.
func TestStreamRanksByIntent(t *testing.T) {
	t.Setenv("SHU_WINDOW", "shell")
	a, _ := streamApp(t, "Just a newsletter.", "Your verification code is 482913.",
		"Just a note.", "See you Friday.")
	tt := ui.NewTester(a.view, 1220, 800)
	// Everything but the code was read: mail already read only tells.
	for i := range a.all {
		a.all[i].Seen = a.all[i].Preview != "Your verification code is 482913."
	}
	a.rankRows()
	tt.Frame()
	if got := a.all[0].Preview; got != "Your verification code is 482913." {
		t.Fatalf("the first card is %q", got)
	}
	next := a.pager.next
	a.rankRows()
	if a.pager.next != next {
		t.Error("ranking moved the pager's cursor")
	}
}
