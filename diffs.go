package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// The diff panel is MyGo's diff viewer (ui/editor), as its code-editor
// example uses it: each file the agent changed, its text before and
// after, unified or side by side, with the words changed highlighted and
// Accept and Reject on the change under the pointer, as an agent's
// changes are reviewed one at a time.

// fileTexts is each mock file's text before and after the agent's turn.
var fileTexts = map[string][2]string{
	"apps/web/src/components/Sidebar.tsx":     {sidebarBefore, sidebarAfter},
	"apps/web/src/components/ChevronIcon.tsx": {"", chevronAfter},
}

const sidebarBefore = `import { useState } from "react";
import { SidebarGroup, SidebarGroupLabel } from "./ui/sidebar";
import { ThreadRow } from "./ThreadRow";
import { useThreads } from "../hooks/useThreads";

export function Sidebar({ projects }: SidebarProps) {
  return projects.map((project) => <ProjectGroup key={project.id} project={project} />);
}

function ProjectGroup({ project }: { project: Project }) {
  const threads = useThreads(project.id);
  const [expanded, setExpanded] = useState(true);
  const sorted = threads.sort((a, b) => b.updatedAt - a.updatedAt);

  return (
    <SidebarGroup>
      <SidebarGroupLabel>{project.name}</SidebarGroupLabel>
      {expanded && sorted.map((t) => (
        <ThreadRow key={t.id} thread={t} />
      ))}
    </SidebarGroup>
  );
}
`

const sidebarAfter = `import { useMemo, useState } from "react";
import { SidebarGroup, SidebarGroupLabel } from "./ui/sidebar";
import { ThreadRow } from "./ThreadRow";
import { ChevronIcon } from "./ChevronIcon";
import { useThreads } from "../hooks/useThreads";

export function Sidebar({ projects }: SidebarProps) {
  return projects.map((project) => <ProjectGroup key={project.id} project={project} />);
}

function ProjectGroup({ project }: { project: Project }) {
  const threads = useThreads(project.id);
  const [expanded, setExpanded] = useState(true);
  const sorted = useMemo(
    () => [...threads].sort((a, b) => b.updatedAt - a.updatedAt),
    [threads],
  );

  return (
    <SidebarGroup>
      <SidebarGroupLabel onClick={() => setExpanded(!expanded)}>
        <ChevronIcon open={expanded} />
        {project.name}
      </SidebarGroupLabel>
      {expanded && sorted.map((t) => (
        <ThreadRow key={t.id} thread={t} />
      ))}
    </SidebarGroup>
  );
}
`

const chevronAfter = `import { ChevronRight } from "lucide-react";
import { cn } from "../lib/utils";

export function ChevronIcon({ open }: { open: boolean }) {
  return (
    <ChevronRight
      className={cn(
        "size-3 text-muted-foreground transition-transform",
        open && "rotate-90",
      )}
    />
  );
}
`

// diffFor returns the viewer's state of a file, made once: the buffers of
// its text before and after, highlighted as its language.
func (a *app) diffFor(path string) *editor.DiffState {
	if d := a.diffs[path]; d != nil {
		return d
	}
	texts := fileTexts[path]
	lang := editor.LanguageFor(path)
	before, after := editor.NewBuffer(texts[0]), editor.NewBuffer(texts[1])
	before.SetLanguage(lang)
	after.SetLanguage(lang)
	d := &editor.DiffState{Old: before, New: after, Editable: true, HunkActions: true}
	d.RenderHunkActions = func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
		bar.Gap(4).Children(func() {
			if reviewButton(c, "Reject", false).Label("Reject change").Clicked() {
				h.Revert()
			}
			if reviewButton(c, "Accept", true).Label("Accept change").Clicked() {
				h.Keep()
			}
		})
	}
	a.diffs[path] = d
	return d
}

func reviewButton(c *ui.Context, label string, primary bool) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Padding(3, 9).Radius(6).Cursor(ui.CursorPointer).
		Background(palette.surface).Border(1, palette.border).TextColor(palette.text).
		Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.25)).Transition(hoverFade)
	if primary {
		b.Background(palette.green.Mix(ui.Hex("#000000"), 0.35)).Border(1, palette.green.Alpha(0.6)).TextColor(ui.Hex("#ffffff"))
	}
	if b.Hovered() {
		b.Background(palette.surface.Mix(palette.text, 0.1))
		if primary {
			b.Background(palette.green.Mix(ui.Hex("#000000"), 0.2))
		}
	}
	return b.Children(func() { ui.Text(c, label).FontSize(11.5).FontWeight(500) })
}

