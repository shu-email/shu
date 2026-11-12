package mail

import (
	"context"
	"slices"
	"strconv"
	"time"
)

// CheckIntervals are the choices of how often mail is checked, in
// minutes, and DefaultCheckMinutes the one in use until the user picks.
var CheckIntervals = []int{1, 5, 15, 30}

// DefaultCheckMinutes is how often mail is checked by default.
const DefaultCheckMinutes = 5

// Settings are how mail is received in the background and how new mail
// is announced. The engine uses the interval; the rest it only keeps for
// the window, which is what shows notifications.
type Settings struct {
	// CheckMinutes is one of CheckIntervals: how often the folders and
	// their counts are refreshed, and how often new mail is looked for on
	// a server that does not push it.
	CheckMinutes int
	// Notify turns the notifications of all accounts on or off.
	Notify bool
	// NotifyDetails lets a notification name the sender and the subject.
	// Off, it names the account and a number of messages only.
	NotifyDetails bool
	// Muted holds the accounts whose notifications are off, by ID.
	Muted map[string]bool
}

// DefaultSettings are the settings of a new install: mail is checked
// every few minutes, and announced without saying what it holds.
func DefaultSettings() Settings {
	return Settings{CheckMinutes: DefaultCheckMinutes, Notify: true, Muted: map[string]bool{}}
}

// The keys of the settings in the store.
const (
	settingCheckMinutes  = "mail.check_minutes"
	settingNotify        = "notify.enabled"
	settingNotifyDetails = "notify.details"
)

// MutedKey is the key of the setting that turns one account's
// notifications off. The store removes it with the account.
func MutedKey(accountID string) string { return "notify.muted:" + accountID }

// checkMinutes returns minutes if it is one of the choices, else the
// default: a value nobody offered is not obeyed.
func checkMinutes(minutes int) int {
	if slices.Contains(CheckIntervals, minutes) {
		return minutes
	}
	return DefaultCheckMinutes
}

// Settings reads the settings, with the defaults for what was never set.
func (s *Service) Settings(ctx context.Context) (Settings, error) {
	out := DefaultSettings()
	flag := func(key string, value *bool) error {
		raw, ok, err := s.store.Setting(ctx, key)
		if err == nil && ok {
			*value = raw == "1"
		}
		return err
	}
	raw, ok, err := s.store.Setting(ctx, settingCheckMinutes)
	if err != nil {
		return out, err
	}
	if ok {
		minutes, _ := strconv.Atoi(raw)
		out.CheckMinutes = checkMinutes(minutes)
	}
	if err := flag(settingNotify, &out.Notify); err != nil {
		return out, err
	}
	if err := flag(settingNotifyDetails, &out.NotifyDetails); err != nil {
		return out, err
	}
	accounts, err := s.store.Accounts(ctx)
	if err != nil {
		return out, err
	}
	for _, a := range accounts {
		muted := false
		if err := flag(MutedKey(a.ID), &muted); err != nil {
			return out, err
		}
		if muted {
			out.Muted[a.ID] = true
		}
	}
	return out, nil
}

// SaveSettings writes the settings and has the watchers check at the new
// interval.
func (s *Service) SaveSettings(ctx context.Context, set Settings) error {
	set.CheckMinutes = checkMinutes(set.CheckMinutes)
	bit := func(on bool) string {
		if on {
			return "1"
		}
		return "0"
	}
	if err := s.store.SetSetting(ctx, settingCheckMinutes, strconv.Itoa(set.CheckMinutes)); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, settingNotify, bit(set.Notify)); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, settingNotifyDetails, bit(set.NotifyDetails)); err != nil {
		return err
	}
	// Only the accounts there are get a row: one for an account that was
	// removed meanwhile would never be deleted.
	accounts, err := s.store.Accounts(ctx)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if err := s.store.SetSetting(ctx, MutedKey(a.ID), bit(set.Muted[a.ID])); err != nil {
			return err
		}
	}
	s.setInterval(time.Duration(set.CheckMinutes) * time.Minute)
	return nil
}
