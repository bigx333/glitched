package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"glitched/internal/jsonfile"
	"glitched/internal/plugins/guard"
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

// cloneSource is what the field names: the URL git clones, and typed as
// owner/name, the repository gh clones.
type cloneSource struct {
	url string
	// repo is owner/name as typed, "" for a URL.
	repo string
	// name is the folder the clone makes.
	name string
}

var (
	ownerName = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$`)
	scpLike   = regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:(.+)$`)
	// gitProgress is one of git's progress lines, which it redraws in
	// place, and which say nothing of why a clone failed.
	gitProgress = regexp.MustCompile(`^(remote: )?[A-Za-z][A-Za-z ]*:\s+\d+%`)
	authFailure = regexp.MustCompile(`Authentication failed|could not read Username|Permission denied \(publickey\)|Repository not found`)
	credentials = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)
)

const (
	cloneTimeout = 15 * time.Minute
	cloneHint    = "Sign in with gh auth login, or paste a clone URL your git can reach."
)

// parseCloneSource reads s as owner/name, a clone URL (https, http, ssh,
// git or file, on any host, or user@host:path), or a GitHub page, which
// clones its repository.
func parseCloneSource(s string) (cloneSource, bool) {
	s = strings.TrimSpace(s)
	if m := ownerName.FindStringSubmatch(s); m != nil {
		if dots(m[1]) || dots(m[2]) {
			return cloneSource{}, false
		}
		repo := m[1] + "/" + strings.TrimSuffix(m[2], ".git")
		return named(cloneSource{url: "https://github.com/" + repo + ".git", repo: repo}, repo)
	}
	if m := scpLike.FindStringSubmatch(s); m != nil && !strings.Contains(s, "://") {
		return named(cloneSource{url: s}, m[1])
	}
	u, err := url.Parse(s)
	if err != nil {
		return cloneSource{}, false
	}
	switch u.Scheme {
	case "https", "http":
		if strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == "github.com" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) < 2 {
				return cloneSource{}, false
			}
			owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
			if !ownerName.MatchString(owner+"/"+name) || dots(owner) || dots(name) {
				return cloneSource{}, false
			}
			u = &url.URL{Scheme: "https", User: u.User, Host: "github.com", Path: "/" + owner + "/" + name + ".git"}
			return named(cloneSource{url: u.String()}, name)
		}
		fallthrough
	case "ssh", "git":
		if u.Host == "" {
			return cloneSource{}, false
		}
	case "file":
	default:
		return cloneSource{}, false
	}
	return named(cloneSource{url: u.Scheme + s[len(u.Scheme):]}, u.Path)
}

// named is src with the folder name p's last part gives it.
func named(src cloneSource, p string) (cloneSource, bool) {
	src.name = strings.TrimSuffix(path.Base(strings.TrimRight(p, "/")), ".git")
	return src, src.name != "" && src.name != "/" && !dots(src.name)
}

func dots(s string) bool { return s == "." || s == ".." }

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
	src, ok := parseCloneSource(a.clone.input)
	if !ok || a.clone.run != nil {
		return
	}
	dest := filepath.Join(a.clone.parent, src.name)
	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	run := &cloneRun{cancel}
	a.clone.run, a.clone.line, a.clone.failure = run, "", ""
	go func() {
		err := clone(ctx, src, dest, clonePath(), func(line string) {
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

// clone clones src into dest, a folder that isn't there yet or is empty,
// with the programs path finds, and says each line they print. A clone
// that fails or is stopped takes away dest if it made it. Its error is
// the last lines git said, with what to do if it was refused access.
func clone(ctx context.Context, src cloneSource, dest, path string, progress func(string)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// Making dest here claims it: only a folder this clone made is taken
	// away when it fails, never one the user made meanwhile.
	created := os.Mkdir(dest, 0o755) == nil
	if entries, err := os.ReadDir(dest); err != nil || len(entries) > 0 {
		return errors.New(dest + " already exists and isn't empty")
	}
	env := append(os.Environ(), "PATH="+path, "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GCM_INTERACTIVE=never")
	command := func(program string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Env = env
		guard.OwnGroup(cmd)
		cmd.WaitDelay = time.Second
		return cmd
	}
	var cmd *exec.Cmd
	gh, hasGH := guard.Find("gh", path)
	git, hasGit := guard.Find("git", path)
	switch {
	case src.repo != "" && hasGH && command(gh, "auth", "status", "--hostname", "github.com").Run() == nil:
		cmd = command(gh, "repo", "clone", src.repo, dest, "--", "--progress")
	case hasGit:
		cmd = command(git, "clone", "--progress", "--", src.url, dest)
	default:
		return errors.New("git isn't installed on this machine (or isn't on your PATH)")
	}
	stderr, err := cmd.StderrPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return err
	}
	var tail []string
	refused := false
	lines := bufio.NewScanner(stderr)
	lines.Split(splitLines)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" {
			continue
		}
		progress(line)
		if !gitProgress.MatchString(line) {
			tail = append(tail[max(0, len(tail)-3):], credentials.ReplaceAllString(line, "$1"))
			refused = refused || authFailure.MatchString(line)
		}
	}
	err = cmd.Wait()
	if err == nil {
		return nil
	}
	if created {
		os.RemoveAll(dest)
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.New("The clone took longer than 15 minutes, and was stopped.")
	case ctx.Err() != nil:
		return ctx.Err()
	case len(tail) == 0:
		tail = []string{"The repository couldn't be cloned."}
	}
	if refused {
		tail = append(tail, cloneHint)
	}
	return errors.New(strings.Join(tail, "\n"))
}

// splitLines splits git's output at each \r as well as each \n: a
// progress line redraws itself after a \r.
func splitLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 || len(data) >= maxCloneLine {
		n := min(len(data), maxCloneLine)
		return n, data[:n], nil
	}
	return 0, nil, nil
}

// maxCloneLine is as much of one line as is kept: a longer one comes in
// pieces, so that no line stops the reading, and git with it.
const maxCloneLine = 4096

// cloneDialog is the Clone a repository sheet, drawn as the app's alerts
// are (MyGo's AlertDialog), with their measures: a field for the
// repository, the folder it goes into, and Cancel and Clone. Escape
// cancels, and stops the clone.
func (a *app) cloneDialog(c *ui.Context) {
	if !a.clone.open {
		return
	}
	src, ok := parseCloneSource(a.clone.input)
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
			dest = filepath.Join(dest, src.name)
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
