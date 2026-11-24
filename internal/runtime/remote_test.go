package runtime_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// laptop is the machine the tests place remote children on.
var laptop = api.Remote{Engine: api.NewID(), Link: api.NewID()}

// onlyLaptop finds laptop and no other machine.
func onlyLaptop(label string) (api.Remote, bool, error) { return laptop, label == "laptop", nil }

// placed has a turn of parent's start kids and returns each one's thread
// and how its create went, in order.
func placed(t *testing.T, ep api.Endpoint, ad *fake.Adapter, parent api.ID, kids ...fake.Child) ([]api.ID, []error) {
	t.Helper()
	text := fmt.Sprint("start child ", spawnTexts.Add(1))
	ad.Spawns(ep, text, kids...)
	run := submit(t, ep, parent, dispatch(text, api.AfterActive)).Command
	until(t, ep, parent, "the spawning turn", func(th api.Thread) bool {
		return th.Run == api.RunIdle && th.Queued == 0 && ended(t, ep, parent, run)
	})
	ids, errs := make([]api.ID, len(kids)), make([]error, len(kids))
	for i := range kids {
		ids[i] = fake.ChildOf(run, i)
		answered, err := ad.Spawned(ids[i])
		if !answered {
			t.Fatalf("the spawning turn ended before the engine answered create %d", i)
		}
		errs[i] = err
	}
	return ids, errs
}

func remoteHost(t *testing.T, opts ...runtime.Option) (node, *fake.Adapter) {
	t.Helper()
	ad := fake.New(fake.Turn(true))
	st, err := store.Open(dbPath(t))
	if err != nil {
		t.Fatal(err)
	}
	st.SetPlaces(onlyLaptop)
	h, err := runtime.New(ctx, st, provider.Mux{"fake": ad}, opts...)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	n := node{st, h}
	t.Cleanup(n.kill)
	return n, ad
}

func refusedWith(t *testing.T, what string, err error, want string) {
	t.Helper()
	var rej *api.RejectedError
	if !errors.As(err, &rej) || !strings.Contains(rej.Reason, want) {
		t.Fatalf("%s: %v; want refused with %q", what, err, want)
	}
}

// A placeholder is a child of its parent's with its machine frozen on
// it, creating there, and nothing run here: no message queued, no turn.
// Its create sent twice makes one, and the stored row is its log's fold.
func TestARemoteChildIsAPlaceholderThatRunsNothing(t *testing.T) {
	n, ad := remoteHost(t)
	parent := repoThread(t, n.h)
	starts := ad.Starts()
	ids, errs := placed(t, n.h, ad, parent, fake.Child{Title: "on the laptop", Task: "run the tests there", Remote: "laptop", Tries: 2})
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	kid := ids[0]
	th, err := n.st.Thread(ctx, kid)
	if err != nil {
		t.Fatal(err)
	}
	if want := (engine.Remote{Engine: laptop.Engine, Link: laptop.Link, State: engine.RemoteCreating}); th.Remote == nil || !reflect.DeepEqual(*th.Remote, want) || th.Parent != parent {
		t.Fatalf("the placeholder: parent %v, remote %+v; want under %v, %+v", th.Parent, th.Remote, parent, want)
	}
	evs := events(t, n.h, kid)
	if len(evs) != 1 || evs[0].Type != "thread.created" {
		t.Fatalf("the placeholder's log: %v; want its one create", types(evs))
	}
	if len(th.Runs) != 0 || len(th.Queue) != 0 || th.Workspace != "" {
		t.Fatalf("the placeholder has runs %+v, queue %+v and folder %q; want none", th.Runs, th.Queue, th.Workspace)
	}
	// The parent's spawning turn is the one start since.
	if got := ad.Starts() - starts; got != 1 {
		t.Errorf("%d starts since the placeholder's parent was made; want the spawning turn's only", got)
	}
	assertReplay(t, n.st)
}

