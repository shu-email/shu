package app

import (
	"context"
	"sort"

	"shu/ai"
	"shu/locales"
	"shu/mail"
)

// Topics are the model's grouping of the mail on screen by what its
// subjects are about: the one thing Shu sends a model without a message
// being read — the subjects alone, after the user asks for it and the
// review gate shows exactly what leaves. The groups order the cards on the
// window; the store's cursor is untouched, so paging still reads the same
// pages in the same order underneath.

// topicState is the grouping the window asked for.
type topicState struct {
	aiJob
	groups []ai.Topic
	// byKey is the group of each row's key. A row the model said nothing
	// about keeps -1 and draws last, ungrouped.
	byKey map[string]int
}

// grouped reports that the list draws the model's groups.
func (a *app) grouped() bool { return a.topics != nil && a.topics.byKey != nil }

// topicItems lists the rows on screen, newest first, as the grouping reads
// them: their key and their subject, and nothing else.
func (a *app) topicItems() []ai.TopicItem {
	n := min(len(a.all), ai.TopicLimit)
	items := make([]ai.TopicItem, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, ai.TopicItem{ID: a.rowKey(i), Subject: a.all[i].Subject})
	}
	return items
}

// groupTopics asks the model to group the rows on screen by subject. The
// first time, it shows what would be sent.
func (a *app) groupTopics() {
	if a.ai == nil || len(a.all) == 0 || a.draftsList() {
		return
	}
	if a.grouped() {
		a.clearTopics()
		return
	}
	items := a.topicItems()
	g := a.gen
	language := aiLanguage()
	a.aiBegin(ai.FeatureTopics,
		func(r *aiReview) { a.review = r },
		func(err error) {
			if !a.stale(g) {
				a.topics = &topicState{aiJob: *aiFailed(err)}
			}
		},
		func(ctx context.Context) (ai.Payload, error) { return a.ai.TopicsPayload(ctx, items, language) },
		func(model string) {
			if a.stale(g) {
				return
			}
			ctx, cancel := context.WithCancel(context.Background())
			state := &topicState{aiJob: aiJob{busy: true, cancel: cancel, model: model}}
			a.topics = state
			a.run(func() {
				defer cancel()
				groups, err := a.ai.Topics(ctx, items, language)
				a.post(func() {
					if a.topics != state {
						return
					}
					if err != nil {
						a.topics = &topicState{aiJob: *aiFailed(err)}
						return
					}
					a.applyTopics(groups)
				})
			})
		})
}

// applyTopics puts the groups on the rows and orders the cards by them,
// keeping the rows' own order inside a group. The choice is kept by key,
// so the message that was open stays open.
func (a *app) applyTopics(groups []ai.Topic) {
	if a.stale(a.gen) {
		return
	}
	byKey := make(map[string]int, len(a.all))
	for i, g := range groups {
		for _, id := range g.IDs {
			if _, seen := byKey[id]; !seen {
				byKey[id] = i
			}
		}
	}
	state := &topicState{groups: groups, byKey: byKey}
	a.topics = state
	a.orderByTopics()

}

// orderByTopics reorders the rows read so that a group's cards sit
// together, as the model named them. A row the model left out draws last.
func (a *app) orderByTopics() {
	n := len(a.all)
	if n == 0 || !a.grouped() {
		return
	}
	key := a.selectedKey()
	group := make([]int, n)
	for i := 0; i < n; i++ {
		g, ok := a.topics.byKey[a.rowKey(i)]
		if !ok {
			g = len(a.topics.groups)
		}
		group[i] = g
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool { return group[order[x]] < group[order[y]] })
	all := make([]mail.Summary, n)
	convs := make([]mail.Conversation, len(a.convs))
	for to, from := range order {
		all[to] = a.all[from]
		if len(a.convs) == n {
			convs[to] = a.convs[from]
		}
	}
	a.all = all
	if len(a.convs) == n {
		a.convs = convs
	}
	a.restoreSelection(key)
}

// clearTopics drops the grouping, cancelling a request in flight and
// putting the rows back in the list's own order.
func (a *app) clearTopics() {
	if a.topics != nil && a.topics.cancel != nil {
		a.topics.cancel()
	}
	was := a.grouped()
	a.topics = nil
	if was {
		a.selectFolder()
	}
}

// topicGroup is the group of row i when the model grouped the list: its
// index, and the name the model gave it.
func (a *app) topicGroup(i int) (int, string, bool) {
	if !a.grouped() || i < 0 || i >= len(a.all) {
		return 0, "", false
	}
	g, ok := a.topics.byKey[a.rowKey(i)]
	if !ok {
		return len(a.topics.groups), locales.Unsorted(), true
	}
	if g < 0 || g >= len(a.topics.groups) {
		return len(a.topics.groups), locales.Unsorted(), true
	}
	return g, a.topics.groups[g].Name, true
}
