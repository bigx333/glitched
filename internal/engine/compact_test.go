package engine_test

import (
	"errors"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
)

func TestIsAntigravityGeminiModel(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-3.8-flash":          true,
		"gemini-4-neon":             true,
		"GEMINI-2.5-PRO":            true,
		"gemini":                    true,
		"claude-opus-4-6-thinking":  false,
		"claude-sonnet-4-6":         false,
		"gpt-oss-120b":              false,
		"":                          false,
	} {
		if got := engine.IsAntigravityGeminiModel(model); got != want {
			t.Errorf("IsAntigravityGeminiModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func makeIdleThread(provider, model, session string, used int64, ended, cacheExpires time.Time) (engine.Thread, api.ID) {
	id, run := api.NewID(), api.NewID()
	ref := api.ModelRef{Provider: provider, Model: model}
	var th engine.Thread
	seq := uint64(0)
	step := func(at time.Time, b api.EventBody) {
		seq++
		th = engine.Apply(th, engine.Event{Thread: id, Seq: seq, At: at, Body: b})
	}
	start := ended.Add(-10 * time.Second)
	step(start, api.ThreadCreated{Workspace: "/w", Title: "t", Model: ref})
	step(start, api.MessageQueued{Message: run, Text: "work", Mode: api.StartNow})
	step(start, api.TurnAdmitted{Run: run, Generation: 1})
	step(start, api.TurnStarted{Run: run})
	if session != "" {
		step(start, api.SessionBound{Run: run, Session: session})
	}
	if used > 0 {
		step(start.Add(time.Second), api.ContextReported{Run: run, Used: used, Window: 1000000, Model: model, CacheExpires: cacheExpires})
	}
	step(ended, api.TurnEnded{Run: run, Outcome: api.OutcomeCompleted})
	return th, run
}

func TestIdleCompactWindowClaude(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	expires := base.Add(time.Hour)
	th, run := makeIdleThread("claude", "claude-opus-4-6", "sess-1", engine.MinIdleCompactTokens, base, expires)

	after, start, end, ok := engine.IdleCompactWindow(th, base.Add(time.Minute))
	if !ok || after != run || !start.Equal(expires.Add(-engine.ClaudeCompactLead)) || !end.Equal(expires) {
		t.Fatalf("claude window = (%v, %v, %v, %v), want (%v, %v, %v, true)", after, start, end, ok, run, expires.Add(-engine.ClaudeCompactLead), expires)
	}

	// Past expiry is ineligible.
	if _, _, _, ok := engine.IdleCompactWindow(th, expires); ok {
		t.Error("expired cache was eligible")
	}
	// Pure cache hit (no CacheExpires) is ineligible.
	noCache, _ := makeIdleThread("claude", "claude-opus-4-6", "sess-1", engine.MinIdleCompactTokens, base, time.Time{})
	if _, _, _, ok := engine.IdleCompactWindow(noCache, base.Add(time.Minute)); ok {
		t.Error("pure cache hit with zero CacheExpires was eligible")
	}
	// Below threshold is ineligible.
	small, _ := makeIdleThread("claude", "claude-opus-4-6", "sess-1", engine.MinIdleCompactTokens-1, base, expires)
	if _, _, _, ok := engine.IdleCompactWindow(small, base.Add(time.Minute)); ok {
		t.Error("thread below 200k tokens was eligible")
	}
	// Missing session is ineligible.
	noSess, _ := makeIdleThread("claude", "claude-opus-4-6", "", engine.MinIdleCompactTokens, base, expires)
	if _, _, _, ok := engine.IdleCompactWindow(noSess, base.Add(time.Minute)); ok {
		t.Error("thread without session was eligible")
	}
	// Stale context (already compacted) is ineligible.
	stale := th
	stale.Context.Stale = true
	if _, _, _, ok := engine.IdleCompactWindow(stale, base.Add(time.Minute)); ok {
		t.Error("stale context was eligible")
	}
	// Settled / snoozed / child threads are ineligible.
	settled := th
	settled.Disposition = api.Disposition{State: api.Settled}
	if _, _, _, ok := engine.IdleCompactWindow(settled, base.Add(time.Minute)); ok {
		t.Error("settled thread was eligible")
	}
	snoozed := th
	snoozed.Disposition = api.Disposition{State: api.Snoozed, Until: base.Add(2 * time.Hour)}
	if _, _, _, ok := engine.IdleCompactWindow(snoozed, base.Add(time.Minute)); ok {
		t.Error("snoozed thread was eligible")
	}
	child := th
	child.Parent = api.NewID()
	if _, _, _, ok := engine.IdleCompactWindow(child, base.Add(time.Minute)); ok {
		t.Error("child thread was eligible")
	}
	// Thread whose last message was manual /compact is ineligible.
	manual := th
	manual.Queue = append(append([]engine.Message(nil), th.Queue[:len(th.Queue)-1]...), engine.Message{ID: run, Text: "/compact", State: engine.QueueDispatched})
	if _, _, _, ok := engine.IdleCompactWindow(manual, base.Add(time.Minute)); ok {
		t.Error("thread whose last run was manual /compact was eligible")
	}
}

func TestIdleCompactWindowAntigravityGeminiOnly(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	gemini, run := makeIdleThread("antigravity", "gemini-3.8-flash", "ag-1", engine.MinIdleCompactTokens, base, time.Time{})
	after, start, end, ok := engine.IdleCompactWindow(gemini, base.Add(5*time.Minute))
	wantStart := base.Add(engine.AntigravityIdleDelay)
	wantEnd := wantStart.Add(engine.AntigravityGrace)
	if !ok || after != run || !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("antigravity gemini window = (%v, %v, %v, %v), want (%v, %v, %v, true)", after, start, end, ok, run, wantStart, wantEnd)
	}
	if _, _, _, ok := engine.IdleCompactWindow(gemini, wantEnd); ok {
		t.Error("antigravity gemini past grace window was eligible")
	}

	// Claude models and non-Gemini models on Antigravity must NOT be eligible.
	for _, model := range []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b", ""} {
		th, _ := makeIdleThread("antigravity", model, "ag-2", engine.MinIdleCompactTokens, base, time.Time{})
		if _, _, _, ok := engine.IdleCompactWindow(th, base.Add(5*time.Minute)); ok {
			t.Errorf("antigravity model %q was eligible, want excluded", model)
		}
	}

	// Switching a Gemini thread to Claude (or vice versa) excludes it until a Gemini turn completes.
	switched := gemini
	switched.Model.Model = "claude-sonnet-4-6"
	if _, _, _, ok := engine.IdleCompactWindow(switched, base.Add(5*time.Minute)); ok {
		t.Error("antigravity thread switched to claude was eligible")
	}
	fromClaude, _ := makeIdleThread("antigravity", "claude-sonnet-4-6", "ag-3", engine.MinIdleCompactTokens, base, time.Time{})
	fromClaude.Model.Model = "gemini-3.8-flash"
	if _, _, _, ok := engine.IdleCompactWindow(fromClaude, base.Add(5*time.Minute)); ok {
		t.Error("antigravity thread whose last run was claude was eligible")
	}
}

