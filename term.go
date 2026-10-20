package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// The terminal drawer runs a real shell, the user's, in a pseudo-terminal
// emulated by Ghostty's libghostty-vt (MyGo's terminal plugin): one for
// each project, started in the project's directory the first time the
// drawer shows for one of its threads, and kept while the app runs, as
// T3 Code keeps a terminal per project.

type shell struct {
	term *terminal.Terminal
	// title is what the shell last set as its title, from the terminal's
	// goroutine; the drawer's header shows it.
	mu    sync.Mutex
	title string
}

func (s *shell) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

// shellFor returns the project's shell, starting it if it has none or its
// last one exited. It returns nil where there is no window, as in tests,
// which keep to the drawer's canned output rather than start shells.
func (a *app) shellFor(p *project) (*shell, error) {
	if a.win == nil {
		return nil, nil
	}
	if s := a.shells[p.id]; s != nil {
		select {
		case <-s.term.Done():
			s.term.Close() // exited: start another
		default:
			return s, nil
		}
	}
	s := &shell{title: p.name}
	term, err := terminal.New(terminal.Options{
		Dir:  projectDir(p.path),
		Font: terminal.Font{Family: "ui-monospace, Menlo, monospace", Size: 12},
		// The drawer is dark whatever the theme, as T3 Code's is.
		Theme: drawerTheme(),
		OnTitle: func(title string) {
			s.mu.Lock()
			s.title = title
			s.mu.Unlock()
			a.win.Invalidate()
		},
		OnExit: func(int) { a.win.Invalidate() },
	})
	if err != nil {
		return nil, err
	}
	s.term = term
	a.shells[p.id] = s
	return s, nil
}

// closeShells hangs up every project's shell, as the window closes.
func (a *app) closeShells() {
	for id, s := range a.shells {
		s.term.Close()
		delete(a.shells, id)
	}
}

// projectDir expands a project's path, as "~/Projects/x"; one that does not
// exist, as the mock projects' may not, is the home directory instead.
func projectDir(path string) string {
	home, _ := os.UserHomeDir()
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		path = filepath.Join(home, rest)
	}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return path
	}
	return home
}

// drawerTheme is the terminal's colors: the drawer's near-black, with the
// app's text, and VS Code's palette from the plugin's dark theme.
func drawerTheme() *terminal.Theme {
	t := terminal.DarkTheme()
	t.Background = ui.Hex("#08080a")
	t.Foreground = darkColors.text
	return t
}

// terminalBody is the drawer's body: the project's shell, or the reason
// it could not start; without a window, the canned output.
func (a *app) terminalBody(c *ui.Context, s *shell, err error) {
	switch {
	case err != nil:
		ui.Column(c).Grow(1).Padding(12).Gap(4).Children(func() {
			ui.Text(c, "The terminal could not start.").TextColor(darkColors.red).FontWeight(500)
			ui.Text(c, err.Error()).FontSize(11.5).Font("monospace").TextColor(darkColors.muted).Selectable()
		})
	case s != nil:
		// The view sizes the terminal in cells of its font, and takes
		// the keyboard as the drawer opens.
		terminal.View(c.Key(s), s.term).Grow(1).Padding(6, 8).AutoFocus()
	default:
		a.cannedTerminal(c)
	}
}
