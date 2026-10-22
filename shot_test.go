package main

import (
	"fmt"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// TestShot writes a PNG of the window: SHOT=out.png, with PAGE for a
// path, DIFF, TERM2, CMDK, LIGHT, PICKER=<effort>, FAST and STRIKE=30ms, FULL, HOLD=500ms and GRANT=60ms, LEVEL=ask|auto, ATTACH, TICK=<turn>, TINT=0..1, SCROLLUP, SPLIT, WIDTH=1100, GAG=snooze|settle|undo with AT=350ms for what to show, and HIDE=60ms for
// a frame that long into hiding the sidebar.
func TestShot(t *testing.T) {
	out := os.Getenv("SHOT")
	if out == "" {
		t.Skip()
	}
	a := newApp()
	a.showDiff = os.Getenv("DIFF") != ""
	if os.Getenv("SPLIT") != "" {
		a.showDiff, a.diffLayout = true, editor.DiffSplit
	}
	a.showTerminal = os.Getenv("TERM2") != ""
	a.settings.theme = 2
	if os.Getenv("LIGHT") != "" {
		a.settings.theme = 1
	}
	if name := os.Getenv("THEME"); name != "" {
		a.settings.theme = slices.Index(themeNames(), name)
	}
	if v := os.Getenv("TINT"); v != "" {
		fmt.Sscan(v, &a.settings.vibrancy)
	}
	if p := os.Getenv("PAGE"); p != "" {
		a.router.Push(p)
	}
	w := 1320
	if v := os.Getenv("WIDTH"); v != "" {
		fmt.Sscan(v, &w)
	}
	tt := ui.NewTester(a.view, w, 860)
	if os.Getenv("CMDK") != "" {
		a.openCommandPalette()
	}
	if e := os.Getenv("PICKER"); e != "" {
		a.pickerOpen, a.effort, a.fast = true, e, os.Getenv("FAST") != ""
	}
	tt.Frame()
	tt.Advance(time.Second)
	if ms := os.Getenv("HIDE"); ms != "" {
		// Partway through hiding the sidebar.
		d, _ := time.ParseDuration(ms)
		if err := tt.Click("Hide sidebar  ⌘B"); err != nil {
			t.Fatal(err)
		}
		tt.Advance(d)
	}
	if k := os.Getenv("TICK"); k != "" {
		// The pointer on the turn marker of that index.
		_, th := a.current()
		turns := turnsOf(th)
		i := int(k[0] - '0')
		r, ok := tt.Find("Go to: " + preview(th.entries[turns[i].entry]))
		if !ok {
			t.Fatal("no marker")
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(500 * time.Millisecond)
	}
	if os.Getenv("SCROLLUP") != "" {
		_, th := a.current()
		a.listFor(th).ScrollTo(0, ui.Start)
		tt.Advance(time.Second)
	}
	if os.Getenv("ATTACH") != "" {
		a.attachFiles([]string{"resources/icon.png", "go.mod"})
		a.onPaste(strings.Repeat("log line\n", 400))
		tt.Frame()
		tt.Advance(500 * time.Millisecond)
	}
	if os.Getenv("FULL") != "" {
		a.access = accessFull
	}
	if ms := os.Getenv("HOLD"); ms != "" {
		// That far into holding Full access, its popover open.
		d, _ := time.ParseDuration(ms)
		a.accessOpen = true
		tt.Frame()
		r, _ := tt.Find(accessFull)
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Press(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(d)
	}
	if ms := os.Getenv("GRANT"); ms != "" {
		// That far into the shake after Full access was granted.
		d, _ := time.ParseDuration(ms)
		a.access, a.accessAt = accessFull, tt.Now()
		tt.Frame()
		tt.Advance(d)
	}
	if lv := os.Getenv("LEVEL"); lv != "" {
		// 300ms into the effect of switching to an access level.
		a.access, a.accessAt = map[string]string{"ask": accessAsk, "auto": accessAuto}[lv], tt.Now()
		tt.Frame()
		tt.Advance(300 * time.Millisecond)
	}
	if g := os.Getenv("GAG"); g != "" {
		// 350ms into an inbox action's joke.
		t2, t8 := a.projects[0].threads[1], a.projects[2].threads[0]
		switch g {
		case "snooze":
			t2.snoozed = "Tomorrow"
			a.rowFx[t2.id] = newGag(gagSnooze, tt.Now(), catInbox)
		case "settle":
			t2.settled = true
			a.rowFx[t2.id] = newGag(gagSettle, tt.Now(), catInbox)
		case "undo":
			t8.snoozed = ""
			a.rowFx[t8.id] = newGag(gagUndo, tt.Now(), catSnoozed)
		}
		tt.Frame()
		d, _ := time.ParseDuration(os.Getenv("AT"))
		tt.Advance(d)
	}
	if ms := os.Getenv("STRIKE"); ms != "" {
		// That far into fast mode's lightning strike.
		d, _ := time.ParseDuration(ms)
		a.fast, a.fastAt = true, tt.Now()
		tt.Frame()
		tt.Advance(d)
	}
	f, _ := os.Create(out)
	defer f.Close()
	png.Encode(f, tt.Image())
}
