package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/engine"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
	"glitched/internal/store"
)

const probe = "probe-value-0123"

var probeAsk = api.Credentials{Reason: "probe", Fields: []api.Field{{Name: "GLX_PROBE", Label: "Probe key"}}}

// vaulted is a host with a vault whose thread's turn waits on an approval,
// so a credentials request has a live run to belong to.
type vaulted struct {
	h      *runtime.Host
	vault  *secrets.Vault
	thread api.ID
	access api.Access
}

func startVaulted(t *testing.T) vaulted {
	t.Helper()
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.New(t.TempDir())
	h, err := runtime.New(ctx, st, provider.Mux{"fake": fake.New(fake.Turn(true))}, runtime.WithVault(vault))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	thread := newThread(t, h)
	submit(t, h, thread, dispatch("go", api.StartNow))
	th := until(t, h, thread, "the approval", waiting)
	return vaulted{h, vault, thread, th.Turn.Access}
}

type askResult struct {
	asked api.Asked
	err   error
}

// ask calls AskCredentials as glitched mcp does, and returns its pending
// request once the thread shows it.
func (v vaulted) ask(t *testing.T, c context.Context, call string) (api.Request, <-chan askResult) {
	t.Helper()
	done := make(chan askResult, 1)
	go func() {
		a, err := v.h.AskCredentials(c, api.Ask{Thread: v.thread, Access: v.access, Call: call, Credentials: probeAsk})
		done <- askResult{a, err}
	}()
	th := until(t, v.h, v.thread, "the credentials request", func(th api.Thread) bool {
		return slices.ContainsFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })
	})
	i := slices.IndexFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })
	return th.Requests[i], done
}

func (v vaulted) provide(r api.Request, values map[string]string) error {
	return v.h.ProvideCredentials(ctx, api.Provide{Thread: v.thread, Request: r.ID, Generation: r.Generation, Values: values})
}

func wait(t *testing.T, done <-chan askResult) askResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the ask never returned")
		return askResult{}
	}
}

// A value the user provides goes to the thread's vault, and the ask
// returns where it is; the log holds the request's names alone, and a
// retry of the same call gets the same answer without a second card.
func TestCredentialsProvided(t *testing.T) {
	v := startVaulted(t)
	r, done := v.ask(t, ctx, "call-1")
	if err := v.provide(r, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	got := wait(t, done)
	dir := v.vault.Dir(v.thread)
	if got.err != nil || got.asked.Status != api.AskProvided || !slices.Equal(got.asked.Names, []string{"GLX_PROBE"}) ||
		got.asked.Files["GLX_PROBE"] != filepath.Join(dir, "GLX_PROBE") || got.asked.Env != filepath.Join(dir, secrets.EnvScript) {
		t.Fatalf("the ask returned %+v, %v", got.asked, got.err)
	}
	if b, _ := os.ReadFile(got.asked.Files["GLX_PROBE"]); string(b) != probe {
		t.Errorf("the file holds %q", b)
	}
	evs := events(t, v.h, v.thread)
	for deadline := time.Now().Add(5 * time.Second); count(evs, "request.delivered") == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		evs = events(t, v.h, v.thread)
	}
	for _, e := range evs {
		if b, _ := json.Marshal(e); strings.Contains(string(b), probe) {
			t.Errorf("%s holds the value", e.Type)
		}
	}
	if count(evs, "request.answered") != 1 || count(evs, "request.delivered") != 1 {
		t.Errorf("answered %d, delivered %d", count(evs, "request.answered"), count(evs, "request.delivered"))
	}
	again, err := v.h.AskCredentials(ctx, api.Ask{Thread: v.thread, Access: v.access, Call: "call-1", Credentials: probeAsk})
	if err != nil || again.Status != api.AskProvided || count(events(t, v.h, v.thread), "request.opened") != 2 {
		t.Errorf("a retry got %+v, %v, with %d requests opened, want the same answer and no new card", again, err, count(events(t, v.h, v.thread), "request.opened"))
	}
}

