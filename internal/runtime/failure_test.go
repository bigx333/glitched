package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
)

// sideDB is a second connection to a host's database, for injecting faults
// the host can't see coming.
func sideDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func failEvents(t *testing.T, db *sql.DB, typ string) func() {
	exec(t, db, `CREATE TRIGGER fail BEFORE INSERT ON events WHEN NEW.type = '`+typ+`' BEGIN SELECT RAISE(ABORT, 'injected'); END`)
	return func() { exec(t, db, `DROP TRIGGER fail`) }
}

func TestStartedCommitIsRetriedNotTheStart(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(false))
	n := start(t, path, ad)
	heal := failEvents(t, sideDB(t, path), "turn.started")
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "the start reaches the agent", func(api.Thread) bool { return ad.Starts() == 1 })
	time.Sleep(50 * time.Millisecond)
	heal()

	until(t, n.h, thread, "turn end", idle)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || ad.Starts() != 1 {
		t.Fatalf("turn ended %v after %d starts, want completed after 1", got, ad.Starts())
	}
	assertReplay(t, n.st)
}

func TestPumpKeepsAnEventUntilItCommits(t *testing.T) {
	path := dbPath(t)
	n := start(t, path, fake.New(fake.Turn(true)))
	heal := failEvents(t, sideDB(t, path), "request.opened")
	thread := newThread(t, n.h)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	until(t, n.h, thread, "text ahead of the request", func(api.Thread) bool {
		return transcript(t, events(t, n.h, thread)) == "Looking at the code. May I edit it?"
	})
	time.Sleep(50 * time.Millisecond)
	heal()

	until(t, n.h, thread, "the request after the store recovers", waiting)
	respond(t, n.h, thread, until(t, n.h, thread, "approval", waiting).Requests[0])
	until(t, n.h, thread, "turn end", idle)
	assertReplay(t, n.st)
}

// breakThread makes the thread unreadable from the moment its next effect
// commits, so an effect fails before it reaches the provider. The returned
// func repairs it.
func breakThread(t *testing.T, db *sql.DB) func() {
	exec(t, db, `CREATE TRIGGER break AFTER INSERT ON effects BEGIN UPDATE threads SET model = '{' WHERE id = NEW.thread; END`)
	return func() {
		exec(t, db, `DROP TRIGGER IF EXISTS break`)
		exec(t, db, `UPDATE threads SET model = '{"provider":"fake","model":"fake"}'`)
	}
}

func TestAStartThatFailsBeforeTheProviderIsRetried(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(false))
	n := start(t, path, ad)
	db := sideDB(t, path)
	thread := newThread(t, n.h)
	repair := breakThread(t, db)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	exec(t, db, `DROP TRIGGER break`)
	time.Sleep(50 * time.Millisecond)
	repair()

	until(t, n.h, thread, "turn end", idle)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || ad.Starts() != 1 {
		t.Fatalf("turn ended %v after %d starts, want completed after 1", got, ad.Starts())
	}
}

func TestAStartThatNeverReachedTheProviderSurvivesARestart(t *testing.T) {
	path, ad := dbPath(t), fake.New(fake.Turn(false))
	n := start(t, path, ad)
	db := sideDB(t, path)
	thread := newThread(t, n.h)
	repair := breakThread(t, db)
	submit(t, n.h, thread, dispatch("go", api.StartNow))
	exec(t, db, `DROP TRIGGER break`)
	time.Sleep(50 * time.Millisecond)
	n.stop()
	repair()

	n = start(t, path, ad)
	until(t, n.h, thread, "turn end", idle)
	if got := outcomes(t, n.h, thread); len(got) != 1 || got[0] != api.OutcomeCompleted || ad.Starts() != 1 {
		t.Fatalf("turn ended %v after %d starts, want completed after 1", got, ad.Starts())
	}
	assertReplay(t, n.st)
}

// slowAgent hands over its first event, then thinks until its reader goes
// away. late makes its second Next return an event it pulled just as the
// reader went away.
type slowAgent struct {
	provider.Agent
	late  bool
	first chan struct{}
	mu    sync.Mutex
	calls int
}

func (a *slowAgent) Next(ctx context.Context) (provider.Event, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()
	if call == 1 {
		e, err := a.Agent.Next(ctx)
		close(a.first)
		return e, err
	}
	<-ctx.Done()
	if a.late && call == 2 {
		return a.Agent.Next(context.Background())
	}
	return nil, ctx.Err()
}

type slowAdapter struct {
	*fake.Adapter
	late  bool
	first chan struct{}
}

func (s *slowAdapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	a, err := s.Adapter.Open(ctx, spec)
	return &slowAgent{Agent: a, late: s.late, first: s.first}, err
}

func TestCloseCommitsWhatWasTakenFromTheAgent(t *testing.T) {
	for name, late := range map[string]bool{"buffered text": false, "an event pulled while closing": true} {
		t.Run(name, func(t *testing.T) {
			path := dbPath(t)
			ad := &slowAdapter{fake.New(fake.Turn(false)), late, make(chan struct{})}
			n := start(t, path, ad)
			thread := newThread(t, n.h)
			submit(t, n.h, thread, dispatch("go", api.StartNow))
			<-ad.first
			time.Sleep(5 * time.Millisecond) // inside the batch window
			n.stop()

			n = start(t, path, ad.Adapter)
			until(t, n.h, thread, "turn end", idle)
			if text := transcript(t, events(t, n.h, thread)); text != "Looking at the code. Done." {
				t.Fatalf("transcript after a restart %q, want everything the agent emitted", text)
			}
		})
	}
}

// closeCounter counts how many of its agents were closed. With die set,
// an agent's Next fails as if its process died.
type closeCounter struct {
	*fake.Adapter
	die    bool
	closes atomic.Int32
}

func (c *closeCounter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	ag, err := c.Adapter.Open(ctx, spec)
	return counted{ag, c}, err
}

type counted struct {
	provider.Agent
	c *closeCounter
}

func (a counted) Next(ctx context.Context) (provider.Event, error) {
	if a.c.die {
		return nil, errors.New("agent died")
	}
	return a.Agent.Next(ctx)
}

func (a counted) Close(ctx context.Context) error {
	a.c.closes.Add(1)
	return a.Agent.Close(ctx)
}

func TestAnEndThatNeverCommittedLeavesTheAgentToTheNextHost(t *testing.T) {
	t.Cleanup(runtime.SetCloseGrace(50 * time.Millisecond))
	for name, die := range map[string]bool{"the agent reports its end": false, "the agent dies": true} {
		t.Run(name, func(t *testing.T) {
			path, ad := dbPath(t), &closeCounter{Adapter: fake.New(fake.Turn(false)), die: die}
			n := start(t, path, ad)
			failEvents(t, sideDB(t, path), "turn.ended")
			thread := newThread(t, n.h)
			submit(t, n.h, thread, dispatch("go", api.StartNow))
			until(t, n.h, thread, "the run starts", func(th api.Thread) bool { return th.Run == api.RunRunning })
			if !die {
				until(t, n.h, thread, "everything before the end", func(api.Thread) bool {
					return transcript(t, events(t, n.h, thread)) == "Looking at the code. Done."
				})
			}
			time.Sleep(50 * time.Millisecond)
			n.stop()
			if got := ad.closes.Load(); got != 0 {
				t.Fatalf("the host closed %d agents whose end it never recorded, so the next host can't reconcile them", got)
			}
		})
	}
}
