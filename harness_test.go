package main

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// posts stands in for the window's Update: the manager posts here, and the
// test runs what it posted on its own goroutine, between frames.
type posts struct {
	mu   sync.Mutex
	q    []func()
	wake chan struct{}
}

func (p *posts) post(fn func()) {
	p.mu.Lock()
	p.q = append(p.q, fn)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// run runs what was posted, and reports whether there was any.
func (p *posts) run() bool {
	p.mu.Lock()
	q := p.q
	p.q = nil
	p.mu.Unlock()
	for _, fn := range q {
		fn()
	}
	return len(q) > 0
}

// offline is the app before its manager starts: no machines, no threads.
func offline(t *testing.T) *app {
	return newApp(t.TempDir(), func(func()) {})
}

// harness is the app on the engines its state directory reaches, with its
// manager started.
type harness struct {
	t  *testing.T
	a  *app
	tt *ui.Tester
	p  *posts
}

func start(t *testing.T, dir string) *harness {
	t.Helper()
	p := &posts{wake: make(chan struct{}, 1)}
	a := newApp(dir, p.post)
	if err := a.backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.backend.Close() })
	h := &harness{t, a, ui.NewTester(a.view, 1320, 860), p}
	h.until("this machine's engine", func() bool { return a.backend.Envs()[0].Status == client.Status(client.Online{}) })
	return h
}

// until runs what the manager posts, a frame after each batch, until ok
// holds.
func (h *harness) until(what string, ok func() bool) {
	h.t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if h.p.run() {
			h.tt.Frame()
		}
		if ok() {
			return
		}
		select {
		case <-h.p.wake:
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; the window shows %q", what, h.tt.Texts())
		}
	}
}

func (h *harness) local() *client.Env { return h.a.backend.Envs()[0] }

// thread is the thread titled title, on whichever machine.
func (h *harness) thread(title string) thread {
	h.t.Helper()
	var found thread
	h.until("a thread titled "+title, func() bool {
		for _, p := range h.a.gather() {
			for _, t := range p.threads {
				if t.View.Title == title {
					found = t
					return true
				}
			}
		}
		return false
	})
	return found
}

// hover points at the text's middle.
func (h *harness) hover(text string) {
	h.t.Helper()
	r, ok := h.tt.Find(text)
	if !ok {
		h.t.Fatalf("no %q to point at in %q", text, h.tt.Texts())
	}
	h.tt.Move(r.X+r.W/2, r.Y+r.H/2)
}

func (h *harness) click(text string) {
	h.t.Helper()
	if err := h.tt.Click(text); err != nil {
		h.t.Fatal(err)
	}
}

// tempDir is short: unix socket paths are limited to about 100 bytes.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// engine runs an engine whose agents adapter opens, for the app to attach
// to or reach.
func engine(t *testing.T, cfg daemon.Config, adapter provider.Adapter) *daemon.Daemon {
	t.Helper()
	cfg.Hub, cfg.Adapter = true, adapter
	d, err := daemon.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

var fakeModel = api.ModelRef{Provider: "fake", Model: "fake"}

// seed creates a thread on the engine and sends it messages, returning
// once every turn they started has ended.
func seed(t *testing.T, d *daemon.Daemon, workspace, title string, messages ...string) api.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep, id := d.Endpoint(), api.NewID()
	bodies := []api.Intent{api.ThreadCreate{Workspace: workspace, Title: title, Model: fakeModel}}
	for _, m := range messages {
		bodies = append(bodies, api.MessageDispatch{Text: m, Mode: api.AfterActive})
	}
	for _, b := range bodies {
		if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	for len(messages) > 0 {
		snap, err := ep.Read(ctx, api.Query{Thread: id})
		if err != nil {
			t.Fatal(err)
		}
		if v := snap.Threads[0]; v.LastTurn != nil && v.Run == api.RunIdle && v.Queued == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return id
}

// inbox is threads in three projects on this machine, as an engine in dir
// holds them: some with a finished turn, unread, the others never sent to.
func inbox(t *testing.T, dir string) *daemon.Daemon {
	t.Helper()
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "Fix terminal resize flicker on Windows", "Why does it flicker?")
	seed(t, d, "/work/t3code", "Add keyboard shortcut for new thread")
	seed(t, d, "/work/t3code", "Investigate websocket reconnect loop", "Why does it loop?")
	seed(t, d, "/work/mygo", "Markdown table column alignment")
	seed(t, d, "/work/agent-phone", "SIP re-INVITE handling")
	return d
}

// inboxDir is a state directory whose engine holds inbox's threads.
func inboxDir(t *testing.T) string {
	dir := tempDir(t)
	inbox(t, dir)
	return dir
}

// texts is what the timeline shows of a thread's entries, for a failure.
func texts(es []entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.key+" "+e.text+e.took+e.note)
	}
	return strings.Join(out, "\n")
}
