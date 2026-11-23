package main

import (
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/daemon"
	"glitched/internal/provider"
	"glitched/internal/provider/fake"

	"github.com/egoist/mygo/ui"
)

// playedOnce is a harness showing a thread whose one turn played script,
// to its end.
func playedOnce(t *testing.T, script []provider.Event) *harness {
	t.Helper()
	ws := gitWorkspace(t)
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(script))
	seed(t, d, ws, "Ship the sort", "Sort them by most recent activity.")
	h := start(t, dir)
	h.a.select_(h.thread("Ship the sort"))
	h.until("its turn to end", func() bool {
		v := h.a.current().View
		return !v.Working() && v.LastTurn != nil && h.tt.HasText("Sort them by most recent activity.")
	})
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	return h
}

// pointAt points at the message that says text, as a person reaching for
// its toolbar does.
func pointAt(t *testing.T, h *harness, text string) {
	t.Helper()
	h.tt.Move(800, 30)
	h.tt.Advance(time.Second)
	h.hover(text)
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
}

// Retry is under a turn that failed or was stopped, on its last reply, or
// on its message when it said nothing: a turn that completed has nothing
// to ask again. Retry asks it again.
func TestRetryIsOnlyUnderATurnThatFailedOrWasStopped(t *testing.T) {
	const msg = "Sort them by most recent activity."
	for _, tc := range []struct {
		name   string
		script []provider.Event
		retry  string // the message whose toolbar has Retry; "" for none
		others []string
	}{
		{"completed", checks, "", []string{msg, "One test fails."}},
		{"failed after some text", []provider.Event{
			provider.TextDelta{Text: "Half of it is sorted."},
			provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "The agent stopped answering."},
		}, "Half of it is sorted.", []string{msg}},
		{"failed saying nothing", []provider.Event{
			provider.TurnEnded{Outcome: api.OutcomeFailed, Error: "Claude Code is not signed in."},
		}, msg, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := playedOnce(t, tc.script)
			for _, at := range tc.others {
				pointAt(t, h, at)
				if !h.tt.HasText("Copy") || h.tt.HasText("Retry") {
					t.Errorf("pointing at %q shows %q, want Copy and no Retry", at, h.tt.Texts())
				}
			}
			if tc.retry == "" {
				return
			}
			pointAt(t, h, tc.retry)
			if !h.tt.HasText("Copy") || !h.tt.HasText("Retry") {
				t.Fatalf("pointing at %q shows %q, want Copy and Retry", tc.retry, h.tt.Texts())
			}
			h.click("Retry")
			h.until("the message asked again", func() bool {
				n := 0
				for _, e := range entriesOf(h.a.current()) {
					if e.kind == entryUser && e.text == msg {
						n++
					}
				}
				return n == 2
			})
		})
	}
}

// A turn stopped part way has Retry on its last reply alone, not on the
// text it said before its work.
func TestRetryIsUnderAStoppedTurnsLastReply(t *testing.T) {
	h := start(t, tempDir(t))
	h.a.draft = "Fix the flicker"
	h.tt.Frame()
	h.click("Send")
	h.until("the turn to wait on its approval", func() bool { return h.a.current().View.Run == api.RunWaiting })
	h.click("Stop")
	h.until("the turn to end", func() bool {
		v := h.a.current().View
		return !v.Working() && v.LastTurn != nil
	})
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	pointAt(t, h, "Looking at the code.")
	if !h.tt.HasText("Copy") || h.tt.HasText("Retry") {
		t.Errorf("the stopped turn's first reply shows %q, want Copy and no Retry", h.tt.Texts())
	}
	pointAt(t, h, "May I edit it?")
	if !h.tt.HasText("Retry") {
		t.Errorf("the stopped turn's last reply shows %q, want Retry", h.tt.Texts())
	}
}

