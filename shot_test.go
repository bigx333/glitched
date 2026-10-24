package main

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// TestShot writes a PNG of the window: SHOT=out.png, with
// SCENE=empty|running|approval|servers|machines|inbox|checks|refused|plain|stuck
// for the engines' threads (none started without one; ARCHIVE=<title>
// archives one of inbox's first; refused is an archived thread whose
// delete the engine refused; plain is an edit in a workspace with no
// git; stuck is a run left uncertain, with STEER for a
// steer it refused over a draft and DRAFT=<text> for the composer's), PAGE
// for a path, and for what to show: DIFF,
// SPLIT, TERM2, CMDK with CMDKQ=<query>, LIGHT, THEME=<name>,
// RENAME=<title>, ALERT=<title>, MENU=<title> (its items to
// SHOT.menu.txt), HOVER=<text>, FAILED, PICKER=<effort>, FAST and
// STRIKE=30ms, FULL, HOLD=500ms and GRANT=60ms, LEVEL=ask|auto, ATTACH,
// TICK=<turn>, TINT=0..1, PANELS, SCROLLUP, WIDTH=1100,
// GAG=snooze|settle|undo with AT=350ms, and HIDE=60ms for a frame that
// long into hiding the sidebar.
func TestShot(t *testing.T) {
	out := os.Getenv("SHOT")
	if out == "" {
		t.Skip()
	}
	a, tt := scene(t, os.Getenv("SCENE"))
	a.showDiff = os.Getenv("DIFF") != ""
	if os.Getenv("SPLIT") != "" {
		a.showDiff, a.diffLayout = true, editor.DiffSplit
	}
	a.showTerminal = os.Getenv("TERM2") != ""
	a.settings.theme = darkTheme.name
	if os.Getenv("LIGHT") != "" {
		a.settings.theme = lightTheme.name
	}
	if name := os.Getenv("THEME"); name != "" {
		a.settings.theme = name
	}
	if v := os.Getenv("TINT"); v != "" {
		fmt.Sscan(v, &a.settings.vibrancy)
	}
	a.settings.tintPanels = os.Getenv("PANELS") != ""
	if p := os.Getenv("PAGE"); p != "" {
		a.router.Push(p)
	}
	if v := os.Getenv("WIDTH"); v != "" {
		w := 1320
		fmt.Sscan(v, &w)
		tt.SetSize(w, 860)
	}
	if os.Getenv("CMDK") != "" {
		a.openCommandPalette()
		a.cmdkQuery = os.Getenv("CMDKQ")
	}
	if e := os.Getenv("PICKER"); e != "" {
		a.pickerOpen, a.effort, a.fast = true, e, os.Getenv("FAST") != ""
	}
	tt.Frame()
	tt.Advance(time.Second)
	if title := os.Getenv("RENAME"); title != "" {
		// Renaming that thread, after the composer took the first focus.
		for _, it := range a.itemsIn(catInbox) {
			if it.t.View.Title == title {
				a.renaming, a.renameDraft = it.t.id(), "Websocket reconnect loop"
			}
		}
		tt.Frame()
	}
	if title := os.Getenv("ALERT"); title != "" {
		// Asking to delete that thread.
		for _, it := range a.itemsIn(catInbox) {
			if it.t.View.Title == title {
				a.askDelete(it.t)
			}
		}
		tt.Frame()
		tt.Advance(time.Second)
	}
	if title := os.Getenv("MENU"); title != "" {
		// The native menu draws outside the window: its items go beside
		// the picture.
		if err := tt.RightClick(title); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(out+".menu.txt", []byte(strings.Join(tt.Menu(), "\n")+"\n"), 0o644)
	}
	if s := os.Getenv("HOVER"); s != "" {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q to point at", s)
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(time.Second)
	}
	if ms := os.Getenv("HIDE"); ms != "" {
		// Partway through hiding the sidebar.
		d, _ := time.ParseDuration(ms)
		if err := tt.Click("Hide sidebar  ⌘B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(d)
	}
	if k := os.Getenv("TICK"); k != "" {
		// The pointer on the turn marker of that index.
		es := entriesOf(a.current())
		turns := turnsOf(es)
		i := int(k[0] - '0')
		r, ok := tt.Find("Go to: " + preview(es[turns[i].entry]))
		if !ok {
			t.Fatal("no marker")
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(500 * time.Millisecond)
	}
	if os.Getenv("SCROLLUP") != "" {
		a.listFor(a.current().id()).ScrollTo(0, ui.Start)
		tt.Advance(time.Second)
	}
	if os.Getenv("ATTACH") != "" {
		a.attachFiles([]string{"resources/icon.png", "go.mod"})
		a.onPaste(strings.Repeat("log line\n", 400))
		tt.Frame()
		tt.Advance(500 * time.Millisecond)
	}
	if os.Getenv("FULL") != "" {
		a.access = accessFull
	}
	if ms := os.Getenv("HOLD"); ms != "" {
		// That far into holding Full access, its popover open.
		d, _ := time.ParseDuration(ms)
		a.accessOpen = true
		tt.Frame()
		r, _ := tt.Find(accessFull)
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Press(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(d)
	}
	if ms := os.Getenv("GRANT"); ms != "" {
		// That far into the shake after Full access was granted.
		d, _ := time.ParseDuration(ms)
		a.access, a.accessAt = accessFull, tt.Now()
		tt.Frame()
		tt.Advance(d)
	}
	if lv := os.Getenv("LEVEL"); lv != "" {
		// 300ms into the effect of switching to an access level.
		a.access, a.accessAt = map[string]string{"ask": accessAsk, "auto": accessAuto}[lv], tt.Now()
		tt.Frame()
		tt.Advance(300 * time.Millisecond)
	}
	if g := os.Getenv("GAG"); g != "" && len(a.itemsIn(catInbox)) > 0 {
		// 350ms into an inbox action's joke, on the inbox's first thread.
		th := a.itemsIn(catInbox)[0].t
		switch g {
		case "snooze":
			a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Snoozed, Until: morning(tt.Now(), 1)})
			a.rowFx[th.id()] = newGag(gagSnooze, tt.Now(), catInbox)
		case "settle":
			a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Settled})
			a.rowFx[th.id()] = newGag(gagSettle, tt.Now(), catInbox)
		case "undo":
			a.rowFx[th.id()] = newGag(gagUndo, tt.Now(), catSnoozed)
		}
		tt.Frame()
		d, _ := time.ParseDuration(os.Getenv("AT"))
		tt.Advance(d)
	}
	if os.Getenv("FAILED") != "" {
		// The shown thread's settle and a message, failed.
		th := a.current()
		boom := errors.New("client: not connected to the engine")
		th.Pending = append(th.Pending,
			client.Pending{Command: api.NewID(), Body: api.MessageDispatch{Text: "Also check the staging keys."}, Err: boom},
			client.Pending{Command: api.NewID(), Body: api.InboxSet{Disposition: api.Disposition{State: api.Settled}}, Err: boom})
		tt.Frame()
		tt.Advance(time.Second)
	}
	if ms := os.Getenv("STRIKE"); ms != "" {
		// That far into fast mode's lightning strike.
		d, _ := time.ParseDuration(ms)
		a.fast, a.fastAt = true, tt.Now()
		tt.Frame()
		tt.Advance(d)
	}
	f, _ := os.Create(out)
	defer f.Close()
	png.Encode(f, tt.Image())
}

