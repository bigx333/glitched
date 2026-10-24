package client

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/jsonfile"
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
	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
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

	if err := m.AddServer(Server{Label: "stranger", Addr: addr, Token: "wrong"}); err != nil {
		t.Fatal(err)
	}
	stranger := m.Envs()[2]
	g.until(t, "the stranger refused", func() bool { return stranger.Status == Status(Refused{}) }, func() string { return describeStatus(stranger.Status) })

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

	if err := m.RemoveServer("stranger"); err != nil {
		t.Fatal(err)
	}
	if envs := m.Envs(); len(envs) != 2 || envs[1] != box {
		t.Fatalf("after removing the stranger the envs are %d, want local and the box", len(envs))
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
	if err := m.MarkRead(th); err != nil {
		t.Fatal(err)
	}
	if m.Unread(th) {
		t.Fatal("unread after MarkRead")
	}
	for _, s := range []Server{{"a", "127.0.0.1:1", "ta"}, {"b", "127.0.0.1:1", "tb"}, {"a", "127.0.0.1:1", "ta2"}} {
		if err := m.AddServer(s); err != nil {
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
	want := []Server{{"a", "127.0.0.1:1", "ta2"}}
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
	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
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
	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
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
	if err := m.AddServer(box); err != nil {
		t.Fatal(err)
	}
	env := m.Envs()[1]
	online(t, g, env)
	id := m.Create(env, "/work", "t", fakeModel, api.AccessAsk)
	g.until(t, "the create confirmed", func() bool { th := env.Threads[id]; return th.known && len(th.Pending) == 0 })

	for _, d := range []api.DispositionState{api.Settled, api.Open, api.Settled} {
		m.SetDisposition(env, id, api.Disposition{State: d})
	}
	if err := m.AddServer(box); err != nil {
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

	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	box := m.Envs()[1]
	online(t, g, box)
	id := m.Create(box, "/srv", "x", fakeModel, api.AccessAsk)
	m.OpenThread(box, id)
	g.until(t, "the transcript", func() bool { return box.Threads[id].Transcript.Cursor.Seq > 0 })
	settles(t, g, "with the box's thread open", idle+2)

	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
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
	a, _ := manager(t, dir)
	b, _ := manager(t, dir)
	var wg sync.WaitGroup
	for _, m := range []*Manager{a, b} {
		wg.Go(func() {
			for i := range 20 {
				if err := m.AddServer(Server{Label: fmt.Sprintf("%p-%d", m, i), Addr: "127.0.0.1:1"}); err != nil {
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
	if err := a.MarkRead(read); err != nil {
		t.Fatal(err)
	}
	gone := a.Servers()[0].Label
	if err := b.RemoveServer(gone); err != nil {
		t.Fatal(err)
	}
	if b.Unread(read) {
		t.Fatal("the other GUI's read mark is lost")
	}
	env := a.Envs()[1]
	if err := a.MarkRead(&Thread{View: api.Thread{ID: api.NewID(), LastTurn: &api.TurnResult{Ended: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	if a.Envs()[1] != env || a.Servers()[0].Label != gone {
		t.Fatal("marking a thread read made the envs follow gui.json, want only the read mark saved")
	}
	if err := a.AddServer(Server{Label: "late", Addr: "127.0.0.1:1"}); err != nil {
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
	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
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
	if err := m.AddServer(Server{Label: "box", Addr: addr, Token: token}); err != nil {
		t.Fatal(err)
	}
	want := fresh(t, d.Endpoint(), id)
	if want[len(want)-1] != `user "later" held` {
		t.Fatalf("the box's transcript ends %q, want the held message", want[len(want)-1])
	}
	g.until(t, "the missed message", func() bool { return slices.Equal(lines(box.Threads[id].Transcript), want) },
		func() string { return strings.Join(lines(box.Threads[id].Transcript), "\n") })
}
