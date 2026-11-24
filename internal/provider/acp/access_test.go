package acp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/provider/acp/acptest"
)

// Each access level reaches agy by its own controls: Ask for approval
// and Approve for me by the session's mode, Full access by the flag agy
// starts with, which no mode stands for.
func TestEachAccessLevelRunsAtItsOwnModeAndFlags(t *testing.T) {
	for _, tc := range []struct {
		access api.Access
		mode   string // set_mode's, "" for none sent
		flag   bool
	}{
		{api.AccessAsk, "", false},
		{api.AccessAutoEdits, "auto", false},
		{api.AccessFull, "", true},
	} {
		a, log := fake(t, "hi")
		s := spec(t)
		s.Access = tc.access
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		entries := wire(t, log)
		argv := find(t, entries, "initialize")[0].Argv
		if got := slices.Contains(argv, "--dangerously-skip-permissions"); got != tc.flag {
			t.Errorf("access %v: agy started with %q, the skip flag %v, want %v", tc.access, argv, got, tc.flag)
		}
		var modes []string
		for _, e := range entries {
			if e.Method == "session/set_mode" {
				var p struct{ ModeId string }
				json.Unmarshal(e.Params, &p)
				modes = append(modes, p.ModeId)
			}
		}
		if want := []string{tc.mode}; tc.mode == "" && len(modes) != 0 || tc.mode != "" && !slices.Equal(modes, want) {
			t.Errorf("access %v: set_mode %q, want %q", tc.access, modes, tc.mode)
		}
		if i, j := slices.Index(methods(entries), "session/set_mode"), slices.Index(methods(entries), "session/prompt"); i > j {
			t.Errorf("access %v: the mode was set after the prompt: %q", tc.access, methods(entries))
		}
	}
}

// A mode the agent refuses fails the run, in the agent's words, before
// any prompt goes.
func TestARefusedModeFailsTheRun(t *testing.T) {
	a, log := fake(t, "mode-refused")
	s := spec(t)
	s.Access = api.AccessAutoEdits
	_, err := a.Open(context.Background(), s)
	if err == nil || !strings.Contains(err.Error(), "Unknown mode: auto") {
		t.Fatalf("open with a refused mode = %v, want the agent's refusal", err)
	}
	if slices.Contains(methods(wire(t, log)), "session/prompt") {
		t.Error("a prompt went to an agent that refused the run's mode")
	}
}

// A request that still comes under Full access, as from the user's own
// ask rule, reaches Glitched's card, and nothing answers it but the user.
func TestARequestUnderFullAccessAsksTheUser(t *testing.T) {
	a, log := fake(t, "permission")
	s := spec(t)
	s.Access = api.AccessFull
	g := open(t, a, s)
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "edit"})
	var req provider.RequestOpened
	for {
		if r, ok := next(t, g).(provider.RequestOpened); ok {
			req = r
			break
		}
	}
	if slices.Contains(methods(wire(t, log)), "permission answered") {
		t.Fatal("a request under Full access was answered before the user did")
	}
	do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(`{"allow":false}`)})
	if got := text(turn(t, g)); got != "Answer: reject-once" {
		t.Errorf("the agent heard %q, want the user's deny", got)
	}
}

// An ACP agent offers no grant for the session, so an answer for the
// session fails rather than going to the agent as another answer; the
// agent hears the one the user gives next.
func TestNoSessionGrantOnAnACPCard(t *testing.T) {
	for _, scenario := range []string{"permission", "permission-once"} {
		a, _ := fake(t, scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "edit"})
		for {
			if r, ok := next(t, g).(provider.RequestOpened); ok {
				if strings.Contains(string(r.Body), "session") {
					t.Errorf("%s: the card offers a session grant: %s", scenario, r.Body)
				}
				if err := g.Do(context.Background(), provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true,"session":true}`)}); err == nil {
					t.Errorf("%s: an answer for the session went through", scenario)
				}
				do(t, g, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":false}`)})
				break
			}
		}
		if got := text(turn(t, g)); got != "Answer: reject-once" {
			t.Errorf("%s: the agent heard %q, want the deny alone", scenario, got)
		}
		closeRun(t, g)
	}
}

