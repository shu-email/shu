package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// Grouping the list by topic asks first, sends the subjects alone, and
// puts the cards under the model's words.
func TestGroupByTopic(t *testing.T) {
	t.Setenv("SHU_WINDOW", "shell")
	a, _, provider, _ := aiMessageApp(t, true)
	tt := ui.NewTester(a.view, 1220, 800)
	openPaletteEntry(t, tt, locales.GroupByTopic())
	if !tt.HasText(locales.AiReviewTitle()) || len(provider.texts) != 0 {
		t.Fatalf("grouping did not wait for approval: %q", tt.Texts())
	}
	if !tt.HasText(locales.AiReviewTopicsSize(3, a.review.payload.Chars)) {
		t.Errorf("the size line: %q", tt.Texts())
	}
	click(t, tt, locales.AiReviewSend("gpt-4o-mini"))
	if len(provider.texts) != 1 {
		t.Fatalf("requests = %d", len(provider.texts))
	}
	sent := provider.texts[0]
	for _, leak := range []string{"I pulled the latest frames", "Shall we try the new place", "maya@example.com"} {
		if strings.Contains(sent, leak) {
			t.Errorf("grouping sent %q", leak)
		}
	}
	if !a.grouped() {
		t.Fatalf("the list is not grouped: %q", tt.Texts())
	}
	if !tt.HasText("Design") || !tt.HasText("Admin") {
		t.Errorf("the groups do not show: %q", tt.Texts())
	}
	// The cards a group took sit together: the model put the design
	// review and the contract under Design, so the pull request between
	// them moves after both.
	if got := rowKeys(a); len(got) != 3 || got[0] != "a1/INBOX/1" || got[1] != "a1/INBOX/3" || got[2] != "a1/INBOX/2" {
		t.Errorf("rows = %v", got)
	}
	// Grouped again by the same words, it drops the grouping: the rows go
	// back to the list's own order.
	openPaletteEntry(t, tt, locales.GroupByTopic())
	if a.grouped() {
		t.Error("the grouping stayed")
	}
	if got := rowKeys(a); got[0] != "a1/INBOX/1" || got[1] != "a1/INBOX/2" {
		t.Errorf("ungrouped rows = %v", got)
	}
}

// openPaletteEntry chooses a command from the window's one field.
func openPaletteEntry(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	click(t, tt, locales.AskMailPlaceholder())
	if err := tt.Click(label); err != nil {
		t.Fatalf("choose %q: %v\ntexts: %q", label, err, tt.Texts())
	}
	tt.Frame()
}
