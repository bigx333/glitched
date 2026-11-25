package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

// hiAt is the uuid of the assistant message hi.jsonl ends its turn with.
const hiAt = "7abb9ac7-137e-45fe-87a3-52eaa2c15b83"

// prompts taps the user messages claude is sent, by uuid, in order, for
// every run of every claude the tap goes to.
type prompts struct {
	mu   sync.Mutex
	sent []string
}

func (p *prompts) tap(_ provider.Protocol, pipe provider.Pipe, line []byte, _ int) {
	if pipe == provider.Stdin && kind(line) == "user" {
		p.mu.Lock()
		p.sent = append(p.sent, uuidIn(line))
		p.mu.Unlock()
	}
}

func (p *prompts) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return ""
	}
	return p.sent[len(p.sent)-1]
}

// finish runs text on ag to the end of its turn.
func finish(t *testing.T, ctx context.Context, ag provider.Agent, text string) provider.TurnEnded {
	t.Helper()
	if err := ag.Do(ctx, provider.Start{Text: text}); err != nil {
		t.Fatal(err)
	}
	for {
		if e, ok := must(ag.Next(ctx)).(provider.TurnEnded); ok {
			return e
		}
	}
}

// A run's head is where it left the conversation: its last message of
// its own, and the message that started it, both by the uuids Claude Code
// knows them by.
func TestATurnEndsWithWhereItLeftTheConversation(t *testing.T) {
	keep(t, []string{"hi"}, nil)
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var sent prompts
	spec := provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}, Access: api.AccessAsk, Tap: sent.tap}
	ag := must(ad.Open(bounded, spec))
	defer ag.Close(bounded)
	e := finish(t, bounded, ag, "Say hi in three words.")
	var h struct{ At, Prompt string }
	if err := json.Unmarshal([]byte(e.Head), &h); err != nil || h.At != hiAt || h.Prompt == "" || h.Prompt != sent.last() {
		t.Errorf("the turn ended at %q, want its assistant message %s and its prompt %s", e.Head, hiAt, sent.last())
	}
}

// The probe finds a hidden flag anywhere in the program, across the
// probe's reads too, and only the flag itself.
func TestTheForkProbeFindsTheHiddenFlagsAProgramHolds(t *testing.T) {
	dir := t.TempDir()
	with := filepath.Join(dir, "with")
	body := strings.Repeat("x", claude.ProbeChunk-5) + "--resume-session-at" + strings.Repeat("y", claude.ProbeChunk) + "--fork-session\x00--resume-drops-turn"
	without := filepath.Join(dir, "without")
	for path, b := range map[string]string{with: body, without: "#!/bin/sh\n# --resume-session --fork --resume-drops\nexec claude \"$@\"\n"} {
		if err := os.WriteFile(path, []byte(b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := claude.Probe(with), []string{"--resume-session-at", "--resume-drops-turn", "--fork-session"}; !slices.Equal(got, want) {
		t.Errorf("the probe found %q in a program that holds %q", got, want)
	}
	if got := claude.Probe(without); len(got) != 0 {
		t.Errorf("the probe found %q in a shim that holds none", got)
	}
	if got := claude.Probe(filepath.Join(dir, "missing")); len(got) != 0 {
		t.Errorf("the probe found %q in no program", got)
	}
}

// The probe reads a program once for each size and modification time it
// has, so a launch costs a stat, and reads it again once it changes.
func TestTheForkProbeReadsAChangedProgramAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude")
	then, later := time.Now().Add(-time.Hour).Round(time.Second), time.Now().Round(time.Second)
	write := func(body string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("# "+strings.Repeat("-", len("--resume-session-at")), then)
	if got := claude.Probe(path); len(got) != 0 {
		t.Fatalf("the probe found %q in a program that holds none", got)
	}
	write("# --resume-session-at", then)
	if got := claude.Probe(path); len(got) != 0 {
		t.Errorf("the probe found %q in a program of the size and time it probed, want what it found then", got)
	}
	write("# --resume-session-at", later)
	if got := claude.Probe(path); !slices.Equal(got, []string{"--resume-session-at"}) {
		t.Errorf("the probe found %q in a program changed since, want --resume-session-at", got)
	}
	// An update moves a new program over the old one, which may keep its
	// size and time.
	beside := path + ".new"
	if err := os.WriteFile(beside, []byte("# "+strings.Repeat("-", len("--resume-session-at"))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(beside, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(beside, path); err != nil {
		t.Fatal(err)
	}
	if got := claude.Probe(path); len(got) != 0 {
		t.Errorf("the probe found %q in a program moved over the one it probed, of its size and time, want none", got)
	}
	write("# --resume-session-at --fork-session", later)
	if got := claude.Probe(path); !slices.Equal(got, []string{"--resume-session-at", "--fork-session"}) {
		t.Errorf("the probe found %q in a program grown since, want --resume-session-at and --fork-session", got)
	}
}