// Dismissing the card declines the ask.
func TestCredentialsDeclined(t *testing.T) {
	v := startVaulted(t)
	r, done := v.ask(t, ctx, "call-1")
	submit(t, v.h, v.thread, api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{}`)})
	if got := wait(t, done); got.err != nil || got.asked.Status != api.AskDeclined {
		t.Fatalf("the ask returned %+v, %v, want declined", got.asked, got.err)
	}
	if len(v.vault.Values(v.thread)) != 0 {
		t.Error("a declined ask left values")
	}
}

// A provide that doesn't give exactly the names asked for, or a value no
// variable can hold, is refused and leaves the request waiting.
func TestCredentialsProvideRefusesAMisfit(t *testing.T) {
	v := startVaulted(t)
	r, done := v.ask(t, ctx, "call-1")
	for _, bad := range []map[string]string{
		{},
		{"OTHER": probe},
		{"GLX_PROBE": probe, "OTHER": probe},
		{"GLX_PROBE": ""},
		{"GLX_PROBE": "two\nlines"},
		{"GLX_PROBE": strings.Repeat("x", api.MaxSecret+1)},
	} {
		if err := v.provide(r, bad); err == nil {
			t.Errorf("a provide of %d values was taken", len(bad))
		}
	}
	if len(v.vault.Values(v.thread)) != 0 {
		t.Error("a refused provide left values")
	}
	if err := v.provide(r, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, done); got.asked.Status != api.AskProvided {
		t.Fatalf("the ask returned %+v, %v", got.asked, got.err)
	}
	if err := v.provide(r, map[string]string{"GLX_PROBE": probe}); !errors.Is(err, api.ErrAlreadyAnswered) {
		t.Errorf("a second provide got %v, want ErrAlreadyAnswered", err)
	}
}

// An answer that names what nobody provided, as an agent could submit
// itself, gets the agent nothing.
func TestCredentialsAnswerWithoutValuesProvidesNothing(t *testing.T) {
	v := startVaulted(t)
	r, done := v.ask(t, ctx, "call-1")
	submit(t, v.h, v.thread, api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"provided":["GLX_PROBE"]}`)})
	if got := wait(t, done); got.err == nil {
		t.Fatalf("the ask returned %+v, want an error", got.asked)
	}
	if _, err := v.h.Submit(ctx, api.Command{ID: api.NewID(), Target: v.thread, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation,
		Answer: json.RawMessage(`{"provided":["GLX_PROBE"],"values":{"GLX_PROBE":"x"}}`)}}); err == nil {
		t.Error("an answer carrying values was taken")
	}
}

// An answer is kept as the names it gives: whatever else its JSON held,
// as a value under a repeated key, is not.
func TestCredentialsAnswerIsKeptAsItsNames(t *testing.T) {
	v := startVaulted(t)
	r, done := v.ask(t, ctx, "call-1")
	submit(t, v.h, v.thread, api.RequestRespond{Request: r.ID, Generation: r.Generation,
		Answer: json.RawMessage(`{"provided":["` + probe + `"],"provided":[]}`)})
	if got := wait(t, done); got.err != nil || got.asked.Status != api.AskDeclined {
		t.Fatalf("the ask returned %+v, %v, want declined", got.asked, got.err)
	}
	b, _ := json.Marshal(events(t, v.h, v.thread))
	if strings.Contains(string(b), probe) {
		t.Errorf("the thread's events keep the value: %s", b)
	}
}

// Stopping the turn cancels the ask; an agent that stops waiting withdraws
// its card, and a provide after it is refused and keeps nothing.
func TestCredentialsEndWithTheTurnOrTheCall(t *testing.T) {
	v := startVaulted(t)
	_, done := v.ask(t, ctx, "call-1")
	submit(t, v.h, v.thread, api.TurnInterrupt{})
	if got := wait(t, done); got.err != nil || got.asked.Status != api.AskCanceled {
		t.Fatalf("the ask returned %+v, %v, want cancelled", got.asked, got.err)
	}

	v = startVaulted(t)
	c, cancel := context.WithCancel(ctx)
	r, done := v.ask(t, c, "call-1")
	cancel()
	if got := wait(t, done); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("the ask returned %+v, %v", got.asked, got.err)
	}
	until(t, v.h, v.thread, "the card gone", func(th api.Thread) bool { return len(th.Requests) == 1 })
	if err := v.provide(r, map[string]string{"GLX_PROBE": probe}); err == nil {
		t.Error("a provide for a withdrawn card was taken")
	}
	if len(v.vault.Values(v.thread)) != 0 {
		t.Error("a refused provide left values")
	}
}

