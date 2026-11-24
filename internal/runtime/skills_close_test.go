package runtime_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// closingProvider holds the close of thread's agent, its reconcile, till
// release closes: closing closes as it starts.
type closingProvider struct {
	*skilled
	thread  api.ID
	closing chan struct{}
	release chan struct{}
}

func (a closingProvider) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if id.Thread == a.thread {
		select {
		case <-a.closing:
		default:
			close(a.closing)
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.skilled.Reconcile(ctx, id)
}

// A deleted thread's agent keeps its skills till its provider has closed
// it, whatever another thread's run sweeps meanwhile, and through a
// restart with the close still to do.
func TestADeletedThreadsAgentKeepsItsSkillsTillItCloses(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "restarted"}[restart], func(t *testing.T) {
			state, first, other := t.TempDir(), t.TempDir(), t.TempDir()
			skillAt(t, filepath.Join(first, ".glitched", "skills"), "first")
			skillAt(t, filepath.Join(other, ".glitched", "skills"), "other")
			a := closingProvider{newSkilled(), api.NewID(), make(chan struct{}), make(chan struct{})}
			path := dbPath(t)
			startHost := func() node {
				t.Helper()
				st, err := store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				h, err := runtime.New(ctx, st, provider.Mux{"fake": a}, runtime.WithSkills(state))
				if err != nil {
					st.Close()
					t.Fatal(err)
				}
				n := node{st, h}
				t.Cleanup(n.kill)
				return n
			}
			n := startHost()
			defer close(a.release)
			turn := func(h *runtime.Host, id api.ID, ws string) {
				t.Helper()
				submit(t, h, id, api.ThreadCreate{Workspace: ws, Title: "test", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
				submit(t, h, id, dispatch("go", api.StartNow))
				until(t, h, id, "turn end", idle)
			}
			turn(n.h, a.thread, first)
			file := a.last().List[0].Path
			submit(t, n.h, a.thread, api.ThreadDelete{})
			select {
			case <-a.closing:
			case <-time.After(5 * time.Second):
				t.Fatal("the thread's agent's close did not begin")
			}
			if restart {
				n.kill()
				n = startHost()
			}
			turn(n.h, api.NewID(), other)
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("another thread's run swept the deleted thread's skills while its agent's close was still to do: %v", err)
			}
		})
	}
}
