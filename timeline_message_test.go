package main

import (
	"encoding/json"
	"slices"
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

// A copy a plugin answers null for is drawn by whoever draws the part when
// the plugin doesn't, and Settings says so, of any part: one answered null
// for every copy shown isn't drawing it, and one answered null for some
// draws only the others.
func TestThePluginsPageSaysWhoDrawsTheCopiesAPluginAnswersNullFor(t *testing.T) {
	h, _ := timelineHarness(t)
	a, tt := h.a, h.tt
	state := func(id, part string) partState {
		settlePlugins(t, a, tt, h.p)
		return a.partState(installed(a, id), part)
	}

	writeOwnPlugin(t, a, "my-rows", rowsManifest, `export function place({ items }) { return items.map(() => null) }`)
	if err := a.plugins.SetGranted("my-rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if st := state("my-rows", "timeline.message"); st.word != "Answered null" || st.why != "It answered null for every copy shown, so the built-in draws it." {
		t.Errorf("Settings says %q: %q of rows answered null for every copy", st.word, st.why)
	}
	if drawer, drawn := a.partDrawer("timeline.message"); !drawn || drawer.ID != "timeline" {
		t.Errorf("Settings has %+v (%v) draw the rows my-rows answers null for", drawer, drawn)
	}
	if err := a.plugins.SetEnabled("timeline", false); err != nil {
		t.Fatal(err)
	}
	if st := state("my-rows", "timeline.message"); st.why != "It answered null for every copy shown, so Glitched draws its own." {
		t.Errorf("with the built-in off, Settings says %q", st.why)
	}

	writeOwnPlugin(t, a, "my-rows", rowsManifest, rowsModule(`context.kind === "user" ? null : { type: "column", children: [{ type: "text", text: "Mine " + context.kind }, { type: "native", id: "body", name: "transcript.body" }] }`))
	st := state("my-rows", "timeline.message")
	if !tt.HasText("Mine assistant") || tt.HasText("Mine user") {
		t.Fatalf("my-rows doesn't draw only the rows it answers: %q", tt.Texts())
	}
	if st.word != "Drawing some" || !strings.HasPrefix(st.why, "It answered null for ") || !strings.HasSuffix(st.why, " copies shown, so Glitched draws its own for those.") {
		t.Errorf("Settings says %q: %q of rows answered null for some copies", st.word, st.why)
	}
	// Each copy it answers null for names who draws that one.
	writeOwnPlugin(t, a, "my-rows", rowsManifest, rowsModule(`context.kind !== "assistant" ? null : { type: "column", children: [{ type: "text", text: "Mine " + context.kind }, { type: "native", id: "body", name: "transcript.body" }] }`))
	writeOwnPlugin(t, a, "my-work", strings.NewReplacer("my-rows", "my-work", "My rows", "My work").Replace(rowsManifest), rowsModule(`context.kind !== "work" ? null : { type: "column", children: [{ type: "text", text: "Work " + context.kind }, { type: "native", id: "body", name: "transcript.body" }] }`))
	if err := a.plugins.SetGranted("my-work", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	st = state("my-rows", "timeline.message")
	if !tt.HasText("Mine assistant") || !tt.HasText("Work work") {
		t.Fatalf("my-rows and my-work don't each draw their rows: %q", tt.Texts())
	}
	if !strings.HasSuffix(st.why, " copies shown, so My work and Glitched draw them.") {
		t.Errorf("Settings says %q of rows that My work and Glitched draw", st.why)
	}
	if err := a.plugins.SetEnabled("my-work", false); err != nil {
		t.Fatal(err)
	}

	// A new thread shows no rows: none answered null.
	writeOwnPlugin(t, a, "my-rows", rowsManifest, `export function place({ items }) { return items.map(() => null) }`)
	if st := state("my-rows", "timeline.message"); st.word != "Answered null" {
		t.Fatalf("Settings says %q of rows answered null for every copy", st.word)
	}
	a.newThread()
	if st := state("my-rows", "timeline.message"); st.word == "Answered null" || tt.HasText(turnText(1)) {
		t.Errorf("a new thread with no rows has Settings say %q: %q", st.word, st.why)
	}

	writeOwnPlugin(t, a, "my-actions", `{"id": "my-actions", "name": "My actions", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace"], "glitched": {"places": [{"id": "actions", "at": "thread.header.actions", "replaces": true}]}}`,
		`export function place({ items }) { return items.map(() => null) }`)
	if err := a.plugins.SetGranted("my-actions", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	if st := state("my-actions", "thread.header.actions"); st.word != "Answered null" || st.why != "It answered null for every copy shown, so the built-in draws it." {
		t.Errorf("Settings says %q: %q of header actions answered null", st.word, st.why)
	}
	if err := a.plugins.SetEnabled("header-actions", false); err != nil {
		t.Fatal(err)
	}
	if st := state("my-actions", "thread.header.actions"); st.why != "It answered null for every copy shown, and nothing draws it." {
		t.Errorf("with the built-in off, Settings says %q of header actions answered null", st.why)
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

// replyKeyOf is the key of the fake turn's reply.
func replyKeyOf(t *testing.T, h *harness) string {
	t.Helper()
	es := entriesOf(h.a.current())
	i := slices.IndexFunc(es, func(e entry) bool {
		return e.kind == entryAssistant && strings.Contains(e.text, "Looking at the code.")
	})
	if i < 0 {
		t.Fatal("reply missing from the transcript")
	}
	return es[i].key
}

// A reply's selection and its Quote bar outlive the built-in drawing the
// row Glitched drew while the built-in had not.
func TestASelectedReplyKeepsItsQuoteWhenTheBuiltInTakesOver(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	a, tt := h.a, h.tt
	if err := a.plugins.SetGranted("timeline", "ui.replace", false); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, tt, h.p)
	if _, ok := a.partDrawer("timeline.message"); ok {
		t.Fatal("the timeline plugin still draws with the grant revoked")
	}
	sel := dragHalfReply(t, h)
	tt.Advance(quoteWait + 50*time.Millisecond)
	if !barShown(h) {
		t.Fatal("could not open the quote bar on the native fallback")
	}
	if err := a.plugins.SetGranted("timeline", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, a, tt, h.p)
	tt.Advance(quoteWait + 50*time.Millisecond)
	if drawer, ok := a.partDrawer("timeline.message"); !ok || drawer.ID != "timeline" || !drawer.Builtin {
		t.Fatalf("the built-in did not take over from Glitched's own rows: %+v, %v", drawer, ok)
	}
	if node := a.plugins.App("timeline").PlaceView("row", replyKeyOf(t, h)); node == nil || node.Name != "transcript.body" {
		t.Fatalf("the built-in did not draw the selected reply: %+v", node)
	}
	if !barShown(h) || a.quote.text != sel {
		t.Fatalf("after the layout handoff, selection=%q and quote bar=%v; want %q with the quote bar", a.quote.text, barShown(h), sel)
	}
	if err := tt.Click("Quote"); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(a.attachments, func(at attachment) bool { return at.quote && at.text == sel }) {
		t.Fatalf("Quote did not attach the original selected text %q", sel)
	}
}

// And a user's copy that places only transcript.body, drawn late, keeps
// them too.
func TestASelectedReplyKeepsItsQuoteWhenALateCopyTakesOver(t *testing.T) {
	h := enterReady(t, fake.New(fake.Turn(false)), "go")
	a, tt := h.a, h.tt
	if err := a.plugins.SetGranted("timeline", "ui.replace", false); err != nil {
		t.Fatal(err)
	}
	h.untilShown("the reply", "Looking at the code.")
	settlePlugins(t, a, tt, h.p)
	manifest := `{"id":"async-rows","name":"Async rows","main":"index.js","contract":"glitched","contractVersion":2,
	"capabilities":["ui.place","ui.replace"],"glitched":{"places":[{"id":"row","at":"timeline.message","replaces":true}]}}`
	writeOwnPlugin(t, a, "async-rows", manifest, `
export async function place({items}) {
	await glitched.app.settings.get()
	return items.map(() => ({type:"native", id:"body", name:"transcript.body"}))
}`)
	if err := a.plugins.SetGranted("async-rows", "ui.replace", true); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	key := replyKeyOf(t, h)
	s := a.plugins.App("async-rows")
	if s.PlaceView("row", key) != nil {
		t.Fatal("the user layout drew before the UI answered its pending call")
	}
	sel := dragHalfReply(t, h)
	tt.Advance(quoteWait + 50*time.Millisecond)
	if !barShown(h) {
		t.Fatal("no quote bar on the native fallback")
	}
	if s.PlaceView("row", key) != nil {
		t.Fatal("the user layout replaced the native fallback before selection was captured")
	}
	settlePlugins(t, a, tt, h.p)
	tt.Advance(quoteWait + 50*time.Millisecond)
	if node := s.PlaceView("row", key); node == nil || node.Name != "transcript.body" || s.Err != nil {
		t.Fatalf("did not publish the user's finished layout: %+v, %v", node, s.Err)
	}
	if !barShown(h) || a.quote.text != sel {
		t.Fatalf("finishing a native-only layout cleared selection %q to %q (quote bar: %v)", sel, a.quote.text, barShown(h))
	}
}
