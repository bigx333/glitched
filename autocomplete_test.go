package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

// skills is an adapter whose sessions have cmds, as Claude Code's have the
// user's skills and the plugins'.
type skills struct {
	provider.Adapter
	cmds []provider.Command
}

func (s skills) Commands(context.Context, string) ([]provider.Command, error) { return s.cmds, nil }

// silent is an adapter that can't list its commands.
type silent struct{ provider.Adapter }

// broken is an adapter whose every listing fails, counting them.
type broken struct {
	provider.Adapter
	asked *atomic.Int32
}

func (b broken) Commands(context.Context, string) ([]provider.Command, error) {
	b.asked.Add(1)
	return nil, errors.New("claude: not found")
}

func labels(ss []suggestion) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.label)
	}
	return out
}

// many is more commands than the flyout shows at once.
var many = func() []provider.Command {
	var out []provider.Command
	for i := range 20 {
		out = append(out, provider.Command{Name: fmt.Sprintf("skill-%02d", i)})
	}
	return out
}()

// slashEngine is the app on an engine whose providers each list their own
// commands, codex none, with a thread on each.
func slashEngine(t *testing.T) *harness {
	t.Helper()
	dir := tempDir(t)
	d, err := daemon.Start(context.Background(), daemon.Config{Dir: dir, Hub: true, Providers: provider.Mux{
		"fake": fake.New(nil),
		"claude": skills{fake.New(nil), []provider.Command{
			{Name: "compact", Description: "Free up context by summarizing the conversation so far"},
			{Name: "pstack:poteto-mode", Description: "(pstack) poteto's agent style for concise, detailed responses\nUse for /poteto-mode."},
		}},
		"codex":  silent{fake.New(nil)},
		"gemini": skills{fake.New(nil), many},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for title, p := range map[string]string{"On the fake": "fake", "On Claude": "claude", "On Codex": "codex", "On Gemini": "gemini"} {
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(),
			Body: api.ThreadCreate{Workspace: "/work/t3code", Title: title, Model: api.ModelRef{Provider: p, Model: p}}}); err != nil {
			t.Fatal(err)
		}
	}
	return start(t, dir)
}

// / at the start of a line suggests the commands of the thread's
// provider, as its engine lists them, saying so while it lists.
func TestSlashSuggestsTheThreadsProviderCommands(t *testing.T) {
	h := slashEngine(t)
	h.a.select_(h.thread("On the fake"))
	h.tt.Advance(time.Second)
	h.tt.Type("/compact")
	h.tt.Frame()
	if got := labels(h.a.suggestions); !slices.Equal(got, []string{"Listing the agent's commands…"}) {
		t.Fatalf("before the engine answers: %q", got)
	}
	// The note isn't taken: Enter sends the draft as typed.
	h.tt.Key(0, ui.KeyEnter)
	h.until("/compact sent", func() bool { return slices.Equal(sentTexts(h), []string{"/compact"}) })
	h.tt.Type("/")
	h.tt.Frame()
	h.until("the fake's commands", func() bool {
		return slices.Equal(labels(h.a.suggestions), []string{"/compact", "/review", "/fake:tidy"})
	})
	h.tt.Type("ti")
	h.tt.Frame()
	if got := labels(h.a.suggestions); !slices.Equal(got, []string{"/fake:tidy"}) {
		t.Fatalf("/ti: %q, want the plugin's tidy", got)
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Advance(100 * time.Millisecond)
	if h.a.draft != "/fake:tidy " || len(sentTexts(h)) != 1 {
		t.Errorf("draft %q, sent %q; want the command taken, nothing more sent", h.a.draft, sentTexts(h))
	}

	h.a.select_(h.thread("On Claude"))
	h.a.draft = "/"
	h.tt.Frame()
	h.until("Claude's commands", func() bool {
		return slices.Equal(labels(h.a.suggestions), []string{"/compact", "/pstack:poteto-mode"})
	})
	h.a.draft = "/pot"
	h.tt.Frame()
	if len(h.a.suggestions) != 1 || h.a.suggestions[0].insert != "/pstack:poteto-mode " || h.a.suggestions[0].detail != "poteto's agent style for concise, detailed responses" {
		t.Fatalf("/pot: %+v", h.a.suggestions)
	}

	h.a.select_(h.thread("On Codex"))
	h.a.draft = "/"
	h.tt.Frame()
	h.until("codex, which lists none, to suggest none", func() bool { return len(h.a.suggestions) == 0 })
}

// A server's threads suggest the commands Claude Code takes, as this
// machine's socket doesn't reach the server's engine to list its own.
func TestSlashOnAServersThreadSuggestsTheFallback(t *testing.T) {
	h := machines(t)
	h.a.select_(h.thread("Retry failed invoice webhooks"))
	h.a.draft = "/"
	h.tt.Frame()
	if got := labels(h.a.suggestions); !slices.Equal(got, []string{"/model", "/compact", "/clear", "/init"}) {
		t.Fatalf("a server's thread: %q", got)
	}
}

// The flyout shows suggestRows of a long list, and the arrows scroll the
// choice into view.
func TestTheFlyoutScrollsToTheChoice(t *testing.T) {
	h := slashEngine(t)
	h.a.select_(h.thread("On Gemini"))
	h.tt.Advance(time.Second)
	h.tt.Type("/")
	h.tt.Frame()
	h.until("gemini's commands", func() bool { return len(h.a.suggestions) == len(many) })
	fly := ui.NewTester(h.a.suggestView, 460, suggestRows*suggestRow+12)
	fly.Frame()
	if !slices.Contains(fly.Texts(), "/skill-00") || slices.Contains(fly.Texts(), "/skill-12") {
		t.Fatalf("the flyout at first shows %q", fly.Texts())
	}
	for range 12 {
		h.tt.Key(0, ui.KeyDown)
	}
	fly.Frame()
	if h.a.suggestIndex != 12 || !slices.Contains(fly.Texts(), "/skill-12") || slices.Contains(fly.Texts(), "/skill-00") {
		t.Fatalf("after twelve Downs the choice is %d and the flyout shows %q", h.a.suggestIndex, fly.Texts())
	}
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
	if h.a.draft != "/skill-12 " {
		t.Errorf("draft %q, want the twelfth taken", h.a.draft)
	}
}

// A listing that fails is asked again only once commandsRecheck passes,
// not on each rebuild its answer causes while / stays open.
func TestAFailedListingIsNotAskedAgainAtOnce(t *testing.T) {
	dir := tempDir(t)
	var asked atomic.Int32
	d, err := daemon.Start(context.Background(), daemon.Config{Dir: dir, Hub: true, Providers: provider.Mux{
		"claude": broken{fake.New(nil), &asked},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: api.NewID(),
		Body: api.ThreadCreate{Workspace: "/work/t3code", Title: "On Claude", Model: api.ModelRef{Provider: "claude", Model: "claude"}}}); err != nil {
		t.Fatal(err)
	}
	h := start(t, dir)
	h.a.select_(h.thread("On Claude"))
	h.tt.Advance(time.Second)
	h.tt.Type("/")
	h.tt.Frame()
	h.until("the error row", func() bool {
		return slices.Contains(labels(h.a.suggestions), "Couldn't list the agent's commands")
	})
	for range 20 {
		h.tt.Advance(50 * time.Millisecond)
		h.tt.Frame()
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("a failed listing was asked %d times while / stayed open, want once", n)
	}
	if got := labels(h.a.suggestions); !slices.Contains(got, "/model") {
		t.Errorf("after a failure: %q, want the fallback with the error", got)
	}
}

// Files of one name in two folders are two rows of the flyout, each its
// own: clicking one takes that file.
func TestTheFlyoutKeepsFilesOfOneNameApart(t *testing.T) {
	ws := t.TempDir()
	for _, f := range []string{"src/index.ts", "tests/index.ts"} {
		if err := os.MkdirAll(filepath.Join(ws, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := offline(t)
	a.fresh.workspace = ws
	tt := ui.NewTester(a.view, 1320, 860)
	a.draft = "@index"
	tt.Frame()
	if len(a.suggestions) != 2 {
		t.Fatalf("suggestions for @index: %+v", a.suggestions)
	}
	fly := ui.NewTester(a.suggestView, 460, suggestRows*suggestRow+12)
	fly.Frame()
	if got := fly.Texts(); !slices.Contains(got, "src") || !slices.Contains(got, "tests") {
		t.Fatalf("the flyout shows %q, want both folders", got)
	}
}
