package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// The diff panel is MyGo's diff viewer (ui/editor), as its code-editor
// example uses it: each file the agent's edits name, as the workspace's
// git HEAD has it and as its working tree does, unified or side by side,
// with the words changed highlighted. The diff is the workspace's
// uncommitted change to the file, whoever made it. The panel only reads:
// marking a change or a file reviewed is the review's own state. A thread
// on a server has its files there, so the panel shows none.

// diffKey is a file of a thread's diff.
type diffKey struct {
	thread api.ID
	path   string
}

// fileDiff is the viewer over one file, read when the edits naming it
// last changed. reviewed is the user's mark, which an edit after it
// clears, as the file it was for has changed.
type fileDiff struct {
	*editor.DiffState // nil when the file isn't shown: see unshown
	stamp             time.Time
	// unshown is why the panel shows no diff of the file, and noHead why
	// it shows the working file alone, without HEAD's side; "" when it
	// does.
	unshown, noHead string
	reviewed        bool
}

// diffRead is a read of a file under way, for the edits' stamp. A newer
// read of the file, or its thread going, supersedes it, and what it read
// is dropped.
type diffRead struct {
	stamp time.Time
	gen   uint64
}

// diffSlots caps the files read and diffed at once.
var diffSlots = make(chan struct{}, 4)

// maxDiffBytes is the most of either side of a file the panel reads.
const maxDiffBytes = 1 << 20

var errTooLarge = errors.New("the file is over 1 MB")

// readWorking reads a working file, a missing one as empty. It reads only
// a regular file, and opening one swapped for a FIFO since it was located
// neither blocks nor follows a link.
func readWorking(file string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|openFlags, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("it isn't a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxDiffBytes+1))
	switch {
	case err != nil:
		return nil, err
	case len(b) > maxDiffBytes:
		return nil, errTooLarge
	}
	return b, nil
}

// diffFor returns the viewer of a file of the thread's: nil until the
// file is first read, and for a thread on a server. Reading and diffing
// it runs off the UI thread, again once the agent edits it again, while
// the last read shows.
func (a *app) diffFor(t thread, f fileChange) *fileDiff {
	if !t.local() {
		return nil
	}
	k := diffKey{t.id(), f.path}
	d := a.diffs[k]
	r, busy := a.reading[k]
	if (d == nil || !d.stamp.Equal(f.stamp)) && (!busy || !r.stamp.Equal(f.stamp)) {
		a.readGen++
		r = diffRead{f.stamp, a.readGen}
		a.reading[k] = r
		ws := t.View.Workspace
		go func() {
			diffSlots <- struct{}{}
			read := readDiff(ws, f.path, f.stamp)
			<-diffSlots
			a.post(func() {
				if a.reading[k].gen != r.gen {
					return
				}
				delete(a.reading, k)
				a.diffs[k] = read
			})
		}()
	}
	return d
}

// pruneDiffs forgets the diffs of threads gone, as with their server.
func (a *app) pruneDiffs() {
	gone := func(id api.ID) bool { _, ok := a.find(id); return !ok }
	maps.DeleteFunc(a.diffs, func(k diffKey, _ *fileDiff) bool { return gone(k.thread) })
	maps.DeleteFunc(a.reading, func(k diffKey, _ diffRead) bool { return gone(k.thread) })
	maps.DeleteFunc(a.diffFile, func(id api.ID, _ string) bool { return gone(id) })
}

// readDiff reads the file an edit names in the workspace, and diffs it
// with HEAD's.
func readDiff(workspace, path string, stamp time.Time) *fileDiff {
	d := &fileDiff{stamp: stamp}
	file, err := locate(workspace, path)
	if err != nil {
		d.unshown = err.Error()
		return d
	}
	disk, err := readWorking(file)
	if err != nil {
		d.unshown = err.Error()
		return d
	}
	head, err := headOf(file)
	switch {
	case errors.Is(err, errTooLarge):
		d.unshown = err.Error()
		return d
	case err != nil:
		d.noHead, head = err.Error(), string(disk)
	}
	if binary(disk) || binary([]byte(head)) {
		d.unshown = "the file is binary"
		return d
	}
	lang := editor.LanguageFor(path)
	before, after := editor.NewBuffer(head), editor.NewBuffer(string(disk))
	before.SetLanguage(lang)
	after.SetLanguage(lang)
	d.DiffState = &editor.DiffState{Old: before, New: after, HunkActions: d.noHead == "", ExpandUnchanged: d.noHead != ""}
	d.RenderHunkActions = func(c *ui.Context, bar ui.Element, h *editor.DiffHunkActions) {
		bar.Children(func() {
			if reviewButton(c, "Mark reviewed").Label("Mark change reviewed").Clicked() {
				h.Keep()
			}
		})
	}
	// The diff is made here, not on the frame that first counts it.
	d.Stats()
	return d
}

// binary is whether text is a binary file's, as git tells: a NUL in its
// first 8000 bytes.
func binary(text []byte) bool {
	return bytes.IndexByte(text[:min(len(text), 8000)], 0) >= 0
}

// workingFile is where a file an edit names is: the path itself when it
// is absolute, else the path in the workspace.
func workingFile(workspace, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(expandHome(workspace), path)
}

// locate is where the file an edit names is, its folders' links resolved,
// or why the panel won't read it. An agent's path can be absolute, climb
// out with .., or pass through a link, and the panel reads only regular
// files in the workspace.
func locate(workspace, path string) (string, error) {
	file := workingFile(workspace, path)
	ws, err := filepath.EvalSymlinks(workingFile(workspace, "."))
	if err != nil {
		return "", errors.New("the workspace can't be found")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		return "", errors.New("its folder can't be found")
	}
	file = filepath.Join(dir, filepath.Base(file))
	if rel, err := filepath.Rel(ws, file); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("the file is outside the workspace")
	}
	switch fi, err := os.Lstat(file); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink != 0:
		return "", errors.New("the file is a symbolic link")
	case !fi.Mode().IsRegular():
		return "", errors.New("it isn't a regular file")
	}
	return file, nil
}

