package codex

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	fakeprovider "glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type gateOpen struct {
	*Adapter
	opened, stopped, release chan struct{}
	agent                    provider.Agent
}

func (a *gateOpen) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, s)
	if err == nil && s.Generation == 2 {
		a.agent = g
		close(a.opened)
		<-ctx.Done()
		close(a.stopped)
		<-a.release
	}
	return g, err
}

func TestAStopAsCodexOpensSendsNoPrompt(t *testing.T) {
	a, log := fake(t, "hi")
	gate := &gateOpen{Adapter: a, opened: make(chan struct{}), stopped: make(chan struct{}), release: make(chan struct{})}
	st, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	h, err := runtime.New(ctx, st, provider.Mux{Provider: gate})
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() { close(gate.release) })
	defer func() {
		release()
		h.Close()
		if gate.agent != nil {
			closeRun(t, gate.agent)
		}
	}()
	thread := api.NewID()
	submit := func(body api.Intent) {
		t.Helper()
		if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(ch <-chan struct{}, why string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", why)
		}
	}
	submit(api.ThreadCreate{Title: "review", Workspace: t.TempDir(), Access: api.AccessAsk, Model: api.ModelRef{Provider: Provider, Model: "gpt-6-luna"}})
	submit(api.MessageDispatch{Text: "first", Mode: api.StartNow})
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, err := st.Thread(ctx, thread)
		if err != nil {
			t.Fatal(err)
		}
		if row.Live() == nil && row.Session != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first turn never completed")
		}
		time.Sleep(time.Millisecond)
	}
	submit(api.MessageDispatch{Text: "stopped-second", Mode: api.StartNow})
	wait(gate.opened, "second Codex Open")
	if got := len(sent(t, log, "turn/start")); got != 1 {
		t.Fatalf("before Stop there are %d starts, want only the first turn", got)
	}
	submit(api.TurnInterrupt{})
	wait(gate.stopped, "cancellation of the second Open")
	release()
	untilOver(t, st, thread)
	if got := sent(t, log, "turn/start"); len(got) != 1 {
		t.Fatalf("Codex received %d turn/start RPCs, including %s after the host committed Stop", len(got), got[1])
	}
}

// untilOver waits for thread's run to be over.
func untilOver(t *testing.T, st *store.Store, thread api.ID) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		row, err := st.Thread(context.Background(), thread)
		if err != nil {
			t.Fatal(err)
		}
		if row.Live() == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never ended")
		}
	}
}

// busyAtStart opens agents that find Codex busy as the host gives them
// their prompt: lock takes the lock it holds, the agent's or the
// connection's, until release.
type busyAtStart struct {
	*Adapter
	lock                     func(*Agent) sync.Locker
	agent                    *Agent
	busy, cancelled, release chan struct{}
}
type busyAgent struct {
	*Agent
	ad *busyAtStart
}

func (a *busyAtStart) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, s)
	if err != nil {
		return nil, err
	}
	a.agent = g.(*Agent)
	return busyAgent{a.agent, a}, nil
}

func (g busyAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Start); ok {
		l := g.ad.lock(g.Agent)
		l.Lock()
		go func() {
			<-ctx.Done()
			close(g.ad.cancelled)
			<-g.ad.release
			l.Unlock()
		}()
		close(g.ad.busy)
	}
	return g.Agent.Do(ctx, act)
}

func TestAStopWhileCodexWaitsToSendSendsNoPrompt(t *testing.T) {
	for name, lock := range map[string]func(*Agent) sync.Locker{
		"agent":      func(g *Agent) sync.Locker { return &g.mu },
		"connection": func(g *Agent) sync.Locker { return &g.p.conn.mu },
	} {
		t.Run(name, func(t *testing.T) {
			a, log := fake(t, "hang")
			gate := &busyAtStart{Adapter: a, lock: lock, busy: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
			st, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			h, err := runtime.New(ctx, st, provider.Mux{Provider: gate}, runtime.WithIdleCompaction(false))
			if err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(func() { close(gate.release) })
			defer func() {
				release()
				h.Close()
				if gate.agent != nil {
					closeRun(t, gate.agent)
				}
			}()
			thread := api.NewID()
			submit := func(body api.Intent) {
				t.Helper()
				if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body}); err != nil {
					t.Fatal(err)
				}
			}
			wait := func(ch <-chan struct{}, why string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatalf("timed out waiting for %s", why)
				}
			}
			submit(api.ThreadCreate{Title: "review", Workspace: t.TempDir(), Access: api.AccessAsk, Model: api.ModelRef{Provider: Provider}})
			submit(api.MessageDispatch{Text: "unsent", Mode: api.StartNow})
			wait(gate.busy, "the host to give the prompt")
			submit(api.TurnInterrupt{})
			wait(gate.cancelled, "the Stop to cancel the start")
			release()
			untilOver(t, st, thread)
			if got := sent(t, log, "turn/start"); len(got) != 0 {
				t.Fatalf("Codex received %s after the host committed Stop", got)
			}
		})
	}
}