// yourToolbar is the built-in Message toolbar as main drew it, before
// Retry was a failed turn's alone: a copy of it places Retry under every
// reply.
const yourToolbar = `export function place({ items }) {
  return items.map(({ context }) => ({
    type: "row",
    gap: 2,
    children: [
      { type: "native", id: "copy", name: "message.copy" },
      context.role === "user"
        ? { type: "native", id: "edit", name: "message.edit" }
        : { type: "native", id: "retry", name: "message.retry" },
      ...Array.from({ length: context.items ?? 0 }, (_, i) => ({ type: "native", id: "item-" + i, name: "message.item", props: { index: i } })),
    ],
  }))
}`

// A copy of yours may place an action twice, with no ids, and give one
// the room left: Copy draws twice and grows as it did before Retry could
// be absent.
func TestYourToolbarsAnonymousActionsKeepTheirOwnRoom(t *testing.T) {
	h, _ := checked(t)
	writeOwnPlugin(t, h.a, "my-bar", barManifest, barModule(`{ type: "row", width: 400, children: [{ type: "native", name: "message.copy" }, { type: "native", name: "message.retry" }, { type: "native", name: "message.copy", grow: true }, { type: "text", text: "End" }] }`))
	allow(t, h.a, "my-bar", "ui.replace")
	pointAt(t, h, "One test fails.")
	if n := strings.Count(strings.Join(h.tt.Texts(), "\n"), "Copy"); n != 2 || h.tt.HasText("Retry") {
		t.Fatalf("yours shows %d Copy and Retry %v, want 2 and none: %q", n, h.tt.HasText("Retry"), h.tt.Texts())
	}
	cp, _ := h.tt.Find("Copy")
	end, _ := h.tt.Find("End")
	if end.X-cp.X < 300 {
		t.Errorf("End is %v after the first Copy: the growing Copy took no room", end.X-cp.X)
	}
}

// A copy of yours that places Retry under a reply that completed draws no
// Retry there, and the actions after it close up, on screen and in the
// keyboard's order, as the built-in's do.
func TestYourToolbarGetsNoRetryUnderAReplyThatCompleted(t *testing.T) {
	for _, yours := range []bool{false, true} {
		h, _ := checked(t)
		writeOwnPlugin(t, h.a, "mine", menusManifest, menusModule)
		if yours {
			writeOwnPlugin(t, h.a, "my-bar", barManifest, yourToolbar)
			allow(t, h.a, "my-bar", "ui.replace")
		}
		pointAt(t, h, "One test fails.")
		if yours {
			if drawer, _ := h.a.partDrawer("message.toolbar"); drawer.ID != "my-bar" {
				t.Fatalf("the toolbar is drawn by %+v, want yours", drawer)
			}
		}
		if !h.tt.HasText("Copy") || !h.tt.HasText("Quote") || h.tt.HasText("Retry") {
			t.Fatalf("yours %v: the reply's toolbar shows %q, want Copy and Quote, no Retry", yours, h.tt.Texts())
		}
		cp, _ := h.tt.Find("Copy")
		q, _ := h.tt.Find("Quote")
		// From Copy's words to the next action's: Copy's padding, the
		// row's gap, then the next's padding, icon and gap.
		if want := float32(6 + 2 + 6 + 11 + 4); q.X-(cp.X+cp.W) > want+1 || q.X-(cp.X+cp.W) < want-1 {
			t.Errorf("yours %v: Quote starts %v after Copy ends, want %v", yours, q.X-(cp.X+cp.W), want)
		}
		focused := false
		for range 80 {
			if h.tt.Key(0, ui.KeyTab); h.tt.Focused("Copy") {
				focused = true
				break
			}
		}
		if !focused {
			t.Fatalf("yours %v: Tab never reaches Copy", yours)
		}
		if h.tt.Key(0, ui.KeyTab); !h.tt.Focused("Quote") {
			t.Errorf("yours %v: Tab after Copy doesn't go to Quote", yours)
		}
	}
}
