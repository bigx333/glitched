package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
	"glitched/internal/transport"
)

func TestRemoteServersShowTheirThreadsAndComeBack(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, rdir, "127.0.0.1:0")
	addr := d.Addrs()[0].String()
	there := api.NewID()
	create := api.ThreadCreate{Workspace: "/srv", Title: "on the box", Model: fakeModel}
	if _, err := d.Endpoint().Submit(timeout(t), api.Command{ID: api.NewID(), Target: there, Body: create}); err != nil {
		t.Fatal(err)
	}
	host, _ := d.Endpoint().Info(timeout(t))

	m, g := manager(t, stateDir(t))
	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	box := m.Envs()[1]
	online(t, g, box)
	if box.Kind != Remote || box.Name != "box" || box.ID != host.ID {
		t.Fatalf("the remote env is %v %q %s, want the box's host %s", box.Kind, box.Name, box.ID, host.ID)
	}
	if th := box.Threads[there]; th == nil || th.View.Title != "on the box" {
		t.Fatalf("the box shows %+v, want its thread", box.Threads)
	}

	here := m.Create(box, "/srv", "from the gui", fakeModel, api.AccessAsk)
	g.until(t, "the box to confirm the thread", func() bool {
		th := box.Threads[here]
		return th.View.Revision > 0 && len(th.Pending) == 0
	})
	if _, err := d.Endpoint().Read(timeout(t), api.Query{Thread: here}); err != nil {
		t.Fatalf("the box doesn't hold the thread created through the manager: %v", err)
	}

	d.Close()
	g.until(t, "the box offline", func() bool {
		_, ok := box.Status.(Offline)
		return ok
	}, func() string { return describeStatus(box.Status) })
	engine(t, rdir, addr)
	online(t, g, box)
	if box.Threads[there] == nil || box.Threads[here] == nil {
		t.Fatalf("after the box came back it shows %d threads, want both", len(box.Threads))
	}
}

// add adds s as Settings does, and returns what AddServer said.
func add(t *testing.T, g *gui, m *Manager, s Server) error {
	t.Helper()
	var err error
	done := false
	m.AddServer(s, func(e error) { err, done = e, true })
	g.until(t, "adding "+s.Label, func() bool { return done })
	return err
}

// remoteEngine is an engine serving over TCP, with a token paired under
// each of its names.
type remoteEngine struct {
	addr   string
	id     api.ID
	tokens map[string]string
}

func paired(t *testing.T, names ...string) remoteEngine {
	t.Helper()
	dir := stateDir(t)
	r := remoteEngine{tokens: map[string]string{}}
	for _, n := range names {
		token, err := daemon.Pair(dir, n)
		if err != nil {
			t.Fatal(err)
		}
		r.tokens[n] = token
	}
	d := engine(t, dir, "127.0.0.1:0")
	info, err := d.Endpoint().Info(timeout(t))
	if err != nil {
		t.Fatal(err)
	}
	r.addr, r.id = d.Addrs()[0].String(), info.ID
	return r
}

func (r remoteEngine) server(label, token string) Server {
	return Server{Label: label, Addr: r.addr, Token: r.tokens[token]}
}

// A server is listed only once it answers with the token. One that
// doesn't is left out of gui.json, and the error says why.
func TestAServerThatDoesntAnswerIsNotSaved(t *testing.T) {
	dir := stateDir(t)
	m, g := manager(t, dir)
	box := paired(t, "gui")
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone.Close()
	for _, s := range []Server{{Label: "gone", Addr: gone.Addr().String()}, {Label: "stranger", Addr: box.addr, Token: "wrong"}} {
		err := add(t, g, m, s)
		if err == nil || !strings.Contains(err.Error(), s.Addr) {
			t.Fatalf("adding %s said %v, want why %s didn't answer", s.Label, err, s.Addr)
		}
		if s.Label == "stranger" && !errors.Is(err, transport.ErrRefused) {
			t.Fatalf("adding the stranger said %v, want the token refused", err)
		}
	}
	if saved, err := savedIn(dir); err != nil || len(saved.Servers) != 0 || len(m.Servers()) != 0 || len(m.Envs()) != 1 {
		t.Fatalf("gui.json lists %+v (%v) and the manager %+v, want no servers", saved.Servers, err, m.Servers())
	}
}