// scene is the app showing what SCENE names, from engines the test runs.
func scene(t *testing.T, name string) (*app, *ui.Tester) {
	if name == "" {
		a := newApp(t.TempDir(), func(func()) {})
		return a, ui.NewTester(a.view, 1320, 860)
	}
	var h *harness
	switch name {
	case "empty":
		h = start(t, tempDir(t))
	case "running":
		dir := tempDir(t)
		d := engine(t, daemon.Config{Dir: dir}, stalled{fake.New(midTurn), len(midTurn)})
		seed(t, d, "/work/mygo", "Markdown table column alignment")
		seed(t, d, "/work/agent-phone", "SIP re-INVITE handling")
		id := seed(t, d, "/work/t3code", "Sort sidebar threads by recent activity")
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id,
			Body: api.MessageDispatch{Text: "Sort the sidebar's threads by recent activity, newest first."}}); err != nil {
			t.Fatal(err)
		}
		h = start(t, dir)
		h.a.select_(h.thread("Sort sidebar threads by recent activity"))
		h.until("the turn mid-edit", func() bool { return h.tt.HasText("Edit apps/web/src/components/Sidebar.tsx") })
	case "approval":
		h, _ = asking(t, runTests)
	case "servers":
		rdir := tempDir(t)
		token, err := daemon.Pair(rdir, "gui")
		if err != nil {
			t.Fatal(err)
		}
		d := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
		addr := d.Addrs()[0].String()
		dir := tempDir(t)
		// Saved as they were when added: the token since revoked, the
		// address since pointed at devbox.
		listServers(t, dir,
			client.Server{Label: "devbox", Addr: addr, Token: token},
			client.Server{Label: "stranger", Addr: addr, Token: "wrong"},
			client.Server{Label: "devbox-dns", Addr: addr, Token: token},
			client.Server{Label: "upgraded", Addr: newer(t), Token: "any"})
		h = start(t, dir)
		h.a.router.Push("/settings/servers")
		h.until("one online, one refused, one a duplicate and one incompatible", func() bool {
			return h.tt.HasText("Online") && h.tt.HasText("Refused") && h.tt.HasText("Same engine as devbox — remove one") && h.tt.HasText("Incompatible")
		})
		h.a.server = client.Server{Label: "devbox-ip", Addr: addr, Token: token}
		h.tt.Frame()
		h.click("Add server")
		h.until("why devbox-ip wasn't added", func() bool { return h.tt.HasText("already listed as devbox") })
	case "inbox":
		h = start(t, inboxDir(t))
		h.thread("SIP re-INVITE handling")
		if title := os.Getenv("ARCHIVE"); title != "" {
			th := h.thread(title)
			h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Archived})
			h.until("the archive", func() bool { return th.View.Disposition.State == api.Archived })
		}
		h.tt.Advance(time.Second)
	case "refused":
		var th thread
		h, th = asking(t, runTests)
		h.a.backend.SetDisposition(th.env, th.id(), api.Disposition{State: api.Archived})
		h.until("the archive", func() bool { return th.View.Disposition.State == api.Archived && len(th.Pending) == 0 })
		h.a.delete(th)
		h.until("the refusal", func() bool { return len(th.Pending) == 1 && th.Pending[0].Err != nil })
	case "checks":
		h, _ = checked(t)
		if os.Getenv("DIFF") != "" {
			h.click("Edit " + sidebarPath)
			h.diffsRead()
		}
	case "plain":
		// An edit in a workspace with no git, whose HEAD is unknown.
		ws := filepath.Join(t.TempDir(), "notes")
		if err := os.MkdirAll(ws, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, "plan.md"), []byte("# Plan\n\nShip the sidebar sort.\nThen the README.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := tempDir(t)
		d := engine(t, daemon.Config{Dir: dir}, fake.New([]provider.Event{
			provider.Work{Item: "e", Kind: "edit", Title: "Edit plan.md", Status: api.WorkCompleted, Path: "plan.md"},
			provider.TextDelta{Text: "Added the README to the plan."},
		}))
		seed(t, d, ws, "Plan the week", "Add the README to the plan.")
		h = start(t, dir)
		h.a.select_(h.thread("Plan the week"))
		h.until("its transcript", func() bool { return h.tt.HasText("Added the README to the plan.") })
		h.tt.Advance(time.Second)
		h.click("Edit plan.md")
		h.diffsRead()
	case "stuck":
		// A run left uncertain, which refuses a steer.
		h = start(t, uncertainDir(t))
		th := h.thread("Stuck")
		h.a.select_(th)
		h.until("its transcript", func() bool { return h.tt.HasText("May I edit it?") })
		if os.Getenv("STEER") != "" {
			h.a.draft = "Keep going."
			h.a.send(th)
			h.a.draft = "And then the README."
			h.until("the refusal", func() bool { h.tt.Frame(); return len(th.Pending) == 1 && th.Pending[0].Err != nil })
		}
		if d := os.Getenv("DRAFT"); d != "" {
			h.a.draft = d
		}
		h.tt.Frame()
		h.tt.Advance(time.Second)
	case "machines":
		h = machines(t)
		h.a.select_(h.thread("Rotate the API keys"))
		h.until("the server's transcript", func() bool { return h.tt.HasText("Which keys are older than 90 days?") })
	default:
		t.Fatalf("no scene %q", name)
	}
	return h.a, h.tt
}

