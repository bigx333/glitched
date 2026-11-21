package main

import (
	"fmt"
	"image"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/client"
	"glitched/internal/daemon"
	"glitched/internal/provider/fake"
)

// workspaceHarness is a new thread's composer beside a project, in a
// window that draws its menus, so that a menu shows in the image.
func workspaceHarness(t *testing.T) (*harness, thread) {
	t.Helper()
	ws := gitWorkspace(t)
	dir := tempDir(t)
	d := engine(t, daemon.Config{Dir: dir}, fake.New(checks))
	seed(t, d, ws, "Sort sidebar threads by recent activity", "Sort them by most recent activity.")
	h := start(t, dir)
	h.tt = ui.NewTester(func(c *ui.Context) {
		c.SetMenuPresentation(ui.PopoverMenus)
		h.a.view(c)
	}, 1320, 860)
	th := h.thread("Sort sidebar threads by recent activity")
	settlePlugins(t, h.a, h.tt, h.p)
	h.a.newThread()
	h.tt.Frame()
	h.tt.Advance(time.Second)
	settlePlugins(t, h.a, h.tt, h.p)
	return h, th
}

// workspaceDrawnBy is who drew composer.workspace in the last frame.
func workspaceDrawnBy(h *harness) string {
	d := h.a.partsDrawn["composer.workspace"]
	switch {
	case d == nil || len(d.copies) != 1:
		return "none"
	case d.copies[0].glitched:
		return "Glitched"
	}
	return d.copies[0].plugin
}

// still is the window once nothing changes in it any more.
func still(h *harness) *image.RGBA {
	img := h.tt.Image()
	for range 20 {
		h.p.run()
		h.tt.Advance(time.Second)
		next := h.tt.Image()
		if sameImage(img, next) {
			return next
		}
		img = next
	}
	return img
}

// menuShot opens the workspace menu: the window with it open, its items,
// and whether the button has the focus once Escape closes it.
func menuShot(t *testing.T, h *harness) (*image.RGBA, []string, bool) {
	t.Helper()
	h.click("Workspace")
	img := still(h)
	items := h.tt.Menu()
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)
	return img, items, h.tt.Focused("Workspace")
}

func sameImage(a, b *image.RGBA) bool {
	return a.Rect == b.Rect && slices.Equal(a.Pix, b.Pix)
}

