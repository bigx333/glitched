package runtime_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

type lateOpen struct {
	*fake.Adapter
	opening, entered, cancelled, release chan struct{}
	interrupted                          chan error
	closing, closeRelease                chan struct{}
	blockOpen, failOpen                  bool
}
type lateOpenAgent struct {
	provider.Agent
	a *lateOpen
}

func (a *lateOpen) Open(ctx context.Context, s provider.SessionSpec) (provider.Agent, error) {
	ag, err := a.Adapter.Open(ctx, s)
	if err != nil {
		return nil, err
	}
	if a.blockOpen {
		close(a.opening)
		<-ctx.Done()
		close(a.cancelled)
		<-a.release
		if a.failOpen {
			_ = ag.Close(context.Background())
			return nil, ctx.Err()
		}
	}
	return lateOpenAgent{ag, a}, nil
}

func (a lateOpenAgent) Do(ctx context.Context, action provider.Action) error {
	switch action.(type) {
	case provider.Start:
		close(a.a.entered)
		if !a.a.blockOpen {
			<-ctx.Done()
			close(a.a.cancelled)
			<-a.a.release
		}
		return a.Agent.Do(context.Background(), action)
	case provider.Interrupt:
		a.a.interrupted <- ctx.Err()
	}
	return a.Agent.Do(ctx, action)
}

func (a lateOpenAgent) Close(ctx context.Context) error {
	if a.a.closing != nil {
		close(a.a.closing)
		<-a.a.closeRelease
	}
	return a.Agent.Close(ctx)
}

func newLateOpen() *lateOpen {
	return &lateOpen{
		Adapter: fake.New(fake.Turn(true)), opening: make(chan struct{}), entered: make(chan struct{}),
		cancelled: make(chan struct{}), release: make(chan struct{}), interrupted: make(chan error, 1),
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestADeletedRunsLateAgentIsClosedAfterAGracefulRestart(t *testing.T) {
	ad, before := newLateOpen(), &heard{}
	path := dbPath(t)
	n := awakeHost(t, path, ad, before)
	release := sync.OnceFunc(func() { close(ad.release) })
	defer release()
	thread := newThread(t, n.h)
	run := submit(t, n.h, thread, dispatch("first run", api.StartNow)).Command
	waitClosed(t, ad.entered, "Start")
	submit(t, n.h, thread, api.TurnInterrupt{})
	waitClosed(t, ad.cancelled, "Start cancellation")
	submit(t, n.h, thread, api.ThreadDelete{})
	if got := before.all(); !slices.Equal(got, []bool{true}) {
		t.Fatalf("awake during deleted Start = %v, want [true]", got)
	}
	done := make(chan struct{})
	go func() { n.stop(); close(done) }()
	waitClosed(t, n.h.Closing(), "host cancellation")
	release()
	waitClosed(t, done, "shutdown")
	after := &heard{}
	n2 := awakeHost(t, path, ad, after)
	untilClosed := time.Now().Add(5 * time.Second)
	for {
		closing, err := n2.st.Closing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(closing, thread) {
			break
		}
		if time.Now().After(untilClosed) {
			t.Fatal("close effect never drained after reboot")
		}
		time.Sleep(time.Millisecond)
	}
	id := provider.RuntimeIdentity{Thread: thread, Run: run}
	rec, err := ad.Reconcile(ctx, id)
	if r, ok := rec.(provider.Restored); ok {
		defer r.Agent.Close(ctx)
	}
	if _, lost := rec.(provider.Lost); err != nil || !lost {
		row, rowErr := n2.st.Thread(ctx, thread)
		leases, leaseErr := n2.st.Leased(ctx)
		m, _ := row.Message(run)
		t.Fatalf("restart drained the delete's CloseAgent effect, but agent is %T %v, message=%v generation=%d leases=%v awake after restart=%v (read errors %v %v)", rec, err, m.State, row.Generation, leases, after.all(), rowErr, leaseErr)
	}
}

func TestAStopWhileTheAgentOpensSendsNoPrompt(t *testing.T) {
	ad, heard := newLateOpen(), &heard{}
	ad.blockOpen = true
	n := awakeHost(t, dbPath(t), ad, heard)
	release := sync.OnceFunc(func() { close(ad.release) })
	defer release()
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("should never be sent", api.StartNow))
	waitClosed(t, ad.opening, "Open")
	submit(t, n.h, thread, api.TurnInterrupt{})
	waitClosed(t, ad.cancelled, "Open cancellation")
	release()
	heard.until(t, thread, "Stop ends", idle)
	select {
	case <-ad.entered:
		t.Fatalf("host called Start after Stop even though no prompt was in flight; provider saw %d Starts", ad.Starts())
	default:
	}
}
