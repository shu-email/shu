package main

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"shu/locales"
	"shu/mail"
)

// The settings' cache check (issue: cache and schema integrity). It is a
// diagnostic: it reports what a crash or a bug left out of step, and
// changes nothing.

// cacheCheck is the check's state: the run in flight, and what the last
// one found.
type cacheCheck struct {
	running  bool
	done     bool
	findings []mail.IntegrityFinding
	err      string
}

// checkCache runs the check off the UI thread and keeps what it found.
func (a *app) checkCache() {
	if a.cache.running {
		return
	}
	a.cache.running, a.cache.err = true, ""
	a.run(func() {
		ctx, cancel := a.ctx()
		defer cancel()
		report, err := a.svc.CheckIntegrity(ctx)
		// The result is the settings' own: it is neither stale because the
		// mail list moved on, nor dropped when another tab shows.
		a.post(func() {
			a.cache.running = false
			a.cache.done = true
			if err != nil {
				a.cache.findings = nil
				a.cache.err = locales.CacheCheckFailed(failureText(err))
				return
			}
			a.cache.err = ""
			a.cache.findings = report.Findings
		})
	})
}

// cacheCheckView draws the cache check in the settings' General tab: what
// it does, the button, and what the last run found.
func (a *app) cacheCheckView(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, locales.CacheCheck()).FontSize(13)
			ui.Text(c, locales.CacheCheckHint()).FontSize(11).TextColor(t.TextMuted).MaxLines(4)
		})
		ui.Button(c, locales.CheckCache()).Disabled(a.cache.running).OnClick(a.checkCache)
	})
	switch {
	case a.cache.err != "":
		a.cacheResultLine(c, t, alertIcon, t.Warning, a.cache.err)
	case a.cache.running:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, locales.CacheChecking()).FontSize(12).TextColor(t.TextMuted)
		})
	case !a.cache.done:
		return
	case len(a.cache.findings) == 0:
		a.cacheResultLine(c, t, checkIcon, t.Success, locales.CacheCheckHealthy())
	default:
		for _, f := range a.cache.findings {
			a.cacheResultLine(c, t, alertIcon, t.Warning, cacheFindingText(f))
		}
	}
}

// cacheResultLine is one line of the check's result, with the icon its state
// calls for.
func (a *app) cacheResultLine(c *ui.Context, t *ui.Theme, icon *ui.SVG, color ui.Color, text string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(color)
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).MaxLines(3).Role(ui.RoleStatus)
	})
}

// cacheFindingText names one kind of finding, how many of it the check
// found, and the first few IDs it named.
func cacheFindingText(f mail.IntegrityFinding) string {
	var title string
	switch f.Kind {
	case mail.IntegritySchema:
		title = locales.CacheProblemSchema()
	case mail.IntegrityReferences:
		title = locales.CacheProblemReferences()
	case mail.IntegritySearchIndex:
		title = locales.CacheProblemSearchIndex()
	case mail.IntegrityThreads:
		title = locales.CacheProblemThreads()
	case mail.IntegrityAttachmentMissing:
		title = locales.CacheProblemAttachmentMissing()
	case mail.IntegrityAttachmentOrphans:
		title = locales.CacheProblemAttachmentOrphans()
	case mail.IntegrityDraftMissing:
		title = locales.CacheProblemDraftMissing()
	case mail.IntegrityDraftOrphans:
		title = locales.CacheProblemDraftOrphans()
	default:
		return string(f.Kind)
	}
	if len(f.IDs) == 0 {
		return locales.CacheProblem(title, f.Count)
	}
	return locales.CacheProblemIDs(title, f.Count, strings.Join(f.IDs, ", "))
}