// midTurn is a turn that stops mid-edit, as stalled holds it.
var midTurn = []provider.Event{
	provider.TextDelta{Text: "The threads come from `useThreads(projectId)`, in the order they were created. "},
	provider.TextDelta{Text: "I'll sort a copy by `updatedAt` inside `useMemo`, so the store's array isn't mutated in render."},
	provider.Work{Item: "search-1", Kind: "search", Title: "Searched useThreads in apps/web/src", Status: api.WorkCompleted},
	provider.Work{Item: "read-1", Kind: "read", Title: "Read apps/web/src/hooks/useThreads.ts", Status: api.WorkCompleted},
	provider.Work{Item: "edit-1", Kind: "edit", Title: "Edit apps/web/src/components/Sidebar.tsx", Status: api.WorkStarted},
}

// stalled opens the fake's agents, which play the first n events of a
// turn and then work on until interrupted.
type stalled struct {
	*fake.Adapter
	n int
}

func (s stalled) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	g, err := s.Adapter.Open(ctx, spec)
	return &stalledAgent{Agent: g, left: s.n, stop: make(chan struct{})}, err
}

type stalledAgent struct {
	provider.Agent
	left int // events still to play; only Next's reader counts them
	once sync.Once
	stop chan struct{}
}

func (g *stalledAgent) Next(ctx context.Context) (provider.Event, error) {
	if g.left == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-g.stop:
		}
	} else {
		g.left--
	}
	return g.Agent.Next(ctx)
}

func (g *stalledAgent) Do(ctx context.Context, act provider.Action) error {
	if _, ok := act.(provider.Interrupt); ok {
		g.once.Do(func() { close(g.stop) })
	}
	return g.Agent.Do(ctx, act)
}
