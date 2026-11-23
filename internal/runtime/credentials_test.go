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

// quickAsker is a provider whose agent asks for credentials through
// glitched mcp before its Open returns, as a quick agent may while the
// host is still recording the run's start.
type quickAsker struct {
	provider.Adapter
	h     *runtime.Host
	asked chan askResult
}

func (q *quickAsker) Open(c context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	go func() {
		a, err := q.h.AskCredentials(ctx, api.Ask{Thread: spec.Thread, Access: spec.Access, Call: "call-1", Credentials: probeAsk})
		q.asked <- askResult{a, err}
	}()
	select {
	case r := <-q.asked:
		q.asked <- r
	case <-time.After(time.Second):
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
	q := &quickAsker{Adapter: fake.New(fake.Turn(true)), asked: make(chan askResult, 1)}
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