// diffStats counts the lines a file's change adds and deletes, as its diff
// has them now: accepting or rejecting changes counts down.
func (a *app) diffStats(path string) (add, del int) {
	if _, ok := fileTexts[path]; !ok {
		return 0, 0
	}
	return a.diffFor(path).Stats()
}

func (a *app) diffPanel(c *ui.Context, t *thread) {
	files := t.changed()
	ui.Column(c).Width(a.diffWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 8, 0, 14).Gap(8).AlignItems(ui.Center)
		head.DragWindow().Children(func() {
			ui.Text(c, "Changes").FontWeight(600)
			ui.Textf(c, "%d", len(files)).FontSize(11).TextColor(palette.muted).Padding(0, 6).Radius(999).Background(palette.hover)
			ui.Spacer(c)
			a.layoutSwitch(c)
			if iconButton(c, "wrap", icWrap, "Wrap long lines", a.diffWrap).Clicked() {
				a.diffWrap = !a.diffWrap
			}
			if iconButton(c, "close-diff", icX, "Close diff", false).Clicked() {
				a.showDiff = false
			}
		})
		// Below its glass header, the panel on the sidebar's color.
		ui.Column(c).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border).Children(func() {
			if len(files) == 0 {
				ui.Text(c, "No changes in this thread yet.").TextColor(palette.muted).Margin(ui.Auto)
				return
			}
			a.diffFile = min(a.diffFile, len(files)-1)
			ui.Column(c).Padding(6).Gap(1).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
				for i, f := range files {
					a.fileRow(c, i, f)
				}
			})
			path := files[a.diffFile].path
			if _, ok := fileTexts[path]; !ok {
				ui.Text(c, "No preview for this file.").TextColor(palette.muted).Margin(ui.Auto)
				return
			}
			d := a.diffFor(path)
			d.Layout, d.Wrap = a.diffLayout, a.diffWrap
			a.hunkBar(c, d)
			editor.DiffView(c.Key(path), d).Grow(1).FillWidth()
		})
	})
}

// splitWidth is the least width side by side reads at: two columns of
// code, each wide enough for a line.
const splitWidth = 800

// splitFits reports whether there is room for the panel splitWidth wide
// beside the chat.
func (a *app) splitFits() bool {
	return a.roomBeside-minChatWidth >= splitWidth
}

// layoutSwitch chooses Unified or Split. Split needs the room: short of
// it, its segment is dimmed and says so, and the layout falls back to
// Unified.
func (a *app) layoutSwitch(c *ui.Context) {
	fits := a.splitFits()
	if !fits {
		a.diffLayout = editor.DiffUnified
	}
	ui.Row(c).Radius(7).Border(1, palette.border).Padding(2).Gap(2).Children(func() {
		segment := func(key, label string, layout editor.DiffLayout, enabled bool) {
			b := ghost(c, key, a.diffLayout == layout).Padding(3, 9).Disabled(!enabled)
			if !enabled {
				b.Tooltip(fmt.Sprintf("Split needs %d DIPs of room", splitWidth))
			}
			if b.Children(func() { ui.Text(c, label).FontSize(11.5) }).Clicked() {
				a.diffLayout = layout
			}
		}
		segment("unified", "Unified", editor.DiffUnified, true)
		segment("split", "Split", editor.DiffSplit, fits)
	})
}

