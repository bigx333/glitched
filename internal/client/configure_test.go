package client

import (
	"testing"

	"glitched/internal/api"
)

// A configure's receipt can come before the view that shows it: until the
// view does, the configure stays pending, so the thread shows its model
// and access and the next configure builds on them, not on the old view.
func TestAConfigureStaysUntilTheViewShowsIt(t *testing.T) {
	id := api.NewID()
	old := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5"}
	th := &Thread{View: api.Thread{ID: id, Model: old, Access: api.AccessAutoEdits}}
	e := &Env{Threads: map[api.ID]*Thread{id: th}}
	m := &Manager{}
	ask := api.ThreadConfigure{Model: old, Access: api.AccessAsk}
	sonnet := api.ThreadConfigure{Model: api.ModelRef{Provider: "claude", Model: "Claude Sonnet 5.5"}, Access: api.AccessAsk}
	var cmds []api.Command
	for _, c := range []api.ThreadConfigure{ask, sonnet} {
		cmd := api.Command{ID: api.NewID(), Target: id, Body: c}
		th.Pending = append(th.Pending, Pending{Command: cmd.ID, Body: c})
		cmds = append(cmds, cmd)
	}
	m.submitted(e, cmds[0], nil)
	if len(th.Pending) != 2 {
		t.Fatalf("the receipt dropped the configure before the view showed it: %+v", th.Pending)
	}
	m.submitted(e, cmds[1], nil)
	// The view of the first configure lands: only it goes.
	th.View.Access = api.AccessAsk
	th.configured()
	if len(th.Pending) != 1 || th.Pending[0].Command != cmds[1].ID {
		t.Fatalf("with the first configure shown, pending is %+v", th.Pending)
	}
	th.View.Model = sonnet.Model
	th.configured()
	if len(th.Pending) != 0 {
		t.Fatalf("with every configure shown, pending is %+v", th.Pending)
	}
	// A view that skips straight to the last configure drops the ones
	// before it too.
	th.Pending = nil
	for _, c := range []api.ThreadConfigure{ask, sonnet} {
		th.Pending = append(th.Pending, Pending{Command: api.NewID(), Body: c, sent: true})
	}
	th.configured()
	if len(th.Pending) != 0 {
		t.Fatalf("a view showing the last configure left %+v", th.Pending)
	}
}

// A configure the view showed before its receipt came goes with the
// receipt, though the view has moved on since, as another client's
// configure moves it.
func TestAConfigureTheViewShowedGoesWithItsReceipt(t *testing.T) {
	id := api.NewID()
	th := &Thread{View: api.Thread{ID: id, Access: api.AccessAutoEdits}}
	e := &Env{Threads: map[api.ID]*Thread{id: th}}
	m := &Manager{}
	ask := api.ThreadConfigure{Access: api.AccessAsk}
	cmd := api.Command{ID: api.NewID(), Target: id, Body: ask}
	th.Pending = []Pending{{Command: cmd.ID, Body: ask}}
	v := th.View
	v.Access = api.AccessAsk
	m.view(e, v)
	v.Access = api.AccessFull
	m.view(e, v)
	m.submitted(e, cmd, nil)
	if len(th.Pending) != 0 {
		t.Fatalf("a configure the view showed and moved past stays: %+v", th.Pending)
	}
}

// A snapshot comes after every configure committed before it: one it
// doesn't show, the thread moved past.
func TestASnapshotDropsTheConfiguresItMovedPast(t *testing.T) {
	id := api.NewID()
	th := &Thread{View: api.Thread{ID: id}}
	e := &Env{Threads: map[api.ID]*Thread{id: th}}
	th.Pending = []Pending{{Command: api.NewID(), Body: api.ThreadConfigure{Access: api.AccessFull}, sent: true}}
	snap := api.Snapshot{Threads: []api.Thread{{ID: id, Access: api.AccessAsk}}}
	(&Manager{}).shell(e, nil, api.Environment{}, api.StreamItem{Kind: api.StreamSnapshot, Snapshot: &snap})
	if len(th.Pending) != 0 {
		t.Fatalf("after a snapshot at Ask the thread still has %+v", th.Pending)
	}
}

// A failed configure isn't sent again the same, as a held slider would
// each frame; another drops it, as its retry would undo the new one.
func TestAFailedConfigureIsNotSentAgainEachFrame(t *testing.T) {
	id := api.NewID()
	th := &Thread{View: api.Thread{ID: id}}
	e := &Env{Threads: map[api.ID]*Thread{id: th}}
	m := &Manager{}
	high := api.ModelRef{Model: "m", Effort: "high"}
	sent := map[api.ID]bool{}
	for range 3 {
		sent[m.ConfigureThread(e, id, high, api.AccessAsk)] = true // no engine: each fails at once
	}
	if len(sent) != 1 || len(th.Pending) != 1 || th.Pending[0].Err == nil {
		t.Fatalf("three of the same failed configure sent %d commands and left %+v", len(sent), th.Pending)
	}
	low := high
	low.Effort = "low"
	m.ConfigureThread(e, id, low, api.AccessAsk)
	if len(th.Pending) != 1 || th.Pending[0].Body.(api.ThreadConfigure).Model != low {
		t.Fatalf("a new configure after a failed one left %+v", th.Pending)
	}
}
