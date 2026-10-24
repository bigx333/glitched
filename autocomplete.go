package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Typing @ in the composer suggests the project's files, and / at the
// start of a line its commands, in a flyout above the composer, as MyGo's
// flyout example opens one: a window of its own beside the main one, so
// that it may reach past its edges. The keyboard stays in the composer:
// Up and Down move the choice, Enter or Tab takes it, Escape dismisses.

type suggestion struct {
	insert, label, detail string
	icon                  *ui.SVG
}

var slashCommands = []suggestion{
	{"/model ", "/model", "Switch the model", icSparkles},
	{"/plan ", "/plan", "Plan before changing anything", icListChecks},
	{"/review ", "/review", "Review the changes so far", icDiff},
	{"/compact ", "/compact", "Summarize the conversation to save context", icArchive},
	{"/clear ", "/clear", "Start the thread over", icX},
	{"/init ", "/init", "Write an AGENTS.md for the project", icFile},
}

// maxSuggestions is how many show at once.
const maxSuggestions = 6

// token is the draft's last word, which is what's being typed, and where
// it starts.
func token(draft string) (string, int) {
	i := strings.LastIndexAny(draft, " \t\n") + 1
	return draft[i:], i
}

// suggestionsFor returns what the token being typed could become: files
// for @, commands for / at the start of a line.
func (a *app) suggestionsFor(draft string) []suggestion {
	tok, at := token(draft)
	switch {
	case strings.HasPrefix(tok, "@"):
		t := a.current()
		if !t.local() {
			return nil // its files are on the server
		}
		q := strings.ToLower(tok[1:])
		var out []suggestion
		for _, f := range a.projectFiles(t.View.Workspace) {
			if q == "" || strings.Contains(strings.ToLower(f), q) {
				out = append(out, suggestion{insert: "@" + f + " ", label: filepath.Base(f), detail: filepath.Dir(f), icon: icFile})
			}
			if len(out) == maxSuggestions {
				break
			}
		}
		return out
	case strings.HasPrefix(tok, "/") && (at == 0 || draft[at-1] == '\n'):
		var out []suggestion
		for _, s := range slashCommands {
			if strings.HasPrefix(s.label, tok) {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// projectFiles lists a workspace's files, relative to it, read once.
func (a *app) projectFiles(workspace string) []string {
	if files, ok := a.files[workspace]; ok {
		return files
	}
	var files []string
	dir := projectDir(workspace)
	home, _ := os.UserHomeDir()
	if dir != home {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || len(files) >= 2000 {
				return filepath.SkipDir
			}
			name := d.Name()
			if d.IsDir() && path != dir && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "build" || name == "vendor") {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				files = append(files, rel)
			}
			return nil
		})
	}
	sort.Strings(files)
	a.files[workspace] = files
	return files
}

// accept puts a suggestion in place of the token being typed.
func (a *app) accept(s suggestion) {
	_, at := token(a.draft)
	a.draft = a.draft[:at] + s.insert
	a.suggestIndex = 0
}

// autocomplete follows the draft: it shows the flyout of suggestions while
// there are some and they weren't dismissed, and closes it otherwise;
// suggestKey takes the keys that move and take them. card is the
// composer's, which the flyout stands above.
func (a *app) autocomplete(c *ui.Context, card ui.Element) {
	tok, _ := token(a.draft)
	if tok != a.suggestFor {
		a.suggestFor, a.suggestDismissed, a.suggestIndex = tok, false, 0
	}
	a.suggestions = nil
	if !a.suggestDismissed {
		a.suggestions = a.suggestionsFor(a.draft)
	}
	n := len(a.suggestions)
	if n == 0 {
		a.closeSuggest()
		return
	}
	a.suggestIndex = min(a.suggestIndex, n-1)
	a.showSuggest(card.Bounds(), n)
}

const suggestRow = 34

func (a *app) showSuggest(card ui.Rect, n int) {
	if a.win == nil {
		return // in tests, the suggestions without their window
	}
	anchor := mygo.Rectangle{X: int(card.X), Y: int(card.Y), Width: int(card.W), Height: int(card.H)}
	w, h := 360, n*suggestRow+12
	if a.suggestFly == nil {
		a.suggestBox = [5]int{anchor.X, anchor.Y, anchor.Width, anchor.Height, h}
		a.suggestFly = mygo.NewFlyout(mygo.FlyoutOptions{
			Parent:    a.win,
			Anchor:    anchor,
			Placement: mygo.PlacementTopStart,
			Gap:       8,
			Width:     w,
			Height:    h,
			Shadow:    true,
			Content:   ui.View(a.suggestView),
		})
		fly := a.suggestFly
		fly.OnClosed(func() {
			if a.suggestFly == fly {
				a.suggestFly = nil
			}
		})
		return
	}
	// Moved or resized only when it changes: the view builds often.
	if box := [5]int{anchor.X, anchor.Y, anchor.Width, anchor.Height, h}; box != a.suggestBox {
		a.suggestBox = box
		a.suggestFly.SetSize(w, h)
		a.suggestFly.SetAnchor(anchor)
	}
	a.suggestFly.Invalidate()
}

func (a *app) closeSuggest() {
	if a.suggestFly != nil {
		a.suggestFly.Close()
		a.suggestFly = nil
	}
}

// suggestView is the flyout's view: the suggestions, the choice
// highlighted.
func (a *app) suggestView(c *ui.Context) {
	a.applyTheme(c)
	c.Root().Background(ui.Transparent)
	ui.Column(c).Fill().Radius(12).Background(palette.surface).Border(1, palette.border).Padding(6).Gap(0).Children(func() {
		for i, s := range a.suggestions {
			r := row(c, s.insert, i == a.suggestIndex).Height(suggestRow).Padding(0, 10).Gap(10)
			if r.Hovered() && a.suggestIndex != i {
				a.suggestIndex = i
				a.win.Invalidate()
			}
			if r.Children(func() {
				ui.Icon(c, s.icon).FontSize(14).TextColor(palette.muted)
				ui.Text(c, s.label).Font("monospace").FontSize(12.5).TextColor(palette.text).SingleLine()
				ui.Text(c, s.detail).FontSize(11.5).TextColor(palette.faint).SingleLine().Shrink(1)
			}).Clicked() {
				a.win.Update(func() {
					a.accept(s)
					a.closeSuggest()
				})
			}
		}
	})
}

// suggestKey takes the draft's keys while suggestions show, before the
// text area acts on them (OnTextKey): Up and Down move the choice, Enter
// and Tab take it, Escape dismisses them. It runs between frames.
func (a *app) suggestKey(k ui.TextKey) bool {
	n := len(a.suggestions)
	if n == 0 || k.Mods != 0 {
		return false
	}
	switch k.Key {
	case ui.KeyDown:
		a.suggestIndex = (a.suggestIndex + 1) % n
	case ui.KeyUp:
		a.suggestIndex = (a.suggestIndex + n - 1) % n
	case ui.KeyEnter, ui.KeyTab:
		a.draft = k.Text()
		a.accept(a.suggestions[min(a.suggestIndex, n-1)])
		k.SetText(a.draft)
		a.suggestions = nil
	case ui.KeyEscape:
		a.suggestDismissed, a.suggestions = true, nil
	default:
		return false
	}
	return true
}
