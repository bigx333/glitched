package main

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
)

// rowActions is the context of thread.row.actions: the thread, the
// section its row shows in, and the times the app offers to snooze it
// until.
type rowActions struct {
	Thread  plugins.ThreadRecord `json:"thread"`
	Section string               `json:"section"`
	Snooze  []snoozeChoice       `json:"snooze"`
}

type snoozeChoice struct {
	When  string `json:"when"`
	Joke  string `json:"joke"`
	Emoji string `json:"emoji"`
	Until string `json:"until"`
}

// rowActions are a pointed-at thread's actions, a plugin's: the built-in
// snoozes and settles it, or sends it back to the inbox. They stay while
// its popover is open, which takes the pointer away from the row.
func (a *app) rowActions(c *ui.Context, t thread, cat category) {
	var snooze []snoozeChoice
	for _, s := range snoozeTimes {
		snooze = append(snooze, snoozeChoice{s.when, s.joke, s.emoji, s.until(a.now).UTC().Format(time.RFC3339)})
	}
	box := func() ui.Element { return ui.Row(c).Key("component:thread.row.actions").Gap(0) }
	n := a.pluginComponent(c, box, "thread.row.actions", t.id().String(), &t, func(r plugins.ThreadRecord) any {
		return rowActions{r, sectionNames[cat], snooze}
	})
	open := false
	n.Walk(func(n *widget.Node) { open = open || n.Popover != nil && n.Open })
	switch {
	case open:
		a.snoozeFor = t.id()
	case a.snoozeFor == t.id():
		a.snoozeFor = api.ID{}
	}
}

// jokes are the row animations a plugin may play as it moves a thread,
// each with the app's toast, if it has one.
var jokes = map[string]gagKind{"snooze": gagSnooze, "settle": gagSettle, "undo": gagUndo}

// setDispositionEffect is threads.setDisposition: the thread goes to the
// inbox, is snoozed till a time to come, or is settled, and its row plays
// the joke the plugin names, as the app's own actions do.
func (a *app) setDispositionEffect(raw json.RawMessage) (any, error) {
	var args struct{ State, Until, Animation string }
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	kind, joking := jokes[args.Animation]
	if args.Animation != "" && !joking {
		return nil, errors.New("threads.setDisposition: animation is snooze, settle or undo")
	}
	t, err := a.pluginFind(raw)
	if err != nil {
		return nil, err
	}
	var d api.Disposition
	switch args.State {
	case "inbox":
	case "settled":
		d.State = api.Settled
	case "snoozed":
		until, err := time.Parse(time.RFC3339, args.Until)
		if err != nil || !until.After(a.now) {
			return nil, errors.New("threads.setDisposition: a snooze needs an until to come, in RFC 3339")
		}
		d = api.Disposition{State: api.Snoozed, Until: until}
	default:
		return nil, errors.New("threads.setDisposition: state is inbox, snoozed or settled")
	}
	from := a.categoryOf(t)
	a.backend.SetDisposition(t.env, t.id(), d)
	if !joking {
		return nil, nil
	}
	a.pluginLater = append(a.pluginLater, func(c *ui.Context) {
		a.gag(c, t.id(), kind, from)
		switch kind {
		case gagSnooze:
			c.AddToast(ui.Toast{Title: "Sleeping until " + lower(snoozeWhen(d.Until, a.now)) + " 💤", Description: t.View.Title, Type: "info"})
		case gagSettle:
			c.AddToast(ui.Toast{Title: "Settled. Phew.", Description: title(t)})
		}
	})
	return nil, nil
}

// snoozeWhen names a snooze's time as the app's choices do, when it is
// one of them, and as the sidebar shows it otherwise.
func snoozeWhen(until, now time.Time) string {
	if i := slices.IndexFunc(snoozeTimes, func(s struct {
		when, joke, emoji string
		until             func(now time.Time) time.Time
	}) bool {
		return s.until(now).Equal(until)
	}); i >= 0 {
		return snoozeTimes[i].when
	}
	return snoozeLabel(until, now)
}