func TestIdleCompactSilentLifecycleAndUserQueueing(t *testing.T) {
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	expires := base.Add(time.Hour)
	th, firstRun := makeIdleThread("claude", "claude-opus-4-6", "sess-1", engine.MinIdleCompactTokens, base, expires)
	updatedBefore := th.UpdatedAt

	applyAll := func(evs []engine.Event) {
		for _, e := range evs {
			e.Seq = th.Revision + 1
			th = engine.Apply(th, e)
		}
	}

	// Refused before window opens.
	compactRun := api.NewID()
	if _, _, err := engine.Observe(th, engine.IdleCompact{Run: compactRun, AfterRun: firstRun}, expires.Add(-3*time.Minute)); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("early IdleCompact: %v, want ErrRefused", err)
	}
	// Refused with wrong AfterRun.
	if _, _, err := engine.Observe(th, engine.IdleCompact{Run: compactRun, AfterRun: api.NewID()}, expires.Add(-time.Minute)); !errors.Is(err, engine.ErrRefused) {
		t.Fatalf("stale AfterRun: %v, want ErrRefused", err)
	}

	// Accepted inside [expires-2m, expires).
	atCompact := expires.Add(-time.Minute)
	evs, effects, err := engine.Observe(th, engine.IdleCompact{Run: compactRun, AfterRun: firstRun}, atCompact)
	if err != nil {
		t.Fatalf("IdleCompact inside window: %v", err)
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %+v, want 1 StartTurn", effects)
	}
	st, ok := effects[0].(engine.StartTurn)
	if !ok || st.Run != compactRun || st.Text != "/compact" || !st.ResumeOnly {
		t.Fatalf("StartTurn = %+v, want Run=%v Text=/compact ResumeOnly=true", effects[0], compactRun)
	}
	applyAll(evs)

	// UpdatedAt must not move, and View marks Turn.Silent without changing LastTurn.
	if !th.UpdatedAt.Equal(updatedBefore) {
		t.Errorf("UpdatedAt moved to %v, want %v", th.UpdatedAt, updatedBefore)
	}
	v := engine.View(th)
	if v.Turn == nil || !v.Turn.Silent || v.LastTurn == nil || !v.LastTurn.Ended.Equal(base) {
		t.Fatalf("View during compaction: Turn=%+v LastTurn=%+v", v.Turn, v.LastTurn)
	}

	// Start the silent run.
	evs, _, err = engine.Observe(th, engine.Started{Run: compactRun}, atCompact.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(evs)

	// A user message sent with SteerActive during silent compaction queues behind it (AfterActive),
	// without emitting a Steer or Interrupt effect.
	userMsg := api.NewID()
	userAt := atCompact.Add(2 * time.Second)
	evs, effects, err = engine.Decide(th, api.Command{ID: userMsg, Target: th.ID, Body: api.MessageDispatch{Text: "next task", Mode: api.SteerActive}}, userAt)
	if err != nil {
		t.Fatalf("user SteerActive during silent compaction: %v", err)
	}
	if len(effects) != 0 {
		t.Fatalf("user message during silent compaction emitted effects %+v, want none (queued behind)", effects)
	}
	applyAll(evs)
	if m, ok := th.Message(userMsg); !ok || m.Mode != api.AfterActive || m.State != engine.QueueReady {
		t.Fatalf("user message state = %+v (ok=%v), want AfterActive QueueReady", m, ok)
	}

	// Compaction reports Compacted: true -> clears CacheExpires and marks Stale.
	evs, _, err = engine.Observe(th, engine.Context{Run: compactRun, Compacted: true, PreTokens: 250000, PostTokens: 32000}, atCompact.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	applyAll(evs)
	if !th.Context.Stale || !th.Context.CacheExpires.IsZero() {
		t.Errorf("context after compaction = %+v, want Stale=true CacheExpires=0", th.Context)
	}

	// Silent compaction ends -> queued user message is admitted immediately.
	evs, effects, err = engine.Observe(th, engine.Ended{Run: compactRun, Outcome: api.OutcomeCompleted}, atCompact.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var startedUser bool
	for _, ef := range effects {
		if st, ok := ef.(engine.StartTurn); ok && st.Run == userMsg && st.Text == "next task" && !st.ResumeOnly {
			startedUser = true
		}
	}
	if !startedUser {
		t.Fatalf("ending silent compaction emitted effects %+v, want StartTurn for queued user message", effects)
	}
	applyAll(evs)

	// LastTurn still points to firstRun's end (silent run excluded from LastTurn).
	if v := engine.View(th); v.LastTurn == nil || !v.LastTurn.Ended.Equal(base) {
		t.Errorf("LastTurn after silent run = %+v, want Ended=%v", v.LastTurn, base)
	}
}
