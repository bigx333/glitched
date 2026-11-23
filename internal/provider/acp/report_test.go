package acp

import (
	"context"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

// reports are the run's Granted and Ran, in order.
func reports(events []provider.Event) []provider.Event {
	return slices.DeleteFunc(slices.Clone(events), func(e provider.Event) bool {
		switch e.(type) {
		case provider.Granted, provider.Ran:
			return false
		}
		return true
	})
}

// reported is one run's turn's reports, each checked to come before its
// first text, and the agent's pid.
func reported(t *testing.T, a *Adapter, s provider.SessionSpec) ([]provider.Event, int) {
	t.Helper()
	g := open(t, a, s)
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	first := slices.IndexFunc(events, func(e provider.Event) bool { _, ok := e.(provider.TextDelta); return ok })
	for i, e := range events {
		if len(reports([]provider.Event{e})) > 0 && i > first {
			t.Errorf("%+v came after the turn's first text", e)
		}
	}
	return reports(events), g.(*Agent).p.cmd.Process.Pid
}

// Each access level is reported as the session's mode makes it, and the
// model and effort as the session's options show them, before the turn's
// first text. Full access is the flag agy started with; its mode is the
// session's own.
func TestARunReportsWhatItsAgentConfirmed(t *testing.T) {
	for _, tc := range []struct {
		access api.Access
		mode   string
	}{
		{api.AccessAsk, "default"},
		{api.AccessAutoEdits, "auto"},
		{api.AccessFull, "default"},
	} {
		a, _ := fake(t, "hi")
		s := spec(t)
		s.Access, s.Model.Model, s.Model.Effort = tc.access, "fake-pro", "low"
		got, _ := reported(t, a, s)
		want := []provider.Event{provider.Granted{Access: tc.access, Mode: tc.mode}, provider.Ran{Model: "fake-pro", Effort: "low"}}
		if !slices.Equal(got, want) {
			t.Errorf("access %v: reported %+v, want %+v", tc.access, got, want)
		}
	}
}

// Approve for me in a session without agy's auto mode is reported in the
// mode it fell back to, with why.
func TestApproveForMeFallingBackReportsTheFallback(t *testing.T) {
	t.Setenv(acptest.Offered, "default,accept-edits,plan")
	a, _ := fake(t, "hi")
	s := spec(t)
	s.Access = api.AccessAutoEdits
	got, _ := reported(t, a, s)
	want := provider.Granted{Access: api.AccessAutoEdits, Mode: "accept-edits", Why: "Antigravity doesn't offer its auto mode"}
	if len(got) == 0 || got[0] != want {
		t.Fatalf("reported %+v, want %+v first", got, want)
	}
}

// A thread that moves from Ask for approval to Approve for me and back
// reports, on each run, the mode agy says it moved to, the fallback's
// reason included, while the conversation goes on.
func TestEachAccessChangeReportsItsMode(t *testing.T) {
	for offered, auto := range map[string]provider.Granted{
		"":                          {Access: api.AccessAutoEdits, Mode: "auto"},
		"default,accept-edits,plan": {Access: api.AccessAutoEdits, Mode: "accept-edits", Why: "Antigravity doesn't offer its auto mode"},
	} {
		t.Setenv(acptest.Offered, offered)
		a, _ := fake(t, "memory")
		s := spec(t)
		ask := provider.Granted{Access: api.AccessAsk, Mode: "default"}
		for i, step := range []struct {
			access api.Access
			want   provider.Granted
		}{{api.AccessAsk, ask}, {api.AccessAutoEdits, auto}, {api.AccessAsk, ask}} {
			s.Access, s.Run, s.Generation = step.access, api.NewID(), s.Generation+1
			g := open(t, a, s)
			s.Session = g.(provider.Sessioned).Session()
			do(t, g, provider.Start{Text: "hi"})
			got := reports(turn(t, g))
			closeRun(t, g)
			if len(got) == 0 || got[0] != step.want {
				t.Errorf("offered %q, run %d: reported %+v, want %+v first", offered, i+1, got, step.want)
			}
		}
	}
}

// The thread's next run reports what it changed: a model on the agent the
// thread kept, an access level on the new agent it starts with; a run that
// changed nothing reports again.
func TestTheNextRunReportsItsChange(t *testing.T) {
	a, _ := fake(t, "hi")
	s := spec(t)
	steps := []struct {
		change func()
		want   []provider.Event
		kept   bool
	}{
		{func() {}, []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"}, provider.Ran{Model: "fake-pro", Effort: "high"}}, false},
		{func() {}, []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"}, provider.Ran{Model: "fake-pro", Effort: "high"}}, true},
		{func() { s.Model.Model = "fake-flash" }, []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"}, provider.Ran{Model: "fake-flash"}}, true},
		{func() { s.Access = api.AccessAutoEdits }, []provider.Event{provider.Granted{Access: api.AccessAutoEdits, Mode: "auto"}, provider.Ran{Model: "fake-flash"}}, false},
	}
	last := 0
	for i, step := range steps {
		step.change()
		s.Run, s.Generation = api.NewID(), s.Generation+1
		got, pid := reported(t, a, s)
		if !slices.Equal(got, step.want) {
			t.Errorf("run %d: reported %+v, want %+v", i+1, got, step.want)
		}
		if kept := pid == last; kept != step.kept {
			t.Errorf("run %d: ran on the kept agent %v, want %v", i+1, kept, step.kept)
		}
		last = pid
	}
}

// What agy runs other than asked is reported as it runs it, with why: a
// model it substituted, an effort the model has no level for, and what the
// session moved to mid-turn. A model it refuses fails the run, so nothing
// reports it.
func TestAnAgentThatRunsOtherwiseIsReportedSo(t *testing.T) {
	for _, tc := range []struct {
		scenario, model, effort string
		want                    []provider.Event
	}{
		{"substitute-model", "fake-flash", "", []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"},
			provider.Ran{Model: "fake-pro", Effort: "high", Why: "Antigravity runs fake-pro"}}},
		{"hi", "fake-flash", "high", []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"},
			provider.Ran{Model: "fake-flash", Why: "Antigravity offers no effort level for fake-flash"}}},
		{"switch-mid-turn", "fake-pro", "", []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"}, provider.Ran{Model: "fake-pro", Effort: "high"},
			provider.Granted{Access: api.AccessAsk, Mode: "plan", Why: "Antigravity runs in its plan mode"},
			provider.Ran{Model: "fake-flash", Why: "Antigravity runs fake-flash"}}},
	} {
		a, _ := fake(t, tc.scenario)
		s := spec(t)
		s.Model.Model, s.Model.Effort = tc.model, tc.effort
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		if got := reports(turn(t, g)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: reported %+v, want %+v", tc.scenario, got, tc.want)
		}
		closeRun(t, g)
	}
	a, _ := fake(t, "reject-model")
	s := spec(t)
	s.Model.Model = "fake-flash"
	if g, err := a.Open(context.Background(), s); err == nil {
		closeRun(t, g)
		t.Fatal("a run opened on a model agy refused")
	}
}

// A session that lists no modes and no options confirms nothing of them,
// and nothing is reported but the flag Full access started agy with.
func TestAnAgentThatSaysNothingIsNotReported(t *testing.T) {
	for _, tc := range []struct {
		access api.Access
		want   []provider.Event
	}{
		{api.AccessAsk, nil},
		{api.AccessFull, []provider.Event{provider.Granted{Access: api.AccessFull}}},
	} {
		a, _ := fake(t, "silent")
		s := spec(t)
		s.Access = tc.access
		if got, _ := reported(t, a, s); !slices.Equal(got, tc.want) {
			t.Errorf("access %v: reported %+v, want %+v", tc.access, got, tc.want)
		}
	}
}
