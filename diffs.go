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
			// The segments' order, Unified then Split, is not the
			// layouts' own (DiffSplit is 0).
			layout := 0
			if a.diffLayout == editor.DiffSplit {
				layout = 1
			}
			if ui.Segmented(c, &layout, "Unified", "Split").Label("Diff layout").Changed() {
				a.diffLayout = editor.DiffUnified
				if layout == 1 {
					a.diffLayout = editor.DiffSplit
				}
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
					if row(c, f.path, i == a.diffFile).Padding(5, 8).Children(func() {
						ui.Icon(c, icFile).FontSize(12)
						ui.Text(c, f.path).FontSize(12).Font("monospace").SingleLine().Grow(1).Shrink(1)
						add, del := a.diffStats(f.path)
						counts(c, add, del)
					}).Clicked() {
						a.diffFile = i
					}
				}
			})
			path := files[a.diffFile].path
			if _, ok := fileTexts[path]; !ok {
				ui.Text(c, "No preview for this file.").TextColor(palette.muted).Margin(ui.Auto)
				return
			}
			d := a.diffFor(path)
			d.Layout = a.diffLayout
			a.hunkBar(c, d)
			editor.DiffView(c.Key(path), d).Grow(1).FillWidth()
		})
	})
}

// hunkBar counts the file's changes, "2 of 5", and steps between them.
func (a *app) hunkBar(c *ui.Context, d *editor.DiffState) {
	n := len(d.Hunks())
	i, _ := d.CurrentHunk()
	ui.Row(c).Padding(4, 8, 4, 14).Gap(4).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
		label := fmt.Sprintf("%d changes", n)
		switch {
		case n == 0:
			label = "All changes reviewed"
		case i >= 0:
			label = fmt.Sprintf("%d of %d", i+1, n)
		}
		ui.Text(c, label).FontSize(11.5).TextColor(palette.muted).Grow(1)
		if iconButton(c, "prev-hunk", icChevronUp, "Previous change", false).Padding(3).Disabled(n == 0).Clicked() {
			d.GoToHunk(max(i-1, 0))
		}
		if iconButton(c, "next-hunk", icChevronD, "Next change", false).Padding(3).Disabled(n == 0).Clicked() {
			d.GoToHunk(min(i+1, n-1))
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
		share, lo, hi = 0.58, 480, 820
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
