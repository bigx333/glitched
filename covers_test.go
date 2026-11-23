package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider"
)

// dropTable is a tool's input whose last line, the one an allow must not
// approve unread, is dropTable's needle.
const dropTable = "note: \"list\"\nlimit: 10\nschema: \"public\"\nmode: \"rw\"\ntimeout: 30\nretry: false\nlabel: \"x\"\n" + dropNeedle

const dropNeedle = `sql: "DROP TABLE users"`

func dropAsk() provider.Event {
	body, _ := json.Marshal(map[string]string{"operation": "use mcp__db__exec", "args": dropTable})
	return provider.RequestOpened{ID: "ask", Kind: "approval", Body: body}
}

// tallDraft is ten lines, which grow the composer over the timeline.
var tallDraft = strings.Repeat("steer line\n", 9) + "steer line"

// answerOnlyLegible presses answer up to four times, a second apart, and
// fails if one answers r while no frame since legible was true drew needle
// legibly.
func answerOnlyLegible(t *testing.T, h *harness, r *client.RequestEntry, field, needle string, legible bool, answer func()) {
	t.Helper()
	for i := range 4 {
		h.tt.Advance(time.Second)
		legible = legible || legibleNow(h, r, field, needle)
		answer()
		h.tt.Frame()
		if answering(h, r) {
			if !legible {
				t.Errorf("press %d answered with %q never legible", i, needle)
			}
			return
		}
	}
}

// change sees nothing of a needle the text does not hold, however the
// window changed in the frames before.
func TestChangeSeesOnlyTheNeedle(t *testing.T) {
	h := askedIn(t, 760, 480, dropAsk())
	r := lastRequest(h.a.current())
	h.a.draft = tallDraft
	scrollTexts(h, 80)
	scrollTexts(h, -10000)
	h.a.draft = ""
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if most, rows := change(h, r, "args", "a string absent from these args"); most != 0 || rows != 0 {
		t.Errorf("a needle the args lack changes the window by %d in %d rows", most, rows)
	}
}

// A draft that grows as the card scrolls covers the card's last line from
// that frame on: the composer and the card's lines are read from one
// frame, so the covered line counts unread, and once the draft is gone
// and the line scrolled out of view, neither ⌘⏎ nor Allow once approves
// it.
func TestNoAnswerApprovesALineAGrowingDraftCovered(t *testing.T) {
	for _, size := range [][2]int{{760, 480}, {1320, 480}} {
		for _, how := range []string{"⌘⏎", "Allow once"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], how), func(t *testing.T) {
				h := askedIn(t, size[0], size[1], dropAsk())
				r := lastRequest(h.a.current())
				h.tt.Frame()
				if legibleNow(h, r, "args", dropNeedle) {
					t.Fatalf("the args' last line shows before the texts scroll")
				}
				h.a.draft = tallDraft
				header, _ := h.tt.Find("Approval needed")
				// The args' last line, which no frame drew, scrolled to the
				// box's end, the last place the composer grows over in it.
				s, rd := h.a.drawn[r.Request], h.a.reads[r.Request]
				h.tt.Scroll(header.X+60, header.Y+header.H+40, 0, rd.last.Y+rd.last.H-s.texts.Y-s.texts.H)
				h.tt.Frame()
				h.tt.Advance(time.Second)
				if legibleNow(h, r, "args", dropNeedle) {
					t.Fatalf("the draft does not cover the args' last line")
				}
				scrollTexts(h, -10000)
				h.a.draft = ""
				h.tt.Frame()
				h.tt.Advance(time.Second)
				if legibleNow(h, r, "args", dropNeedle) {
					t.Fatalf("the args' last line shows once scrolled back")
				}
				if how == "⌘⏎" {
					h.tt.Key(ui.Cmd, ui.KeyEnter)
				} else {
					h.click("Allow once")
				}
				h.tt.Frame()
				if answering(h, r) {
					t.Errorf("%s approved %q, which no frame drew uncovered", how, dropNeedle)
				}
			})
		}
	}
}

// counted is whether the card of r counts every rune of needle, in field,
// drawn: whether an answer may approve it.
func counted(h *harness, r *client.RequestEntry, field, needle string) bool {
	rd := h.a.reads[r.Request]
	var b map[string]string
	json.Unmarshal(r.Body, &b)
	at := strings.Index(b[field], needle)
	if rd == nil || rd.texts[field] == nil || at < 0 {
		return false
	}
	from := utf8.RuneCountInString(b[field][:at])
	return !slices.Contains(rd.texts[field].drawn[from:from+utf8.RuneCountInString(needle)], false)
}

