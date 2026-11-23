package main

import (
	"context"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"
)

var opusHigh = api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}

// confirming is a thread on Claude Opus at Ask for approval whose turns
// play script on an agent that confirms what confirms says, shown with
// the built-ins settled, after its first turn's start.
func confirming(t *testing.T, script []provider.Event, confirms func(provider.SessionSpec) []provider.Event) (*harness, *daemon.Daemon, api.ID) {
	t.Helper()
	dir := tempDir(t)
	ad := fake.New(script)
	ad.Confirms = confirms
	d := engine(t, daemon.Config{Dir: dir}, ad)
	ctx := context.Background()
	id := api.NewID()
	for _, b := range []api.Intent{
		api.ThreadCreate{Workspace: "/work/glitched", Title: "Confirm the access", Model: opusHigh},
		api.MessageDispatch{Text: "Look around.", Mode: api.StartNow},
	} {
		if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	h := start(t, dir)
	h.a.select_(h.thread("Confirm the access"))
	h.until("the first turn's confirmation", func() bool { h.tt.Frame(); return h.a.current().View.Confirmed.Access != nil })
	settlePlugins(t, h.a, h.tt, h.p)
	return h, d, id
}

func (h *harness) configure(c api.ThreadConfigure) {
	h.t.Helper()
	th := h.a.current()
	h.a.backend.ConfigureThread(th.env, th.id(), c)
	h.until("the configure taken", func() bool { h.tt.Frame(); return len(h.a.current().Pending) == 0 })
	h.tt.Advance(time.Second)
}

// tipOn is the tooltip that shows over the control whose text is s.
func tipOn(t *testing.T, h *harness, s, want string) {
	t.Helper()
	h.tt.Advance(2 * time.Second)
	if !has(hover(t, h, s), want) {
		t.Errorf("hovering %q doesn't say %q: %q", s, want, h.tt.Texts())
	}
	h.tt.Move(0, 0)
	h.tt.Advance(time.Second)
}

// accessShot writes the composer's window to ACCESSSHOTS as name.png.
func accessShot(t *testing.T, h *harness, name string) {
	t.Helper()
	out := os.Getenv("ACCESSSHOTS")
	if out == "" {
		return
	}
	f, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, h.tt.Image())
}

// A changed access shows pending until the next turn's provider confirms
// it, and in effect again once switched back before it applied.
func TestAnAccessChangeIsPendingUntilTheNextTurnConfirmsIt(t *testing.T) {
	h, d, id := confirming(t, fake.Turn(false), nil)
	h.until("idle", func() bool { h.tt.Frame(); return !h.a.current().View.Live() })
	tipOn(t, h, accessAsk, "How freely the agent acts")
	accessShot(t, h, "confirmed")

	h.configure(api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
	tipOn(t, h, accessAuto, "Applies on the next turn")
	accessShot(t, h, "pending")
	h.configure(api.ThreadConfigure{Access: new(api.AccessAsk)})
	tipOn(t, h, accessAsk, "How freely the agent acts")

	h.configure(api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Again.", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	h.until("the next turn confirmed", func() bool {
		h.tt.Frame()
		v := h.a.current().View
		return !v.Live() && v.Confirmed.Access != nil && v.Confirmed.Access.Asked == api.AccessAutoEdits
	})
	tipOn(t, h, accessAuto, "How freely the agent acts")
}

// A change made while a turn runs shows that the turn still runs at the
// level it started at.
func TestARunningTurnKeepsItsAccessWhileTheNewOneShowsPending(t *testing.T) {
	h, _, _ := confirming(t, fake.Turn(true), nil)
	h.until("the approval", func() bool { h.tt.Frame(); return len(h.a.current().View.Requests) == 1 })
	h.configure(api.ThreadConfigure{Access: new(api.AccessFull)})
	tipOn(t, h, accessFull, "This turn is still running at "+accessAsk)
	accessShot(t, h, "pending-running")
	h.configure(api.ThreadConfigure{Effort: new("low")})
	tipOn(t, h, "Claude Opus 5.5", "This turn is still running Claude Opus 5.5 at High")
	accessShot(t, h, "pending-running-effort")
}

