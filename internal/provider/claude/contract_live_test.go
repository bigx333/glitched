//go:build claudelive

package claude_test

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"glitched/internal/provider/claude"
	"glitched/internal/provider/claude/transcript"
)

var record = flag.Bool("record", false, "write the transcripts of the contract test to testdata, for the replay tests")

// TestContract pins the --permission-prompt-tool stdio protocol, which
// the CLI docs do not describe, against the signed-in claude on PATH: the
// can_use_tool request, the replies Claude Code takes, that it takes only
// the first reply to a request, and that it cancels a request one of the
// user's hooks decided. A CLI upgrade that changes any of it fails here.
//
// It runs a few haiku turns in scratch directories under /tmp. With
// -record, the transcripts go to testdata.
func TestContract(t *testing.T) {
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	out, err := claude.Command(ctx, real, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := strings.Cut(strings.TrimSpace(string(out)), " "); v != shippedCLI {
		t.Fatalf("claude on PATH is %s, want %s: check the protocol against the new release, then move shippedCLI", v, shippedCLI)
	}
	tapes := t.TempDir()
	if *record {
		tapes = must(filepath.Abs("testdata"))
	}
	t.Setenv(transcript.EnvReal, real)
	for _, s := range []promptScenario{approve, settled} {
		t.Run(s.name, func(t *testing.T) {
			ws := must(os.MkdirTemp("/tmp", "glitched-contract-"))
			t.Cleanup(func() { os.RemoveAll(ws) })
			tape := filepath.Join(tapes, s.name)
			t.Setenv(transcript.EnvOut, tape)
			if s.name == approve.name {
				t.Setenv(transcript.EnvDupe, "1")
			}
			replies := stdin(t)
			h := host(t, nil)
			got := s.play(t, h, s.workspace(t, ws))
			waitExit(t, tape+".jsonl")

			s.check(t, got)
			checkReplies(t, got, replies(), tape+".jsonl")
			checkTape(t, tape+".jsonl", s)
			for file, want := range s.files {
				if _, err := os.Stat(filepath.Join(ws, file)); (err == nil) != want {
					t.Errorf("%s exists: %v, want %v", file, err == nil, want)
				}
			}
		})
	}
}

// waitExit waits for the tee to finish the transcript at path.
func waitExit(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if data, _ := os.ReadFile(path); strings.Contains(string(data), `{"exit":`) {
			return
		}
	}
	t.Fatalf("the transcript %s never ended", path)
}
