package main

import (
	"testing"

	"github.com/egoist/mygo"

	"shu/locales"
)

// The settings open in a window of their own, with the tab chosen and
// the mail still behind it; a second call reuses it.
func TestSettingsOpenTheirOwnWindow(t *testing.T) {
	a, _ := sampleMessageApp(t)
	var got mygo.WindowOptions
	calls := 0
	a.newWindow = func(o mygo.WindowOptions) *mygo.Window {
		calls++
		got = o
		return nil
	}
	a.openSettings(settingsTabNetwork)
	if calls != 1 {
		t.Fatalf("windows = %d", calls)
	}
	if got.Title != locales.Settings() || got.Width < 760 || got.Height < 560 || got.Content == nil {
		t.Errorf("window = %+v", got)
	}
	if !a.showSettings || a.settingsTab != settingsTabNetwork {
		t.Errorf("settings = %v, tab %d", a.showSettings, a.settingsTab)
	}
	a.closeSettings()
	if a.showSettings {
		t.Error("closeSettings left the settings open")
	}
}

// With no window available, the settings show in the main view, which is
// how the tests without a window read them.
func TestSettingsWithoutAWindow(t *testing.T) {
	a, _ := sampleMessageApp(t)
	a.newWindow = nil
	a.openSettings(settingsTabGeneral)
	if !a.showSettings {
		t.Fatal("the settings did not open")
	}
	if a.settingsWin != nil {
		t.Error("a window appeared without newWindow")
	}
}
