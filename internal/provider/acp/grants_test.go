package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

const fakeOpenCodeEnv = "GLITCHED_FAKE_OPENCODE"

func playOpenCodeCLI() bool {
	out := os.Getenv(fakeOpenCodeEnv)
	if out == "" || len(os.Args) < 2 || os.Args[1] != "session" {
		return false
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	f.WriteString(strings.Join(os.Args[1:], " ") + "\n")
	f.Close()
	return true
}

func playing(t *testing.T, profile Profile) (*Adapter, string) {
	t.Helper()
	a, log := fake(t, "hi")
	path := a.Path
	a.Profile = profile
	a.Path = path
	return a, log
}

func started(t *testing.T, log string) map[string][]string {
	t.Helper()
	inits := find(t, wire(t, log), "initialize")
	out := map[string][]string{}
	for _, kv := range inits[len(inits)-1].Env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = append(out[k], v)
	}
	return out
}

func setModes(t *testing.T, log string) []string {
	t.Helper()
	var out []string
	for _, e := range find(t, wire(t, log), "session/set_mode") {
		var p struct{ ModeId string }
		json.Unmarshal(e.Params, &p)
		out = append(out, p.ModeId)
	}
	return out
}

func runOnce(t *testing.T, a *Adapter, s provider.SessionSpec) []provider.Event {
	t.Helper()
	g := open(t, a, s)
	do(t, g, provider.Start{Text: "hi"})
	events := turn(t, g)
	closeRun(t, g)
	return reports(events)
}

func TestOpenCodeRunsEachAccessLevelAsItsPermissionConfig(t *testing.T) {
	t.Setenv(acptest.Offered, "build,plan")
	for _, tc := range []struct {
		access          api.Access
		any, bash, edit string
	}{
		{api.AccessAsk, "ask", "", ""},
		{api.AccessAutoEdits, "ask", "", "allow"},
		{api.AccessFull, "allow", "", ""},
	} {
		a, log := playing(t, OpenCode)
		s := spec(t)
		s.Access, s.Model.Provider = tc.access, "opencode"
		got := runOnce(t, a, s)
		env := started(t, log)["OPENCODE_PERMISSION"]
		if len(env) != 1 {
			t.Fatalf("access %v: OpenCode started with OPENCODE_PERMISSION %q, want one", tc.access, env)
		}
		var perm map[string]string
		if err := json.Unmarshal([]byte(env[0]), &perm); err != nil {
			t.Fatalf("access %v: OPENCODE_PERMISSION %q: %v", tc.access, env[0], err)
		}
		if perm["*"] != tc.any || perm["bash"] != tc.bash || perm["edit"] != tc.edit || tc.access != api.AccessFull && perm["read"] != "allow" {
			t.Errorf("access %v: OPENCODE_PERMISSION %s", tc.access, env[0])
		}
		if m := setModes(t, log); len(m) != 0 {
			t.Errorf("access %v: set OpenCode's mode to %q", tc.access, m)
		}
		if len(got) == 0 || got[0] != (provider.Granted{Access: tc.access, Mode: "default"}) {
			t.Errorf("access %v: reported %+v", tc.access, got)
		}
	}
}

func TestAGrantsEnvironmentGoesOverTheProfiles(t *testing.T) {
	t.Setenv("GLX_GRANT", "engine")
	profile := Antigravity
	profile.Env = map[string]string{"GLX_GRANT": "profile", "GLX_KEEP": "profile"}
	profile.Access = provider.Grants{api.AccessAsk: {Mode: "default", Env: map[string]string{"GLX_GRANT": "grant"}}}
	a, log := playing(t, profile)
	got := runOnce(t, a, spec(t))
	env := started(t, log)
	if !slices.Equal(env["GLX_GRANT"], []string{"grant"}) || !slices.Equal(env["GLX_KEEP"], []string{"profile"}) {
		t.Errorf("the agent started with %q", env)
	}
	if len(got) == 0 || got[0] != (provider.Granted{Access: api.AccessAsk, Mode: "default"}) {
		t.Errorf("reported %+v", got)
	}
}

func TestGeminiTrustsTheWorkspaceOnlyToApproveForMe(t *testing.T) {
	t.Setenv(acptest.Offered, "default,autoEdit,yolo,plan")
	for _, tc := range []struct {
		access api.Access
		trust  []string
		modes  []string
	}{
		{api.AccessAsk, nil, nil},
		{api.AccessAutoEdits, []string{"true"}, []string{"autoEdit"}},
	} {
		a, log := playing(t, Gemini)
		s := spec(t)
		s.Access, s.Model.Provider = tc.access, "gemini"
		got := runOnce(t, a, s)
		if trust := started(t, log)["GEMINI_CLI_TRUST_WORKSPACE"]; !slices.Equal(trust, tc.trust) {
			t.Errorf("access %v: GEMINI_CLI_TRUST_WORKSPACE %q, want %q", tc.access, trust, tc.trust)
		}
		if m := setModes(t, log); !slices.Equal(m, tc.modes) {
			t.Errorf("access %v: set_mode %q, want %q", tc.access, m, tc.modes)
		}
		want := provider.Granted{Access: tc.access, Mode: "default"}
		if tc.access == api.AccessAutoEdits {
			want.Mode = "autoEdit"
		}
		if len(got) == 0 || got[0] != want {
			t.Errorf("access %v: reported %+v, want %+v", tc.access, got, want)
		}
	}
}

