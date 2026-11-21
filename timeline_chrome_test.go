package main

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// oneRow draws the timeline row e alone, as a thread's timeline would.
func oneRow(t *testing.T, e *entry) (*app, *ui.Tester, *posts) {
	t.Helper()
	a := offline(t)
	p, _ := offlinePosts.Load(a)
	th := thread{nil, &client.Thread{View: api.Thread{ID: api.NewID()}}}
	tt := ui.NewTester(func(c *ui.Context) {
		defer a.syncPlaces()
		ui.Column(c).Fill().Padding(16).Children(func() {
			a.entry(c, th, rows{es: []entry{*e}}, 0)
		})
	}, 560, 300)
	return a, tt, p.(*posts)
}

// A copy written before transcript.retry, which places transcript.body
// alone, still shows a message that wasn't sent with its note, Retry and
// Dismiss: the body draws them where the copy places no transcript.retry.
func TestACopyThatPlacesNoRetryKeepsTheNoteAndRetry(t *testing.T) {
	e := entry{key: "unsent", kind: entryUser, text: "Unsent message", note: "Not sent: offline", failed: client.Pending{Err: errors.New("offline")}}
	a, tt, p := oneRow(t, &e)
	writeOwnPlugin(t, a, "my-rows", rowsManifest, rowsModule(`{type:"native", id:"body", name:"transcript.body"}`))
	allow(t, a, "my-rows", "ui.replace")
	settlePlugins(t, a, tt, p)
	tt.Advance(time.Second)
	if got := a.partsDrawn["timeline.message"]; got == nil || len(got.copies) != 1 || got.copies[0].plugin != "my-rows" {
		t.Fatalf("my-rows doesn't draw the row: %+v", got)
	}
	for _, label := range []string{"Unsent message", "Not sent: offline", "Retry", "Dismiss"} {
		if !tt.HasText(label) {
			t.Errorf("the row lost %q: %q", label, tt.Texts())
		}
	}
}

// A message that fails shows it in the frame it fails, before the
// built-in has drawn the row again: Glitched draws the built-in's drawing
// of the row as it is till then.
func TestAMessageShowsItFailedBeforeTheBuiltInDrawsAgain(t *testing.T) {
	e := entry{key: "pending", kind: entryUser, text: "Pending message", note: "Sending…"}
	a, tt, p := oneRow(t, &e)
	settlePlugins(t, a, tt, p)
	tt.Advance(time.Second)
	if got := a.partsDrawn["timeline.message"]; got == nil || len(got.copies) != 1 || got.copies[0].plugin != "timeline" {
		t.Fatalf("the built-in doesn't draw the row: %+v", got)
	}
	if !tt.HasText(e.note) {
		t.Fatalf("no %q: %q", e.note, tt.Texts())
	}
	e.note, e.failed = "Not sent: offline", client.Pending{Err: errors.New("offline")}
	tt.Frame()
	for _, label := range []string{e.note, "Retry", "Dismiss"} {
		if !tt.HasText(label) {
			t.Errorf("the frame it fails lacks %q: %q", label, tt.Texts())
		}
	}
	if tt.HasText("Sending…") {
		t.Errorf("the frame it fails still says Sending…: %q", tt.Texts())
	}
	settlePlugins(t, a, tt, p)
	for _, label := range []string{e.note, "Retry", "Dismiss"} {
		if !tt.HasText(label) {
			t.Fatalf("once the built-in draws again, no %q: %q", label, tt.Texts())
		}
	}
}
