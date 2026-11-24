package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/plugins"
	"glitched/internal/provider/fake"
)

// applyTo folds event body b into tr, from log, as following the thread does.
func applyTo(t *testing.T, tr *client.Transcript, log api.ID, b api.EventBody) {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	seq := tr.Cursor.Seq + 1
	tr.Apply(api.Event{
		Cursor: api.Cursor{Log: log, Seq: seq}, Type: api.EventType(b),
		Schema: api.EventSchema, At: time.Unix(int64(seq), 0), Payload: raw,
	})
}

// snapshotOf is a transcript folded from a snapshot: one turn saying
// text, whose edit names path.
func snapshotOf(t *testing.T, text, path string) client.Transcript {
	t.Helper()
	var tr client.Transcript
	log, run := api.NewID(), api.NewID()
	for _, b := range []api.EventBody{
		api.MessageQueued{Message: run, Text: text},
		api.TurnAdmitted{Run: run},
		api.TurnStarted{Run: run},
		api.WorkItem{Run: run, Item: "edit", Kind: "edit", Path: path, Status: api.WorkCompleted},
	} {
		applyTo(t, &tr, log, b)
	}
	return tr
}

// TestARefoldShowsItsFiles replaces a short transcript with a snapshot
// as long: the diff lists the snapshot's files.
func TestARefoldShowsItsFiles(t *testing.T) {
	tr := snapshotOf(t, "before", "original.go")
	th := thread{Thread: &client.Thread{Transcript: &tr}}
	a := &app{rows: map[api.ID]*rowsCache{}}
	check := func(want string) {
		t.Helper()
		got := a.rowsOf(th).files
		if len(got) != 1 || got[0].path != want {
			t.Fatalf("diff files = %v, want [%s]; full derivation gives %v", got, want, changedFiles(entriesOf(th)))
		}
	}
	check("original.go")
	tr.Replace(snapshotOf(t, "after", "refold.go"))
	a.posts++
	check("refold.go")
}

// TestARefoldTellsItsNewMessage replaces a short transcript with a
// snapshot as long: a plugin hears its new message.
func TestARefoldTellsItsNewMessage(t *testing.T) {
	dir := tempDir(t)
	dst := filepath.Join(plugins.Dir(dir), "review-ears")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"plugin.json": `{"id":"review-ears","name":"Review Ears","main":"index.js","capabilities":["ui.place","messages:read"],"glitched":{"events":["thread.message"],"places":[{"id":"count","at":"sidebar.footer"}]}}`,
		"index.js": `
			export function init() { return {state: ["heard"]} }
			export function update({state, event}) { return event.type === "thread.message" ? {state: [...state, event.data.text]} : {} }
			export function place({state, items}) { return items.map(() => ({type: "text", text: state.join(" | ")})) }
		`,
	} {
		if err := os.WriteFile(filepath.Join(dst, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	signAsOwn(t, dir, dst)
	grant(t, dir, `{"grants":{"review-ears":["messages:read"]}}`)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(fake.Turn(false)))
	seed(t, d, "/work/glitched", "Short history")
	h := start(t, dir)
	th := h.thread("Short history")
	h.a.select_(th)
	h.until("the empty snapshot observed", func() bool {
		return th.Transcript != nil && th.Transcript.Cursor.Seq > 0 && slices.Contains(h.tt.Texts(), "heard")
	})
	th.Transcript.Replace(snapshotOf(t, "before", "original.go"))
	h.a.changed()
	h.until("the plugin heard the old live message", func() bool {
		return slices.Contains(h.tt.Texts(), "heard | before")
	})
	th.Transcript.Replace(snapshotOf(t, "after", "refold.go"))
	h.a.changed()
	h.until("plugin events handled", h.a.plugins.Idle)
	h.p.run()
	h.tt.Frame()
	if !slices.Contains(h.tt.Texts(), "heard | before | after") {
		var got string
		for _, label := range h.tt.Texts() {
			if len(label) >= 5 && label[:5] == "heard" {
				got = label
			}
		}
		t.Fatalf("plugin output = %q, want %q", got, "heard | before | after")
	}
}