func TestAnElseTheSessionDoesntOfferIsNotSent(t *testing.T) {
	t.Setenv(acptest.Offered, "default,plan")
	a, log := fake(t, "hi")
	s := spec(t)
	s.Access = api.AccessAutoEdits
	got := runOnce(t, a, s)
	if m := setModes(t, log); len(m) != 0 {
		t.Errorf("set_mode %q on a session that offers neither auto nor accept-edits", m)
	}
	if len(got) == 0 || got[0].(provider.Granted).Access != api.AccessAsk {
		t.Errorf("reported %+v, want the session's own default, which asks", got)
	}
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.NoAuto != "Antigravity doesn't offer its auto mode" {
		t.Errorf("NoAuto %q", info.NoAuto)
	}
}

func TestACustomAgentKeepsItsOwnModeWhereItOffersNoneOfTheGuesses(t *testing.T) {
	custom, err := CustomProfile{Provider: "mine", Binary: "/bin/mine"}.ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offered string
		modes   map[api.Access][]string
	}{
		{"build,plan", map[api.Access][]string{}},
		{"default,auto,bypassPermissions", map[api.Access][]string{api.AccessAutoEdits: {"auto"}, api.AccessFull: {"bypassPermissions"}}},
		{"default,yolo", map[api.Access][]string{api.AccessFull: {"yolo"}}},
	} {
		t.Setenv(acptest.Offered, tc.offered)
		for _, access := range []api.Access{api.AccessAsk, api.AccessAutoEdits, api.AccessFull} {
			a, log := playing(t, custom)
			s := spec(t)
			s.Access, s.Model.Provider = access, "mine"
			runOnce(t, a, s)
			if m := setModes(t, log); !slices.Equal(m, tc.modes[access]) {
				t.Errorf("offered %s, access %v: set_mode %q, want %q", tc.offered, access, m, tc.modes[access])
			}
		}
	}
}

func TestFullAccessFallingBackSaysNothingOfAutoMode(t *testing.T) {
	t.Setenv(acptest.Offered, "default,auto,yolo")
	custom, err := CustomProfile{Provider: "mine", Binary: "/bin/mine"}.ToProfile()
	if err != nil {
		t.Fatal(err)
	}
	a, log := playing(t, custom)
	s := spec(t)
	s.Access, s.Model.Provider = api.AccessFull, "mine"
	runOnce(t, a, s)
	if m := setModes(t, log); !slices.Equal(m, []string{"yolo"}) {
		t.Fatalf("set_mode %q, want the fallback yolo", m)
	}
	info, err := a.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.NoAuto != "" {
		t.Errorf("after Full access fell back, the agent says its auto mode is missing: %q", info.NoAuto)
	}
}

func TestAProbesSessionIsCleanedUpAfterItsAgent(t *testing.T) {
	a, log := fake(t, "hi")
	var cleaned []string
	a.Profile.CleanupProbe = func(ctx context.Context, prog provider.Program, session string) {
		if prog.Path != a.Path {
			t.Errorf("cleanup ran %s, want the agent's own program", prog.Path)
		}
		entries := wire(t, log)
		if methods(entries)[len(entries)-1] != "stdin closed" {
			t.Errorf("cleanup ran while the probe's agent still ran: %q", methods(entries))
		}
		cleaned = append(cleaned, session)
	}
	for range 2 {
		if _, err := a.Describe(context.Background()); err != nil {
			t.Fatal(err)
		}
		a.Forget()
	}
	if !slices.Equal(cleaned, []string{"session-1", "session-1"}) {
		t.Errorf("cleaned up %q, want each probe's session", cleaned)
	}
}

func TestAProbeWithNoSessionCleansNothingUp(t *testing.T) {
	a, _ := fake(t, "signed-out")
	a.Profile.CleanupProbe = func(context.Context, provider.Program, string) {
		t.Error("cleaned up a session the probe never opened")
	}
	if _, err := a.Describe(context.Background()); err == nil {
		t.Fatal("a signed-out probe succeeded")
	}
}

func TestDescribingOpenCodeDeletesItsProbesSession(t *testing.T) {
	out := filepath.Join(t.TempDir(), "opencode")
	t.Setenv(fakeOpenCodeEnv, out)
	a, _ := playing(t, OpenCode)
	if _, err := a.Describe(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("OpenCode's CLI never ran: %v", err)
	}
	if got := string(b); got != "session delete session-1\n" {
		t.Errorf("OpenCode's CLI ran with %q", got)
	}
}

func TestASilentSetModeReportsTheGrantedMode(t *testing.T) {
	t.Setenv(acptest.Offered, "default,autoEdit,yolo,plan")
	a, log := fake(t, "mode-silent")
	a.Profile = Gemini
	s := spec(t)
	s.Model = api.ModelRef{Provider: Gemini.Provider}
	s.Access = api.AccessAutoEdits
	for i := range 2 {
		s.Run, s.Generation = api.NewID(), uint64(i+1)
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		var granted *provider.Granted
		for _, e := range turn(t, g) {
			if gr, ok := e.(provider.Granted); ok {
				granted = &gr
			}
		}
		closeRun(t, g)
		want := provider.Granted{Access: api.AccessAutoEdits, Mode: "autoEdit"}
		if granted == nil || *granted != want {
			t.Fatalf("turn %d: granted %+v, want %+v", i+1, granted, want)
		}
	}
	if n := len(find(t, wire(t, log), "session/set_mode")); n != 1 {
		t.Errorf("session/set_mode sent %d times across 2 turns, want 1", n)
	}
}
