package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/runtime"
	"glitched/internal/secrets"
)

// credentialsPrompt is the turn the credentials transcript recorded.
const credentialsPrompt = `Use the request_credentials tool of the glitched MCP server once, with reason "probe" and one field named GLX_PROBE. Then run the Bash command: echo len=${#GLX_PROBE}. Reply with only its output.`

// probeValue is what the user types for GLX_PROBE: no real secret.
const probeValue = "probe-value-0123"

// credentialsTurn plays credentialsPrompt on a host with a vault, and
// provides probeValue when the card opens.
func credentialsTurn(t *testing.T, h *runtime.Host, vault *secrets.Vault) (api.ID, promptLog) {
	t.Helper()
	thread := api.NewID()
	submit(t, h, thread, api.ThreadCreate{Workspace: t.TempDir(), Title: "probe", Access: api.AccessFull,
		Model: api.ModelRef{Provider: "claude", Model: "haiku"}})
	got := playOn(t, h, thread, credentialsPrompt, func(r requestLog) bool {
		if r.opened.Kind != api.KindCredentials {
			t.Errorf("a %s request opened, want only credentials", r.opened.Kind)
			return true
		}
		if err := h.ProvideCredentials(ctx, api.Provide{Thread: thread, Request: r.opened.Request, Generation: r.opened.Generation,
			Values: map[string]string{"GLX_PROBE": probeValue}}); err != nil {
			t.Errorf("provide: %v", err)
			return true
		}
		return false
	})
	return thread, got
}

// A Claude Code agent that calls request_credentials opens a credentials
// card for its turn. The value the user gives goes to the thread's vault,
// which its claude sources before each Bash command, and nowhere in the
// thread's log: the answer holds the name alone.
func TestAClaudeAgentAsksForCredentials(t *testing.T) {
	argv := replay(t, "credentials", "")
	entry := filepath.Join(t.TempDir(), "entry")
	t.Setenv(envEntry, entry)
	vault := secrets.New(t.TempDir())
	h := delegateHost(t, nil, runtime.WithVault(vault))
	thread, got := credentialsTurn(t, h, vault)
	checkCredentialsTurn(t, h, vault, thread, got)

	var env []string
	json.Unmarshal(must(os.ReadFile(entry)), &env)
	want := "CLAUDE_ENV_FILE=" + filepath.Join(vault.Dir(thread), secrets.ClaudeEnv)
	if !slices.Contains(env, want) {
		t.Errorf("claude ran with %q, want %q", env, want)
	}
	first, _, _ := strings.Cut(string(must(os.ReadFile(argv))), "\n")
	if !strings.Contains(first, "request_credentials") {
		t.Errorf("claude ran with %s, want instructions for request_credentials", first)
	}
}

func checkCredentialsTurn(t *testing.T, h *runtime.Host, vault *secrets.Vault, thread api.ID, got promptLog) {
	t.Helper()
	if got.ended.Outcome != api.OutcomeCompleted || len(got.requests) != 1 {
		t.Fatalf("the turn: %+v, want it completed after one request", got)
	}
	r := got.requests[0]
	var c api.Credentials
	json.Unmarshal(r.opened.Body, &c)
	if !slices.Equal(c.Names(), []string{"GLX_PROBE"}) || r.state != api.RequestDelivered || string(r.answer) != `{"provided":["GLX_PROBE"]}` {
		t.Errorf("the request %+v asked %+v, want GLX_PROBE delivered with its name alone", r, c)
	}
	if v := vault.Values(thread)["GLX_PROBE"]; v != probeValue {
		t.Errorf("the vault holds %q", v)
	}
	if b := must(os.ReadFile(filepath.Join(vault.Dir(thread), "GLX_PROBE"))); string(b) != probeValue {
		t.Errorf("GLX_PROBE's file holds %q", b)
	}
	snap := must(h.Read(ctx, api.Query{Thread: thread}))
	for _, e := range snap.Events {
		if strings.Contains(string(must(json.Marshal(e))), probeValue) {
			t.Errorf("the thread's log holds the value, in %s", e.Type)
		}
	}
}
