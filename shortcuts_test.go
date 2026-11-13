package main

import (
	"testing"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"shu/locales"
)

// Cmd+/ opens the reference, which lists every group and can be closed
// with Done or Escape.
func TestShortcutReference(t *testing.T) {
	a, _ := sampleMessageApp(t)
	tt := ui.NewTester(a.view, 1220, 800)
	tt.Key(ui.Cmd, ui.KeySlash)
	if !tt.HasText(locales.ShortcutsTitle()) {
		t.Fatalf("the reference did not open: %q", tt.Texts())
	}
	for _, text := range []string{
		locales.ShortcutsReading(), locales.ShortcutsWriting(), locales.ShortcutsApp(),
		"J", locales.ShortcutNext(), "K", locales.ShortcutPrevious(),
		primaryKey() + "K", locales.ShortcutSearch(),
		locales.ShortcutSend(), locales.ShortcutAttach(),
	} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	click(t, tt, locales.Done())
	if a.shortcutsOpen || tt.HasText(locales.ShortcutsIntro()) {
		t.Error("the reference stayed open")
	}
	// The account menu opens it too.
	click(t, tt, locales.AccountsMenu())
	if err := tt.ChooseMenuItem(locales.ShortcutsTitle()); err != nil {
		t.Fatal(err)
	}
	if !a.shortcutsOpen {
		t.Error("the menu did not open the reference")
	}
	tt.Key(0, ui.KeyEscape)
	if a.shortcutsOpen {
		t.Error("Escape did not close the reference")
	}
}

// The list covers the shortcuts the window actually takes.
func TestShortcutListCoversTheWindow(t *testing.T) {
	var keys []string
	for _, group := range shortcuts() {
		if len(group.rows) == 0 || group.title == "" {
			t.Errorf("an empty group: %+v", group)
		}
		for _, row := range group.rows {
			if row.keys == "" || row.label == "" {
				t.Errorf("an empty row: %+v", row)
			}
			keys = append(keys, row.keys)
		}
	}
	for _, want := range []string{"J", "K", "U", "S", "E", "R", shiftKey() + "R", "F", "L",
		primaryKey() + "N", primaryKey() + "K", primaryKey() + "R", primaryKey() + ",", primaryKey() + "/"} {
		found := false
		for _, key := range keys {
			if key == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the reference misses %q", want)
		}
	}
}

func TestShortcutReferenceInChinese(t *testing.T) {
	i18n.SetLocaleFunc(func() string { return "zh-Hans" })
	defer i18n.SetLocaleFunc(func() string { return "en" })
	a, _ := sampleMessageApp(t)
	a.shortcutsOpen = true
	tt := ui.NewTester(a.view, 1220, 800)
	tt.SetLocale("zh-Hans")
	tt.Frame()
	for _, text := range []string{"键盘快捷键", "浏览与阅读", "下一封邮件", "撰写新邮件"} {
		if !tt.HasText(text) {
			t.Errorf("missing %q in %q", text, tt.Texts())
		}
	}
	if tt.HasText("Next message") {
		t.Errorf("English string in the Chinese reference: %q", tt.Texts())
	}
}