func types(evs []api.Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// A machine the person didn't add is refused, and nothing is made.
func TestARemoteChildOnAMachineNotInTheListIsRefused(t *testing.T) {
	n, ad := remoteHost(t)
	parent := repoThread(t, n.h)
	ids, errs := placed(t, n.h, ad, parent, fake.Child{Title: "elsewhere", Task: "go", Remote: "desktop"})
	refusedWith(t, "a child on desktop", errs[0], `no machine called "desktop"`)
	if th, _ := n.st.Thread(ctx, ids[0]); th.Exists() {
		t.Fatalf("a refused placeholder was stored: %+v", th)
	}
}

// A placeholder whose child is being created on its machine takes a place
// among those at work: beside four local children at work, a sixth is
// refused, and one starts once the placeholder is settled.
func TestAPlaceholderTakesAPlaceAtWork(t *testing.T) {
	n, ad := remoteHost(t)
	parent := repoThread(t, n.h)
	kids := []fake.Child{{Title: "remote", Task: "go", Remote: "laptop"}}
	for i := range 4 {
		kids = append(kids, fake.Child{Title: fmt.Sprint("local ", i), Task: "go"})
	}
	ids, errs := placed(t, n.h, ad, parent, append(kids, fake.Child{Title: "sixth", Task: "go"})...)
	for i, err := range errs[:5] {
		if err != nil {
			t.Fatalf("child %d: %v", i, err)
		}
	}
	for _, id := range ids[1:5] {
		until(t, n.h, id, "a local child at work", waiting)
	}
	refusedWith(t, "a sixth child", errs[5], "5 child threads of this thread are at work")

	submit(t, n.h, ids[0], api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	_, errs = placed(t, n.h, ad, parent, fake.Child{Title: "sixth again", Task: "go"})
	if errs[0] != nil {
		t.Fatalf("a sixth child once the placeholder is settled: %v; want it started", errs[0])
	}
	assertReplay(t, n.st)
}

// A remote child's task that holds a value of its parent's vault is
// refused before anything is stored: the vault never leaves the machine.
func TestARemoteTaskWithAVaultValueIsRefused(t *testing.T) {
	vault := secrets.New(t.TempDir())
	n, ad := remoteHost(t, runtime.WithVault(vault))
	parent := repoThread(t, n.h)
	if err := vault.Put(parent, map[string]string{"DEPLOY_TOKEN": "tok-8c1f2e9a77", "PIN": "k29Qm7", "OWN_NAME": "[OWN_NAME]"}); err != nil {
		t.Fatal(err)
	}
	before, err := n.st.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids, errs := placed(t, n.h, ad, parent,
		fake.Child{Title: "leaky", Task: "deploy with tok-8c1f2e9a77", Remote: "laptop"},
		fake.Child{Title: "deploy tok-8c1f2e9a77", Task: "deploy", Remote: "laptop"},
		fake.Child{Title: "using k29Qm7", Task: "go", Remote: "laptop"},
		fake.Child{Title: "using [OWN_NAME]", Task: "go", Remote: "laptop"},
		fake.Child{Title: "named", Task: "deploy with $DEPLOY_TOKEN", Remote: "laptop"})
	for i, what := range []string{"a task with the token's value", "a title with the token's value", "a title with a value too short to redact", "a title with a value that is its own redaction"} {
		refusedWith(t, what, errs[i], "the task holds a credential value; name the variable instead")
	}
	if errs[4] != nil {
		t.Fatalf("a task that names the variable: %v; want it placed", errs[4])
	}
	after, err := n.st.ReadLog(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after[len(before):] {
		if slices.Contains(ids[:4], e.Thread) {
			t.Fatalf("a refused create stored %s", e.Type)
		}
	}
	for _, id := range ids[:4] {
		if th, _ := n.st.Thread(ctx, id); th.Exists() {
			t.Fatalf("a refused create stored its thread: %+v", th)
		}
	}
}

// Settling or deleting a tree takes its placeholder in the tree's one
// commit; a stop of the tree passes it by. On the placeholder itself a
// message and a stop are refused, as nothing reaches its machine yet, and
// nothing ever runs it here.
func TestAPlaceholderGoesWithItsTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		body api.Intent
		took func(engine.Thread) bool
	}{
		{"settle", api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, func(th engine.Thread) bool { return th.Disposition.State == api.Settled }},
		{"delete", api.ThreadDelete{}, func(th engine.Thread) bool { return th.Deleted }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, ad := remoteHost(t)
			parent := repoThread(t, n.h)
			ids, errs := placed(t, n.h, ad, parent, fake.Child{Title: "remote", Task: "go", Remote: "laptop"})
			if errs[0] != nil {
				t.Fatal(errs[0])
			}
			kid := ids[0]
			starts := ad.Starts()
			refusedWith(t, "a message to the placeholder", tryOn(n.h, kid, dispatch("and this", api.StartNow)), "a message doesn't reach it there yet")
			refusedWith(t, "a stop of the placeholder", tryOn(n.h, kid, api.TurnInterrupt{}), "a stop doesn't reach it there yet")
			submit(t, n.h, parent, api.TurnInterrupt{Tree: true})

			log, err := n.st.ReadLog(ctx, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			r := submit(t, n.h, parent, tc.body)
			after, err := n.st.ReadLog(ctx, uint64(len(log)), 0)
			if err != nil {
				t.Fatal(err)
			}
			var kidAt, parentAt uint64
			for _, e := range after {
				switch {
				case e.Thread == kid && (e.Type == "disposition.changed" || e.Type == "thread.deleted"):
					kidAt = e.Cursor.Seq
				case e.Thread == parent && (e.Type == "disposition.changed" || e.Type == "thread.deleted"):
					parentAt = e.Cursor.Seq
				}
			}
			if kidAt == 0 || parentAt == 0 || kidAt > r.Committed.Seq || parentAt > r.Committed.Seq {
				t.Fatalf("the %s: placeholder at %d, parent at %d, commit up to %d; want both in it", tc.name, kidAt, parentAt, r.Committed.Seq)
			}
			th, err := n.st.Thread(ctx, kid)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.took(th) || len(th.Runs) != 0 {
				t.Fatalf("the placeholder after the %s: %+v", tc.name, th)
			}
			if got := ad.Starts(); got != starts {
				t.Errorf("%d starts after the placeholder's; want %d", got, starts)
			}
			assertReplay(t, n.st)
		})
	}
}

// tryOn submits body to thread and returns how it went.
func tryOn(ep api.Endpoint, thread api.ID, body api.Intent) error {
	_, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: thread, Body: body})
	return err
}

