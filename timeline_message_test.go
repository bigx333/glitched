package main

import (
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
	h.until("its transcript", func() bool { return th.Transcript != nil && len(th.Transcript.Entries) > 0 })
	th.Transcript.Entries = synthetic(6)
	h.a.changed()
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
		settleTester(tt)
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
	settleTester(tt)
	reply := &client.AssistantEntry{Run: api.NewID(), Streaming: true}
	tr.Entries = append(tr.Entries, reply)
	for i := range 40 {
		reply.Text += "Word" + strings.Repeat("x", i%3) + string(rune('A'+i%26)) + " "
		a.changed()
		tt.Frame()
		last := strings.Fields(reply.Text)[i]
		if i > 0 && !tt.HasText(last) {
			t.Fatalf("step %d: the streaming reply doesn't show %q: %q", i, last, tt.Texts())
		}
		tt.Advance(time.Second / 30)
	}
	settleTester(tt)
	if !tt.HasText("Mine assistant streaming 1") {
		t.Fatalf("the plugin was asked for the streaming row again as it streamed: %q", tt.Texts())
	}
	reply.Streaming = false
	a.changed()
	settleTester(tt)
	if !tt.HasText("Mine assistant 2") {
		t.Fatalf("the plugin wasn't asked once more as the reply ended: %q", tt.Texts())
	}
}
