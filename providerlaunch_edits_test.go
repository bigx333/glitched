package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// heldVersion is a claude whose --version, for a launch with holdOn among
// its arguments, waits once until released.
type heldVersion struct {
	placing
	holdOn  string
	hold    atomic.Bool
	begun   chan struct{}
	release chan struct{}
}

func (p *heldVersion) Version(ctx context.Context, prog provider.Program) (string, error) {
	if slices.Contains(prog.Args, p.holdOn) && p.hold.CompareAndSwap(true, false) {
		close(p.begun)
		select {
		case <-p.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "fake 1", nil
}

// heldRow is Claude's row on this machine, whose engine keeps --old, with
// the next --version of a launch with holdOn held.
func heldRow(t *testing.T, holdOn string) (*harness, launchKey, *heldVersion, func()) {
	dir := tempDir(t)
	p := &heldVersion{holdOn: holdOn, begun: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(p.release) }) }
	t.Cleanup(release)
	l := provider.Launch{Path: demoProgram(t), Args: "--old"}
	if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.Launch = map[string]provider.Launch{"claude": l}; return nil }); err != nil {
		t.Fatal(err)
	}
	h := startWith(t, dir, func(c *daemon.Config) {
		engineReports("claude", provider.Model{ID: "claude-opus-5-5", Name: "Opus"})(c)
		p.Adapter = c.Providers["claude"]
		c.Providers["claude"] = p
	})
	allAnswered(h)
	openProviders(h, "claude")
	h.until("the launch settings", func() bool { return h.tt.HasText("CLAUDE_CONFIG_DIR") })
	p.hold.Store(true)
	return h, launchKey{h.local().ID, "claude"}, p, release
}

func begun(t *testing.T, p *heldVersion) {
	t.Helper()
	// Not h.until: nothing posts to the app while the check is held, so
	// a wait for the app's next wake would last until the check times out.
	select {
	case <-p.begun:
	case <-time.After(30 * time.Second):
		t.Fatal("the --version check never began")
	}
}

// An answer the engine gave before a save doesn't replace the save's.
func TestALaunchReadFromBeforeASaveIsDropped(t *testing.T) {
	h, k, p, release := heldRow(t, "--old")
	s := h.a.launchState()
	r := s.read[k.engine]
	r.at = time.Time{}
	h.tt.Frame()
	begun(t, p)
	next := r.infos[k.provider].Launch
	next.Args = "--new"
	s.drafts[k] = next
	h.tt.Frame()
	h.click("Save")
	h.until("the save", func() bool { return r.infos[k.provider].Launch == next })
	release()
	h.until("the older read", func() bool { return !r.asking })
	if shown := r.infos[k.provider].Launch; shown != next {
		t.Errorf("the row shows %+v after the save of %+v", shown, next)
	}
}

// What is typed while a save is checked stays a draft once it's kept.
func TestTypingWhileASaveIsCheckedKeepsTheNewDraft(t *testing.T) {
	h, k, p, release := heldRow(t, "--new")
	s := h.a.launchState()
	next := s.read[k.engine].infos[k.provider].Launch
	next.Args = "--new"
	s.drafts[k] = next
	h.tt.Frame()
	h.click("Save")
	begun(t, p)
	newer := next
	newer.Args = "--newer"
	s.drafts[k] = newer
	h.tt.Frame()
	release()
	h.until("the save", func() bool { return !s.saving[k] })
	if got := s.drafts[k]; got != newer {
		t.Errorf("the draft is %+v, want %+v", got, newer)
	}
	if got := s.read[k.engine].infos[k.provider].Launch; got != next {
		t.Errorf("the engine's launch shows as %+v, want %+v", got, next)
	}
}

// Revert drops what the engine found wrong with the draft too.
func TestRevertDropsTheRefusal(t *testing.T) {
	h, k, _, _ := heldRow(t, "--never")
	msg := "--model is set by Glitched itself"
	h.a.launchState().drafts[k] = provider.Launch{Args: "--model opus"}
	h.tt.Frame()
	h.click("Save")
	h.until("the refusal", func() bool { return h.tt.HasText(msg) })
	h.click("Revert")
	h.tt.Frame()
	if h.tt.HasText(msg) {
		t.Errorf("after Revert the row still says %q", msg)
	}
}

// A server re-paired to another engine shows that engine's launch, and a
// draft for the old engine isn't saved on the new one.
func TestARePairedServersDraftStaysWithItsEngine(t *testing.T) {
	h, _, _, _ := heldRow(t, "--never")
	remote := func(args string) (client.Server, string) {
		dir := tempDir(t)
		token, err := daemon.Pair(dir, "gui")
		if err != nil {
			t.Fatal(err)
		}
		l := provider.Launch{Path: demoProgram(t), Args: args}
		if _, err := daemon.Edit(dir, func(c *daemon.Config) error { c.Launch = map[string]provider.Launch{"claude": l}; return nil }); err != nil {
			t.Fatal(err)
		}
		d, err := startEngine(t.Context(), daemon.Config{Dir: dir, Hub: true, Listen: []string{"127.0.0.1:0"}, Providers: provider.Mux{"claude": &placing{Adapter: fake.New(nil)}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return client.Server{Label: "devbox", Addr: d.Addrs()[0].String(), Token: token}, dir
	}
	first, _ := remote("--first")
	second, secondDir := remote("--second")
	if err := h.addServer(first); err != nil {
		t.Fatal(err)
	}
	h.until("devbox online", func() bool { return len(h.a.launchMachines()) == 2 })
	h.click("devbox")
	h.until("devbox's launch", func() bool { return h.tt.HasText("--first") })
	e := h.a.launchEnv()
	old := launchKey{e.ID, "claude"}
	s := h.a.launchState()
	draft := s.read[old.engine].infos["claude"].Launch
	draft.Args = "--entered-for-first"
	s.drafts[old] = draft
	if err := h.addServer(second); err != nil {
		t.Fatal(err)
	}
	h.until("the second engine's launch", func() bool { return e.ID != old.engine && h.tt.HasText("--second") })
	if h.tt.HasText("--entered-for-first") {
		t.Errorf("the second engine's row shows the first's draft")
	}
	h.a.saveLaunch(e, old, draft)
	h.until("the save", func() bool { return !s.saving[old] })
	if !strings.Contains(s.failed[old], "another engine") {
		t.Errorf("the old engine's save: %q", s.failed[old])
	}
	if got := launchesOn(t, secondDir)["claude"]; got.Args != "--second" {
		t.Errorf("the second engine keeps %+v", got)
	}
}

// The command a row shows runs as shown, a folder with spaces in it too.
func TestTheShownCommandRunsInAShell(t *testing.T) {
	line := shellLine([]string{"CODEX_HOME=/tmp/config folder"}, []string{"/usr/bin/printenv", "CODEX_HOME"})
	out, err := exec.Command("/bin/sh", "-c", line).CombinedOutput()
	if err != nil || string(out) != "/tmp/config folder\n" {
		t.Errorf("%s: %q, %v", line, out, err)
	}
}
