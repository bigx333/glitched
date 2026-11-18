package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/provider"
)

// Typing @ in the composer suggests the project's files, and / at the
// start of a line its commands, in a flyout above the composer, as MyGo's
// flyout example opens one: a window of its own beside the main one, so
// that it may reach past its edges. The keyboard stays in the composer:
// Up and Down move the choice, Enter or Tab takes it, Escape dismisses.

// A suggestion is what taking it puts in place of the token; one with
// nothing to put is a note, as while the commands are listed.
type suggestion struct {
	insert, label, detail string
	icon                  *ui.SVG
}

func (s suggestion) note() bool { return s.insert == "" }

// fallbackCommands are what / suggests where the provider's own commands
// can't be listed, as on a server's threads, whose engine this machine's
// socket doesn't reach. Glitched takes none of them itself: a command
// goes to the agent as typed, and these are Claude Code's.
var fallbackCommands = []suggestion{
	{"/model ", "/model", "Switch the model", icSparkles},
	{"/compact ", "/compact", "Summarize the conversation to save context", icArchive},
	{"/clear ", "/clear", "Clear the agent's context", icX},
	{"/init ", "/init", "Write a CLAUDE.md for the project", icFile},
}

// maxSuggestions is how many files @ suggests.
const maxSuggestions = 6

// suggestRows is how many rows the flyout shows; more scroll. A provider
// lists every skill the user has, often a hundred or more.
const suggestRows = 8

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
		t := a.current()
		if !t.local() {
			return fallbackFor(tok)
		}
		l := a.slashListFor(t)
		switch {
		case l.listed:
			return commandSuggestions(l.cmds, tok[1:])
		case l.err != nil:
			return append(fallbackFor(tok), suggestion{label: "Couldn't list the agent's commands", detail: l.err.Error(), icon: icX})
		}
		return []suggestion{{label: "Listing the agent's commands…", icon: icClock}}
	}
	return nil
}

func fallbackFor(tok string) []suggestion {
	var out []suggestion
	for _, s := range fallbackCommands {
		if strings.HasPrefix(s.label, tok) {
			out = append(out, s)
		}
	}
	return out
}

// commandSuggestions are the commands whose name starts with q, then
// those of a plugin whose own name does, as q "pot" finds
// pstack:poteto-mode.
func commandSuggestions(cmds []provider.Command, q string) []suggestion {
	var named, plugins []suggestion
	for _, c := range cmds {
		plugin, own, scoped := strings.Cut(c.Name, ":")
		if !scoped {
			plugin = ""
		}
		s := suggestion{insert: "/" + c.Name + " ", label: "/" + c.Name, detail: summary(c.Description, plugin), icon: icCommand}
		switch {
		case strings.HasPrefix(c.Name, q):
			named = append(named, s)
		case scoped && strings.HasPrefix(own, q):
			plugins = append(plugins, s)
		}
	}
	return append(named, plugins...)
}

// summary is a description's first line, without the "(plugin) " that
// the command's name already says, cut to what a row could show.
func summary(desc, plugin string) string {
	desc, _, _ = strings.Cut(desc, "\n")
	if plugin != "" {
		desc = strings.TrimPrefix(desc, "("+plugin+") ")
	}
	if r := []rune(desc); len(r) > 120 {
		desc = string(r[:120]) + "…"
	}
	return desc
}

// commandsRecheck is how long the composer shows a provider's commands,
// or why they couldn't be listed, before asking the engine again, which
// answers from its own listing while that's fresh (daemon.commandsFresh).
const commandsRecheck = time.Minute

type slashKey struct{ provider, workspace string }

// slashList is what the engine last listed of a provider's commands in a
// workspace.
type slashList struct {
	cmds   []provider.Command
	listed bool  // cmds came, once
	err    error // why the last ask failed
	asking bool
	at     time.Time // when the last answer came
}

// slashListFor is the commands of t's provider in t's workspace, as
// listed last. It asks the engine for them off the UI thread when it has
// none or they're old, and shows the old meanwhile.
func (a *app) slashListFor(t thread) *slashList {
	k := slashKey{t.View.Model.Provider, t.View.Workspace}
	if t.id().IsZero() {
		k.provider = a.modelRef().Provider
	}
	l := a.slashLists[k]
	if l == nil {
		l = &slashList{}
		a.slashLists[k] = l
	}
	if l.asking || !l.at.IsZero() && time.Since(l.at) < commandsRecheck {
		return l
	}
	l.asking = true
	engine := a.serviceClient(a.dir)
	go func() {
		// Claude Code's listing takes several seconds, and up to its own
		// timeout when claude hangs.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmds, err := engine.ProviderCommands(ctx, k.provider, k.workspace)
		cancel()
		a.post(func() {
			l.asking, l.at, l.err = false, time.Now(), err
			if err == nil {
				l.cmds, l.listed = cmds, true
			}
		})
	}()
	return l
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
	if s.note() {
		return
	}
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
		a.suggestList.ScrollTo(0, ui.Start)
	}
	a.suggestions = nil
	// Code is no place for commands or @ mentions.
	if !a.suggestDismissed && !a.code.on {
		a.suggestions = a.suggestionsFor(a.draft)
	}
	n := len(a.suggestions)
	if n == 0 {
		a.closeSuggest()
		return
	}
	a.suggestIndex = min(a.suggestIndex, n-1)
	a.showSuggest(card.Bounds(), n)
	// A window of its own over the timeline, which the frame on screen
	// does not place.
	a.blinding = a.blinding || a.suggestFly != nil
}

const suggestRow = 34

func (a *app) showSuggest(card ui.Rect, n int) {
	if a.win == nil {
		return // in tests, the suggestions without their window
	}
	anchor := mygo.Rectangle{X: int(card.X), Y: int(card.Y), Width: int(card.W), Height: int(card.H)}
	w, h := 460, min(n, suggestRows)*suggestRow+12
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
// highlighted, in a list that scrolls past suggestRows.
func (a *app) suggestView(c *ui.Context) {
	a.applyTheme(c)
	c.Root().Background(ui.Transparent)
	ui.Column(c).Fill().Radius(12).Background(palette.surface).Border(1, palette.border).Padding(6).Gap(0).Children(func() {
		l := &a.suggestList
		l.Key = func(i int) any { return a.suggestions[i].insert }
		ui.List(c.Key("suggestions"), l, len(a.suggestions), func(i int) {
			s := a.suggestions[i]
			r := row(c, "row", i == a.suggestIndex).Height(suggestRow).Padding(0, 10).Gap(10)
			if r.Hovered() && a.suggestIndex != i {
				a.suggestIndex = i
				a.win.Invalidate()
			}
			if r.Children(func() {
				ui.Icon(c, s.icon).FontSize(14).TextColor(palette.muted)
				ui.Text(c, s.label).Font("monospace").FontSize(12.5).TextColor(palette.text).SingleLine().Shrink(0).MaxWidth(280)
				ui.Text(c, s.detail).FontSize(11.5).TextColor(palette.faint).SingleLine().Shrink(1)
			}).Clicked() && !s.note() {
				a.win.Update(func() {
					a.accept(s)
					a.closeSuggest()
				})
			}
		}).Grow(1)
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
		a.suggestList.ScrollIntoView(a.suggestIndex)
	case ui.KeyUp:
		a.suggestIndex = (a.suggestIndex + n - 1) % n
		a.suggestList.ScrollIntoView(a.suggestIndex)
	case ui.KeyEnter, ui.KeyTab:
		if a.suggestions[min(a.suggestIndex, n-1)].note() {
			return false // the draft goes as typed
		}
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