func diffBox(a, b *image.RGBA) (image.Rectangle, int) {
	var r image.Rectangle
	n := 0
	for y := a.Rect.Min.Y; y < a.Rect.Max.Y; y++ {
		for x := a.Rect.Min.X; x < a.Rect.Max.X; x++ {
			i := a.PixOffset(x, y)
			if !slices.Equal(a.Pix[i:i+4], b.Pix[i:i+4]) {
				n++
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r, n
}

// The workspace button and its menu are the built-in Workspace plugin's,
// drawn as Glitched draws its own, which draws them when the plugin is
// off: the button, pixel for pixel, its name to assistive tools, and the
// menu, its choices checked as Glitched checks them and ended with
// Glitched's own Add project and Remove project.
func TestTheWorkspaceButtonIsTheBuiltInsDrawnAsGlitchedsOwn(t *testing.T) {
	h, th := workspaceHarness(t)
	enable := func(on bool) {
		t.Helper()
		if err := h.a.plugins.SetEnabled("workspace", on); err != nil {
			t.Fatal(err)
		}
		settlePlugins(t, h.a, h.tt, h.p)
	}
	for _, step := range []struct {
		name string
		show func()
		want []string
	}{
		{"a new thread's", func() {}, []string{"user", "t3code", "-", "Add project…", "Remove project"}},
		{"a thread's", func() {
			h.a.select_(th)
			h.until("the edit's counts", func() bool { return h.tt.HasText("+10") })
		}, nil},
	} {
		step.show()
		h.tt.Move(0, 0)
		settlePlugins(t, h.a, h.tt, h.p)
		if by := workspaceDrawnBy(h); by != "workspace" {
			t.Fatalf("%s workspace is drawn by %s, want the built-in Workspace", step.name, by)
		}
		button := still(h)
		enable(false)
		if by := workspaceDrawnBy(h); by != "Glitched" {
			t.Fatalf("with the built-in off, %s workspace is drawn by %s, want Glitched", step.name, by)
		}
		ownButton := still(h)
		ownMenu, ownItems, ownFocus := menuShot(t, h)
		enable(true)
		menu, items, focus := menuShot(t, h)
		for _, c := range []struct {
			what      string
			mine, own *image.RGBA
		}{{"button", button, ownButton}, {"menu", menu, ownMenu}} {
			if !sameImage(c.mine, c.own) {
				r, n := diffBox(c.mine, c.own)
				t.Errorf("%s workspace %s differs from Glitched's own: %d pixels in %v", step.name, c.what, n, r)
			}
		}
		if !slices.Equal(items, ownItems) || focus != ownFocus {
			t.Errorf("%s workspace menu is %q and the button focused %v after it; Glitched's own %q and %v", step.name, items, focus, ownItems, ownFocus)
		}
		if len(items) > 0 {
			items[0] = "user"
		}
		if !slices.Equal(items, step.want) {
			t.Errorf("%s workspace menu is %q, want %q", step.name, items, step.want)
		}
	}
}

// Choosing in the built-in's menu starts the draft there, as Glitched's
// own menu does.
func TestTheBuiltInWorkspaceMenuChoosesWhereTheDraftStarts(t *testing.T) {
	for _, builtIn := range []bool{true, false} {
		t.Run(map[bool]string{true: "built-in", false: "Glitched's"}[builtIn], func(t *testing.T) {
			h, th := workspaceHarness(t)
			if err := h.a.plugins.SetEnabled("workspace", builtIn); err != nil {
				t.Fatal(err)
			}
			settlePlugins(t, h.a, h.tt, h.p)
			if by, want := workspaceDrawnBy(h), map[bool]string{true: "workspace", false: "Glitched"}[builtIn]; by != want {
				t.Fatalf("the workspace is drawn by %s, want %s", by, want)
			}
			h.a.draft = "Check the certificates"
			h.click("Workspace")
			h.tt.Advance(time.Second)
			if err := h.tt.ChooseMenuItem("t3code"); err != nil {
				t.Fatalf("%v; the menu has %q", err, h.tt.Menu())
			}
			if got := h.a.current(); !got.id().IsZero() || got.View.Workspace != th.View.Workspace {
				t.Fatalf("the draft starts in %q, want %q", got.View.Workspace, th.View.Workspace)
			}
			h.click("Send")
			if got := h.a.current(); got.id().IsZero() || got.View.Workspace != th.View.Workspace {
				t.Fatalf("Send right after the choice started a thread in %q, want %q", got.View.Workspace, th.View.Workspace)
			}
		})
	}
}

const yourWorkspaceModule = `export function place({ items }) {
	return items.map(({ context }) => ({ type: "menu", id: "where", style: "ghost", label: "Where", icon: "folder", options: context.draft ? [] : ["Pin"], onChange: "pin" }))
}`

// A plugin of yours draws the workspace button in place of the built-in:
// in a draft, a menu of Glitched's choices, Add project and Remove
// project; in a thread that has started, its own options alone. One that
// fails gives way to the built-in, or with it off to Glitched's own.
func TestYourWorkspaceButtonReplacesTheBuiltIns(t *testing.T) {
	h, th := workspaceHarness(t)
	manifest := strings.Replace(replacing("your-workspace", "composer.workspace"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "your-workspace", manifest, yourWorkspaceModule)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("your-workspace", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := workspaceDrawnBy(h); by != "your-workspace" {
		t.Fatalf("the workspace is drawn by %s, want yours: %v", by, h.a.plugins.Failed("your-workspace"))
	}
	h.click("Where")
	h.tt.Advance(time.Second)
	items := h.tt.Menu()
	if len(items) != 5 || !slices.Equal(items[1:], []string{"t3code", "-", "Add project…", "Remove project"}) {
		t.Fatalf("your workspace menu is %q, want Glitched's choices, Add project and Remove project", items)
	}
	if err := h.tt.ChooseMenuItem("t3code"); err != nil {
		t.Fatal(err)
	}
	if got := h.a.current(); got.View.Workspace != th.View.Workspace {
		t.Fatalf("your menu started the draft in %q, want %q", got.View.Workspace, th.View.Workspace)
	}
	h.a.select_(th)
	h.tt.Frame()
	settlePlugins(t, h.a, h.tt, h.p)
	h.click("Where")
	h.tt.Advance(time.Second)
	if items := h.tt.Menu(); !slices.Equal(items, []string{"Pin"}) {
		t.Fatalf("your menu in a thread that has started is %q, want yours alone", items)
	}
	h.tt.Key(0, ui.KeyEscape)
	h.tt.Advance(time.Second)

	writeOwnPlugin(t, h.a, "your-workspace", manifest, brokenModule)
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by := workspaceDrawnBy(h); by != "workspace" || h.a.plugins.Failed("your-workspace") == nil {
		t.Fatalf("with yours broken, the workspace is drawn by %s (%v), want the built-in", by, h.a.plugins.Failed("your-workspace"))
	}
	if err := h.a.plugins.SetEnabled("workspace", false); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if by := workspaceDrawnBy(h); by != "Glitched" || h.a.plugins.Failed("your-workspace") == nil {
		t.Fatalf("with yours broken and the built-in off, the workspace is drawn by %s (%v), want Glitched", by, h.a.plugins.Failed("your-workspace"))
	}
	if _, ok := h.tt.Find("Workspace"); !ok {
		t.Fatalf("no Workspace button once yours failed: %q", h.tt.Texts())
	}
}

// A plugin of yours too slow to draw a new draft's button gives way to
// Glitched's until it has, never showing the last draft's workspace.
func TestASlowWorkspaceButtonGivesWayToGlitchedsUntilItDraws(t *testing.T) {
	h, th := workspaceHarness(t)
	next := draft{th.env, th.View.Workspace}
	if h.a.current().View.Workspace == next.workspace {
		next.workspace = homeDir()
	}
	before := filepath.Base(h.a.current().View.Workspace)
	module := fmt.Sprintf(`export function place({ items }) {
	return items.map(({ context }) => {
		if (context.workspace === %q) {
			const until = Date.now() + 1000
			while (Date.now() < until) {}
		}
		return { type: "menu", id: "workspace", style: "ghost", label: "Workspace", options: [], children: [{ type: "text", text: context.workspace }] }
	})
}`, filepath.Base(next.workspace))
	manifest := strings.Replace(replacing("slow-workspace", "composer.workspace"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "slow-workspace", manifest, module)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("slow-workspace", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by, got := workspaceDrawnBy(h), workspaceLabel(h); by != "slow-workspace" || got != before {
		t.Fatalf("the workspace is drawn by %s showing %q, want yours showing %q", by, got, before)
	}
	h.a.startIn(next)
	h.tt.Frame()
	if by, got := workspaceDrawnBy(h), workspaceLabel(h); by != "Glitched" || got != filepath.Base(next.workspace) {
		t.Fatalf("before yours draws it, the new draft's button is drawn by %s showing %q, want Glitched's showing %q", by, got, filepath.Base(next.workspace))
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if by, got := workspaceDrawnBy(h), workspaceLabel(h); by != "slow-workspace" || got != filepath.Base(next.workspace) {
		t.Fatalf("once yours has drawn, the workspace is drawn by %s showing %q", by, got)
	}
}

// workspaceLabel is the text the workspace button shows in the last frame.
func workspaceLabel(h *harness) string {
	texts := h.tt.Texts()
	i := slices.Index(texts, "Workspace")
	if i < 0 || i+1 == len(texts) {
		return ""
	}
	return texts[i+1]
}

// A new thread's workspace button shows where its draft starts from the
// frame it opens in, as Glitched's own does, never the last draft's
// drawing while the plugin draws the new one.
func TestANewDraftsWorkspaceButtonIsNeverTheLastDrafts(t *testing.T) {
	h, th := workspaceHarness(t)
	here := filepath.Base(th.View.Workspace)
	h.click("Workspace")
	h.tt.Advance(time.Second)
	other := ""
	for _, it := range h.tt.Menu() {
		if it != here && it != "" && !strings.Contains(it, "project") {
			other = it
			break
		}
	}
	if other == "" {
		t.Fatalf("the menu has no choice but %q: %q", here, h.tt.Menu())
	}
	if err := h.tt.ChooseMenuItem(other); err != nil {
		t.Fatal(err)
	}
	settlePlugins(t, h.a, h.tt, h.p)
	if got := workspaceLabel(h); got != other {
		t.Fatalf("the draft's button shows %q, want %q", got, other)
	}
	h.a.select_(th)
	h.tt.Frame()
	h.a.newThread()
	h.tt.Frame()
	if got := workspaceLabel(h); got != here {
		t.Fatalf("the new draft's button shows %q, want %q", got, here)
	}
}

// A plugin's draft workspace button must be a ghost menu of no options of
// its own, which Glitched's fill: one that leaves the menu out, or adds
// to it, fails, says why in Settings › Plugins, and gives way.
func TestAWorkspaceButtonWithoutGlitchedsMenuFails(t *testing.T) {
	for name, body := range map[string]string{
		"a button":            `{ type: "button", id: "where", style: "ghost", label: "Where", icon: "folder" }`,
		"its own options too": `{ type: "menu", id: "where", style: "ghost", label: "Where", icon: "folder", options: ["Pin"], onChange: "pin" }`,
		"disabled":            `{ type: "menu", id: "where", style: "ghost", label: "Where", icon: "folder", options: [], disabled: true }`,
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := workspaceHarness(t)
			manifest := strings.Replace(replacing("your-workspace", "composer.workspace"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
			writeOwnPlugin(t, h.a, "your-workspace", manifest, "export function place({ items }) { return items.map(() => ("+body+")) }")
			for _, c := range []string{"ui.replace", "threads:read"} {
				h.a.plugins.SetGranted("your-workspace", c, true)
			}
			h.a.plugins.Reload()
			settlePlugins(t, h.a, h.tt, h.p)
			if by := workspaceDrawnBy(h); by != "workspace" {
				t.Fatalf("the workspace is drawn by %s, want the built-in", by)
			}
			state := h.a.partState(installed(h.a, "your-workspace"), "composer.workspace")
			if state.word != "Failed" || !strings.Contains(state.why, "ghost menu of no options, which Glitched's choices fill, never disabled") {
				t.Fatalf("Settings says %q: %q", state.word, state.why)
			}
		})
	}
}

// A new draft in another folder of the same name, on the same machine, is
// never shown with the last draft's drawing.
func TestAWorkspaceInAnotherFolderOfTheSameNameIsNotTheLastDrafts(t *testing.T) {
	h, _ := workspaceHarness(t)
	first := filepath.Join(t.TempDir(), "first", "same-name")
	second := filepath.Join(t.TempDir(), "second", "same-name")
	h.a.startIn(draft{h.a.current().env, first})
	h.tt.Frame()
	settlePlugins(t, h.a, h.tt, h.p)
	module := fmt.Sprintf(`export function place({ items }) {
	return items.map(({ context }) => {
		if (context.thread.workspace === %q) {
			const until = Date.now() + 1000
			while (Date.now() < until) {}
		}
		return { type: "menu", id: "workspace", style: "ghost", label: "Workspace", options: [], children: [{ type: "text", text: context.thread.workspace }] }
	})
}`, second)
	manifest := strings.Replace(replacing("slow-workspace", "composer.workspace"), `"ui.replace"]`, `"ui.replace", "threads:read"]`, 1)
	writeOwnPlugin(t, h.a, "slow-workspace", manifest, module)
	for _, c := range []string{"ui.replace", "threads:read"} {
		h.a.plugins.SetGranted("slow-workspace", c, true)
	}
	h.a.plugins.Reload()
	settlePlugins(t, h.a, h.tt, h.p)
	if by, got := workspaceDrawnBy(h), workspaceLabel(h); by != "slow-workspace" || got != first {
		t.Fatalf("the workspace is drawn by %s showing %q, want yours showing %q", by, got, first)
	}
	h.a.startIn(draft{h.a.current().env, second})
	h.tt.Frame()
	if by, got := workspaceDrawnBy(h), workspaceLabel(h); by != "Glitched" || got != "same-name" {
		t.Fatalf("before yours draws it, the new draft's button is drawn by %s showing %q, want Glitched's showing %q", by, got, "same-name")
	}
}

// A machine paired while Settings shows names the draft's machine on the
// button from the first frame back in the chat, as Glitched's own does.
func TestTheWorkspaceButtonNamesAMachinePairedInSettings(t *testing.T) {
	for _, builtIn := range []bool{true, false} {
		t.Run(map[bool]string{true: "built-in", false: "Glitched's"}[builtIn], func(t *testing.T) {
			rdir := tempDir(t)
			token, err := daemon.Pair(rdir, "gui")
			if err != nil {
				t.Fatal(err)
			}
			remote := engine(t, daemon.Config{Dir: rdir, Listen: []string{"127.0.0.1:0"}}, fake.New(fake.Turn(false)))
			h, _ := workspaceHarness(t)
			if err := h.a.plugins.SetEnabled("workspace", builtIn); err != nil {
				t.Fatal(err)
			}
			settlePlugins(t, h.a, h.tt, h.p)
			machine := "on " + h.a.current().env.Name
			if h.tt.HasText(machine) {
				t.Fatalf("one machine, and the button says %q", machine)
			}
			h.a.router.Push("/settings/servers")
			h.tt.Frame()
			added := false
			h.a.backend.AddServer(client.Server{Label: "devbox", Addr: remote.Addrs()[0].String(), Token: token}, func(err error) {
				if err != nil {
					t.Error(err)
				}
				added = true
			})
			h.until("the server", func() bool { return added })
			h.a.router.Push("/")
			h.tt.Frame()
			if !h.tt.HasText(machine) {
				t.Fatalf("back in the chat, the button doesn't say %q: drawn by %s, %q", machine, workspaceDrawnBy(h), h.tt.Texts())
			}
		})
	}
}
