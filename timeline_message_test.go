package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

const rowsManifest = `{"id": "my-rows", "name": "My rows", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "row", "at": "timeline.message", "replaces": true}]}}`

func rowsModule(body string) string {
	return `export function place({ items }) { return items.map(({ key, context }) => (` + body + `)) }`
}

// timelineHarness is a thread of a few turns, shown.
func timelineHarness(t *testing.T) (*harness, *client.Transcript) {
	t.Helper()
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/t3code", "A thread", "Start")
	h := start(t, dir)
	th := h.thread("A thread")
	h.a.select_(th)
	h.until("its first turn", func() bool { settleTester(h.tt); return h.tt.HasText("Done.") })
	fresh := *th.Transcript
	fresh.Entries = synthetic(6)
	th.Transcript.Replace(fresh)
	h.a.changed()
	h.until("its rows", func() bool { settleTester(h.tt); return h.tt.HasText(turnText(1)) })
	return h, th.Transcript
}

// Each row of the timeline but approval, question and report cards is the
// built-in Timeline rows plugin's, around the native transcript.body.
// Yours replaces it once granted ui.replace and may draw around the body;
// one that leaves it out, places it twice, draws nothing or fails gives way
// to Glitched's own row.
func TestTimelineRowsAreABuiltInPluginYoursCanWrap(t *testing.T) {
	h, _ := timelineHarness(t)
	a, tt := h.a, h.tt
	shows := func(label string) bool {
		settlePlugins(t, a, tt, h.p)
		return tt.HasText(label)
	}
	rows := func() bool { return shows(turnText(1)) && shows("Reply 1.") && shows("Worked for 40s") }
	if !rows() {
		t.Fatalf("no rows: %q", tt.Texts())
	}
	if drawer, drawn := a.partDrawer("timeline.message"); !drawn || drawer.ID != "timeline" || !drawer.Builtin {
		t.Fatalf("the rows aren't the built-in Timeline rows': %+v %v", drawer, drawn)
	}

	wrap := rowsModule(`{ type: "column", children: [{ type: "text", text: "Mine " + context.kind }, { type: "native", id: "body", name: "transcript.body" }] }`)
	writeOwnPlugin(t, a, "my-rows", rowsManifest, wrap)
	if shows("Mine user") || !rows() {
		t.Fatal("yours drew the rows before it was granted ui.replace")
	}
	if err := a.plugins.SetGranted("my-rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if !shows("Mine user") || !shows("Mine assistant") || !shows("Mine work") || !rows() {
		t.Fatalf("yours doesn't draw around transcript.body: %q", tt.Texts())
	}

	for name, body := range map[string]string{
		"leaves it out":   `{ type: "text", text: "Mine " + context.kind }`,
		"places it twice": `{ type: "column", children: [{ type: "text", text: "Mine " + context.kind }, { type: "native", id: "a", name: "transcript.body" }, { type: "native", id: "b", name: "transcript.body" }] }`,
	} {
		writeOwnPlugin(t, a, "my-rows", rowsManifest, rowsModule(body))
		if shows("Mine user") || !rows() {
			t.Errorf("a layout that %s drew in place of Glitched's rows: %q", name, tt.Texts())
		}
		if note := a.partState(installed(a, "my-rows"), "timeline.message").why; !strings.Contains(note, "places transcript.body once") {
			t.Errorf("Settings says %q of a layout that %s", note, name)
		}
	}
	for name, js := range map[string]string{
		"throws":        brokenModule,
		"draws nothing": `export function place({ items }) { return items.map(() => null) }`,
	} {
		writeOwnPlugin(t, a, "my-rows", rowsManifest, js)
		if shows("Mine user") || !rows() {
			t.Errorf("rows that %s left Glitched's rows out: %q", name, tt.Texts())
		}
	}

	writeOwnPlugin(t, a, "my-rows", rowsManifest, wrap)
	if !shows("Mine user") {
		t.Fatalf("yours doesn't draw again: %q", tt.Texts())
	}
	for _, id := range []string{"timeline", "my-rows"} {
		if err := a.plugins.SetEnabled(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if shows("Mine user") || !rows() {
		t.Fatalf("with no Timeline rows plugin on, Glitched's rows don't draw: %q", tt.Texts())
	}
}

// A reply streaming in shows its newest text in every frame while the
// plugin that draws around it isn't asked again: its context says it
// streams, not what it says. It is asked once more as it ends.
func TestAStreamingReplyIsNeverStaleNorDrawnAgainByThePlugin(t *testing.T) {
	h, tr := timelineHarness(t)
	a, tt := h.a, h.tt
	writeOwnPlugin(t, a, "my-rows", rowsManifest, `let drawn = {}
export function place({ items }) {
	return items.map(({ key, context }) => ({ type: "column", children: [
		{ type: "text", text: "Mine " + context.kind + (context.streaming ? " streaming " : " ") + (drawn[key] = (drawn[key] || 0) + 1) },
		{ type: "native", id: "body", name: "transcript.body" }] }))
}`)
	if err := a.plugins.SetGranted("my-rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	a.listFor(h.thread("A thread").id()).ScrollToEnd()
	settlePlugins(t, a, tt, h.p)
	// apply folds an event in, as following the thread does.
	apply := func(b api.EventBody) {
		p, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		tr.Apply(api.Event{Cursor: api.Cursor{Log: tr.Cursor.Log, Seq: tr.Cursor.Seq + 1}, Type: api.EventType(b), Schema: api.EventSchema, At: time.Now(), Payload: p})
		a.changed()
		tt.Frame()
	}
	run := api.NewID()
	for i := range 40 {
		word := "Word" + strings.Repeat("x", i%3) + string(rune('A'+i%26))
		apply(api.AssistantDelta{Run: run, Text: word + " "})
		// Streamed text fades in; once it has, the row shows it all.
		tt.Advance(time.Second / 2)
		if !tt.HasText(word) {
			t.Fatalf("step %d: the streaming reply doesn't show %q: %q", i, word, tt.Texts())
		}
	}
	settlePlugins(t, a, tt, h.p)
	if !tt.HasText("Mine assistant streaming 1") {
		t.Fatalf("the plugin was asked for the streaming row again as it streamed: %q", tt.Texts())
	}
	// A reply of another run ends this one's.
	apply(api.AssistantDelta{Run: api.NewID(), Text: "Next."})
	settlePlugins(t, a, tt, h.p)
	if !tt.HasText("Mine assistant 2") {
		t.Fatalf("the plugin wasn't asked once more as the reply ended: %q", tt.Texts())
	}
}
