package pty

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
)

func TestReattachReplaysNoTerminalQueries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: []string{"/bin/sh"}})
	defer mgr.Close()
	ws := t.TempDir()

	st, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "printf '\\033[c\\033]11;?\\007asked-%s\\n' ok\n")
	out, _ := readUntilContains(t, ctx, st, "asked-ok")
	if !strings.Contains(out, "\x1b[c\x1b]11;?\x07asked-ok") {
		t.Fatalf("live output lost the queries: %q", out)
	}
	st.Close()

	again, first := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	replay := string(first.Data)
	if !strings.Contains(replay, "asked-ok") {
		rest, _ := readUntilContains(t, ctx, again, "asked-ok")
		replay += rest
	}
	if strings.Contains(replay, "\x1b[c") || strings.Contains(replay, "\x1b]11;?") {
		t.Fatalf("the replay asks the terminal again: %q", replay)
	}

	input(t, ctx, mgr, ws, "a", "printf '\\033[c\\033]11;?\\007live-%s\\n' ok\n")
	live, _ := readUntilContains(t, ctx, again, "live-ok")
	if !strings.Contains(live, "\x1b[c\x1b]11;?\x07live-ok") {
		t.Fatalf("live output after reattach lost the queries: %q", live)
	}
}

// A replay larger than what a subscriber may buffer pauses and resumes from
// the ring: the part sent after resuming is replay too.
func TestPausedReplayStaysFiltered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mgr := New(Options{Command: []string{"/bin/sh"}, HighWatermark: 256, LowWatermark: 128})
	defer mgr.Close()
	ws := t.TempDir()

	st, _ := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	input(t, ctx, mgr, ws, "a", "i=0; while [ $i -lt 40 ]; do printf 'line %02d \\033[6n\\n' $i; i=$((i+1)); done; echo done-$((2+2))\n")
	readUntilContains(t, ctx, st, "done-4")
	st.Close()

	again, first := open(t, ctx, mgr, api.TerminalOpen{Workspace: ws, Client: "a"})
	var replay bytes.Buffer
	replay.Write(first.Data)
	for !bytes.Contains(replay.Bytes(), []byte("done-4")) {
		it, err := again.Next(ctx)
		if err != nil {
			t.Fatalf("Next (saw %q): %v", replay.String(), err)
		}
		replay.Write(it.Data)
	}
	if !bytes.Contains(replay.Bytes(), []byte("line 39")) {
		t.Fatalf("replay lost lines: %q", replay.String())
	}
	if bytes.Contains(replay.Bytes(), []byte("\x1b[6n")) {
		t.Fatalf("the replay asks the terminal again: %q", replay.String())
	}
}
