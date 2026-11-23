package daemon

import (
	"context"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// inhibitor stands in for the system's: it says true as it holds and
// false as it lets go.
type inhibitor chan bool

func (i inhibitor) hold(ctx context.Context) error {
	i <- true
	<-ctx.Done()
	i <- false
	return nil
}

func (i inhibitor) next(t *testing.T, want bool) {
	t.Helper()
	select {
	case got := <-i:
		if got != want {
			t.Fatalf("the inhibitor held %v, want %v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the inhibitor never went to %v", want)
	}
}

// The engine holds the machine awake while an agent works, lets go at once
// when the setting goes off and holds again when it comes back on, and
// lets go when it stops.
func TestTheEngineHoldsTheMachineAwakeWhileAnAgentWorks(t *testing.T) {
	held := fake.New(fake.Turn(false))
	held.HoldStart = true
	in := make(inhibitor, 4)
	d, err := Start(context.Background(), Config{Dir: stateDir(t), Hub: true, Providers: provider.Mux{"fake": held}, Inhibit: in.hold})
	if err != nil {
		t.Fatal(err)
	}
	ep, id := d.Endpoint(), api.NewID()
	for _, b := range []api.Intent{api.ThreadCreate{Workspace: "/src/app", Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}}, api.MessageDispatch{Text: "go", Mode: api.StartNow}} {
		if _, err := ep.Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	in.next(t, true)
	d.SetKeepAwake(false)
	in.next(t, false)
	d.SetKeepAwake(true)
	in.next(t, true)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	in.next(t, false)
}

// Once no agent works, the hold lasts its grace and then goes.
func TestTheHoldOutlastsTheWorkByItsGrace(t *testing.T) {
	in := make(inhibitor, 4)
	w := newAwake(in.hold, true)
	w.grace = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	w.work(true)
	in.next(t, true)
	start := time.Now()
	w.work(false)
	in.next(t, false)
	if took := time.Since(start); took < w.grace {
		t.Fatalf("let go after %v, before its grace of %v", took, w.grace)
	}
}
