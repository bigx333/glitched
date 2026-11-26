package main

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// The terminal drawer runs a real shell through the engine's PTY manager
// (replayed and rendered locally by Ghostty's libghostty-vt), one per
// (environment, project workspace), kept alive in the engine across GUI closes.

type shellKey struct {
	env       api.ID
	workspace string
}

type shell struct {
	term   *terminal.Terminal
	conn   *client.TerminalConn
	themed string
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

// shellFor returns the workspace's shell on env, starting or reattaching it if
// it has none or its last one exited. It starts none where there is no window,
// as in tests, which keep to the drawer's canned output unless they hold one.
func (a *app) shellFor(env *client.Env, workspace string) (*shell, error) {
	var envID api.ID
	if env != nil {
		envID = env.ID
	}
	key := shellKey{env: envID, workspace: workspace}
	if s := a.shells[key]; s != nil {
		select {
		case <-s.term.Done():
			s.term.Close() // exited: start another
		default:
			if s.themed != a.theme.name {
				th := a.theme.terminal()
				s.term.SetTheme(th, th)
				s.themed = a.theme.name
			}
			return s, nil
		}
	}
	if a.win == nil {
		return nil, nil
	}
	baseTitle := filepath.Base(workspace)
	if workspace == "" || workspace == "~" {
		baseTitle = "~"
	}
	s := &shell{title: baseTitle, themed: a.theme.name}
	var (
		conn *client.TerminalConn
		term *terminal.Terminal
		err  error
	)
	if env != nil && a.backend != nil {
		conn, err = a.backend.OpenTerminal(env, workspace, 80, 24, func() {
			if a.win != nil {
				a.win.Invalidate()
			}
		})
		if err != nil {
			return nil, err
		}
		term, err = terminal.New(terminal.Options{
			Conn:  conn,
			Font:  terminal.Font{Family: "ui-monospace, Menlo, monospace", Size: 12},
			Theme: a.theme.terminal(),
			OnTitle: func(title string) {
				s.mu.Lock()
				s.title = title
				s.mu.Unlock()
				a.win.Invalidate()
			},
			OnExit: func(int) { a.win.Invalidate() },
		})
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
	} else {
		term, err = terminal.New(terminal.Options{
			Dir:   projectDir(workspace),
			Font:  terminal.Font{Family: "ui-monospace, Menlo, monospace", Size: 12},
			Theme: a.theme.terminal(),
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
	}
	s.conn = conn
	s.term = term
	a.shells[key] = s
	return s, nil
}

// closeShells detaches every terminal stream as the window closes; engine-owned
// PTYs stay alive in the daemon.
func (a *app) closeShells() {
	for id, s := range a.shells {
		s.term.Close()
		delete(a.shells, id)
	}
}

// projectDir expands a workspace's path, as "~/Projects/x"; one that does
// not exist here is the home directory instead.
func projectDir(path string) string {
	path = expandHome(path)
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return path
	}
	return homeDir()
}

// terminal is the theme's colors for the terminal: the theme of Ghostty's
// it names, else the plugin's dark or light palette on the theme's
// background and text.
func (th theme) terminal() *terminal.Theme {
	if t := terminalThemes[th.name]; t != nil {
		return t
	}
	var t *terminal.Theme
	if th.term != "" {
		t, _ = terminal.GhosttyTheme(th.term)
	}
	if t == nil {
		t = terminal.LightTheme()
		if th.dark {
			t = terminal.DarkTheme()
		}
		t.Background, t.Foreground = th.colors.bg, th.colors.text
	}
	terminalThemes[th.name] = t
	return t
}

// terminalThemes are the themes' terminal colors, once each is read:
// a Ghostty theme is a file, not to be read every frame.
var terminalThemes = map[string]*terminal.Theme{}

// terminalBody is the drawer's body: the project's shell, or the reason
// it could not start; without a window, the canned output.
func (a *app) terminalBody(c *ui.Context, s *shell, err error) {
	switch {
	case err != nil:
		ui.Column(c).Grow(1).Padding(12).Gap(4).Children(func() {
			ui.Text(c, "The terminal could not start.").TextColor(palette.red).FontWeight(500)
			ui.Text(c, err.Error()).FontSize(11.5).Font("monospace").TextColor(palette.muted).Selectable()
		})
	case s != nil:
		// The view sizes the terminal in cells of its font, and takes
		// the keyboard as the drawer opens.
		terminal.View(c.Key(s), s.term).Grow(1).FillWidth().FillHeight().Padding(6, 8).AutoFocus()
	default:
		a.cannedTerminal(c)
	}
}
