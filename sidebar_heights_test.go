package main

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// The built-ins draw the sidebar, the palette and the timeline at any
// window height, odd as even: half of an odd sidebar's height is a
// fraction, which once made the sidebar's built-in fail and Glitched draw
// its own in its place, which looks the same.
func TestTheBuiltInsDrawAtOddAndEvenWindowHeights(t *testing.T) {
	halves := map[bool]bool{}
	for _, height := range []int{788, 789, 790, 791} {
		t.Run(strconv.Itoa(height), func(t *testing.T) {
			h := start(t, inboxDir(t))
			h.tt.SetSize(1320, height)
			fix := h.thread("Fix terminal resize flicker on Windows")
			h.tt.Advance(time.Second)
			h.hover(fix.View.Title)
			h.click("Settle")
			h.until("the engine to settle it", func() bool { return fix.View.Disposition.State == api.Settled && len(fix.Pending) == 0 })
			h.tt.Advance(1500 * time.Millisecond)
			h.click("Investigate websocket reconnect loop")
			h.a.openCommandPalette()
			settlePlugins(t, h.a, h.tt, h.p)
			half := h.a.sidebarTall / 2
			halves[half != float32(int(half))] = true
			drew := func(id, place, key string) {
				t.Helper()
				app := h.a.plugins.App(id)
				if app == nil || app.PlaceView(place, key) == nil || app.Err != nil {
					t.Errorf("at %d points the built-in %s didn't draw %s (%v): %q", height, id, key, app.Err, h.tt.Texts())
				}
			}
			drew("sidebar", "body", "inbox")
			drew("sidebar", "body", "bottom")
			drew("palette", "palette", h.a.paletteKey())
			entries := entriesOf(h.a.current())
			if len(entries) == 0 {
				t.Fatal("no timeline to draw")
			}
			for _, e := range entries {
				drew("timeline", "row", e.key)
			}
		})
	}
	if !halves[true] || !halves[false] {
		t.Errorf("the sidebar's halves were fractions %v: want both a whole and a fraction", halves)
	}
}

// A copy's failures, told after a built-in took its place back, aren't
// the built-in's.
func TestACopysLateFailuresArentABuiltIns(t *testing.T) {
	a := offline(t)
	if _, err := a.plugins.Copy("sidebar"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(plugins.Dir(a.dir), "sidebar")
	manifest, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeOwnPlugin(t, a, "sidebar", strings.Replace(string(manifest), `"main": "main.js"`, `"main": "index.js"`, 1), `
export function place({items}) { return items.map(() => ({type:"text",text:"Copy"})); }
export async function update() { await glitched.app.settings.get(); return {}; }
`)
	copied := a.plugins.App("sidebar")
	if copied == nil || copied.Builtin {
		t.Fatal("no copy of the sidebar runs")
	}
	var builtin []bool
	a.plugins.OnFailure(func(id string, b bool, err error) {
		if id == "sidebar" {
			builtin = append(builtin, b)
		}
	})
	for range plugins.MaxQueued + 10 {
		copied.Dispatch(plugins.UIEvent{Type: "click", Handler: "slow", Node: "x"}, nil)
	}
	if err := a.plugins.Revert("sidebar"); err != nil {
		t.Fatal(err)
	}
	q, _ := offlinePosts.Load(a)
	q.(*posts).run()
	if slices.Contains(builtin, true) {
		t.Errorf("the copy's failures were told as the built-in's: %v", builtin)
	}
}
