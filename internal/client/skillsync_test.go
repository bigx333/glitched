package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
	"glitched/internal/skills/skillstest"
	"glitched/internal/transport"
)

// opens tells of each session a run opened, on top of the fake.
type opens struct {
	*fake.Adapter
	ch chan provider.SessionSpec
}

func (o *opens) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	o.ch <- spec
	return o.Adapter.Open(ctx, spec)
}

// next is the session the next run opens with.
func (o *opens) next(t *testing.T) provider.SessionSpec {
	t.Helper()
	select {
	case s := <-o.ch:
		return s
	case <-time.After(30 * time.Second):
		t.Fatal("no run opened")
		return provider.SessionSpec{}
	}
}

// given is the skills a session's agent got, name: description.
func given(spec provider.SessionSpec) []string {
	var out []string
	for _, s := range spec.Skills.List {
		out = append(out, s.Name+": "+s.Description)
	}
	slices.Sort(out)
	return out
}

// peer is an engine on another machine, paired for this GUI, whose runs
// tell of the sessions they open.
type peer struct {
	dir    string
	d      *daemon.Daemon
	runs   *opens
	server Server
}

func pairedNode(t *testing.T, label string) peer {
	t.Helper()
	dir := stateDir(t)
	token, err := daemon.Pair(dir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := daemon.Defaults(dir)
	if err != nil {
		t.Fatal(err)
	}
	runs := &opens{Adapter: fake.New(fake.Turn(false)), ch: make(chan provider.SessionSpec, 8)}
	cfg.Listen, cfg.Providers = []string{"127.0.0.1:0"}, provider.Mux{"fake": runs}
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return peer{dir: dir, d: d, runs: runs, server: Server{Label: label, Addr: d.Addrs()[0].String(), Token: token}}
}

// reach is the env of n in m, online, once m's own engine is.
func reach(t *testing.T, m *Manager, g *gui, n peer) *Env {
	t.Helper()
	online(t, g, m.Envs()[0])
	if err := add(t, g, m, n.server); err != nil {
		t.Fatal(err)
	}
	e := m.Envs()[len(m.Envs())-1]
	online(t, g, e)
	return e
}

// say sends text on thread and waits for its turn to end.
func say(t *testing.T, m *Manager, g *gui, e *Env, thread api.ID, text string) {
	t.Helper()
	m.Send(e, thread, text, api.AfterActive)
	g.until(t, "the turn after "+text, func() bool {
		th := e.Threads[thread]
		return slices.Contains(lines(th.Transcript), "user "+strconv.Quote(text)+" sent") && th.View.Run == api.RunIdle && len(th.Pending) == 0
	}, func() string { return strings.Join(lines(e.Threads[thread].Transcript), "\n") })
}

// thread is a new thread of e's in ws, open.
func thread(t *testing.T, m *Manager, g *gui, e *Env, ws string) api.ID {
	t.Helper()
	id := m.Create(e, ws, "skills", fakeModel, api.AccessAsk, nil)
	m.OpenThread(e, id)
	g.until(t, "the thread confirmed", func() bool { th := e.Threads[id]; return th.View.Revision > 0 && len(th.Pending) == 0 })
	return id
}

func listSkills(t *testing.T, m *Manager, g *gui, e *Env, thread api.ID) api.SkillList {
	t.Helper()
	var list api.SkillList
	var err error
	done := false
	m.Skills(e, thread, true, func(l api.SkillList, e error) { list, err, done = l, e, true })
	g.until(t, "the thread's skills", func() bool { return done })
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A thread on a paired machine gets this machine's skills at its first
// turn, and a skill changed here between turns at the next one.
func TestANodesRunsGetThisMachinesSkills(t *testing.T) {
	src := stateDir(t)
	md := skillstest.Write(t, filepath.Join(src, "skills"), "hello", "Say hello.")
	n := pairedNode(t, "box")
	m, g := manager(t, src)
	box := reach(t, m, g, n)
	id := thread(t, m, g, box, stateDir(t))
	say(t, m, g, box, id, "one")
	spec := n.runs.next(t)
	if got := given(spec); !slices.Equal(got, []string{"hello: Say hello."}) {
		t.Fatalf("the node's run got %v", got)
	}
	want, _ := os.ReadFile(filepath.Join(md, "SKILL.md"))
	if got, err := os.ReadFile(spec.Skills.List[0].Path); err != nil || string(got) != string(want) {
		t.Errorf("the node's run read hello as %q, %v; want %q", got, err, want)
	}
	list := listSkills(t, m, g, box, id)
	here := m.Envs()[0].Name
	if len(list.Skills) != 1 || list.Skills[0].Source != "synced" || list.Skills[0].From != here || list.Synced == nil || list.Synced.Name != here {
		t.Errorf("the node says %+v, want hello synced from %q", list, here)
	}

	skillstest.Write(t, filepath.Join(src, "skills"), "hello", "Say hello twice.")
	say(t, m, g, box, id, "two")
	if got := given(n.runs.next(t)); !slices.Equal(got, []string{"hello: Say hello twice."}) {
		t.Errorf("after hello changed here the node's next run got %v", got)
	}
}

// A run a node opens with no GUI to bring this machine's skills, as a wake
// or a child's would, gets the set it was last given, and the node says
// when that was.
func TestANodeKeepsTheLastSetItGot(t *testing.T) {
	src := stateDir(t)
	skillstest.Write(t, filepath.Join(src, "skills"), "hello", "Say hello.")
	n := pairedNode(t, "box")
	m, g := manager(t, src)
	box := reach(t, m, g, n)
	id := thread(t, m, g, box, stateDir(t))
	say(t, m, g, box, id, "one")
	n.runs.next(t)
	before := listSkills(t, m, g, box, id).Synced
	m.Close()

	skillstest.Write(t, filepath.Join(src, "skills"), "later", "Never given.")
	skillstest.Write(t, filepath.Join(n.dir, "skills"), "own", "The node's own.")
	ep := n.d.Endpoint()
	if _, err := ep.Submit(timeout(t), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "alone", Mode: api.AfterActive}}); err != nil {
		t.Fatal(err)
	}
	if got := given(n.runs.next(t)); !slices.Equal(got, []string{"hello: Say hello.", "own: The node's own."}) {
		t.Errorf("a run with no GUI got %v", got)
	}
	list, err := ep.(transport.Skills).Skills(timeout(t), api.SkillQuery{Thread: id})
	if err != nil || list.Synced == nil || *list.Synced != *before {
		t.Errorf("with no GUI the node says its set came %+v, %v; want %+v", list.Synced, err, before)
	}
}