// An ask nobody answers in time withdraws its card.
func TestCredentialsTimeOut(t *testing.T) {
	defer runtime.SetAskTimeout(100 * time.Millisecond)()
	v := startVaulted(t)
	_, done := v.ask(t, ctx, "call-1")
	if got := wait(t, done); got.err != nil || got.asked.Status != api.AskTimedOut {
		t.Fatalf("the ask returned %+v, %v, want timed out", got.asked, got.err)
	}
	until(t, v.h, v.thread, "the card gone", func(th api.Thread) bool { return len(th.Requests) == 1 })
}

// Only an agent started at the live turn's access may ask, and only for a
// request a card can show.
func TestCredentialsAskRefusals(t *testing.T) {
	v := startVaulted(t)
	for name, a := range map[string]api.Ask{
		"another access": {Thread: v.thread, Access: v.access + 1, Call: "c", Credentials: probeAsk},
		"no fields":      {Thread: v.thread, Access: v.access, Call: "c", Credentials: api.Credentials{Reason: "r"}},
		"a bad name":     {Thread: v.thread, Access: v.access, Call: "c", Credentials: api.Credentials{Reason: "r", Fields: []api.Field{{Name: "A-B"}}}},
		"an http url":    {Thread: v.thread, Access: v.access, Call: "c", Credentials: api.Credentials{Reason: "r", Fields: []api.Field{{Name: "A", URL: "http://x"}}}},
		"an env file":    {Thread: v.thread, Access: v.access, Call: "c", Credentials: api.Credentials{Reason: "r", Fields: []api.Field{{Name: "A"}}, EnvFile: "../.env"}},
	} {
		if _, err := v.h.AskCredentials(ctx, a); err == nil {
			t.Errorf("an ask with %s was taken", name)
		}
	}
}

// A new value replaces the thread's old one; deleting the thread deletes
// its vault.
func TestCredentialsReplaceAndGoWithTheThread(t *testing.T) {
	v := startVaulted(t)
	for i, value := range []string{probe, "second-value-456"} {
		r, done := v.ask(t, ctx, "call-"+string(rune('a'+i)))
		if err := v.provide(r, map[string]string{"GLX_PROBE": value}); err != nil {
			t.Fatal(err)
		}
		wait(t, done)
		if got := v.vault.Values(v.thread)["GLX_PROBE"]; got != value {
			t.Fatalf("the vault holds %q, want %q", got, value)
		}
	}
	submit(t, v.h, v.thread, api.ThreadDelete{})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(v.vault.Dir(v.thread)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted thread's vault is still there")
		}
	}
}

// A context report's model label, which the provider writes, is masked
// as its text is: neither the event log nor the thread shows a credential.
func TestAContextReportKeepsCredentialsOut(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := secrets.New(t.TempDir())
	ad := fake.New([]provider.Event{
		provider.Context{Used: 10, Window: 100, Model: probe},
		provider.TextDelta{Text: probe},
	})
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(vault))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Kill()
	thread := newThread(t, h)
	if err := vault.Put(thread, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	submit(t, h, thread, dispatch("go", api.StartNow))
	v := until(t, h, thread, "the turn", idle)
	if v.Context == nil || v.Context.Used != 10 {
		t.Fatalf("no context report: %+v", v.Context)
	}
	for _, e := range events(t, h, thread) {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), probe) {
			t.Errorf("%s holds the credential", e.Type)
		}
	}
	if strings.Contains(v.Context.Model, probe) {
		t.Errorf("the thread's context model holds the credential: %q", v.Context.Model)
	}
}

// A plan's steps are shown redacted of the thread's credentials, and a
// plan between two halves of one in the text keeps its start held.
func TestAPlanKeepsCredentialsOut(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := secrets.New(t.TempDir())
	ad := fake.New([]provider.Event{
		provider.TextDelta{Text: "Exporting " + probe[:8]},
		provider.Plan{Steps: []api.PlanStep{{Text: "Export GLX_PROBE=" + probe, Status: api.PlanActive}, {Text: "Deploy"}}},
		provider.TextDelta{Text: probe[8:] + " now."},
	})
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(vault))
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
	evs := events(t, h, thread)
	if text := transcript(t, evs); strings.Contains(text, probe) || !strings.HasPrefix(text, "Exporting ") {
		t.Errorf("the transcript %q, want the credential redacted", text)
	}
	var plans []api.PlanUpdated
	for _, e := range evs {
		if b, _ := e.Decode(); e.Type == "plan.updated" {
			plans = append(plans, b.(api.PlanUpdated))
		}
	}
	if len(plans) != 1 || len(plans[0].Steps) != 2 || strings.Contains(plans[0].Steps[0].Text, probe) ||
		!strings.HasPrefix(plans[0].Steps[0].Text, "Export GLX_PROBE=") || plans[0].Steps[1] != (api.PlanStep{Text: "Deploy"}) {
		t.Fatalf("the log's plans %+v, want the one plan with the credential redacted", plans)
	}
}