// Adding an engine listed already, as another server or as this machine's,
// is refused; re-pairing a listed server to the same engine is not.
func TestAddingAServerListedAlreadyIsRefused(t *testing.T) {
	dir := stateDir(t)
	self, err := daemon.Pair(dir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	here := engine(t, dir, "127.0.0.1:0")
	m, g := manager(t, dir)
	online(t, g, m.Envs()[0])
	box := paired(t, "gui", "again")
	if err := add(t, g, m, box.server("box", "gui")); err != nil {
		t.Fatal(err)
	}
	for s, want := range map[Server]string{
		box.server("box-again", "again"):                                       "box-again is already listed as box",
		{Label: "self", Addr: here.Addrs()[0].String(), Token: self}:           "self is this machine's engine",
		{Label: m.Envs()[0].Name, Addr: here.Addrs()[0].String(), Token: self}: "is this machine's engine",
	} {
		if err := add(t, g, m, s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("adding %s said %v, want %q", s.Label, err, want)
		}
	}
	if err := add(t, g, m, box.server("box", "again")); err != nil {
		t.Fatalf("re-pairing box with another token of its engine: %v", err)
	}
	want := box.server("box", "again")
	want.Engine = box.id
	if saved, err := savedIn(dir); err != nil || !slices.Equal(saved.Servers, []Server{want}) {
		t.Fatalf("gui.json lists %+v (%v), want box re-paired", saved.Servers, err)
	}
	online(t, g, m.Envs()[1])
}

// A GUI that read gui.json before another GUI on its dir listed an engine
// still refuses to list that engine again.
func TestAnEngineAnotherGUIListedIsRefused(t *testing.T) {
	dir := stateDir(t)
	a, ga := manager(t, dir)
	b, gb := manager(t, dir)
	box := paired(t, "gui", "again")
	if err := add(t, ga, a, box.server("box", "gui")); err != nil {
		t.Fatal(err)
	}
	if err := add(t, gb, b, box.server("box-again", "again")); err == nil || !strings.Contains(err.Error(), "box-again is already listed as box") {
		t.Errorf("the second GUI adding box-again said %v, want it listed already", err)
	}
	if s, err := savedIn(dir); err != nil || !slices.Equal(serverLabels(s.Servers), []string{"box"}) {
		t.Fatalf("gui.json lists %v (%v), want box alone", serverLabels(s.Servers), err)
	}
}

// AddServer returns before the server answers, so the GUI's thread goes on
// drawing while it waits.
func TestAddingAServerLeavesThePostThreadFree(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { silent.Close() })
	m, _ := manager(t, stateDir(t))
	start := time.Now()
	called := false
	m.AddServer(Server{Label: "silent", Addr: silent.Addr().String(), Token: "t"}, func(error) { called = true })
	if took := time.Since(start); took > time.Second || called {
		t.Fatalf("AddServer took %v and answered %v before the server did", took, called)
	}
}

