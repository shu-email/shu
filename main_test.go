package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// The view runs without a window in tests, which click and type as a user
// would.
func TestView(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1220, 800)
	for _, text := range []string{"Shu", "Inbox", "Starred", "Compose", "Design review — Thursday at 10", "Search mail"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
}

// Choosing another mailbox leaves the message list in place: the window is
// still only a layout.
func TestMailboxChoice(t *testing.T) {
	a := newApp()
	tt := ui.NewTester(a.view, 1220, 800)
	if err := tt.Click("Sent"); err != nil {
		t.Fatal(err)
	}
	if a.mailbox != "sent" {
		t.Errorf("mailbox %q after a click", a.mailbox)
	}
}
