package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
	"glitched/internal/plugins/widget"
	"glitched/internal/provider/fake"
)

// sendModule draws a Send of its own, which sends the draft through
// composer.submit, or with DRAWS "nothing" an empty row. With DRAWS
// "throws", it throws while the composer holds a draft.
const sendModule = `export function place({ items }) {
	return items.map((it) => {
		if (DRAWS === "throws" && it.context.draft) throw new Error("broken on purpose")
		return DRAWS === "nothing" ? { type: "row", children: [] } : { type: "button", id: "mine", label: "My send", onClick: "send" }
	})
}
export async function update({ event }) {
	if (event.kind === "ui" && event.handler === "send") await glitched.composer.submit(event.place.context.thread.id)
	return {}
}`

func sendPlugin(draws string) string {
	return strings.ReplaceAll(sendModule, "DRAWS", strconv.Quote(draws))
}

const sendManifest = `{"id": "my-send", "name": "My send", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:write"], "glitched": {"places": [{"id": "send", "at": "composer.send", "replaces": true}]}}`

// A plugin of the user's draws Send once granted ui.replace and
// threads:write, and sends only the user's draft. Beside it Glitched keeps
// its own Stop while the agent works, whatever the plugin draws, and one
// that fails gives way to Glitched's Send.
func TestAPluginDrawsSendButGlitchedKeepsStop(t *testing.T) {
	h, th := questioning(t, fake.Asking())
	h.a.draft = "Use SQLite."
	shows := func(label string) bool {
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		_, ok := h.tt.Find(label)
		return ok
	}
	if !shows("Send") || !shows("Stop") {
		t.Fatalf("Glitched's Send and Stop don't show: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "my-send", sendManifest, sendPlugin("send"))
	if shows("My send") || !shows("Send") {
		t.Fatal("a plugin drew Send before it was granted ui.replace")
	}
	h.a.plugins.SetGranted("my-send", "ui.replace", true)
	if shows("My send") || !shows("Send") {
		t.Fatal("a plugin drew Send granted ui.replace but not threads:write")
	}
	h.a.plugins.SetGranted("my-send", "threads:write", true)
	if !shows("My send") || shows("Send") || !shows("Stop") {
		t.Fatalf("the granted plugin doesn't draw Send beside Glitched's Stop: %q", h.tt.Texts())
	}
	h.click("My send")
	h.until("the draft sent", func() bool {
		return h.a.draft == "" && strings.Contains(texts(entriesOf(h.a.current())), "Use SQLite.")
	})

	writeOwnPlugin(t, h.a, "my-send", sendManifest, sendPlugin("throws"))
	if !shows("My send") {
		t.Fatalf("the plugin doesn't draw Send: %q", h.tt.Texts())
	}
	h.a.draft = "Use memory."
	if shows("My send") || !shows("Send") || !shows("Stop") {
		t.Fatalf("a Send that threw left no Glitched Send: %q", h.tt.Texts())
	}
	if note := h.a.partState(installed(h.a, "my-send"), "composer.send").why; !strings.Contains(note, "broken on purpose") || !strings.Contains(note, "Glitched draws its own") {
		t.Errorf("Settings says %q of the broken Send", note)
	}

	writeOwnPlugin(t, h.a, "my-send", sendManifest, sendPlugin("nothing"))
	if shows("Send") || !shows("Stop") {
		t.Fatalf("a Send that draws nothing hid Glitched's Stop: %q", h.tt.Texts())
	}
	h.click("Stop")
	h.until("the turn stopped", func() bool { return !h.a.current().View.Live() })
	_ = th
}

// A plugin's composer.submit sends only the open composer's draft: with
// nothing in it, or for a thread whose composer isn't open, it's refused.
func TestComposerSubmitSendsOnlyTheOpenDraft(t *testing.T) {
	h, th := questioning(t, fake.Asking())
	for _, args := range []string{
		`{"thread": "` + th.id().String() + `"}`,
		`{"thread": "` + api.NewID().String() + `"}`,
		`{"thread": ""}`,
	} {
		if _, err := h.a.submitEffect(json.RawMessage(args)); err == nil {
			t.Errorf("composer.submit %s with nothing to send, or another composer, was taken", args)
		}
	}
	h.a.draft = "Go on."
	if _, err := h.a.submitEffect(json.RawMessage(`{"thread": "` + api.NewID().String() + `"}`)); err == nil || h.a.draft != "Go on." {
		t.Fatalf("composer.submit sent the draft for a thread whose composer isn't open: %v", err)
	}
	if _, err := h.a.submitEffect(json.RawMessage(`{"thread": "` + th.id().String() + `"}`)); err != nil {
		t.Fatal(err)
	}
	h.until("the draft sent", func() bool { return strings.Contains(texts(entriesOf(h.a.current())), "Go on.") })
}

// accessModule draws a picker of its own: a button for each level the
// context offers, and one for a level it doesn't. With DRAWS "nothing" it
// draws an empty row; with DRAWS "throws", it throws at Full access.
const accessModule = `export function place({ items }) {
	return items.map((it) => {
		if (DRAWS === "throws" && it.context.access === "Full access") throw new Error("broken on purpose")
		return DRAWS === "nothing" ? { type: "row", children: [] } : { type: "row", children: [
		...it.context.levels.map((l, i) => ({ type: "button", id: "l" + i, label: "Set " + l.name, onClick: { handler: "set", payload: l.name } })),
		{ type: "button", id: "rogue", label: "Set yolo", onClick: { handler: "set", payload: "yolo" } },
	] }
	})
}
export async function update({ event }) {
	if (event.kind !== "ui") return {}
	try {
		await glitched.threads.setAccess(event.place.context.thread.id, event.payload)
	} catch (e) {
		await glitched.toast("Refused", e.message)
	}
	return {}
}
export async function command({ id, args }) {
	await glitched.threads.setAccess(args.thread, args.access)
}`

const accessManifest = `{"id": "my-access", "name": "My access", "main": "index.js", "contract": "glitched", "contractVersion": 2,
	"capabilities": ["ui.place", "ui.replace", "threads:access"], "contributes": {"commands": [{"id": "set", "title": "s"}]},
	"glitched": {"places": [{"id": "access", "at": "composer.access", "replaces": true}]}}`

// A plugin of the user's draws the access picker once granted ui.replace
// and threads:access, which Settings › Plugins names in plain words. It
// sets the access only to a level Glitched's picker offers, through
// threads.setAccess. Beside it Glitched keeps its own sign of the access
// in force, whatever it draws; one that fails gives way to Glitched's
// picker. The question card stays, at whatever access, and is answered
// only by the user.
func TestAPluginDrawsTheAccessPickerButGlitchedShowsTheAccess(t *testing.T) {
	h, th := questioning(t, fake.Asking())
	shows := func(label string) bool {
		settlePlugins(t, h.a, h.tt, h.p)
		h.tt.Advance(time.Second)
		_, ok := h.tt.Find(label)
		return ok
	}
	if !shows("Access") || shows("Access: "+accessAuto) {
		t.Fatalf("Glitched's picker doesn't show: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "my-access", accessManifest, strings.ReplaceAll(accessModule, "DRAWS", strconv.Quote("throws")))
	if shows("Set "+accessFull) || !shows("Access") {
		t.Fatal("a plugin drew the access picker before it was granted ui.replace")
	}
	h.a.plugins.SetGranted("my-access", "ui.replace", true)
	if shows("Set "+accessFull) || !shows("Access") {
		t.Fatal("a plugin drew the access picker granted ui.replace but not threads:access")
	}
	h.a.router.Push("/settings/plugins")
	h.tt.Frame()
	h.openPlugin("My access")
	for _, s := range []string{
		"Set what agents may do in a thread, up to Full access to your computer and the internet, and draw the control that sets it",
		"It draws the control that sets what agents may do, and can give them Full access to your computer without the hold Glitched asks for.",
	} {
		if !h.tt.HasText(s) {
			t.Errorf("Settings › Plugins doesn't say %q of threads:access: %q", s, h.tt.Texts())
		}
	}
	h.a.router.Push("/")
	h.a.plugins.SetGranted("my-access", "threads:access", true)
	if !shows("Set "+accessFull) || shows("Access") || !shows("Access: "+accessAuto) {
		t.Fatalf("the granted plugin doesn't draw the picker beside Glitched's sign: %q", h.tt.Texts())
	}

	h.click("Set yolo")
	h.click("Set " + accessFull)
	h.until("the thread at Full access", func() bool { v := h.a.current(); return v.View.Access == api.AccessFull && len(v.Pending) == 0 })
	if shows("Set "+accessFull) || !shows("Access") {
		t.Errorf("a picker that threw left no Glitched picker: %q", h.tt.Texts())
	}
	if !h.tt.HasText("Claude asks") || !h.tt.HasText("Dismiss") || lastRequest(h.a.current()).State != api.RequestPending {
		t.Errorf("at Full access the question card is gone or answered: %q", h.tt.Texts())
	}

	writeOwnPlugin(t, h.a, "my-access", accessManifest, strings.ReplaceAll(accessModule, "DRAWS", strconv.Quote("nothing")))
	if shows("Set "+accessFull) || !shows("Access: "+accessFull) {
		t.Fatalf("a picker that draws nothing hid the access: %q", h.tt.Texts())
	}
	writeOwnPlugin(t, h.a, "my-access", accessManifest, brokenModule)
	if !shows("Access") || shows("Access: "+accessFull) {
		t.Fatalf("a broken picker left no Glitched picker: %q", h.tt.Texts())
	}
	if note := h.a.partState(installed(h.a, "my-access"), "composer.access").why; !strings.Contains(note, "broken on purpose") {
		t.Errorf("Settings says %q of the broken picker", note)
	}
	_ = th
}

// threads.setAccess needs threads:access, and takes only a level the
// access picker offers, and nothing else: with no thread, the default for
// new threads, as the draft's picker sets it.
func TestSetAccessTakesOnlyThePickersLevels(t *testing.T) {
	h := start(t, tempDir(t))
	opus := api.ModelRef{Provider: "claude", Model: "Claude Opus 5.5", Effort: "high"}
	id := h.a.backend.Create(h.local(), t.TempDir(), "Mine", opus, api.AccessAutoEdits)
	h.thread("Mine")
	writeOwnPlugin(t, h.a, "my-access", strings.Replace(accessManifest, `"threads:access"`, `"threads:write", "threads:access"`, 1), strings.ReplaceAll(accessModule, "DRAWS", strconv.Quote("levels")))
	settlePlugins(t, h.a, h.tt, h.p)
	run := func(args map[string]any) error {
		t.Helper()
		var failed error
		ran := false
		h.a.plugins.RunCommand("my-access", "set", args, func(err error) { failed, ran = err, true })
		h.until("set to run", func() bool { return ran })
		settlePlugins(t, h.a, h.tt, h.p)
		return failed
	}
	if err := run(map[string]any{"thread": id.String(), "access": accessFull}); err == nil || !strings.Contains(err.Error(), "threads:access") {
		t.Fatalf("set the access without threads:access: %v", err)
	}
	h.a.plugins.SetGranted("my-access", "threads:access", true)
	settlePlugins(t, h.a, h.tt, h.p)
	for _, access := range []any{"full", "Full", "yolo", "", 3, nil, string(api.AccessFull)} {
		if err := run(map[string]any{"thread": id.String(), "access": access}); err == nil {
			t.Errorf("setAccess %v was taken", access)
		}
	}
	if v := h.thread("Mine"); v.View.Access != api.AccessAutoEdits || len(v.Pending) != 0 {
		t.Fatalf("a refused setAccess changed the thread: %v, pending %v", v.View.Access, v.Pending)
	}
	for _, args := range []string{
		`{"thread": "` + id.String() + `", "access": "Full access", "model": "Claude Sonnet 5.5"}`,
		`{"thread": "` + id.String() + `", "Access": "Full access", "fast": true}`,
		`{"thread": "nope", "access": "Full access"}`,
	} {
		if _, err := h.a.setAccessEffect(json.RawMessage(args)); err == nil {
			t.Errorf("setAccess %s was taken", args)
		}
	}
	// Full asked for and taken back before it lands: the thread ends where
	// the last call left it.
	for _, level := range []string{accessFull, accessAuto} {
		if _, err := h.a.setAccessEffect(json.RawMessage(`{"thread": "` + id.String() + `", "access": "` + level + `"}`)); err != nil {
			t.Fatal(err)
		}
	}
	h.until("the thread back at Approve for me", func() bool { v := h.thread("Mine"); return len(v.Pending) == 0 })
	if v := h.thread("Mine").View; v.Access != api.AccessAutoEdits {
		t.Fatalf("Full access asked for, then taken back, left the thread at %v", v.Access)
	}
	if err := run(map[string]any{"thread": id.String(), "access": accessAsk}); err != nil {
		t.Fatal(err)
	}
	h.until("the thread at Ask", func() bool { v := h.thread("Mine").View; return v.Access == api.AccessAsk && v.Model == opus })
	if err := run(map[string]any{"thread": "", "access": accessFull}); err != nil {
		t.Fatal(err)
	}
	if h.a.access != accessFull {
		t.Errorf("the default for new threads is %q, want %q", h.a.access, accessFull)
	}
}

// No call a plugin may make answers an approval or a question, and their
// cards stay Glitched's: no plugin replaces or places them.
func TestNoPluginAnswersForTheUser(t *testing.T) {
	for name := range plugins.Effects {
		for _, w := range []string{"answer", "approv", "question", "request", "dismiss"} {
			if strings.Contains(strings.ToLower(name), w) {
				t.Errorf("plugins may call %s", name)
			}
		}
	}
	for _, card := range []string{"approval.card", "question.card"} {
		if widget.Locked[card] == "" {
			t.Errorf("%s isn't locked", card)
		}
	}
}

// However wide the replacements draw, Glitched's Stop and its sign of the
// access keep their room, at the narrowest window Glitched allows.
func TestGlitchedsStopAndAccessSignKeepTheirRoom(t *testing.T) {
	h, _ := questioning(t, fake.Asking())
	writeOwnPlugin(t, h.a, "my-access", accessManifest, strings.ReplaceAll(accessModule, "DRAWS", strconv.Quote("levels")))
	h.a.plugins.SetGranted("my-access", "ui.replace", true)
	h.a.plugins.SetGranted("my-access", "threads:access", true)
	writeOwnPlugin(t, h.a, "my-send", sendManifest, sendPlugin("send"))
	h.a.plugins.SetGranted("my-send", "ui.replace", true)
	h.a.plugins.SetGranted("my-send", "threads:write", true)
	h.tt.SetSize(760, 860)
	settlePlugins(t, h.a, h.tt, h.p)
	h.tt.Advance(time.Second)
	if _, ok := h.tt.Find("My send"); !ok {
		t.Fatalf("the replacements don't draw: %q", h.tt.Texts())
	}
	for _, label := range []string{"Stop", "Access: " + accessAuto} {
		if r, ok := h.tt.Find(label); !ok || r.W < 1 || r.H < 1 {
			t.Errorf("%q has no room beside the replacements: %+v", label, r)
		}
	}
	if err := h.tt.Click("Stop"); err != nil {
		t.Fatal(err)
	}
	h.until("the turn stopped", func() bool { return !h.a.current().View.Live() })
}
