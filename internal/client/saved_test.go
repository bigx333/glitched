package client

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"glitched/internal/api"
	"glitched/internal/daemon"
)

func TestRemoteServersShowTheirThreadsAndComeBack(t *testing.T) {
	rdir := stateDir(t)
	token, err := daemon.Pair(rdir, "gui")
	if err != nil {
		t.Fatal(err)
	}
	d := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}})
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
	engine(t, daemon.Config{Dir: rdir, Listen: []string{addr}})
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