// quickAsker is a provider whose agent asks for credentials through
// glitched mcp before its Open returns, as a quick agent may while the
// host is still recording the run's start.
type quickAsker struct {
	provider.Adapter
	h       *runtime.Host
	asked   chan askResult
	waiting chan struct{}
	// returned is closed once the ask Open started has returned.
	returned chan struct{}
}

func (q *quickAsker) Open(c context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	go func() {
		a, err := q.h.AskCredentials(ctx, api.Ask{Thread: spec.Thread, Access: spec.Access, Call: "call-1", Credentials: probeAsk})
		q.asked <- askResult{a, err}
		close(q.returned)
	}()
	select {
	case <-q.waiting:
	case <-q.returned:
	}
	return q.Adapter.Open(c, spec)
}

// An ask that comes while the run's start is still being recorded waits
// for it, and opens its card, rather than being refused.
func TestCredentialsAskedBeforeTheStartIsRecordedWait(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.New(t.TempDir())
	q := &quickAsker{Adapter: fake.New(fake.Turn(true)), asked: make(chan askResult, 1), waiting: make(chan struct{}), returned: make(chan struct{})}
	t.Cleanup(runtime.SetAwaitingStart(func() { close(q.waiting) }))
	h, err := runtime.New(ctx, st, provider.Mux{"fake": q}, runtime.WithVault(vault))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	q.h = h
	v := vaulted{h: h, vault: vault, thread: newThread(t, h)}
	submit(t, h, v.thread, dispatch("go", api.StartNow))
	th := until(t, h, v.thread, "the credentials request", func(th api.Thread) bool {
		return slices.ContainsFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })
	})
	r := th.Requests[slices.IndexFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })]
	if err := v.provide(r, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	if got := wait(t, q.asked); got.err != nil || got.asked.Status != api.AskProvided {
		t.Fatalf("the ask returned %+v, %v", got.asked, got.err)
	}
}

// An ask still waiting for its run's start gives up at the ask's own
// timeout, as one waiting for its answer does.
func TestCredentialsAskWaitingForTheStartTimesOut(t *testing.T) {
	t.Cleanup(runtime.SetAskTimeout(50 * time.Millisecond))
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	q := &quickAsker{Adapter: fake.New(fake.Turn(true)), asked: make(chan askResult, 1), waiting: make(chan struct{}), returned: make(chan struct{})}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": q}, runtime.WithVault(secrets.New(t.TempDir())))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	q.h = h
	thread := newThread(t, h)
	submit(t, h, thread, dispatch("go", api.StartNow))
	if got := wait(t, q.asked); got.err != nil || got.asked.Status != api.AskTimedOut {
		t.Fatalf("the ask returned %+v, %v, want it timed out", got.asked, got.err)
	}
}

// A child that answers to its parent asks it for secrets, not the user:
// the engine refuses its ask. One the user took over asks them, as a
// thread of theirs does.
func TestCredentialsAChildAsksItsParent(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ad := fake.New(fake.Turn(true))
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(secrets.New(t.TempDir())))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	parent := newThread(t, h)
	kid, err := newChild(t, h, ad, parent, api.AccessAsk, fake.Child{Title: "deploy", Task: "go"})
	if err != nil {
		t.Fatal(err)
	}
	th := until(t, h, kid, "the child's approval", waiting)
	ask := api.Ask{Thread: kid, Access: th.Turn.Access, Call: "c", Credentials: probeAsk}
	if _, err := h.AskCredentials(ctx, ask); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("a child's ask: %v; want it refused, for its parent to ask", err)
	}
	submit(t, h, kid, dispatch("mine now", api.AfterActive))
	until(t, h, kid, "the takeover", func(th api.Thread) bool { return th.TakenOver })
	c, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := h.AskCredentials(c, ask); done <- err }()
	until(t, h, kid, "the taken-over child's request", func(th api.Thread) bool {
		return slices.ContainsFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })
	})
	cancel()
	<-done
}

