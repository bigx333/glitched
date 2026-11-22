//go:build acplive

package acp

import (
	"context"
	"encoding/json"
	"os"
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