// A provider that falls back from its auto mode shows the level the
// thread really runs at and why; one that runs below the level asked
// shows that level.
func TestAFallbackShowsWhatTheThreadReallyRunsAt(t *testing.T) {
	why := "auto mode unavailable for this model"
	h, d, id := confirming(t, fake.Turn(false), func(s provider.SessionSpec) []provider.Event {
		switch s.Access {
		case api.AccessAutoEdits:
			return []provider.Event{provider.Granted{Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: why}}
		case api.AccessFull:
			return []provider.Event{provider.Granted{Access: api.AccessAsk, Mode: "default", Why: "bypassPermissions mode is disabled by settings"}}
		}
		return []provider.Event{provider.Granted{Access: s.Access, Mode: "default"}}
	})
	h.until("idle", func() bool { h.tt.Frame(); return !h.a.current().View.Live() })
	for _, c := range []struct {
		access     api.Access
		shown, tip string
	}{
		{api.AccessAutoEdits, "Approve edits only", "Claude runs Approve edits only (acceptEdits): " + why},
		{api.AccessFull, accessAsk, "Claude runs Ask for approval (default): bypassPermissions mode is disabled by settings"},
	} {
		h.configure(api.ThreadConfigure{Access: new(c.access)})
		if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Again.", Mode: api.StartNow}}); err != nil {
			t.Fatal(err)
		}
		h.until("the turn confirmed", func() bool {
			h.tt.Frame()
			v := h.a.current().View
			return !v.Live() && v.Confirmed.Access.Asked == c.access
		})
		h.tt.Advance(time.Second)
		tipOn(t, h, c.shown, c.tip)
		if c.access == api.AccessAutoEdits {
			accessShot(t, h, "fell-back")
		}
	}
}

// A model the provider substituted shows on the model button as what
// really runs, with why on hover.
func TestASubstitutedModelShowsWhatReallyRuns(t *testing.T) {
	h, _, _ := confirming(t, fake.Turn(false), func(s provider.SessionSpec) []provider.Event {
		return []provider.Event{
			provider.Granted{Access: s.Access, Mode: "default"},
			provider.Ran{Model: "Claude Sonnet 5.5", Effort: s.Model.Effort, Why: "Claude Opus 5.5 is unavailable for this account"},
		}
	})
	h.until("idle", func() bool { h.tt.Frame(); return !h.a.current().View.Live() && h.a.current().View.Confirmed.Model != nil })
	tipOn(t, h, "Claude Sonnet 5.5", "Claude runs Claude Sonnet 5.5: Claude Opus 5.5 is unavailable for this account")
	if h.tt.HasText("Claude Opus 5.5") {
		t.Errorf("the button still shows the model asked for: %q", h.tt.Texts())
	}
	accessShot(t, h, "model-substituted")
}

// An approval card names what its turn really runs at once the turn's
// provider fell back from the level it was admitted at.
func TestAnApprovalCardNamesTheLevelItsTurnFellBackTo(t *testing.T) {
	h, d, id := confirming(t, fake.Turn(true), func(s provider.SessionSpec) []provider.Event {
		if s.Access == api.AccessAutoEdits {
			return []provider.Event{provider.Granted{Access: api.AccessAutoEdits, Mode: "acceptEdits", Why: "auto mode unavailable for this model"}}
		}
		return []provider.Event{provider.Granted{Access: s.Access, Mode: "default"}}
	})
	h.until("the first approval", func() bool { h.tt.Frame(); return len(h.a.current().View.Requests) == 1 })
	r := h.a.current().View.Requests[0]
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.RequestRespond{Request: r.ID, Generation: r.Generation, Answer: json.RawMessage(`{"allow":true}`)}}); err != nil {
		t.Fatal(err)
	}
	h.until("idle", func() bool { h.tt.Frame(); return !h.a.current().View.Live() })
	h.configure(api.ThreadConfigure{Access: new(api.AccessAutoEdits)})
	if _, err := d.Endpoint().Submit(context.Background(), api.Command{ID: api.NewID(), Target: id, Body: api.MessageDispatch{Text: "Again.", Mode: api.StartNow}}); err != nil {
		t.Fatal(err)
	}
	h.until("the second approval", func() bool {
		h.tt.Frame()
		v := h.a.current().View
		return len(v.Requests) == 1 && v.Confirmed.Access.Asked == api.AccessAutoEdits
	})
	h.tt.Advance(time.Second)
	if !h.tt.HasText("Approve edits only") || h.tt.HasText(accessAuto) {
		t.Errorf("the card and control don't name the level the turn fell back to: %q", h.tt.Texts())
	}
}