// fileRow is a changed file in the panel's list; pointed at, its actions:
// mark it reviewed, revert it, copy its path, open it in the editor. A
// reviewed file shows a filled check.
func (a *app) fileRow(c *ui.Context, i int, f fileChange) {
	r := row(c, f.path, i == a.diffFile).Padding(3, 4, 3, 8).Height(28).Label("Diff of " + f.path)
	_, known := fileTexts[f.path]
	hovered := r.Hovered()
	if r.Children(func() {
		icon := icFile
		if a.reviewed[f.path] {
			icon = icCheckCirc
		}
		ui.Icon(c, icon).FontSize(12)
		ui.Text(c, f.path).FontSize(12).Font("monospace").SingleLine().Grow(1).Shrink(1)
		if !hovered {
			add, del := a.diffStats(f.path)
			counts(c, add, del)
			return
		}
		ui.Row(c).Gap(0).Children(func() {
			if iconButton(c, "reviewed", icCheckCirc, "Mark reviewed", a.reviewed[f.path]).Padding(3).Clicked() {
				a.reviewed[f.path] = !a.reviewed[f.path]
			}
			if iconButton(c, "revert", icUndo, "Revert file", false).Padding(3).Disabled(!known).Clicked() {
				d := a.diffFor(f.path)
				for len(d.Hunks()) > 0 {
					d.Revert(0)
				}
			}
			if iconButton(c, "copy-path", icCopy, "Copy path", false).Padding(3).Clicked() {
				c.WriteClipboard(f.path)
				c.AddToast(ui.Toast{Title: "Copied", Description: f.path})
			}
			if iconButton(c, "open-file", icExternal, "Open in editor", false).Padding(3).Clicked() {
				c.AddToast(ui.Toast{Title: "Opened in Cursor", Description: f.path})
			}
		})
	}).Clicked() {
		a.diffFile = i
	}
}

// hunkBar counts the file's changes, "2 of 5", steps between them, and
// keeps the one in view as reviewed. ⌘[ and ⌘] step from the keyboard,
// and ⇧⌘↩ keeps.
func (a *app) hunkBar(c *ui.Context, d *editor.DiffState) {
	n := len(d.Hunks())
	i, _ := d.CurrentHunk()
	prev := func() { d.GoToHunk(max(i-1, 0)) }
	next := func() { d.GoToHunk(min(i+1, n-1)) }
	keep := func() {
		if i >= 0 {
			d.Accept(i)
		}
	}
	if n > 0 {
		switch {
		case c.Shortcut(ui.Cmd, ui.KeyBracketLeft):
			prev()
		case c.Shortcut(ui.Cmd, ui.KeyBracketRight):
			next()
		case c.Shortcut(ui.Cmd|ui.Shift, ui.KeyEnter):
			keep()
		}
	}
	ui.Row(c).Padding(4, 8, 4, 14).Gap(4).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
		label := fmt.Sprintf("%d changes", n)
		switch {
		case n == 0:
			label = "All changes reviewed"
		case i >= 0:
			label = fmt.Sprintf("%d of %d", i+1, n)
		}
		ui.Text(c, label).FontSize(11.5).TextColor(palette.muted).Grow(1)
		if ghost(c, "keep-hunk", false).Padding(3, 6).Disabled(n == 0).Label("Mark change reviewed").Children(func() {
			ui.Icon(c, icCheck).FontSize(12)
			ui.Text(c, "Reviewed").FontSize(11.5)
			kbd(c, "⇧⌘↩")
		}).Clicked() {
			keep()
		}
		if iconButton(c, "prev-hunk", icChevronUp, "Previous change  ⌘[", false).Padding(3).Disabled(n == 0).Clicked() {
			prev()
		}
		if iconButton(c, "next-hunk", icChevronD, "Next change  ⌘]", false).Padding(3).Disabled(n == 0).Clicked() {
			next()
		}
	})
}

// diffWidth is the panel's width: wider side by side.
// diffWidth is the panel's width: a share of the room beside the sidebar,
// wider side by side, and never so wide the chat has less than
// minChatWidth.
func (a *app) diffWidth() float32 {
	share, lo, hi := float32(0.42), float32(380), float32(520)
	if a.diffLayout == editor.DiffSplit {
		share, lo, hi = 0.58, splitWidth, 900
	}
	return panelWidth(a.roomBeside, share, lo, hi)
}

// minChatWidth is the least room the chat keeps beside a panel.
const minChatWidth = 420

// panelWidth is a panel's width in room DIPs beside the sidebar: share of
// it, within lo and hi, leaving the chat minChatWidth.
func panelWidth(room, share, lo, hi float32) float32 {
	w := min(max(room*share, lo), hi)
	return max(min(w, room-minChatWidth), 0)
}
