package render

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"glitched/internal/plugins/widget"
)

// An inline native takes and gives up room in its row as the view it
// draws does drawn there itself: a crumb and a long title side by side
// shrink alike, natives or not.
func TestAnInlineNativeSharesItsRowAsItsView(t *testing.T) {
	crumb := func(c *ui.Context) {
		ui.ButtonBase(c.Key("crumb")).Padding(3, 6).Shrink(1).MinWidth(40).Label("Back").Children(func() {
			ui.Text(c, strings.Repeat("crumb ", 12)).FontSize(12.5).SingleLine().MaxWidth(180)
		})
	}
	title := func(c *ui.Context) {
		ui.Text(c, strings.Repeat("title ", 40)).FontWeight(600).SingleLine().Shrink(1).MinWidth(80)
	}
	v := &widget.View{Body: &widget.Node{Type: "row", Gap: 8, Children: []*widget.Node{
		{Type: "native", Name: "thread.crumb"},
		{Type: "native", Name: "thread.heading"},
	}}}
	st := Style{Native: func(c *ui.Context, n *widget.Node, _ string, _ func(any)) {
		if n.Name == "thread.crumb" {
			crumb(c)
		} else {
			title(c)
		}
	}}
	var md markdown.Options
	var cs Controls
	var native, own ui.Rect
	for i, draw := range []func(c *ui.Context){
		func(c *ui.Context) { View(c, v, &cs, st, &md, nil) },
		func(c *ui.Context) { ui.Row(c).Gap(8).Children(func() { crumb(c); title(c) }) },
	} {
		tt := ui.NewTester(func(c *ui.Context) { ui.Row(c).Width(600).Children(func() { draw(c) }) }, 800, 100)
		tt.Frame()
		r, ok := tt.Find("Back")
		if !ok {
			t.Fatalf("no crumb: %q", tt.Texts())
		}
		if i == 0 {
			native = r
		} else {
			own = r
		}
	}
	if native.W != own.W {
		t.Fatalf("the crumb is %.1f wide placed as a native, %.1f drawn in the row itself", native.W, own.W)
	}
}
