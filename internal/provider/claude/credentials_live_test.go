//go:build claudelive

package claude_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"glitched/internal/provider/claude/transcript"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
)

// TestContractCredentials pins request_credentials against the signed-in
// claude on PATH: the agent calls it, the card's value reaches its next
// Bash command through CLAUDE_ENV_FILE, and the reply holds only the
// value's length. One haiku turn. With -record, the transcript goes to
// testdata for TestAClaudeAgentAsksForCredentials.
func TestContractCredentials(t *testing.T) {
	real, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	tapes := t.TempDir()
	if *record {
		tapes = must(filepath.Abs("testdata"))
	}
	tape := filepath.Join(tapes, "credentials")
	t.Setenv(transcript.EnvReal, real)
	t.Setenv(transcript.EnvOut, tape)

	vault := secrets.New(t.TempDir())
	h := delegateHost(t, nil, runtime.WithVault(vault))
	thread, got := credentialsTurn(t, h, vault)
	waitExit(t, tape+".jsonl")
	checkCredentialsTurn(t, h, vault, thread, got)
	if want := "len=" + strconv.Itoa(len(probeValue)); !strings.Contains(got.text, want) {
		t.Errorf("the reply %q, want %q: the Bash command did not see GLX_PROBE", got.text, want)
	}
	if strings.Contains(string(must(os.ReadFile(tape+".jsonl"))), probeValue) {
		t.Error("the value reached claude")
	}
}