// A linked child (Origin != nil, Parent zero) also answers to its parent
// until taken over: AskCredentials is refused before takeover and opens
// once the user takes it over.
func TestCredentialsALinkedChildAsksItsParent(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	linkID := api.NewID()
	st.SetLinks(func(id api.ID) (api.Link, error) {
		if id == linkID {
			return api.Link{ID: linkID, Access: api.AccessFull}, nil
		}
		return api.Link{}, api.ErrNotFound
	})
	linked, ws := api.NewID(), t.TempDir()
	if _, err := st.Submit(ctx, api.Command{ID: api.NewID(), Target: linked, Body: api.ThreadCreate{
		Title: "remote worker", Model: api.ModelRef{Provider: "fake", Model: "fake"},
		Access: api.AccessAsk, Text: "go",
		Origin:     &api.Origin{Engine: api.NewID(), Thread: api.NewID(), Title: "parent", Depth: 1, Access: api.AccessFull, Link: linkID},
		Repository: &api.Repository{Root: "root", Branch: "main"},
		Project:    ws, Worktree: &api.NewWorktree{Base: "abc"},
	}}, time.Now()); err != nil {
		st.Close()
		t.Fatal(err)
	}
	eff, ok, err := st.Lease(ctx, nil)
	if err != nil || !ok {
		st.Close()
		t.Fatalf("lease worktree.make: ok=%v err=%v", ok, err)
	}
	if _, err := st.Observe(ctx, linked, engine.WorktreeReady{Workspace: ws, Branch: "glitched/x"}, time.Now(), eff.ID); err != nil {
		st.Close()
		t.Fatal(err)
	}
	ad := fake.New(fake.Turn(true))
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(secrets.New(t.TempDir())))
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Kill(); st.Close() })
	th := until(t, h, linked, "the linked child's approval", waiting)
	if th.UserOwned() {
		t.Fatalf("untaken linked child reported UserOwned=true: %+v", th)
	}
	ask := api.Ask{Thread: linked, Access: th.Turn.Access, Call: "c", Credentials: probeAsk}
	if _, err := h.AskCredentials(ctx, ask); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("a linked child's ask: %v; want it refused, for its parent to ask", err)
	}
	submit(t, h, linked, dispatch("mine now", api.AfterActive))
	until(t, h, linked, "the takeover", func(th api.Thread) bool { return th.TakenOver && th.UserOwned() })
	c, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := h.AskCredentials(c, ask); done <- err }()
	until(t, h, linked, "the taken-over linked child's request", func(th api.Thread) bool {
		return slices.ContainsFunc(th.Requests, func(r api.Request) bool { return r.Kind == api.KindCredentials })
	})
	cancel()
	<-done
}

// A parent's vault secrets are redacted from its child's output streams,
// work items, and surfaced requests, so handing a child a parent secret's
// file path never leaks the value into the child's log.
func TestCredentialsParentSecretsAreRedactedInChildOutput(t *testing.T) {
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vault := secrets.New(t.TempDir())
	childEvents := []provider.Event{
		provider.TextDelta{Text: "token=" + probe[:8]},
		provider.TextDelta{Text: probe[8:] + " done"},
		provider.Work{Item: "cmd-1", Kind: "command", Title: "curl -H " + probe, Status: api.WorkFailed, Output: "leaked " + probe},
	}
	ad := fake.New(childEvents)
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithVault(vault))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Kill()
	parent := newThread(t, h)
	if err := vault.Put(parent, map[string]string{"GLX_PROBE": probe}); err != nil {
		t.Fatal(err)
	}
	kid, err := newChild(t, h, ad, parent, api.AccessAsk, fake.Child{Title: "deploy", Task: "go"})
	if err != nil {
		t.Fatal(err)
	}
	until(t, h, kid, "the child turn", idle)
	evs := events(t, h, kid)
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), probe) {
			t.Errorf("child event %s leaked parent secret: %s", e.Type, b)
		}
	}
	if text := transcript(t, evs); !strings.Contains(text, "[GLX_PROBE]") {
		t.Errorf("child transcript = %q, want [GLX_PROBE]", text)
	}
}
