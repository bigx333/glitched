package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/claude"
)

const haikuID = "claude-haiku-5-5"

// tape is hi with each of the pairs replaced in it.
func tape(t *testing.T, oldnew ...string) string {
	t.Helper()
	src := string(must(os.ReadFile(filepath.Join("testdata", "hi.jsonl"))))
	path := filepath.Join(t.TempDir(), "hi.jsonl")
	if err := os.WriteFile(path, []byte(strings.NewReplacer(oldnew...).Replace(src)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// atInit puts lines on hi right after claude's system init.
func atInit(lines ...string) []string {
	const init = `"subtype":"init","type":"system","uuid":"83113e76-1640-4dfb-a20c-c921269629b0"}}`
	with := init
	for _, l := range lines {
		with += "\n" + `{"recv":` + l + `}`
	}
	return []string{init, with}
}

func spec(t *testing.T, access api.Access, model string) provider.SessionSpec {
	return provider.SessionSpec{Thread: api.NewID(), Run: api.NewID(), Generation: 1, Workspace: t.TempDir(),
		Model: api.ModelRef{Provider: "claude", Model: model}, Access: access}
}

// confirmations is what a run of s says it runs at, in order: each
// Granted and Ran it sends, until its turn ends.
func confirmations(t *testing.T, ad *claude.Adapter, s provider.SessionSpec) []provider.Event {
	t.Helper()
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ag := must(ad.Open(bounded, s))
	defer ag.Close(bounded)
	if err := ag.Do(bounded, provider.Start{Text: "Say hi in three words."}); err != nil {
		t.Fatal(err)
	}
	var got []provider.Event
	for {
		switch e := must(ag.Next(bounded)).(type) {
		case provider.Granted, provider.Ran:
			got = append(got, e)
		case provider.TurnEnded:
			if e.Outcome != api.OutcomeCompleted {
				t.Fatalf("the turn ended %+v", e)
			}
			return got
		}
	}
}

func TestARunSaysWhatClaudeRunsItAt(t *testing.T) {
	asked := provider.Granted{Access: api.AccessAsk, Mode: "default"}
	for _, tc := range []struct {
		name   string
		access api.Access
		model  string
		tape   []string
		want   []provider.Event
	}{
		{"as asked", api.AccessAsk, haikuID, nil, []provider.Event{asked, provider.Ran{Model: haikuID}}},
		{"by the GUI's name", api.AccessFull, "Claude Haiku 5.5", []string{`"permissionMode":"default"`, `"permissionMode":"bypassPermissions"`},
			[]provider.Event{provider.Granted{Access: api.AccessFull, Mode: "bypassPermissions"}, provider.Ran{Model: "Claude Haiku 5.5"}}},
		{"in auto", api.AccessAutoEdits, haikuID, []string{`"permissionMode":"default"`, `"permissionMode":"auto"`},
			[]provider.Event{provider.Granted{Access: api.AccessAutoEdits, Mode: "auto"}, provider.Ran{Model: haikuID}}},
		// Managed settings can keep bypassPermissions from a claude.
		{"refused bypass", api.AccessFull, haikuID, nil,
			[]provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default", Why: "Claude Code runs in its default mode"}, provider.Ran{Model: haikuID}}},
		{"another model", api.AccessAsk, "claude-opus-5-5", nil,
			[]provider.Event{asked, provider.Ran{Model: haikuID, Why: "Claude Code runs claude-haiku-5-5"}}},
		{"an alias", api.AccessAsk, "haiku", nil,
			[]provider.Event{asked, provider.Ran{Model: haikuID, Why: "Claude Code runs claude-haiku-5-5"}}},
		{"no model asked", api.AccessAsk, "", nil, []provider.Event{asked, provider.Ran{Model: haikuID}}},
		{"switched mid-turn", api.AccessAsk, haikuID,
			atInit(`{"type":"system","subtype":"model_fallback","trigger":"permission_denied","original_model":"claude-haiku-5-5","fallback_model":"claude-sonnet-5-5","content":"Switched to Sonnet 5.5 because Haiku 5.5 is not available"}`,
				`{"type":"system","subtype":"status","status":null,"permissionMode":"plan"}`),
			[]provider.Event{asked, provider.Ran{Model: haikuID},
				provider.Ran{Model: "claude-sonnet-5-5", Why: "Switched to Sonnet 5.5 because Haiku 5.5 is not available"},
				provider.Granted{Access: api.AccessAsk, Mode: "plan", Why: "Claude Code runs in its plan mode"}}},
		{"saying nothing", api.AccessAsk, haikuID, []string{`"model":"claude-haiku-5-5","permissionMode":"default",`, ``}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envFake, tape(t, tc.tape...))
			ad := &claude.Adapter{Path: must(os.Executable())}
			t.Cleanup(func() { ad.Close() })
			if got := confirmations(t, ad, spec(t, tc.access, tc.model)); !slices.Equal(got, tc.want) {
				t.Errorf("the run said\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

// Approve for me where Claude Code refuses auto runs in acceptEdits, and
// says why in the refusal's own words.
func TestApproveForMeWithoutAutoSaysWhy(t *testing.T) {
	t.Setenv(envFake, tape(t, `"permissionMode":"default"`, `"permissionMode":"acceptEdits"`))
	t.Setenv(envNoAuto, "auto mode unavailable for this model")
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	want := []provider.Event{
		provider.Granted{Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "Cannot set permission mode to auto: auto mode unavailable for this model"},
		provider.Ran{Model: haikuID},
	}
	if got := confirmations(t, ad, spec(t, api.AccessAutoEdits, haikuID)); !slices.Equal(got, want) {
		t.Errorf("the run said\n%+v\nwant\n%+v", got, want)
	}
}

// A run on a kept claude says, before its turn starts, what that claude
// confirmed. A run that asks for other access or another model gets a
// claude of its own, and says what that one runs.
func TestEachRunOfAThreadSaysWhatItRunsAt(t *testing.T) {
	keep(t, []string{"hi", "hi"}, nil)
	ad := &claude.Adapter{Path: must(os.Executable())}
	t.Cleanup(func() { ad.Close() })
	s := spec(t, api.AccessAsk, haikuID)
	asked := []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default"}, provider.Ran{Model: haikuID}}
	if got := confirmations(t, ad, s); !slices.Equal(got, asked) {
		t.Fatalf("the first run said %+v, want %+v", got, asked)
	}

	s.Run, s.Generation = api.NewID(), 2
	bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ag := must(ad.Open(bounded, s))
	var before []provider.Event
	for range asked {
		before = append(before, must(ag.Next(bounded)))
	}
	if !slices.Equal(before, asked) {
		t.Errorf("the kept claude's run said %+v before its turn, want %+v", before, asked)
	}
	ag.Close(bounded)

	t.Setenv(envResume, tape(t, `"model":"claude-haiku-5-5","permissionMode":"default"`, `"model":"claude-sonnet-5-5","permissionMode":"bypassPermissions"`))
	s.Run, s.Generation, s.Access, s.Model.Model = api.NewID(), 3, api.AccessFull, "claude-sonnet-5-5"
	want := []provider.Event{provider.Granted{Access: api.AccessFull, Mode: "bypassPermissions"}, provider.Ran{Model: "claude-sonnet-5-5"}}
	if got := confirmations(t, ad, s); !slices.Equal(got, want) {
		t.Errorf("the changed run said %+v, want %+v", got, want)
	}
}
