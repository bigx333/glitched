package runtime_test

import (
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
	"glitched/internal/store"
)

// A native subagent's events land in its run's log under the step that
// started it, and stay out of everything that is the run's: the reply its
// parent's report gives, and the tokens its turn used.
func TestANativeSubagentsEventsAreKeptApartFromItsRun(t *testing.T) {
	nest := func(e provider.Event) provider.Event { return provider.Nested{Parent: "task-1", Event: e} }
	ad := fake.New([]provider.Event{
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkStarted},
		nest(provider.TextDelta{Text: "The subagent's own words."}),
		nest(provider.Work{Item: "cmd-1", Kind: "command", Title: "go test ./...", Status: api.WorkCompleted}),
		nest(provider.TurnEnded{Outcome: api.OutcomeCompleted, Tokens: 7000}),
		provider.Work{Item: "task-1", Kind: "tool", Title: "Agent Find the bug", Status: api.WorkCompleted},
		provider.TextDelta{Text: "The bug is in the cache."},
		nest(provider.TextDelta{Text: "Said after the run's last words."}),
	})
	ad.Tokens = 1500
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, ad, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("find it", api.StartNow))
	until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	for _, e := range events(t, n.h, parent) {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok && r.Outcome != "The bug is in the cache." {
				t.Errorf("the report says %q, want the run's own last words", r.Outcome)
			}
		}
	}
	if cv := until(t, n.h, c, "the child's turn", idle); cv.Tokens != 1500 {
		t.Errorf("the child's turn used %d tokens, want its own 1500 without its subagent's", cv.Tokens)
	}
	var texts []string
	var work []api.SubagentWork
	var end []api.SubagentEnded
	for _, e := range events(t, n.h, c) {
		b, err := e.Decode()
		if err != nil {
			t.Fatal(err)
		}
		switch b := b.(type) {
		case api.SubagentDelta:
			if b.Parent == "task-1" {
				texts = append(texts, b.Text)
			}
		case api.SubagentWork:
			work = append(work, b)
		case api.SubagentEnded:
			end = append(end, b)
		}
	}
	if len(texts) != 2 || texts[0] != "The subagent's own words." {
		t.Errorf("the log has the subagent saying %q, want both its texts", texts)
	}
	if len(work) != 1 || work[0].Parent != "task-1" || work[0].Item != "cmd-1" || work[0].Status != api.WorkCompleted {
		t.Errorf("the log has the subagent's steps %+v, want its one command under task-1", work)
	}
	if len(end) != 1 || end[0].Tokens != 7000 || end[0].Outcome != api.OutcomeCompleted {
		t.Errorf("the log has the subagent ending %+v, want completed with its 7000 tokens", end)
	}
}

// A native subagent's text is redacted as a stream of its own: a value
// split across its deltas, or across the run's around one of its own, is
// masked, and its steps' fields are too.
func TestASubagentsTextIsRedactedAsAStreamOfItsOwn(t *testing.T) {
	for _, tc := range []struct {
		name                string
		script              []provider.Event
		wantRoot, wantChild string
	}{
		{"child split", []provider.Event{nested(provider.TextDelta{Text: "before " + probe[:8]}), provider.TextDelta{Text: "parent"}, nested(provider.TextDelta{Text: probe[8:] + " after"})}, "parent", "before [GLX_PROBE] after"},
		{"child split across its own step", []provider.Event{nested(provider.TextDelta{Text: "before " + probe[:8]}), nested(provider.Work{Item: "cmd", Kind: "command", Status: api.WorkCompleted}), nested(provider.TextDelta{Text: probe[8:] + " after"})}, "", "before [GLX_PROBE] after"},
		{"parent unbroken control", []provider.Event{provider.TextDelta{Text: "before " + probe[:8]}, provider.TextDelta{Text: probe[8:] + " after"}}, "before [GLX_PROBE] after", ""},
		{"parent split", []provider.Event{provider.TextDelta{Text: "before " + probe[:8]}, nested(provider.TextDelta{Text: "child"}), provider.TextDelta{Text: probe[8:] + " after"}}, "before [GLX_PROBE] after", "child"},
		{"complete fields", []provider.Event{nested(provider.TextDelta{Text: probe}), nested(provider.Work{Item: "cmd", Kind: "edit", Status: api.WorkFailed, Title: probe, Path: probe, Output: probe}), nested(provider.Work{Item: "success", Kind: "tool", Status: api.WorkCompleted, Output: probe})}, "", "[GLX_PROBE]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(dbPath(t))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			vault := secrets.New(t.TempDir())
			script := append([]provider.Event{provider.Work{Item: "spawn", Kind: "tool", Title: "spawn", Status: api.WorkStarted}}, tc.script...)
			h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(script)}, runtime.WithVault(vault))
			if err != nil {
				t.Fatal(err)
			}
			defer h.Kill()
			thread := newThread(t, h)
			if err := vault.Put(thread, map[string]string{"GLX_PROBE": probe}); err != nil {
				t.Fatal(err)
			}
			submit(t, h, thread, dispatch("go", api.StartNow))
			until(t, h, thread, "the turn", idle)
			var root, child strings.Builder
			work := map[string]api.SubagentWork{}
			for _, e := range events(t, h, thread) {
				b, err := e.Decode()
				if err != nil {
					t.Fatal(err)
				}
				switch v := b.(type) {
				case api.AssistantDelta:
					root.WriteString(v.Text)
				case api.SubagentDelta:
					child.WriteString(v.Text)
				case api.SubagentWork:
					work[v.Item] = v
				}
			}
			for name, s := range map[string]string{"root": root.String(), "child": child.String()} {
				if strings.Contains(s, probe) {
					t.Errorf("%s persisted and reconstructed the full test credential", name)
				}
			}
			for name, pair := range map[string][2]string{"root": {root.String(), tc.wantRoot}, "child": {child.String(), tc.wantChild}} {
				if !strings.Contains(pair[0], probe) && pair[0] != pair[1] {
					t.Errorf("%s output %q, want %q", name, pair[0], pair[1])
				}
			}
			if tc.name == "complete fields" {
				w, ok := work["cmd"]
				if !ok || w.Title != "[GLX_PROBE]" || w.Path != "[GLX_PROBE]" || w.Output != "[GLX_PROBE]" {
					t.Error("failed work did not mask its credential fields")
				}
				if w, ok := work["success"]; !ok || w.Output != "" {
					t.Error("successful work should not persist output")
				}
			}
		})
	}
}

func nested(e provider.Event) provider.Nested { return provider.Nested{Parent: "spawn", Event: e} }