// A placeholder outlives a restart as its log folds, and the host it
// restarts in runs nothing for it.
func TestAPlaceholderOutlivesARestart(t *testing.T) {
	path := dbPath(t)
	ad := fake.New(fake.Turn(false))
	n := start(t, path, ad)
	n.st.SetPlaces(onlyLaptop)
	parent := repoThread(t, n.h)
	ids, errs := placed(t, n.h, ad, parent, fake.Child{Title: "remote", Task: "go", Remote: "laptop"})
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	n.kill()

	ad = fake.New(fake.Turn(false))
	n = start(t, path, ad)
	th, err := n.st.Thread(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if th.Remote == nil || th.Remote.State != engine.RemoteCreating || len(th.Runs) != 0 {
		t.Fatalf("the placeholder after a restart: %+v", th)
	}
	assertReplay(t, n.st)
	if ad.Starts() != 0 {
		t.Errorf("the restarted host started %d agents; want none", ad.Starts())
	}
}

// A placeholder has no turn of its own, so a fork of it is refused before
// any worktree is made; a fork of its parent takes the parent's
// conversation and none of its children, and the placeholder runs nothing.
func TestAForkNeverRunsAPlaceholder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	n, ad := remoteHost(t, runtime.WithWorktrees(root))
	project := repo(t)
	parent := api.NewID()
	submit(t, n.h, parent, api.ThreadCreate{Workspace: project, Title: "top", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
	ids, errs := placed(t, n.h, ad, parent, fake.Child{Title: "remote", Task: "go", Remote: "laptop"})
	if errs[0] != nil {
		t.Fatal(errs[0])
	}
	kid := ids[0]
	starts := ad.Starts()
	for _, wt := range []*api.NewWorktree{nil, {}} {
		fork := api.NewID()
		err := tryOn(n.h, fork, api.ThreadCreate{Workspace: project, Title: "fork", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Fork: &api.Fork{Thread: kid}, Worktree: wt})
		refusedWith(t, fmt.Sprintf("a fork of the placeholder, worktree %v", wt != nil), err, "only a finished turn")
		if th, _ := n.st.Thread(ctx, fork); th.Exists() {
			t.Fatalf("a refused fork was stored: %+v", th)
		}
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("worktrees after the refused forks: %v; want none", entries)
	}
	fork := api.NewID()
	submit(t, n.h, fork, api.ThreadCreate{Workspace: project, Title: "fork", Model: api.ModelRef{Provider: "fake", Model: "fake"}, Fork: &api.Fork{Thread: parent}})
	if kids := children(t, n.h, fork); len(kids) != 0 {
		t.Fatalf("the parent's fork has children %+v; want none", kids)
	}
	if evs := events(t, n.h, kid); len(evs) != 1 {
		t.Fatalf("the placeholder's log after the forks: %v; want its create only", types(evs))
	}
	if got := ad.Starts(); got != starts {
		t.Errorf("%d starts after the forks; want %d", got, starts)
	}
	assertReplay(t, n.st)
}

// A remote create of a thread whose local create is making its worktree
// waits for that create to commit, so it never leaves the worktree to a
// placeholder that can't remove it: the local create wins, and the remote
// one finds the thread there.
func TestARemoteCreateWaitsForALocalWorktreeOfTheSameThread(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	n, _ := remoteHost(t, runtime.WithWorktrees(root))
	project := repo(t)
	parent := asking(t, n.h, api.ThreadCreate{Workspace: project})
	gate := t.TempDir()
	t.Setenv("PLACEHOLDER_GATE", gate)
	hook := "#!/bin/sh\ntouch \"$PLACEHOLDER_GATE/entered\"\nn=0\nwhile [ ! -e \"$PLACEHOLDER_GATE/release\" ]; do n=$((n+1)); [ \"$n\" -le 500 ] || exit 42; sleep .02; done\n"
	if err := os.WriteFile(filepath.Join(project, ".git", "hooks", "post-checkout"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	release := func() { os.WriteFile(filepath.Join(gate, "release"), nil, 0o600) }
	defer release()
	id := api.NewID()
	local := isolated(parent)
	remote := local
	remote.Worktree, remote.Remote = nil, "laptop"
	localDone, remoteDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := attempt(n.h, id, local); localDone <- err }()
	for deadline := time.Now().Add(5 * time.Second); !exists(filepath.Join(gate, "entered")); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the local create's git never reached its checkout")
		}
	}
	go func() { _, err := attempt(n.h, id, remote); remoteDone <- err }()
	select {
	case err := <-remoteDone:
		t.Fatalf("the remote create returned while the local one's worktree was being made: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-localDone; err != nil {
		t.Fatalf("the local create: %v; want it made", err)
	}
	var rej *api.RejectedError
	if err := <-remoteDone; !errors.As(err, &rej) {
		t.Fatalf("the remote create of the same thread: %v; want refused", err)
	}
	th, err := n.st.Thread(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if th.Remote != nil || th.Branch == "" {
		t.Fatalf("the thread: remote %+v, branch %q; want the local one in its worktree", th.Remote, th.Branch)
	}
	assertReplay(t, n.st)
}

// repoThread is a thread of the user's on a branch of a repository, as a
// remote child's parent must be.
func repoThread(t *testing.T, ep api.Endpoint) api.ID {
	t.Helper()
	id := api.NewID()
	submit(t, ep, id, api.ThreadCreate{Workspace: repo(t), Title: "fix it", Model: api.ModelRef{Provider: "fake", Model: "fake"}})
	return id
}