// gitTimeout is how long reading HEAD's side of a file may take.
var gitTimeout = 5 * time.Second

// headOf is file as git's HEAD has it, "" when HEAD lacks it, or why
// that's unknown.
func headOf(file string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git isn't installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	text, err := headIn(ctx, file)
	if ctx.Err() != nil {
		return "", fmt.Errorf("git took over %v", gitTimeout)
	}
	return text, err
}

func headIn(ctx context.Context, file string) (string, error) {
	// As ./name, the name can't be read as an option, and ls-tree takes
	// it literally rather than as a pattern.
	name := "./" + filepath.Base(file)
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", filepath.Dir(file), "-c", "core.fsmonitor="}, args...)...)
		cmd.Env = gitEnv()
		// A child git leaves holding its output doesn't hold the read.
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		return string(out), err
	}
	if out, err := git("rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(out) != "true" {
		return "", errors.New("the file isn't in a git work tree")
	}
	if _, err := git("rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return "", errors.New("the repository has no commits yet")
	}
	out, err := git("--literal-pathspecs", "ls-tree", "--long", "HEAD", "--", name)
	if err != nil {
		return "", errors.New("git couldn't list HEAD")
	}
	if out == "" {
		return "", nil
	}
	// <mode> SP <type> SP <object> SP+ <size> TAB <name>
	meta, _, _ := strings.Cut(out, "\t")
	fields := strings.Fields(meta)
	if len(fields) != 4 || fields[1] != "blob" || fields[0] == "120000" {
		return "", errors.New("HEAD has it as something other than a file")
	}
	// A partial clone that lacks the blob lists its size as BAD.
	size, err := strconv.Atoi(fields[3])
	if err != nil {
		return "", errors.New("this clone doesn't have HEAD's copy of the file")
	}
	if size > maxDiffBytes {
		return "", errTooLarge
	}
	text, err := git("cat-file", "blob", fields[2])
	if err != nil {
		return "", errors.New("git couldn't read HEAD's file")
	}
	return text, nil
}

// gitEnv is this process's environment but for git's own variables, which
// an app launched from a git hook or a shell inherits, and which can
// point git at another repository, index or config than the file's. A
// partial clone would fetch a HEAD blob it lacks, so reading a diff would
// reach the network and write to .git; GIT_NO_LAZY_FETCH stops that.
func gitEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(strings.ToUpper(kv), "GIT_")
	})
	return append(env, "GIT_NO_LAZY_FETCH=1")
}

func reviewButton(c *ui.Context, label string) ui.Element {
	b := ui.ButtonBase(c.Key(label)).Padding(3, 9).Radius(6).Cursor(ui.CursorPointer).
		Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.25)).Transition(hoverFade)
	shade := float32(0.35)
	if b.Hovered() {
		shade = 0.2
	}
	bg := palette.green.Mix(ui.Hex("#000000"), shade)
	b.Background(bg).Border(1, palette.green.Alpha(0.6)).TextColor(onColor(bg))
	return b.Children(func() { ui.Text(c, label).FontSize(11.5).FontWeight(500) })
}

