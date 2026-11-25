package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

func (a *app) openFind() {
	a.findOpen = true
	a.findFocus = true
	a.findThread = a.current().id()
}

func (a *app) openFindWith(query string) {
	a.findOpen = true
	a.findQuery = query
	a.findIndex = 0
	a.findFocus = true
	a.findJump = query != ""
	a.findThread = a.current().id()
}

func (a *app) closeFind() {
	a.findOpen = false
}

// findMatches returns the indices of entries in es that contain query
// (case-insensitive) in their text, work steps, or child reports.
func findMatches(es []entry, query string) []int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var out []int
	for i, e := range es {
		if entryMatches(e, q) {
			out = append(out, i)
		}
	}
	return out
}

func entryMatches(e entry, q string) bool {
	if strings.Contains(strings.ToLower(e.text), q) {
		return true
	}
	for _, w := range e.work {
		if strings.Contains(strings.ToLower(w.Title), q) ||
			strings.Contains(strings.ToLower(w.Path), q) ||
			strings.Contains(strings.ToLower(w.Output), q) {
			return true
		}
	}
	for _, r := range e.reports {
		if strings.Contains(strings.ToLower(r.Title), q) ||
			strings.Contains(strings.ToLower(r.Outcome), q) {
			return true
		}
	}
	return false
}

func (a *app) findBar(c *ui.Context, t thread, rs rows) {
	if !a.findOpen {
		return
	}
	if id := t.id(); id != a.findThread {
		a.findThread = id
		a.findIndex = 0
		a.findJump = a.findQuery != ""
	}
	matches := findMatches(rs.es, a.findQuery)
	if len(matches) == 0 {
		a.findIndex = 0
	} else {
		a.findIndex = max(0, min(a.findIndex, len(matches)-1))
	}

	if c.Shortcut(0, ui.KeyEscape) {
		a.closeFind()
		return
	}
	step := func(delta int) {
		if len(matches) == 0 {
			return
		}
		a.findIndex = (a.findIndex + delta + len(matches)) % len(matches)
		a.listFor(t.id()).ScrollIntoView(matches[a.findIndex])
	}
	if c.Shortcut(ui.Cmd, ui.KeyG) {
		step(1)
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG) {
		step(-1)
	}

	bar := a.frosted(c, ui.Row(c).Key("find-bar").Absolute().Top(12).Right(20).
		Padding(6, 8, 6, 12).Gap(6).Radius(12).AlignItems(ui.Center)).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.22))
	bar.Children(func() {
		ui.Icon(c, icSearch).FontSize(13).TextColor(palette.muted)
		in := ui.TextInput(c.Key("find-input"), &a.findQuery).Width(200).FontSize(12.5).
			Background(ui.Transparent).Border(0, ui.Transparent).FocusRing(false).Padding(2, 0).
			Placeholder("Find in thread…").Label("Find in thread")
		if a.findFocus {
			in.Focus()
			a.findFocus = false
		}
		if in.Changed() {
			a.findIndex = 0
			matches = findMatches(rs.es, a.findQuery)
			if len(matches) > 0 {
				a.listFor(t.id()).ScrollIntoView(matches[0])
			}
		} else if a.findJump && len(matches) > 0 {
			a.findJump = false
			a.listFor(t.id()).ScrollIntoView(matches[a.findIndex])
		}
		if in.Submitted() {
			if c.Shortcut(ui.Shift, ui.KeyEnter) {
				step(-1)
			} else {
				step(1)
			}
		}

		label := ""
		switch {
		case strings.TrimSpace(a.findQuery) == "":
		case len(matches) == 0:
			label = "0 matches"
		default:
			label = fmt.Sprintf("%d of %d", a.findIndex+1, len(matches))
		}
		if label != "" {
			ui.Text(c, label).FontSize(11.5).TextColor(palette.muted).Padding(0, 4)
		}
		if a.iconButton(c, "find-prev", icChevronUp, "Previous match  ⇧⌘G", false).Padding(4).Disabled(len(matches) == 0).Clicked() {
			step(-1)
		}
		if a.iconButton(c, "find-next", icChevronD, "Next match  ⌘G", false).Padding(4).Disabled(len(matches) == 0).Clicked() {
			step(1)
		}
		if a.iconButton(c, "find-close", icX, "Close find  Esc", false).Padding(4).Clicked() {
			a.closeFind()
		}
	})
}
