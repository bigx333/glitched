package runtime_test

import (
	"errors"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

func newChild(t *testing.T, ep api.Endpoint, parent api.ID, access api.Access) (api.ID, error) {
	t.Helper()
	id := api.NewID()
	_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: api.ThreadCreate{
		Workspace: "/src/app", Title: "child", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Access: access, Parent: parent,
	}})
	return id, err
}

// A child's finished run lands in its parent's log as a report: done, with
// the last paragraph of what it said, and the parent marked reported. The
// child keeps the tokens its turn used.
func TestAChildReportsBackToItsParent(t *testing.T) {
	ad := fake.New([]provider.Event{
		provider.TextDelta{Text: "Looking at the flicker.\n\n"},
		provider.TextDelta{Text: "Switched the terminal to the fit addon; resizing no longer flickers."},
	})
	ad.Tokens = 1500
	n := start(t, dbPath(t), ad)
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, c, dispatch("fix it", api.StartNow))
	v := until(t, n.h, parent, "the report", func(th api.Thread) bool { return !th.Reported.IsZero() })
	if v.Parent != (api.ID{}) {
		t.Errorf("the parent has a parent: %v", v.Parent)
	}
	var got *api.ChildReported
	for _, e := range events(t, n.h, parent) {
		if b, err := e.Decode(); err == nil {
			if r, ok := b.(api.ChildReported); ok {
				got = &r
			}
		}
	}
	want := "Switched the terminal to the fit addon; resizing no longer flickers."
	if got == nil || got.Child != c || got.Kind != api.ReportDone || got.Title != "child" || got.Outcome != want {
		t.Fatalf("the parent's report: %+v; want done, from the child, saying %q", got, want)
	}
	if cv := until(t, n.h, c, "the child's turn", idle); cv.Parent != parent || cv.Tokens != 1500 {
		t.Errorf("the child: parent %v, tokens %d; want %v, 1500", cv.Parent, cv.Tokens, parent)
	}
}

// The engine refuses a child wider than its parent, deeper than MaxDepth,
// or under a parent it doesn't have.
func TestTheEngineKeepsChildrenInBounds(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	parent := newThread(t, n.h) // Ask
	var rej *api.RejectedError
	if _, err := newChild(t, n.h, parent, api.AccessFull); !errors.As(err, &rej) {
		t.Errorf("a child wider than its parent: %v, want refused", err)
	}
	if _, err := newChild(t, n.h, api.NewID(), api.AccessAsk); !errors.As(err, &rej) {
		t.Errorf("a child of no thread: %v, want refused", err)
	}
	at := parent
	for depth := 1; depth <= api.MaxDepth; depth++ {
		next, err := newChild(t, n.h, at, api.AccessAsk)
		if err != nil {
			t.Fatalf("a child at depth %d: %v", depth, err)
		}
		at = next
	}
	if _, err := newChild(t, n.h, at, api.AccessAsk); !errors.As(err, &rej) {
		t.Errorf("a child below depth %d: %v, want refused", api.MaxDepth, err)
	}
}

// A child whose parent was deleted reports to no one, and its thread goes
// on: its next turn runs.
func TestAnOrphansReportGoesNowhere(t *testing.T) {
	n := start(t, dbPath(t), fake.New(fake.Turn(false)))
	parent := newThread(t, n.h)
	c, err := newChild(t, n.h, parent, api.AccessAsk)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, n.h, parent, api.ThreadDelete{})
	for i := range 2 {
		submit(t, n.h, c, dispatch("go", api.AfterActive))
		until(t, n.h, c, "the child's turn", func(th api.Thread) bool {
			return th.Run == api.RunIdle && th.LastTurn != nil && th.Queued == 0 && count(events(t, n.h, c), "turn.ended") == i+1
		})
	}
}