// onColor is the color of text on bg: white or near black, whichever
// contrasts more, as a theme's green may be dark or light.
func onColor(bg ui.Color) ui.Color {
	white, black := ui.Hex("#ffffff"), ui.Hex("#111114")
	if contrast(white, bg) >= contrast(black, bg) {
		return white
	}
	return black
}

// diffStats counts the lines a file's change adds and deletes, as its diff
// has them now: marking changes reviewed counts down. With HEAD's side
// unknown there is no change to count.
func (a *app) diffStats(t thread, f fileChange) (add, del int) {
	if d := a.diffFor(t, f); d != nil && d.DiffState != nil && d.noHead == "" {
		return d.Stats()
	}
	return 0, 0
}

// diffPanel is the panel of the thread's changed files: a plugin's, the
// built-in Diff panel or one of the user's that replaces it, in two copies
// of thread.panel.diff. The header's copy is in the window's title bar,
// the body's below it, over the native diff.body.
func (a *app) diffPanel(c *ui.Context, t thread, es []entry) {
	files := changedFiles(es)
	panel := diffPanel{Local: t.local(), Files: make([]diffFile, len(files)), Layout: "unified", SplitFits: a.splitFits(),
		SplitWidth: splitWidth, Wrap: a.diffWrap, CanOpen: t.local() && editorCLI() != ""}
	if a.layout() == editor.DiffSplit {
		panel.Layout = "split"
	}
	for i, f := range files {
		d := a.diffFor(t, f)
		add, del := a.diffStats(t, f)
		panel.Files[i] = diffFile{Path: f.path, Add: add, Del: del, Reviewed: d != nil && d.reviewed, Read: d != nil}
	}
	if len(files) > 0 {
		panel.Shown = files[max(slices.IndexFunc(files, func(f fileChange) bool { return f.path == a.diffFile[t.id()] }), 0)].path
	}
	copyOf := func(part string) func(plugins.ThreadRecord) any {
		return func(r plugins.ThreadRecord) any {
			p := panel
			p.Thread, p.Part = r, part
			return p
		}
	}
	key := t.id().String()
	ui.Column(c).Width(a.diffWidth()).FillHeight().Children(func() {
		bar := c.TitleBar()
		head := ui.Row(c).Height(max(bar.Height, headerHeight)).Padding(0, 8+bar.Right, 0, 14).Gap(8).AlignItems(ui.Center).DragWindow()
		a.pluginComponent(c, func() ui.Element { return head }, "thread.panel.diff", key+"/header", t, copyOf("header"))
		// Below its glass header, the panel on the sidebar's color.
		body := ui.Column(c).Grow(1).Background(palette.sidebar).BorderWidth(1, 0, 0, 1).BorderColor(palette.border)
		a.pluginComponent(c, func() ui.Element { return body }, "thread.panel.diff", key+"/body", t, copyOf("body"))
	})
}

// diffPanel is the context of thread.panel.diff.
type diffPanel struct {
	Thread plugins.ThreadRecord `json:"thread"`
	// Part is the copy's: the header or the body.
	Part  string     `json:"part"`
	Local bool       `json:"local"`
	Files []diffFile `json:"files"`
	// Shown is the file diff.body shows, "" with none.
	Shown      string `json:"shown"`
	Layout     string `json:"layout"`
	SplitFits  bool   `json:"splitFits"`
	SplitWidth int    `json:"splitWidth"`
	Wrap       bool   `json:"wrap"`
	// CanOpen says diff.openFile has an editor to open a file in.
	CanOpen bool `json:"canOpen"`
}

// diffFile is a changed file as the panel's context has it: Read once its
// diff is, which marking it reviewed needs.
type diffFile struct {
	Path     string `json:"path"`
	Add      int    `json:"add"`
	Del      int    `json:"del"`
	Reviewed bool   `json:"reviewed"`
	Read     bool   `json:"read"`
}

