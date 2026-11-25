package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/transport"
)

// A setting the first engine embedded here applies as it runs reaches it
// when saved as it starts, after it read engine.json: it applies it, with
// no restart that would cut off the work it started meanwhile.
func TestASettingSavedAsTheFirstEngineStartsReachesIt(t *testing.T) {
	dir := stateDir(t)
	off, on := false, true
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.KeepAwake = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	g := &gui{wake: make(chan struct{}, 1)}
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	holds := make(chan bool, 4)
	read, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	starts := 0
	m := New(Config{Dir: dir, Post: g.post, Engine: func(cfg *daemon.Config) {
		cfg.Providers = provider.Mux{"fake": held}
		cfg.Inhibit = func(ctx context.Context) error {
			holds <- true
			<-ctx.Done()
			holds <- false
			return nil
		}
		mu.Lock()
		starts++
		first := starts == 1
		mu.Unlock()
		if first {
			close(read)
			<-release
		}
	}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	<-read
	if err := m.Configure(func(c *daemon.Config) { c.KeepAwake = &on }); err != nil {
		t.Fatal(err)
	}
	close(release)
	g.until(t, "the engine embedded", func() bool { return m.embedded != nil })
	ep := transport.NewEndpointClient(daemon.Socket(dir), api.ID{})
	t.Cleanup(func() { ep.Close() })
	id := api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/src/app", Title: "busy", Model: api.ModelRef{Provider: "fake", Model: "fake"}}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-holds:
	case <-time.After(10 * time.Second):
		t.Fatal("the engine never held the machine awake for the agent at work")
	}
	g.drain()
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("the engine started %d times, want once", starts)
	}
}

// serviceEngine starts the engine a background service's glitched serve
// -wait-lock would: once no other engine holds dir, within a few seconds.
func serviceEngine(t *testing.T, dir string) *daemon.Daemon {
	t.Helper()
	// The engine lasts as long as ctx.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	late := time.AfterFunc(5*time.Second, cancel)
	defer late.Stop()
	d, err := daemon.StartWhenFree(ctx, func() (daemon.Config, error) {
		return daemon.Config{Dir: dir, Hub: true, Providers: daemon.FakeProviders(fake.New(nil))}, nil
	})
	if err != nil {
		t.Fatalf("the service's engine never got the directory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// With a background service for the directory, the app waits for the
// service's engine as it restarts rather than embed one in the gap: the
// service keeps the directory, so closing the app ends no run.
func TestALocalEnvWaitsForTheServicesEngine(t *testing.T) {
	dir := stateDir(t)
	first := serviceEngine(t, dir)
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{Dir: dir, Post: g.post, Served: func(context.Context) bool { return true }})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	online := func() bool { return m.envs[0].Status == Status(Online{}) }
	g.until(t, "the app attached to the service's engine", func() bool { return online() && !m.machine.Embedded })

	first.Close()           // the service restarts, as after a crash
	time.Sleep(time.Second) // the service's restart delay
	embedded, seen := false, false
	g.post(func() { seen = true })
	g.until(t, "what the app did meanwhile", func() bool {
		embedded = embedded || m.embedded != nil
		return seen
	})
	if embedded {
		t.Fatal("the app embedded an engine while the service restarted")
	}
	serviceEngine(t, dir)
	g.until(t, "the app attached to the restarted service", func() bool { return online() && !m.machine.Embedded })
}

// When the service's engine does not come, the app embeds one after all,
// rather than leave the directory unserved.
func TestALocalEnvEmbedsWhenTheServiceNeverAnswers(t *testing.T) {
	was := serviceWait
	serviceWait = 300 * time.Millisecond
	t.Cleanup(func() { serviceWait = was })
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{Dir: stateDir(t), Post: g.post, Served: func(context.Context) bool { return true }})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	g.until(t, "the engine embedded", func() bool { return m.machine.Embedded && m.envs[0].Status == Status(Online{}) })
}
