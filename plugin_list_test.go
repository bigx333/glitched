package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A plugin's list of 10,000 rows shows the rows in view as the plugin drew
// them; its choice is Glitched's, which a click and the keys move, and a
// row drawn again as its version changes.
func TestALongListShowsTheRowsInViewAndKeepsTheChoice(t *testing.T) {
	h := pluginApp(t, "long-list")
	h.click("Long list")
	h.until("the rows in view", func() bool { return h.tt.HasText("Row 3") && h.tt.HasText("From 0") })
	if h.tt.HasText("Row 400") || h.tt.HasText("Row 9999") {
		t.Fatal("rows far out of view are drawn")
	}
	chosen := func() any { return h.a.pluginControls[pluginKey{"long-list", "home"}].cs.Values()["rows"] }
	h.click("Row 3")
	h.until("Row 3 chosen", func() bool { return chosen() == "r3" })
	h.tt.Key(0, ui.KeyDown)
	h.until("Row 4 chosen", func() bool { h.tt.Frame(); return chosen() == "r4" })
	h.tt.Key(0, ui.KeyEnter)
	h.until("Row 4 opened", func() bool { h.tt.Frame(); return h.tt.HasText("Opened r4") })

	h.hover("Row 5")
	h.until("its star", func() bool { h.tt.Frame(); return h.tt.HasText("Star") })
	if strings.Contains(strings.Join(h.tt.Texts(), "|"), "#5|") {
		t.Fatal("a row pointed at still shows its idle widgets")
	}
	h.click("Star")
	h.until("Row 5 drawn starred", func() bool { h.tt.Frame(); return h.tt.HasText("Unstar") })
}
