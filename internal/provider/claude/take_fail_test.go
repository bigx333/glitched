package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
	"glitched/internal/runtime"
	"glitched/internal/store"
)

type shortSecondOpen struct{ *claude.Adapter }

func (a shortSecondOpen) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	if spec.Generation == 2 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 75*time.Millisecond)
		defer cancel()
	}
	return a.Adapter.Open(ctx, spec)
}

func TestAClaudeStillClosingFailsOneTurnAndTheNextResumes(t *testing.T) {
	argv, pids := keep(t, []string{"hi"}, []string{"again-resume"})
	source := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	first, _, _ := strings.Cut(source, "{\"eof\":true}")
	dir := t.TempDir()
	path := filepath.Join(dir, "first-with-slow-close.jsonl")
	if err := os.WriteFile(path, []byte(`{"graceful":"500ms"}`+"\n"+first+`{"sleep":"5s"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envFake, path)
	a := shortSecondOpen{&claude.Adapter{Path: must(os.Executable())}}
	st := must(store.Open(filepath.Join(dir, "db")))
	h := must(runtime.New(ctx, st, provider.Mux{"claude": a}))
	t.Cleanup(func() { h.Close(); a.Close(); st.Close() })
	thread := newThread(t, h, api.AccessAsk)

	firstResult := turn(t, h, thread, firstAsk)
	if firstResult.ended.Outcome != api.OutcomeCompleted {
		t.Fatalf("first turn did not complete: %+v", firstResult.ended)
	}
	submit(t, h, thread, api.ThreadConfigure{Access: new(api.AccessFull)})
	second := turn(t, h, thread, againAsk)
	if second.ended.Outcome != api.OutcomeFailed || !strings.Contains(second.ended.Error, "earlier claude may still run: context deadline exceeded") {
		t.Fatalf("second turn: %+v, want failed with previous-process cleanup context", second.ended)
	}
	if ps := pids("claude"); len(ps) != 1 || gone(ps[0]) {
		t.Fatalf("expected one live process after failed turn, got %v", ps)
	}
	third := turn(t, h, thread, againAsk)
	if third.ended.Outcome != api.OutcomeCompleted || !strings.Contains(third.text, "say hi") {
		t.Fatalf("third turn did not resume: %+v", third)
	}
	if ps := pids("claude"); len(ps) != 2 || !gone(ps[0]) || !resumed(launches(t, argv)) {
		t.Fatalf("third turn did not replace the old Claude with a single session resume, pids=%v", ps)
	}
	t.Logf("second turn failed with %q; third turn completed on a single session resume", second.ended.Error)
}