// A synced skill replaces the node's own of its name, and a project's
// replaces the synced one.
func TestSyncedSkillsSitBetweenTheProjectsAndTheNodesOwn(t *testing.T) {
	src, ws := stateDir(t), stateDir(t)
	skillstest.Write(t, filepath.Join(src, "skills"), "hello", "This machine's hello.")
	skillstest.Write(t, filepath.Join(src, "skills"), "deploy", "This machine's deploy.")
	n := pairedNode(t, "box")
	skillstest.Write(t, filepath.Join(n.dir, "skills"), "hello", "The node's hello.")
	skillstest.Write(t, filepath.Join(ws, ".glitched", "skills"), "deploy", "The project's deploy.")
	m, g := manager(t, src)
	box := reach(t, m, g, n)
	id := thread(t, m, g, box, ws)
	say(t, m, g, box, id, "one")
	if got := given(n.runs.next(t)); !slices.Equal(got, []string{"deploy: The project's deploy.", "hello: This machine's hello."}) {
		t.Errorf("the node's run got %v", got)
	}
	list := listSkills(t, m, g, box, id)
	got := map[string]api.Skill{}
	for _, s := range list.Skills {
		got[s.Name] = s
	}
	if s := got["hello"]; s.Source != "synced" || s.Replaces != "home" {
		t.Errorf("hello is %+v, want synced, replacing the node's own", s)
	}
	if s := got["deploy"]; s.Source != "project" || s.Replaces != "synced" {
		t.Errorf("deploy is %+v, want the project's, replacing the synced one", s)
	}
}

