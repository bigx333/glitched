package runtime_test

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

// skilled is a fake whose runs say which skills they got.
type skilled struct {
	*fake.Adapter
	mu   sync.Mutex
	runs []provider.Skills
}

func newSkilled() *skilled {
	s := &skilled{Adapter: fake.New(fake.Turn(false))}
	s.Confirms = func(spec provider.SessionSpec) []provider.Event {
		s.mu.Lock()
		s.runs = append(s.runs, spec.Skills)
		s.mu.Unlock()
		return []provider.Event{provider.Granted{Access: spec.Access, Mode: fake.Modes[spec.Access]}, provider.Ran{Model: spec.Model.Model}}
	}
	return s
}

func (s *skilled) last() provider.Skills {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[len(s.runs)-1]
}

func skillAt(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: The "+name+" skill.\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listed(s provider.Skills) []string {
	var out []string
	for _, sk := range s.List {
		out = append(out, sk.Name)
		if sk.Path != filepath.Join(s.Root, "skills", sk.Name, "SKILL.md") {
			out = append(out, "at "+sk.Path)
		}
	}
	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Each run gets the skills its thread's workspace has now, in an overlay
// kept while a thread's agent last opened with it; the overlays an engine
// before left are gone once it starts.
func TestRunsGetTheirThreadsSkills(t *testing.T) {
	state := t.TempDir()
	stale := filepath.Join(state, "run-skills", "stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	skillAt(t, filepath.Join(state, "skills"), "mine")
	wsA, wsB := t.TempDir(), t.TempDir()
	skillAt(t, filepath.Join(wsA, ".glitched", "skills"), "theirs")
	ad := newSkilled()
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithSkills(state))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); st.Close() })
	if exists(stale) {
		t.Errorf("the last engine's overlay %s is still there", stale)
	}
	thread := func(ws string) api.ID {
		id := api.NewID()
		submit(t, h, id, api.ThreadCreate{Workspace: ws, Title: "t", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
		return id
	}
	turn := func(id api.ID) provider.Skills {
		submit(t, h, id, dispatch("go", api.StartNow))
		until(t, h, id, "turn end", idle)
		return ad.last()
	}
	a, b := thread(wsA), thread(wsB)
	first := turn(a)
	if got := listed(first); !slices.Equal(got, []string{"mine", "theirs"}) || filepath.Dir(first.Root) != filepath.Join(state, "run-skills") {
		t.Fatalf("thread A's run got %v in %s", got, first.Root)
	}
	other := turn(b)
	if got := listed(other); !slices.Equal(got, []string{"mine"}) || other.Root == first.Root {
		t.Fatalf("thread B's run got %v in %s", got, other.Root)
	}
	skillAt(t, filepath.Join(wsA, ".glitched", "skills"), "added")
	again := turn(a)
	if got := listed(again); !slices.Equal(got, []string{"added", "mine", "theirs"}) || again.Root == first.Root {
		t.Fatalf("thread A's next run got %v in %s", got, again.Root)
	}
	if exists(first.Root) || !exists(other.Root) || !exists(again.Root) {
		t.Errorf("overlays: A's first %v, B's %v, A's now %v; want only the ones a thread's agent opened with last", exists(first.Root), exists(other.Root), exists(again.Root))
	}
	if same := turn(b); same.Root != other.Root {
		t.Errorf("B's unchanged skills moved from %s to %s", other.Root, same.Root)
	}
}

// Skills Glitched can't copy cost the run nothing but the skills.
func TestARunWhoseSkillsCantBeCopiedRunsWithoutThem(t *testing.T) {
	state := t.TempDir()
	skillAt(t, filepath.Join(state, "skills"), "mine")
	ad := newSkilled()
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, runtime.WithSkills(state))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(); st.Close() })
	if err := os.WriteFile(filepath.Join(state, "run-skills"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	id := newThread(t, h)
	submit(t, h, id, dispatch("go", api.StartNow))
	until(t, h, id, "turn end", idle)
	evs := events(t, h, id)
	end, _ := evs[len(evs)-1].Decode()
	if got := ad.last(); got.Root != "" || len(got.List) != 0 || end.(api.TurnEnded).Outcome != api.OutcomeCompleted {
		t.Errorf("the run got %+v and ended %+v", got, end)
	}
}