func TestGUIJSONKeepsServersAndReadMarks(t *testing.T) {
	dir := stateDir(t)
	m, g := manager(t, dir)
	env := m.Envs()[0]
	online(t, g, env)
	id := finishTurn(t, m, g, env)
	th := env.Threads[id]
	if !m.Unread(th) {
		t.Fatal("a finished turn nobody looked at is read, want unread")
	}
	m.MarkRead(th)
	if m.Unread(th) {
		t.Fatal("unread after MarkRead")
	}
	a, b := paired(t, "ta", "ta2"), paired(t, "tb")
	for _, s := range []Server{a.server("a", "ta"), b.server("b", "tb"), a.server("a", "ta2")} {
		if err := add(t, g, m, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.RemoveServer("b"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "gui.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("gui.json: %v %v, want mode 0600: it holds tokens", fi.Mode(), err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	m, g = manager(t, dir)
	want := []Server{a.server("a", "ta2")}
	want[0].Engine = a.id
	if got := m.Servers(); !slices.Equal(got, want) {
		t.Fatalf("servers after a restart: %+v, want %+v", got, want)
	}
	if envs := m.Envs(); len(envs) != 2 || envs[1].Name != "a" {
		t.Fatalf("envs after a restart: %d, want local and a", len(envs))
	}
	env = m.Envs()[0]
	g.until(t, "the thread after a restart", func() bool { th := env.Threads[id]; return th != nil && th.View.LastTurn != nil })
	if m.Unread(env.Threads[id]) {
		t.Fatal("the turn marked read is unread after a restart")
	}
}

// A server that refused the token is fixed by adding it again with a new
// one. The env stays, with its threads and the commands that failed.
func TestRepairingATokenKeepsTheServersThreads(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	addr := engine(t, rdir, "127.0.0.1:0").Addrs()[0].String()
	m, g := manager(t, stateDir(t))
	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	box := m.Envs()[1]
	online(t, g, box)
	id := m.Create(box, "/work", "draft", fakeModel, api.AccessAsk)
	g.until(t, "the create confirmed", func() bool { th := box.Threads[id]; return th.known && len(th.Pending) == 0 })
	if err := daemon.Revoke(rdir, "gui"); err != nil {
		t.Fatal(err)
	}
	g.until(t, "refused", func() bool { return box.Status == Status(Refused{}) })
	msg := m.Send(box, id, "keep my unsent text", api.AfterActive)
	g.until(t, "the message failed", func() bool { p := box.Threads[id].Pending; return len(p) == 1 && p[0].Err != nil })

	if token, err = daemon.Pair(rdir, "gui"); err != nil {
		t.Fatal(err)
	}
	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	if envs := m.Envs(); len(envs) != 2 || envs[1] != box {
		t.Fatal("adding the server again replaced its env, and with it the threads the GUI showed")
	}
	online(t, g, box)
	if p := box.Threads[id].Pending; len(p) != 1 || p[0].Command != msg {
		t.Fatalf("after the repair the thread's rows are %s, want the failed message", describePending(p))
	}
	m.Retry(box, id, msg)
	g.until(t, "the retry landed", func() bool { return len(box.Threads[id].Pending) == 0 }, func() string { return describePending(box.Threads[id].Pending) })
}

// relay forwards a port to addr once opened, as a name a server answers to
// does once its DNS points there.
type relay struct {
	ln   net.Listener
	open atomic.Bool
}

func newRelay(t *testing.T, addr string) *relay {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if !r.open.Load() {
				c.Close()
				continue
			}
			u, err := net.Dial("tcp", addr)
			if err != nil {
				c.Close()
				continue
			}
			go func() { io.Copy(u, c); u.Close() }()
			go func() { io.Copy(c, u); c.Close() }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return r
}

// list writes servers to gui.json in dir, as another GUI, or this one
// before the engines changed, saved them.
func list(t *testing.T, dir string, servers ...Server) {
	t.Helper()
	if _, err := jsonfile.Edit(filepath.Join(dir, "gui.json"), func(s *saved) error {
		s.Servers = servers
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func env(m *Manager, name string) *Env {
	i := slices.IndexFunc(m.Envs(), func(e *Env) bool { return e.Name == name })
	if i < 0 {
		return nil
	}
	return m.Envs()[i]
}

// One engine listed under several labels, one of them this machine's, shows
// its threads once, by the env listed first. The others say which env that
// is, and show none of the threads, until it goes.
func TestAServerListedTwiceShowsNoneOfItsThreads(t *testing.T) {
	dir := stateDir(t)
	self, err := daemon.Pair(dir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	here := engine(t, dir, "127.0.0.1:0")
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, rdir, "127.0.0.1:0")
	addr := d.Addrs()[0].String()
	there := api.NewID()
	if _, err := d.Endpoint().Submit(timeout(t), api.Command{ID: api.NewID(), Target: there, Body: api.ThreadCreate{Workspace: "/srv", Title: "on the box", Model: fakeModel}}); err != nil {
		t.Fatal(err)
	}
	list(t, dir,
		Server{Label: "self", Addr: here.Addrs()[0].String(), Token: self},
		Server{Label: "box", Addr: addr, Token: token},
		Server{Label: "box-again", Addr: addr, Token: token})
	m, g := manager(t, dir)
	local, box := m.Envs()[0], env(m, "box")
	online(t, g, local)
	online(t, g, box)
	for name, of := range map[string]string{"self": local.Name, "box-again": "box"} {
		e := env(m, name)
		g.until(t, name+" a duplicate", func() bool { return e.Status == Status(Duplicate{Of: of}) }, func() string { return describeStatus(e.Status) })
		if len(e.Threads) != 0 {
			t.Fatalf("%s, a duplicate of %s, shows %d threads, want none", name, of, len(e.Threads))
		}
	}
	if box.Threads[there] == nil {
		t.Fatal("box doesn't show its thread")
	}

	if err := m.RemoveServer("box"); err != nil {
		t.Fatal(err)
	}
	again := env(m, "box-again")
	online(t, g, again)
	g.until(t, "box-again's thread", func() bool { return again.Threads[there] != nil })
}

// A duplicate stays one between its retries, so nothing starts on it that
// its next claim would drop.
func TestNothingStartsOnADuplicateBetweenRetries(t *testing.T) {
	box := paired(t, "gui")
	dir := stateDir(t)
	list(t, dir, box.server("box", "gui"), box.server("box-again", "gui"))
	m, g := manager(t, dir)
	again := env(m, "box-again")
	online(t, g, env(m, "box"))
	isDup := func() bool { _, ok := again.Status.(Duplicate); return ok }
	g.until(t, "box-again a duplicate", isDup, func() string { return describeStatus(again.Status) })
	g.until(t, "box-again retrying", func() bool { return !isDup() }, func() string { return describeStatus(again.Status) })
	if id := m.Create(again, "/srv", "started on the duplicate", fakeModel, api.AccessAsk); !id.IsZero() || len(again.Threads) != 0 {
		t.Fatalf("a thread started on box-again while it was %s", describeStatus(again.Status))
	}
}

// A server listed first that comes to reach the engine of one listed after
// it takes the engine; the one after shows none of its threads any more.
func TestTheServerListedFirstKeepsItsEngine(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, rdir, "127.0.0.1:0")
	addr := d.Addrs()[0].String()
	there := api.NewID()
	if _, err := d.Endpoint().Submit(timeout(t), api.Command{ID: api.NewID(), Target: there, Body: api.ThreadCreate{Workspace: "/srv", Title: "on the box", Model: fakeModel}}); err != nil {
		t.Fatal(err)
	}
	dns := newRelay(t, addr)
	dir := stateDir(t)
	list(t, dir, Server{Label: "box", Addr: dns.ln.Addr().String(), Token: token}, Server{Label: "box-ip", Addr: addr, Token: token})
	m, g := manager(t, dir)
	box, ip := env(m, "box"), env(m, "box-ip")
	online(t, g, ip)
	g.until(t, "box-ip's thread", func() bool { return ip.Threads[there] != nil })

	dns.open.Store(true)
	online(t, g, box)
	g.until(t, "box-ip a duplicate of box", func() bool { return ip.Status == Status(Duplicate{Of: "box"}) }, func() string { return describeStatus(ip.Status) })
	if len(ip.Threads) != 0 || box.Threads[there] == nil {
		t.Fatalf("box shows %d threads and box-ip %d, want box the engine's and box-ip none", len(box.Threads), len(ip.Threads))
	}
}

// Adding a listed server again reconnects it while commands are on their
// way. They go out on the new session instead of failing with the old one.
func TestCommandsInFlightGoOutAfterAReconnect(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	addr := engine(t, rdir, "127.0.0.1:0").Addrs()[0].String()
	m, g := manager(t, stateDir(t))
	box := Server{Label: "box", Addr: addr, Token: token}
	if err := add(t, g, m, box); err != nil {
		t.Fatal(err)
	}
	env := m.Envs()[1]
	online(t, g, env)
	id := m.Create(env, "/work", "t", fakeModel, api.AccessAsk)
	g.until(t, "the create confirmed", func() bool { th := env.Threads[id]; return th.known && len(th.Pending) == 0 })

	for _, d := range []api.DispositionState{api.Settled, api.Open, api.Settled} {
		m.SetDisposition(env, id, api.Disposition{State: d})
	}
	if err := add(t, g, m, box); err != nil {
		t.Fatal(err)
	}
	g.until(t, "every command landed", func() bool {
		th := env.Threads[id]
		if i := slices.IndexFunc(th.Pending, func(p Pending) bool { return p.Err != nil }); i >= 0 {
			t.Fatalf("a command failed across the reconnect: %v", th.Pending[i].Err)
		}
		return len(th.Pending) == 0 && th.View.Disposition.State == api.Settled
	})
}

// managerGoroutines counts the goroutines running the manager's code; the
// engines these tests serve run in the same process.
func managerGoroutines() int {
	buf := make([]byte, 1<<16)
	for {
		if n := runtime.Stack(buf, true); n < len(buf) {
			count := 0
			for _, g := range strings.Split(string(buf[:n]), "\n\n") {
				if strings.Contains(g, "glitched/internal/client.(*Manager)") {
					count++
				}
			}
			return count
		}
		buf = make([]byte, 2*len(buf))
	}
}

func settles(t *testing.T, g *gui, what string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for got := managerGoroutines(); got != want; got = managerGoroutines() {
		if time.Now().After(deadline) {
			t.Fatalf("%d manager goroutines %s, want %d", got, what, want)
		}
		g.until(t, "posts drained", func() bool { return true })
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReconnectingOrRemovingAServerEndsItsGoroutines(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	addr := engine(t, rdir, "127.0.0.1:0").Addrs()[0].String()
	m, g := manager(t, stateDir(t))
	online(t, g, m.Envs()[0])
	settles(t, g, "with the local env idle", 1)
	idle := managerGoroutines()

	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	box := m.Envs()[1]
	online(t, g, box)
	id := m.Create(box, "/srv", "x", fakeModel, api.AccessAsk)
	m.OpenThread(box, id)
	g.until(t, "the transcript", func() bool { return box.Threads[id].Transcript.Cursor.Seq > 0 })
	settles(t, g, "with the box's thread open", idle+2)

	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	online(t, g, box)
	settles(t, g, "after reconnecting the box", idle+2)

	m.Send(box, id, "hello", api.AfterActive)
	if err := m.RemoveServer("box"); err != nil {
		t.Fatal(err)
	}
	settles(t, g, "after removing the box", idle)
}

// Two GUIs on one Dir save at once. Each save keeps what the other saved,
// and a change to the servers shows the ones the other added or removed.
func TestGUIsOnOneDirKeepEachOthersChanges(t *testing.T) {
	dir := stateDir(t)
	a, ga := manager(t, dir)
	b, _ := manager(t, dir)
	var wg sync.WaitGroup
	for _, m := range []*Manager{a, b} {
		wg.Go(func() {
			for i := range 20 {
				if err := m.list(Server{Label: fmt.Sprintf("%p-%d", m, i), Addr: "127.0.0.1:1"}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if s, err := savedIn(dir); err != nil || len(s.Servers) != 40 {
		t.Fatalf("gui.json lists %d servers (%v), want the 20 each GUI added", len(s.Servers), err)
	}

	read := &Thread{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: time.Now()}}}
	a.MarkRead(read)
	saved := func(th *Thread) func() bool {
		return func() bool { s, err := savedIn(dir); return err == nil && !s.Seen[th.View.ID].IsZero() }
	}
	ga.until(t, "the read mark saved", saved(read))
	gone := a.Servers()[0].Label
	if err := b.RemoveServer(gone); err != nil {
		t.Fatal(err)
	}
	if b.Unread(read) {
		t.Fatal("the other GUI's read mark is lost")
	}
	env := a.Envs()[1]
	later := &Thread{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: time.Now()}}}
	a.MarkRead(later)
	ga.until(t, "the later mark saved", saved(later))
	if a.Envs()[1] != env || a.Servers()[0].Label != gone {
		t.Fatal("marking a thread read made the envs follow gui.json, want only the read mark saved")
	}
	if err := a.list(Server{Label: "late", Addr: "127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	s, err := savedIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, e := range a.Envs()[1:] {
		labels = append(labels, e.Name)
	}
	if want := serverLabels(s.Servers); !slices.Equal(serverLabels(a.Servers()), want) || !slices.Equal(labels, want) {
		t.Fatalf("after a change to its servers a GUI lists %v and shows %v, want gui.json's %v", serverLabels(a.Servers()), labels, want)
	}
	if slices.Contains(labels, gone) || b.Unread(read) || a.Unread(read) {
		t.Fatalf("a removed server or a read mark did not carry over")
	}
}

func savedIn(dir string) (saved, error) {
	var s saved
	err := jsonfile.Load(filepath.Join(dir, "gui.json"), &s)
	return s, err
}

func serverLabels(ss []Server) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.Label)
	}
	return out
}

// The GUI loses a server while one of its threads is open, and the thread
// moves on meanwhile. Once the GUI is back, the transcript has what it
// missed, as a fresh fold of the thread does.
func TestAnOpenTranscriptResumesAfterAReconnect(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, rdir, "127.0.0.1:0")
	addr := d.Addrs()[0].String()
	m, g := manager(t, stateDir(t))
	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	box := m.Envs()[1]
	online(t, g, box)
	id := finishTurn(t, m, g, box)

	if err := daemon.Revoke(rdir, "gui"); err != nil {
		t.Fatal(err)
	}
	g.until(t, "refused", func() bool { return box.Status == Status(Refused{}) })
	later := api.MessageDispatch{Text: "later", Mode: api.Defer}
	if _, err := d.Endpoint().Submit(timeout(t), api.Command{ID: api.NewID(), Target: id, Body: later}); err != nil {
		t.Fatal(err)
	}
	if token, err = daemon.Pair(rdir, "gui"); err != nil {
		t.Fatal(err)
	}
	if err := add(t, g, m, Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	want := fresh(t, d.Endpoint(), id)
	if want[len(want)-1] != `user "later" held` {
		t.Fatalf("the box's transcript ends %q, want the held message", want[len(want)-1])
	}
	g.until(t, "the missed message", func() bool { return slices.Equal(lines(box.Threads[id].Transcript), want) },
		func() string { return strings.Join(lines(box.Threads[id].Transcript), "\n") })
}