// With sync turned off here, the node drops this machine's skills the next
// time the GUI reaches it, and its next run gets only its own; the switch
// restarts no engine.
func TestTurningSyncOffDropsTheSetFromTheNode(t *testing.T) {
	src := stateDir(t)
	skillstest.Write(t, filepath.Join(src, "skills"), "hello", "Say hello.")
	n := pairedNode(t, "box")
	skillstest.Write(t, filepath.Join(n.dir, "skills"), "own", "The node's own.")
	m, g := manager(t, src)
	box := reach(t, m, g, n)
	id := thread(t, m, g, box, stateDir(t))
	say(t, m, g, box, id, "one")
	if got := given(n.runs.next(t)); !slices.Equal(got, []string{"hello: Say hello.", "own: The node's own."}) {
		t.Fatalf("the node's run got %v", got)
	}
	embedded := m.embedded
	off := false
	if err := m.Configure(func(c *daemon.Config) { c.SyncSkills = &off }); err != nil {
		t.Fatal(err)
	}
	if m.embedded != embedded || m.restarting {
		t.Error("turning sync off restarted the engine")
	}
	say(t, m, g, box, id, "two")
	if got := given(n.runs.next(t)); !slices.Equal(got, []string{"own: The node's own."}) {
		t.Errorf("with sync off the node's next run got %v", got)
	}
	if sets, _ := os.ReadDir(filepath.Join(n.dir, "skills-synced", "sets")); len(sets) != 0 {
		t.Errorf("the node keeps this machine's skills: %v", sets)
	}
	if list := listSkills(t, m, g, box, id); list.Synced != nil {
		t.Errorf("the node still says its skills came from %+v", list.Synced)
	}
}

// A node that is the GUI's own machine for another GUI passes on its own
// skills alone, never those synced to it.
func TestANodePassesOnOnlyItsOwnSkills(t *testing.T) {
	src := stateDir(t)
	skillstest.Write(t, filepath.Join(src, "skills"), "theirs", "The first machine's.")
	b := pairedNode(t, "b")
	skillstest.Write(t, filepath.Join(b.dir, "skills"), "mine", "The second machine's.")
	m, g := manager(t, src)
	onB := reach(t, m, g, b)
	say(t, m, g, onB, thread(t, m, g, onB, stateDir(t)), "one")
	if got := given(b.runs.next(t)); !slices.Equal(got, []string{"mine: The second machine's.", "theirs: The first machine's."}) {
		t.Fatalf("b's run got %v", got)
	}

	c := pairedNode(t, "c")
	mb, gb := manager(t, b.dir)
	onC := reach(t, mb, gb, c)
	say(t, mb, gb, onC, thread(t, mb, gb, onC, stateDir(t)), "one")
	if got := given(c.runs.next(t)); !slices.Equal(got, []string{"mine: The second machine's."}) {
		t.Errorf("c's run, given b's skills by b's GUI, got %v", got)
	}
}

// heldSync is an engine whose syncs wait on sync, and that says what it is
// submitted.
type heldSync struct {
	api.Endpoint
	sync      func(context.Context) error
	submitted chan api.Intent
}

func (e heldSync) SyncSkills(ctx context.Context, _ api.SkillSync) (api.SkillsSynced, error) {
	return api.SkillsSynced{}, e.sync(ctx)
}
func (e heldSync) PutSkill(context.Context, api.SkillPut) error    { panic("unexpected put") }
func (e heldSync) DropSkills(context.Context, api.SkillDrop) error { panic("unexpected drop") }
func (e heldSync) Skills(context.Context, api.SkillQuery) (api.SkillList, error) {
	panic("unexpected list")
}
func (e heldSync) Submit(_ context.Context, cmd api.Command) (api.Receipt, error) {
	e.submitted <- cmd.Body
	return api.Receipt{}, nil
}

// heldRemote is a remote thread on an engine at ep, and m with dir as its
// state folder.
func heldRemote(t *testing.T, dir string, ep api.Endpoint) (*Manager, *Env, api.ID) {
	t.Helper()
	g := &gui{wake: make(chan struct{}, 1)}
	m := New(Config{Dir: dir, Post: g.post})
	t.Cleanup(func() { m.Close() })
	id := api.NewID()
	e := &Env{Kind: Remote, ID: api.NewID(), ctx: m.ctx, skills: &skillSync{}, slots: make(chan struct{}, 2), session: &session{ctx: m.ctx, ep: ep}, Threads: map[api.ID]*Thread{id: {View: api.Thread{ID: id}}}}
	m.envs = []*Env{{Kind: Local, ID: api.NewID()}, e}
	return m, e, id
}

