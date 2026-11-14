package app

import (
	"shu/mail"
)

// followUpFolder is the sidebar's Follow Up entry: the conversations that
// wait for a reply, derived from the cache alone (mail.FollowUpScope). A
// row is the message the user sent, whose recipients and age tell the
// conversations apart; the user can dismiss one until a newer message of
// it is cached.
const followUpFolder = "follow-up"

// loadFollowUps counts the active account's conversations that wait for a
// reply, for the sidebar and the list.
func (a *app) loadFollowUps(g uint64) {
	accountID := a.active
	if accountID == "" {
		return
	}
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		n, err := a.svc.FollowUpCount(ctx, accountID)
		a.post(func() {
			if a.stale(g) || a.active != accountID || err != nil {
				return
			}
			a.followUps = n
		})
	})
}

// dismissFollowUp takes conversations off the follow-up list, which the
// cache keeps off until a newer message of each arrives. They go in one
// job, so that the list and its count are read once they are all off.
func (a *app) dismissFollowUp(account string, threads ...mail.ThreadID) {
	g := a.gen
	all := append([]mail.ThreadID(nil), threads...)
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		var err error
		for _, thread := range all {
			if err = a.svc.DismissFollowUp(ctx, account, thread); err != nil {
				break
			}
		}
		a.post(func() {
			if a.closed {
				return
			}
			if err != nil {
				a.err = failureText(err)
				return
			}
			a.err = ""
			a.loadFollowUps(a.gen)
			if a.folder == followUpFolder {
				a.readList(g, false, false)
			}
		})
	})
}