// The card offers Always allow only where the agent offers to keep the
// grant, and Always allow picks that option; Allow stays once.
func TestAlwaysAllowIsTheAgentsOwnOption(t *testing.T) {
	for _, tc := range []struct {
		scenario, answer string
		offered          bool
		heard            string
	}{
		{"permission", `{"allow":true,"always":true}`, true, "Answer: allow-always"},
		{"permission", `{"allow":true}`, true, "Answer: allow-once"},
		{"permission-once", `{"allow":true}`, false, "Answer: allow-once"},
	} {
		a, _ := fake(t, tc.scenario)
		g := open(t, a, spec(t))
		do(t, g, provider.Start{Text: "edit"})
		var req provider.RequestOpened
		for {
			if r, ok := next(t, g).(provider.RequestOpened); ok {
				req = r
				break
			}
		}
		var body struct{ Always bool }
		json.Unmarshal(req.Body, &body)
		if body.Always != tc.offered {
			t.Errorf("%s: the card offers Always allow %v, want %v: %s", tc.scenario, body.Always, tc.offered, req.Body)
		}
		do(t, g, provider.Respond{Request: req.ID, Answer: json.RawMessage(tc.answer)})
		if got := text(turn(t, g)); got != tc.heard {
			t.Errorf("%s, %s: the agent heard %q, want %q", tc.scenario, tc.answer, got, tc.heard)
		}
		closeRun(t, g)
	}
	a, _ := fake(t, "permission-once")
	g := open(t, a, spec(t))
	defer closeRun(t, g)
	do(t, g, provider.Start{Text: "edit"})
	for {
		if r, ok := next(t, g).(provider.RequestOpened); ok {
			if err := g.Do(context.Background(), provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":true,"always":true}`)}); err == nil {
				t.Error("Always allow went through where the agent offers no such option")
			}
			do(t, g, provider.Respond{Request: r.ID, Answer: json.RawMessage(`{"allow":false}`)})
			break
		}
	}
	turn(t, g)
}

// Each turn reports what the prompt's answer says it used, never the
// session's context that usage_update tells of, and an answer without
// usage reports none; an agent not known to count a turn alone reports
// none at all.
func TestATurnReportsItsOwnTokens(t *testing.T) {
	a, _ := fake(t, "usage")
	s := spec(t)
	for i, want := range []int64{100, 200} {
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "count"})
		if got := ended(t, turn(t, g)).Tokens; got != want {
			t.Errorf("turn %d used %d tokens, want %d", i+1, got, want)
		}
		closeRun(t, g)
	}
	b, _ := fake(t, "hi")
	g := open(t, b, spec(t))
	do(t, g, provider.Start{Text: "hi"})
	if got := ended(t, turn(t, g)).Tokens; got != 0 {
		t.Errorf("a turn whose answer has no usage used %d tokens", got)
	}
	closeRun(t, g)
	c, _ := fake(t, "usage")
	c.Profile.TurnUsage = false
	g = open(t, c, spec(t))
	do(t, g, provider.Start{Text: "count"})
	if got := ended(t, turn(t, g)).Tokens; got != 0 {
		t.Errorf("an agent not known to count a turn alone reported %d tokens", got)
	}
	closeRun(t, g)
}