type gateStart struct {
	*Adapter
	taken, release chan struct{}
	agent          *Agent
}
type gatedStart struct {
	provider.Agent
	ad *gateStart
}

func (a *gateStart) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, s)
	if err != nil {
		return nil, err
	}
	a.agent = g.(*Agent)
	return gatedStart{g, a}, nil
}
func (a gatedStart) Do(ctx context.Context, act provider.Action) error {
	err := a.Agent.Do(ctx, act)
	if _, ok := act.(provider.Start); ok && err == nil {
		select {
		case <-a.ad.agent.announced:
		case <-ctx.Done():
			return ctx.Err()
		}
		close(a.ad.taken)
		<-a.ad.release
	}
	return err
}

type watchShutdown struct {
	*fakeprovider.Adapter
	next, stopped chan struct{}
}
type shutdownAgent struct {
	provider.Agent
	ad *watchShutdown
}

func (a *watchShutdown) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	g, err := a.Adapter.Open(ctx, s)
	return shutdownAgent{g, a}, err
}
func (a shutdownAgent) Next(ctx context.Context) (provider.Event, error) {
	close(a.ad.next)
	<-ctx.Done()
	close(a.ad.stopped)
	return nil, ctx.Err()
}

func TestADeletedRunsCodexIsClosedAfterAGracefulRestart(t *testing.T) {
	ctx := context.Background()
	a, log := fake(t, "hang")
	gate := &gateStart{Adapter: a, taken: make(chan struct{}), release: make(chan struct{})}
	watch := &watchShutdown{Adapter: fakeprovider.New(nil), next: make(chan struct{}), stopped: make(chan struct{})}
	mux := provider.Mux{Provider: gate, "fake": watch}
	path := filepath.Join(t.TempDir(), "runtime.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h, err := runtime.New(ctx, st, mux)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() { close(gate.release) })
	defer func() {
		release()
		h.Close()
		if gate.agent != nil {
			closeRun(t, gate.agent)
		}
	}()
	wait := func(ch <-chan struct{}, why string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", why)
		}
	}
	cmd := func(id api.ID, body api.Intent) {
		t.Helper()
		if _, err := h.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	start := func(name string) api.ID {
		t.Helper()
		id := api.NewID()
		cmd(id, api.ThreadCreate{Title: "review", Workspace: t.TempDir(), Access: api.AccessAsk, Model: api.ModelRef{Provider: name, Model: "gpt-6-luna"}})
		cmd(id, api.MessageDispatch{Text: "probe", Mode: api.StartNow})
		return id
	}
	start("fake")
	wait(watch.next, "shutdown sentinel's reader")
	id := start(Provider)
	wait(gate.taken, "Codex turn/started before host Start returned")
	if n := len(sent(t, log, "turn/start")); n != 1 {
		t.Fatalf("Codex saw %d prompts, want the first", n)
	}
	cmd(id, api.TurnInterrupt{})
	cmd(id, api.ThreadDelete{})
	shut := make(chan struct{})
	go func() { h.Close(); close(shut) }()
	wait(watch.stopped, "host shutdown cancels all run readers")
	release()
	wait(shut, "graceful shutdown")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	h2, err := runtime.New(ctx, st2, provider.Mux{Provider: gate, "fake": fakeprovider.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		closing, err := st2.Closing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(closing, id) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the deleted Codex turn's CloseAgent never drained")
		}
	}
	select {
	case <-gate.agent.p.reaped:
	default:
		pid := gate.agent.p.cmd.Process.Pid
		status, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if st := strings.TrimSpace(string(status)); err == nil && st != "" && !strings.HasPrefix(st, "Z") {
			t.Fatalf("delete CloseAgent drained, but the marked Codex subprocess PID %d is still live (ps stat %s) and received %d Interrupts", pid, st, len(sent(t, log, "turn/interrupt")))
		}
	}
}
