package engine_test

import (
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func fold(t *testing.T, bodies ...api.EventBody) engine.Thread {
	t.Helper()
	id := api.NewID()
	var th engine.Thread
	for i, b := range bodies {
		th = engine.Apply(th, engine.Event{Thread: id, Seq: uint64(i + 1), At: time.Unix(int64(i), 0), Body: b})
	}
	return th
}

var (
	created = api.ThreadCreated{Workspace: "w", Title: "t", Model: api.ModelRef{Provider: "claude", Model: "haiku"}}
	run     = api.NewID()
)

// A report that leaves the window out keeps the one known only while
// it names the model the known one is of: another's window, or one of a
// model it doesn't name, is not its own.
func TestAReportKeepsOnlyItsOwnModelsWindow(t *testing.T) {
	for _, c := range []struct {
		name string
		next api.ContextReported
		want api.Context
	}{
		{"the same model", api.ContextReported{Run: run, Used: 150000, Model: "claude-haiku-5-5"}, api.Context{Used: 150000, Window: 1000000, Model: "claude-haiku-5-5"}},
		{"a model unsaid", api.ContextReported{Run: run, Used: 150000}, api.Context{Used: 150000}},
		{"another model", api.ContextReported{Run: run, Used: 150000, Model: "claude-opus-5-5"}, api.Context{Used: 150000, Model: "claude-opus-5-5"}},
		{"another model's window", api.ContextReported{Run: run, Used: 150000, Window: 200000, Model: "claude-opus-5-5"}, api.Context{Used: 150000, Window: 200000, Model: "claude-opus-5-5"}},
		{"a compaction", api.ContextReported{Run: run, Compacted: true}, api.Context{Used: 25041, Window: 1000000, Model: "claude-haiku-5-5", Stale: true}},
	} {
		th := fold(t, created, api.ContextReported{Run: run, Used: 25041, Window: 1000000, Model: "claude-haiku-5-5"}, c.next)
		if th.Context != c.want {
			t.Errorf("after %s the context is %+v, want %+v", c.name, th.Context, c.want)
		}
	}
}

// Another model's window isn't the thread's once its model changes; the
// tokens in context still are, as the conversation goes on.
func TestAModelChangeForgetsTheWindow(t *testing.T) {
	th := fold(t, created, api.ContextReported{Run: run, Used: 150000, Window: 1000000},
		api.ThreadConfigured{Model: api.ModelRef{Provider: "claude", Model: "opus"}})
	if want := (api.Context{Used: 150000}); th.Context != want {
		t.Errorf("after the model changed the context is %+v, want %+v", th.Context, want)
	}
	th = fold(t, created, api.ContextReported{Run: run, Used: 150000, Window: 1000000},
		api.ThreadConfigured{Model: api.ModelRef{Provider: "claude", Model: "haiku", Effort: "high"}})
	if want := (api.Context{Used: 150000, Window: 1000000}); th.Context != want {
		t.Errorf("after only the effort changed the context is %+v, want %+v", th.Context, want)
	}
}

// A new conversation holds none of the one it replaced; the same one
// bound again keeps what it held.
func TestANewSessionForgetsTheContext(t *testing.T) {
	th := fold(t, created, api.SessionBound{Run: run, Session: "a"}, api.ContextReported{Run: run, Used: 11469, Window: 1048576},
		api.SessionBound{Run: run, Session: "a"})
	if want := (api.Context{Used: 11469, Window: 1048576}); th.Context != want {
		t.Errorf("after the same session bound again the context is %+v, want %+v", th.Context, want)
	}
	th = engine.Apply(th, engine.Event{Thread: th.ID, Seq: 9, At: time.Unix(9, 0), Body: api.SessionBound{Run: run, Session: "b"}})
	if th.Context != (api.Context{}) {
		t.Errorf("after another session bound the context is %+v, want none", th.Context)
	}
}
