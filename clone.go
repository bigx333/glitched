package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/jsonfile"
	"glitched/internal/plugins/guard"
	"glitched/internal/projects"
)

// Cloning a repository makes a project of it on this machine: the user's
// own git clones it, or gh for an owner/name while gh is signed in, with
// their credentials and no prompts, into a folder that's added as Add
// project adds one, with a new thread in it.

// cloneSheet is the Clone a repository sheet as it shows.
type cloneSheet struct {
	open bool
	// input is the field's URL or owner/name, as typed.
	input string
	// parent is the folder the clone goes into, as a folder of its own.
	parent string
	// run is the clone running, nil while none is.
	run *cloneRun
	// line is the last line the running clone printed; failure what the
	// last one to fail said, "" while none has.
	line, failure string
}

// cloneRun is one clone the sheet started. Its lines and its end show
// only while it's the sheet's run.
type cloneRun struct{ cancel context.CancelFunc }

// clonePath is where a clone finds git and gh: what the user's terminal
// finds, as the plugin guard looks.
var clonePath = guard.UserPath

// chooseCloneParent opens the system's folder picker over the window for
// the folder a clone goes into; "" when none was picked.
var chooseCloneParent = func(a *app) string {
	if a.win == nil {
		return ""
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: a.win, Title: "Clone into", ButtonLabel: "Choose", Directory: true, CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// cloneFile is clone.json in the state directory: the parent folder last
// chosen.
type cloneFile struct {
	Parent string `json:"parent"`
}

func (a *app) cloneFilePath() string { return filepath.Join(a.dir, "clone.json") }

// openClone shows the sheet, into the parent last chosen, else
// ~/Projects. Only this machine clones.
func (a *app) openClone() {
	if a.clone.open || a.localEnv() == nil {
		return
	}
	var f cloneFile
	if err := jsonfile.Load(a.cloneFilePath(), &f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("glitched: clone: %v", err)
	}
	if f.Parent == "" {
		home, _ := os.UserHomeDir()
		f.Parent = filepath.Join(home, "Projects")
	}
	a.clone = cloneSheet{open: true, parent: f.Parent}
}

// chooseParent asks for the folder the clone goes into, off the main
// thread, as addProject does, and keeps it for the next clone.
func (a *app) chooseParent() {
	if a.picking {
		return
	}
	a.picking = true
	go func() {
		dir := chooseCloneParent(a)
		a.post(func() {
			a.picking = false
			if dir == "" {
				return
			}
			a.clone.parent = filepath.Clean(dir)
			if err := jsonfile.Write(a.cloneFilePath(), cloneFile{a.clone.parent}); err != nil {
				log.Printf("glitched: clone: %v", err)
			}
		})
	}()
}

// startClone clones what the field names into the parent folder, off the
// main thread. Once it's cloned, the folder is a project, with a new
// thread in it.
func (a *app) startClone() {
	src, ok := projects.ParseSource(a.clone.input)
	if !ok || a.clone.run != nil {
		return
	}
	dest := filepath.Join(a.clone.parent, src.Name)
	ctx, cancel := context.WithTimeout(context.Background(), projects.CloneTimeout)
	run := &cloneRun{cancel}
	a.clone.run, a.clone.line, a.clone.failure = run, "", ""
	go func() {
		err := projects.Clone(ctx, src, dest, clonePath(), func(line string) {
			a.post(func() {
				if a.clone.run == run {
					a.clone.line = line
				}
			})
		})
		cancel()
		a.post(func() {
			// A clone that finished is a project, though the sheet was
			// cancelled as it ended: its folder is there either way.
			if err == nil {
				a.recordProject(dest)
			}
			if a.clone.run != run {
				return
			}
			a.clone.run = nil
			if err != nil {
				a.clone.failure = err.Error()
				return
			}
			a.clone.open = false
			if e := a.localEnv(); e != nil {
				a.startIn(draft{e, dest})
				a.draftArea.Focus()
			}
		})
	}()
}

// cancelClone closes the sheet and stops its clone, whose folder goes
// with it once the clone has stopped.
func (a *app) cancelClone() {
	if a.clone.run != nil {
		a.clone.run.cancel()
	}
	a.clone.open, a.clone.run = false, nil
}

// cloneDialog is the Clone a repository sheet, drawn as the app's alerts
// are (MyGo's AlertDialog), with their measures but wider, for a URL and
// a folder's path: a field for the repository, the folder it goes into,
// and Cancel and Clone. Escape cancels, and stops the clone.
func (a *app) cloneDialog(c *ui.Context) {
	if !a.clone.open {
		return
	}
	src, ok := projects.ParseSource(a.clone.input)
	running := a.clone.run != nil
	a.dialog(c, &a.clone.open, func(backdrop, panel ui.Element) {
		// A click outside does nothing, as outside an alert: it would
		// lose what was typed, and stop a clone.
		if backdrop.Clicked() {
			a.clone.open = true
		}
		t := c.Theme()
		w, _ := c.Size()
		room := w - t.Space(10)
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(t.Space(115), room)).MaxWidth(room).Padding(t.Space(5)).Gap(t.Space(3)).Radius(t.Space(2.5)).
			Background(t.Background).Label("Clone a repository")
		panel.Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3))
		wrapped := func(s string) ui.Element { return ui.Text(c, s).Width(0).MinWidthPercent(100) }
		wrapped("Clone a repository").Bold().FontSize(t.FontSize + 1)
		in := ui.TextInput(c.Key("clone source"), &a.clone.input).Placeholder("URL or owner/name").Label("URL or owner/name").
			AutoFocus().Disabled(running).Width(0).MinWidthPercent(100)
		if in.Submitted() && ok {
			a.startClone()
		}
		if strings.TrimSpace(a.clone.input) != "" && !ok {
			wrapped("Enter a clone URL or owner/name").TextColor(palette.red)
		}
		dest := a.clone.parent
		if ok {
			dest = filepath.Join(dest, src.Name)
		}
		ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Into "+dest).TextColor(t.TextMuted).Width(0).Grow(1).Shrink(1)
			if ui.Button(c.Key("clone parent"), "Choose…").Disabled(running || a.picking).Clicked() {
				a.chooseParent()
			}
		})
		if running {
			ui.Row(c).Gap(t.Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Label("Cloning")
				ui.Text(c, "Cloning…")
			})
			if a.clone.line != "" {
				wrapped(a.clone.line).TextColor(t.TextMuted).SingleLine()
			}
		}
		if a.clone.failure != "" {
			wrapped(a.clone.failure).TextColor(palette.red)
		}
		ui.Row(c).Wrap().Gap(t.Space(2)).Justify(ui.End).Margin(t.Space(2), 0, 0, 0).Children(func() {
			if ui.Button(c.Key("clone cancel"), "Cancel").MaxWidthPercent(100).Clicked() {
				a.cancelClone()
			}
			label := "Clone"
			if a.clone.failure != "" {
				label = "Retry"
			}
			if ui.PrimaryButton(c.Key("clone start"), label).Disabled(!ok || running).MaxWidthPercent(100).Clicked() {
				a.startClone()
			}
		})
	})
	if !a.clone.open {
		a.cancelClone()
	}
}
