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