// diffBody is the native diff.body: the diff of the thread's file at
// path, or why it isn't shown. Marking one of its changes reviewed fires
// the plugin's change with the file and the changes left.
func (a *app) diffBody(c *ui.Context, t thread, path string, fire func(any)) {
	files := changedFiles(entriesOf(t))
	i := slices.IndexFunc(files, func(f fileChange) bool { return f.path == path })
	switch {
	case len(files) == 0:
		ui.Text(c, "No changes in this thread yet.").TextColor(palette.muted).Margin(ui.Auto)
		return
	case i < 0:
		ui.Text(c, "The thread has no change to "+path+".").TextColor(palette.muted).Margin(ui.Auto)
		return
	case !t.local():
		ui.Text(c, "The files are on "+t.env.Name+".").TextColor(palette.muted).Margin(ui.Auto)
		return
	}
	f := files[i]
	d := a.diffFor(t, f)
	switch {
	case d == nil:
		ui.Text(c, "Reading "+filepath.Base(f.path)+"…").TextColor(palette.muted).Margin(ui.Auto)
		return
	case d.DiffState == nil:
		ui.Text(c, "Not shown: "+d.unshown+".").TextColor(palette.muted).Margin(ui.Auto)
		return
	}
	d.Layout, d.Wrap = a.layout(), a.diffWrap
	if d.noHead != "" {
		// Both sides are the working file: show it once.
		d.Layout = editor.DiffUnified
		notice(c, "HEAD's side is unknown: "+d.noHead+".")
	} else {
		a.hunkBar(c, d.DiffState, func() { fire(map[string]any{"path": f.path, "remaining": len(d.Hunks())}) })
	}
	editor.DiffView(c.Key(f.path), d.DiffState).Grow(1).FillWidth()
}

// splitWidth is the least width side by side reads at: two columns of
// code, each wide enough for a line.
const splitWidth = 800

// splitFits reports whether there is room for the panel splitWidth wide
// beside the chat.
func (a *app) splitFits() bool {
	return a.roomBeside-minChatWidth >= splitWidth
}

// layout is the diff's layout as shown: the one chosen, but Unified
// while Split is short of room. The choice keeps, for when the room is
// back.
func (a *app) layout() editor.DiffLayout {
	if a.diffLayout == editor.DiffSplit && !a.splitFits() {
		return editor.DiffUnified
	}
	return a.diffLayout
}

// notice is a line across the panel saying why it does less than usual.
func notice(c *ui.Context, text string) {
	ui.Row(c).Padding(6, 8, 6, 14).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).
		Background(palette.amber.Alpha(0.08)).Children(func() {
		ui.Icon(c, icShieldAlert).FontSize(13).TextColor(palette.amber)
		ui.Text(c, text).FontSize(11.5).TextColor(palette.muted).Grow(1).Shrink(1)
	})
}

// hunkBar counts the file's changes, "2 of 5", steps between them, and
// marks the one in view reviewed, as the change's own button does. ⌘[
// and ⌘] step from the keyboard, and ⇧⌘↩ marks; marked follows a mark.
func (a *app) hunkBar(c *ui.Context, d *editor.DiffState, marked func()) {
	n := len(d.Hunks())
	i, _ := d.CurrentHunk()
	prev := func() { d.GoToHunk(max(i-1, 0)) }
	next := func() { d.GoToHunk(min(i+1, n-1)) }
	mark := func() {
		if i >= 0 {
			d.Accept(i)
			marked()
		}
	}
	if n > 0 {
		switch {
		case c.Shortcut(ui.Cmd, ui.KeyBracketLeft):
			prev()
		case c.Shortcut(ui.Cmd, ui.KeyBracketRight):
			next()
		case c.Shortcut(ui.Cmd|ui.Shift, ui.KeyEnter):
			mark()
		}
	}
	ui.Row(c).Padding(4, 8, 4, 14).Gap(4).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(palette.border).Children(func() {
		label := plural(n, "change")
		switch {
		case n == 0:
			label = "All changes reviewed"
		case i >= 0:
			label = fmt.Sprintf("%d of %d", i+1, n)
		}
		ui.Text(c, label).FontSize(11.5).TextColor(palette.muted).Grow(1)
		if ghost(c, "mark-hunk", false).Padding(3, 6).Disabled(n == 0).Label("Mark change reviewed").Children(func() {
			ui.Icon(c, icCheck).FontSize(12)
			ui.Text(c, "Mark reviewed").FontSize(11.5)
			kbd(c, "⇧⌘↩")
		}).Clicked() {
			mark()
		}
		if a.iconButton(c, "prev-hunk", icChevronUp, "Previous change  ⌘[", false).Padding(3).Disabled(n == 0).Clicked() {
			prev()
		}
		if a.iconButton(c, "next-hunk", icChevronD, "Next change  ⌘]", false).Padding(3).Disabled(n == 0).Clicked() {
			next()
		}
	})
}

// diffWidth is the panel's width: a share of the room beside the sidebar,
// wider side by side, and never so wide the chat has less than
// minChatWidth.
func (a *app) diffWidth() float32 {
	share, lo, hi := float32(0.42), float32(380), float32(520)
	if a.layout() == editor.DiffSplit {
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
