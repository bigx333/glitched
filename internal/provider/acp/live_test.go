//go:build acplive

package acp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// These run the user's own agy, signed in, with the acplive tag. Describe
// sends no prompt; TestLiveTurn sends one.

func TestLiveDescribe(t *testing.T) {
	a := &Adapter{Profile: Antigravity}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	info, err := a.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	t.Logf("in %v:\n%s", time.Since(start), b)
	if len(info.Models) == 0 {
		t.Fatal("no models")
	}
}

func TestLiveTurn(t *testing.T) {
	if os.Getenv("ACP_LIVE_PROMPT") == "" {
		t.Skip("set ACP_LIVE_PROMPT to spend a prompt")
	}
	a := &Adapter{Profile: Antigravity}
	defer a.Close()
	dir := t.TempDir()
	os.WriteFile(dir+"/hello.txt", []byte("hello\n"), 0o644)
	s := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: dir,
		Model: api.ModelRef{Provider: "antigravity", Model: os.Getenv("ACP_LIVE_MODEL"), Effort: os.Getenv("ACP_LIVE_EFFORT")}}
	for i, text := range []string{os.Getenv("ACP_LIVE_PROMPT"), os.Getenv("ACP_LIVE_PROMPT2")} {
		if text == "" {
			break
		}
		start := time.Now()
		g := open(t, a, s)
		t.Logf("turn %d open in %v", i, time.Since(start))
		do(t, g, provider.Start{Text: text})
		first := time.Duration(0)
		for {
			e := next(t, g)
			if _, ok := e.(provider.TextDelta); ok && first == 0 {
				first = time.Since(start)
			}
			if r, ok := e.(provider.RequestOpened); ok {
				t.Logf("asked %s", r.Body)
				do(t, g, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true}`)})
			}
			t.Logf("%T %+v", e, e)
			if _, ok := e.(provider.TurnEnded); ok {
				break
			}
		}
		t.Logf("turn %d first text after %v, all in %v", i, first, time.Since(start))
		closeRun(t, g)
	}
}

// TestLiveUnknownSession sends no prompt: agy is asked to resume a session
// it never had, and the run starts a new one.
func TestLiveUnknownSession(t *testing.T) {
	s := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "antigravity"}, Session: "00000000-0000-0000-0000-000000000000"}
	a := &Adapter{Profile: Antigravity}
	defer a.Close()
	start := time.Now()
	g := open(t, a, s)
	defer closeRun(t, g)
	got := g.(provider.Sessioned).Session()
	t.Logf("open in %v, bound %s", time.Since(start), got)
	if got == "" || got == s.Session {
		t.Fatalf("bound %q", got)
	}
}

// TestLiveResume spends two prompts: the first asks agy to remember a
// word, and the second, on a new agy that resumes the session, asks for
// it. Between them it measures resume alone, which sends no prompt, five
// times, and what agy says of a session it doesn't have.
func TestLiveResume(t *testing.T) {
	if os.Getenv("ACP_LIVE_RESUME") == "" {
		t.Skip("set ACP_LIVE_RESUME to spend two prompts")
	}
	model := api.ModelRef{Provider: "antigravity", Model: os.Getenv("ACP_LIVE_MODEL"), Effort: os.Getenv("ACP_LIVE_EFFORT")}
	s := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(), Model: model}
	ask := func(a *Adapter, s provider.SessionSpec, prompt string) (string, provider.Agent) {
		t.Helper()
		start := time.Now()
		g := open(t, a, s)
		opened := time.Since(start)
		do(t, g, provider.Start{Text: prompt})
		events := turn(t, g)
		t.Logf("%q: open %v, turn %v, session %s", prompt, opened, time.Since(start), g.(provider.Sessioned).Session())
		closeRun(t, g)
		return text(events), g
	}
	a := &Adapter{Profile: Antigravity}
	said, g := ask(a, s, "Remember the word kumquat. Reply with OK only.")
	t.Logf("said %q", said)
	a.Close()
	s.Session = g.(provider.Sessioned).Session()
	for i := range 5 {
		b := &Adapter{Profile: Antigravity}
		start := time.Now()
		g := open(t, b, s)
		t.Logf("resume %d: open in %v", i, time.Since(start))
		closeRun(t, g)
		b.Close()
	}
	c := &Adapter{Profile: Antigravity}
	defer c.Close()
	s.Run, s.Generation = api.NewID(), 2
	said, _ = ask(c, s, "What word did I ask you to remember? Reply with the word only.")
	if !strings.Contains(strings.ToLower(said), "kumquat") {
		t.Fatalf("after a resume agy says %q", said)
	}
}

// TestLiveModes sends no prompt: agy starts at each access level as the
// adapter starts it, takes the level's mode, and opens a session.
func TestLiveModes(t *testing.T) {
	for _, access := range []api.Access{api.AccessAsk, api.AccessAutoEdits, api.AccessFull} {
		a := &Adapter{Profile: Antigravity}
		s := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
			Model: api.ModelRef{Provider: "antigravity"}, Access: access}
		g := open(t, a, s)
		p := g.(*Agent).p
		p.mu.Lock()
		mode := p.mode
		p.mu.Unlock()
		t.Logf("access %v: argv %q, mode %q", access, p.cmd.Args[1:], mode)
		if want := Antigravity.Access[access].Mode; want != "" && mode != want {
			t.Errorf("access %v runs in mode %q, want %q", access, mode, want)
		}
		closeRun(t, g)
		a.Close()
	}
}

// TestLiveDenyUnderFull spends a prompt: at Full access, with a deny rule
// for rm in the user's agy settings (which the caller sets), agy is asked
// to delete a file with rm, and the file must survive. Any request that
// still reaches Glitched is denied and logged.
func TestLiveDenyUnderFull(t *testing.T) {
	if os.Getenv("ACP_LIVE_DENY") == "" {
		t.Skip("set ACP_LIVE_DENY, with command(rm) in agy's deny list, to spend a prompt")
	}
	a := &Adapter{Profile: Antigravity}
	defer a.Close()
	dir := t.TempDir()
	scratch := dir + "/scratch.txt"
	os.WriteFile(scratch, []byte("keep me\n"), 0o644)
	access := api.AccessFull
	if os.Getenv("ACP_LIVE_DENY") == "ask" {
		access = api.AccessAsk
	}
	s := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: dir,
		Model: api.ModelRef{Provider: "antigravity", Model: os.Getenv("ACP_LIVE_MODEL")}, Access: access}
	g := open(t, a, s)
	defer closeRun(t, g)
	t.Logf("argv %q", g.(*Agent).p.cmd.Args[1:])
	do(t, g, provider.Start{Text: "Delete the file scratch.txt in this folder by running exactly the shell command: rm scratch.txt\nDo not use any other tool or command to delete it. Then say what happened."})
	asked := 0
	for {
		e := next(t, g)
		switch e := e.(type) {
		case provider.RequestOpened:
			asked++
			t.Logf("asked %s", e.Body)
			do(t, g, provider.Respond{Request: e.ID, Answer: json.RawMessage(`{"allow":false}`)})
		case provider.Work:
			t.Logf("step %s %q %v exit %v: %q", e.Kind, e.Title, e.Status, e.Exit, e.Output)
		case provider.TextDelta:
			t.Logf("text %q", e.Text)
		case provider.TurnEnded:
			t.Logf("ended %+v, %d requests reached Glitched", e, asked)
			if _, err := os.Stat(scratch); err != nil {
				t.Fatalf("scratch.txt is gone at access %v with rm denied: %v", access, err)
			}
			return
		}
	}
}