// A window made shorter under a tall draft moves the composer up over the
// card's last line: the frame after reads the card's lines and the
// composer from one layout, so the line the composer covers stays unread.
// No answer reaches it this way, but the read gate's ledger must not count
// it.
func TestAResizeUnderATallDraftLeavesTheCoveredLineUnread(t *testing.T) {
	for _, from := range []int{600, 640, 700} {
		for _, by := range []int{20, 40, 60, 80} {
			t.Run(fmt.Sprintf("%d-%d", from, by), func(t *testing.T) {
				dir := tempDir(t)
				engine(t, daemon.Config{Dir: dir}, burst{[]provider.Event{dropAsk()}})
				h := start(t, dir)
				h.tt.SetSize(1320, from)
				h.a.draft = "Go."
				h.tt.Frame()
				h.click("Send")
				h.a.draft = tallDraft
				h.until("the approval card", func() bool { return h.tt.HasText("Approval needed") })
				h.tt.Advance(time.Second)
				r := lastRequest(h.a.current())
				if counted(h, r, "args", dropNeedle) || legibleNow(h, r, "args", dropNeedle) {
					t.Skip("the args' last line shows over the draft at this size")
				}
				h.tt.SetSize(1320, from-by)
				h.tt.Frame()
				h.tt.Frame()
				if counted(h, r, "args", dropNeedle) && !legibleNow(h, r, "args", dropNeedle) {
					t.Errorf("the args' last line, under the composer, counts read")
				}
			})
		}
	}
}

// The open agents panel floats over the chat, past the composer's frame,
// and covers the lines of a card under it: they count unread, so that
// once the panel is closed, with them scrolled out of view in their box,
// ⌘⏎ shows them before it approves them.
func TestNoAnswerApprovesALineTheAgentsPanelCovered(t *testing.T) {
	// More than the card's box holds, which scrolls: the reason's last line
	// shows only scrolled to.
	body, _ := json.Marshal(map[string]string{"operation": "use mcp__db__exec", "args": dropTable,
		"reason": strings.Repeat("Check the users table.\n", 7) + dropNeedle})
	for _, by := range []int{0, 40} {
		t.Run(fmt.Sprintf("box ends %d above it", by), func(t *testing.T) {
			dir := tempDir(t)
			d := engine(t, daemon.Config{Dir: dir}, burst{[]provider.Event{provider.RequestOpened{ID: "ask", Kind: "approval", Body: body}}})
			top := seed(t, d, "/work/t3code", "Top")
			for i := range 6 {
				seedChild(t, d, top, fmt.Sprint("Kid ", i))
			}
			h := start(t, dir)
			h.tt.SetSize(1320, 660)
			h.a.select_(h.thread("Top"))
			h.tt.Advance(time.Second)
			h.click("Agents")
			h.tt.Advance(time.Second)
			if !h.a.agentsOpen[h.a.current().id()] {
				t.Fatalf("the agents panel did not open")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := d.Endpoint().Submit(ctx, api.Command{ID: api.NewID(), Target: top, Body: api.MessageDispatch{Text: "Go.", Mode: api.AfterActive}}); err != nil {
				t.Fatal(err)
			}
			h.until("the approval card", func() bool { return h.tt.HasText("Approval needed") })
			h.tt.Advance(time.Second)
			r := lastRequest(h.a.current())
			// The window as tall as ends the box by above the reason's last
			// line, which the panel's glass hid from every frame.
			s, rd := h.a.drawn[r.Request], h.a.reads[r.Request]
			h.tt.SetSize(1320, 660+int(rd.last.Y-s.texts.Y-s.texts.H)-by)
			h.tt.Advance(time.Second)
			// Its last line at the box's end, under the panel's glass.
			scrollTexts(h, 10000)
			scrollTexts(h, -10000)
			h.tt.Key(0, ui.KeyEscape)
			h.tt.Advance(time.Second)
			if h.a.agentsOpen[h.a.current().id()] {
				t.Fatalf("Escape left the agents panel open")
			}
			if legibleNow(h, r, "reason", dropNeedle) {
				t.Fatalf("the reason's last line shows once the panel closed")
			}
			answerOnlyLegible(t, h, r, "reason", dropNeedle, false, func() { h.tt.Key(ui.Cmd, ui.KeyEnter); h.tt.HoldModifiers(0) })
		})
	}
}