// A stop sent while the message before it waits for its sync goes at
// once, the message just before it: neither waits on the sync.
func TestAStopNeverWaitsBehindASync(t *testing.T) {
	begin, release := make(chan struct{}), make(chan struct{})
	ep := heldSync{sync: func(ctx context.Context) error {
		close(begin)
		select {
		case <-release:
			return errors.New("test sync refusal")
		case <-ctx.Done():
			return ctx.Err()
		}
	}, submitted: make(chan api.Intent, 2)}
	defer close(release)
	m, e, id := heldRemote(t, t.TempDir(), ep)
	m.Send(e, id, "hello", api.AfterActive)
	select {
	case <-begin:
	case <-time.After(time.Second):
		t.Fatal("no sync reached endpoint")
	}
	m.Interrupt(e, id)
	for _, want := range []string{"send", "stop"} {
		select {
		case b := <-ep.submitted:
			switch b.(type) {
			case api.MessageDispatch:
				if want != "send" {
					t.Fatalf("expected %s, got send", want)
				}
			case api.TurnInterrupt:
				if want != "stop" {
					t.Fatalf("expected %s, got stop", want)
				}
			default:
				t.Fatalf("unexpected %T", b)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s never submitted while the sync was held", want)
		}
	}
}

// A queued message sent now on an idle thread starts a turn, so it goes
// after a sync: here the drop of a switch turned off.
func TestAnIdleQueueSteerSyncsFirst(t *testing.T) {
	src := stateDir(t)
	skillstest.Write(t, filepath.Join(src, "skills"), "hello", "First skill.")
	n := pairedNode(t, "box")
	m, g := manager(t, src)
	box := reach(t, m, g, n)
	id := thread(t, m, g, box, stateDir(t))
	queued := m.Send(box, id, "deferred", api.Defer)
	g.until(t, "message is held on idle thread", func() bool {
		th := box.Threads[id]
		return th.View.Run == api.RunIdle && len(th.Pending) == 0 && len(th.Transcript.Queue) == 1 && th.Transcript.Queue[0].State == Held
	})
	off := false
	if err := m.Configure(func(cfg *daemon.Config) { cfg.SyncSkills = &off }); err != nil {
		t.Fatal(err)
	}
	m.SteerQueued(box, id, queued)
	if got := given(n.runs.next(t)); !slices.Equal(got, []string(nil)) {
		t.Fatalf("first run after app contacted node with switch disabled still got %v", got)
	}
}

// calls is an engine whose syncs hold till their call ends, and that says
// each drop and submit it takes, in order.
type calls struct {
	api.Endpoint
	took chan string
}

func (c calls) SyncSkills(ctx context.Context, _ api.SkillSync) (api.SkillsSynced, error) {
	c.took <- "sync"
	<-ctx.Done()
	return api.SkillsSynced{}, ctx.Err()
}
func (c calls) PutSkill(context.Context, api.SkillPut) error { panic("unexpected put") }
func (c calls) DropSkills(context.Context, api.SkillDrop) error {
	c.took <- "drop"
	return nil
}
func (c calls) Skills(context.Context, api.SkillQuery) (api.SkillList, error) {
	panic("unexpected list")
}
func (c calls) Submit(_ context.Context, cmd api.Command) (api.Receipt, error) {
	c.took <- "submit " + cmd.Body.(api.MessageDispatch).Text
	return api.Receipt{}, nil
}

// Turning sync off ends a sync under way: the drop goes at once, before
// the next message, though the sync would have held past its wait.
func TestTurningSyncOffEndsTheSyncUnderWay(t *testing.T) {
	ep := calls{took: make(chan string, 8)}
	m, e, id := heldRemote(t, t.TempDir(), ep)
	next := func() string {
		t.Helper()
		select {
		case s := <-ep.took:
			return s
		case <-time.After(time.Second):
			t.Fatal("the engine took nothing more")
			return ""
		}
	}
	m.Send(e, id, "one", api.AfterActive)
	if got := next(); got != "sync" {
		t.Fatalf("the engine took %q first", got)
	}
	off := false
	if err := m.Configure(func(cfg *daemon.Config) { cfg.SyncSkills = &off }); err != nil {
		t.Fatal(err)
	}
	m.Send(e, id, "two", api.AfterActive)
	var got []string
	for !slices.Contains(got, "submit two") {
		got = append(got, next())
	}
	if i := slices.Index(got, "drop"); i < 0 || i > slices.Index(got, "submit two") {
		t.Errorf("the engine took %v, the second message before any drop", got)
	}
}