// A session that doesn't list agy's auto mode runs Approve for me in
// accept-edits, as before auto, and agy then says why as it describes
// itself; once a session lists auto again, it says nothing of it.
func TestASessionWithoutAutoRunsInAcceptEdits(t *testing.T) {
	for _, tc := range []struct {
		offered, mode, noAuto string
	}{
		{"default,accept-edits,plan", "accept-edits", "Antigravity doesn't offer its auto mode"},
		{"none", "", "Antigravity's session offers no modes"},
		{"", "auto", ""},
	} {
		t.Setenv(acptest.Offered, tc.offered)
		a, log := fake(t, "hi")
		s := spec(t)
		s.Access = api.AccessAutoEdits
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		turn(t, g)
		closeRun(t, g)
		var modes []string
		for _, e := range wire(t, log) {
			if e.Method == "session/set_mode" {
				var p struct{ ModeId string }
				json.Unmarshal(e.Params, &p)
				modes = append(modes, p.ModeId)
			}
		}
		if want := []string{tc.mode}; tc.mode == "" && len(modes) != 0 || tc.mode != "" && !slices.Equal(modes, want) {
			t.Errorf("%s: set_mode %q, want %q", tc.offered, modes, tc.mode)
		}
		info, err := a.Describe(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.NoAuto != tc.noAuto {
			t.Errorf("%s: agy describes its auto mode as %q, want %q", tc.offered, info.NoAuto, tc.noAuto)
		}
	}
}

// A mode the agent reports after Glitched set one wins: the next run sets
// the access level's mode again rather than trust what it last asked.
func TestTheAgentsLatestModeWinsOnTheNextRun(t *testing.T) {
	a := wireFake(t, "mode-updates-before-reply")
	s := spec(t)
	s.Access = api.AccessAutoEdits
	var modes []string
	for range 2 {
		g := open(t, a, s)
		do(t, g, provider.Start{Text: "hi"})
		modes = append(modes, text(turn(t, g)))
		closeRun(t, g)
		s.Run, s.Generation = api.NewID(), s.Generation+1
	}
	if want := []string{"default", "auto"}; !slices.Equal(modes, want) {
		t.Fatalf("the agent ran its turns in modes %q, want %q", modes, want)
	}
}

// A thread changes its access level between runs and keeps its
// conversation: each level takes an agy of its own, started with the
// skip flag for Full access alone, which resumes the thread's session
// and sets it to the level's mode. A session without auto runs Approve
// for me in accept-edits the same way.
func TestAThreadChangesItsAccessMidConversation(t *testing.T) {
	for offered, auto := range map[string]string{"": "auto", "default,accept-edits,plan": "accept-edits"} {
		t.Setenv(acptest.Offered, offered)
		a, log := fake(t, "memory")
		s := spec(t)
		prompt := "Remember the word kumquat."
		var sessions []string
		var said string
		for i, level := range []api.Access{api.AccessAsk, api.AccessAutoEdits, api.AccessAsk, api.AccessFull, api.AccessAutoEdits, api.AccessAsk} {
			s.Run, s.Generation, s.Access = api.NewID(), uint64(i+1), level
			g := open(t, a, s)
			s.Session = g.(provider.Sessioned).Session()
			sessions = append(sessions, s.Session)
			do(t, g, provider.Start{Text: prompt})
			said = text(turn(t, g))
			closeRun(t, g)
			prompt = "What word did I ask you to remember?"
		}
		if slices.ContainsFunc(sessions, func(id string) bool { return id != sessions[0] }) || !strings.Contains(said, "kumquat") {
			t.Errorf("modes %q: sessions %q, last said %q, want one conversation throughout", offered, sessions, said)
		}
		entries := wire(t, log)
		var flags []bool
		for _, e := range find(t, entries, "initialize") {
			flags = append(flags, slices.Contains(e.Argv, "--dangerously-skip-permissions"))
		}
		var modes []string
		for _, e := range find(t, entries, "session/set_mode") {
			var p struct{ ModeId string }
			json.Unmarshal(e.Params, &p)
			modes = append(modes, p.ModeId)
		}
		if !slices.Equal(flags, []bool{false, false, false, true, false, false}) || len(find(t, entries, "session/new")) != 1 || len(find(t, entries, "session/resume")) != 5 {
			t.Errorf("modes %q: agy started %v with the skip flag, and %q", offered, flags, methods(entries))
		}
		if want := []string{auto, auto}; !slices.Equal(modes, want) {
			t.Errorf("modes %q: set_mode %q, want %q", offered, modes, want)
		}
	}
}
