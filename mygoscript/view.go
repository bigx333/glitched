package mygoscript

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// node is one widget of the tree a script's view returns, as the prelude
// encodes it.
type node struct {
	Kind     string  `json:"kind"`
	Key      string  `json:"key"`
	Text     string  `json:"text"`
	On       string  `json:"on"`
	Gap      float32 `json:"gap"`
	Size     float32 `json:"size"`
	Children []node  `json:"children"`
}

// View draws the plugin's widget tree with native widgets. The first View
// of a plugin starts its VM. A script error shows in place, over the last
// tree the script rendered, with a button that restarts the VM.
func View(c *ui.Context, p *Plugin) ui.Element {
	p.mu.Lock()
	if !p.started {
		p.start()
	}
	tree, err := p.tree, p.err
	p.mu.Unlock()

	return ui.Column(c).Gap(12).Children(func() {
		if err != nil {
			errorView(c, p, err)
		}
		if tree != nil {
			draw(c, p, *tree, p.name)
		}
	})
}

// draw builds n under the key path id. A widget's identity is its script
// key, or its kind and position among its siblings, so a rerun of the view
// keeps MyGo's per-widget state (focus, hover, press) on the same widgets.
func draw(c *ui.Context, p *Plugin, n node, id string) {
	k := c.Key(id)
	switch n.Kind {
	case "column", "row":
		var box ui.Element
		if n.Kind == "row" {
			box = ui.Row(k).AlignItems(ui.Center)
		} else {
			box = ui.Column(k)
		}
		box.Gap(n.Gap).Children(func() {
			seen := map[string]bool{}
			for i, child := range n.Children {
				childID := child.Key
				for n := 0; childID == "" || seen[childID]; n++ {
					childID = fmt.Sprintf("%s#%d.%d", child.Kind, i, n)
				}
				seen[childID] = true
				draw(c, p, child, id+"/"+childID)
			}
		})
	case "text":
		t := ui.Text(k, n.Text)
		if n.Size > 0 {
			t.FontSize(n.Size)
		}
	case "button":
		b := ui.Button(k, n.Text)
		if n.On != "" {
			b.OnClick(func() { p.act(n.On) })
		}
	}
}

func errorView(c *ui.Context, p *Plugin, err error) {
	th := c.Theme()
	ui.Column(c.Key("mygoscript-error")).Gap(8).Padding(12).Radius(8).Border(1, th.Danger).Children(func() {
		ui.Text(c, "Script error in "+p.name).Bold().TextColor(th.Danger)
		ui.Text(c, err.Error()).Selectable()
		ui.Row(c).Children(func() {
			ui.Button(c, "Restart script").OnClick(p.reload)
		})
	})
}
