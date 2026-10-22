package main

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"glitched/internal/api"
	"glitched/internal/client"
)

// The app shows the threads of every engine the client manager reaches:
// this machine's, embedded when none runs, and the servers in Settings.
// That state is the manager's, changed only in what it posts to the
// window; the views read it through the shapes here.

type status int

const (
	statusIdle status = iota
	statusWorking
	statusCompleted
	statusApproval
)

// A thread is one of an env's threads, or the draft New thread opened,
// which has no ID: no engine has it until its first message creates it.
type thread struct {
	env *client.Env // nil for a draft while no engine is reached
	*client.Thread
}

func (t thread) id() api.ID { return t.View.ID }

// local is whether the thread's workspace is on this machine, where the
// terminal, the files and the diffs are.
func (t thread) local() bool { return t.env == nil || t.env.Kind == client.Local }

// A project is a workspace on one machine: the threads the sidebar shows
// under its tile.
type project struct {
	key     projectKey
	env     *client.Env
	threads []thread // the most recently updated first
}

type projectKey struct {
	// server is the server's engine, or its label until it first answers;
	// "" on this machine. A project keeps its key when another label comes
	// to show the engine.
	server    string
	workspace string
}

func keyOf(e *client.Env, workspace string) projectKey {
	switch {
	case e == nil || e.Kind == client.Local:
		return projectKey{workspace: workspace}
	case e.ID.IsZero():
		return projectKey{e.Name, workspace}
	}
	return projectKey{e.ID.String(), workspace}
}

func (p *project) name() string { return filepath.Base(p.key.workspace) }

var (
	projectIcons  = []*ui.SVG{icTerminal, icWind, icPhone, icCode, icGlobe, icSparkles, icZap, icFolder}
	projectColors = []ui.Color{ui.Hex("#8b5cf6"), ui.Hex("#0ea5e9"), ui.Hex("#22c55e"), ui.Hex("#f97316"), ui.Hex("#ec4899"), ui.Hex("#14b8a6"), ui.Hex("#eab308")}
)

// mark is the project's icon and color, picked by its name so that it
// keeps them, and the same workspace on two machines looks the same.
func (p *project) mark() (*ui.SVG, ui.Color) {
	h := fnv.New32a()
	h.Write([]byte(p.name()))
	n := h.Sum32()
	return projectIcons[n%uint32(len(projectIcons))], projectColors[n/7%uint32(len(projectColors))]
}

// draft is where New thread starts the next thread: a machine, and a
// workspace on it.
type draft struct {
	env       *client.Env // nil: this machine's
	workspace string      // "": the home directory
}

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryWork
	entryChanges
	entryRequest
	entryFailed
)

// An entry is a row of the timeline. Its key stays the same while it
// changes and as rows come before it.
type entry struct {
	key       string
	kind      entryKind
	text      string
	streaming bool
	// note is under a message: queued, sending, cancelled.
	note string
	// failed is the command a failed entry, or a message whose send
	// failed, stands for.
	failed client.Pending
	// A run's work: whether some of it is still going, and for how long it
	// went.
	working bool
	took    string
	work    []client.WorkStep
	files   []fileChange
	request *client.RequestEntry
}

type fileChange struct {
	path string
	add  int
	del  int
}

// entriesOf is what the timeline lists of a thread: its transcript, the
// messages waiting their turn, then those sent from here that it doesn't
// show yet, and the other commands sent from here that failed.
func entriesOf(t thread) []entry {
	var es []entry
	listed := map[api.ID]bool{}
	if tr := t.Transcript; tr != nil {
		// A run's entries of a kind are keyed by their order in it.
		n := map[string]int{}
		key := func(kind string, run api.ID) string {
			k := kind + "/" + run.String()
			n[k]++
			return fmt.Sprintf("%s/%d", k, n[k])
		}
		for _, e := range tr.Entries {
			switch e := e.(type) {
			case *client.UserEntry:
				listed[e.Message] = true
				es = append(es, userEntry(e))
			case *client.AssistantEntry:
				es = append(es, entry{key: key("a", e.Run), kind: entryAssistant, text: e.Text, streaming: e.Streaming})
			case *client.WorkEntry:
				es = append(es, workEntry(key("w", e.Run), e))
			case *client.RequestEntry:
				es = append(es, entry{key: "r/" + e.Request.String(), kind: entryRequest, request: e})
			}
		}
		for _, u := range tr.Queue {
			listed[u.Message] = true
			es = append(es, userEntry(u))
		}
	}
	for _, p := range t.Pending {
		if what := failedTo(p); what != "" {
			if p.Err != nil {
				es = append(es, entry{key: "f/" + p.Command.String(), kind: entryFailed, text: "Couldn't " + what + ": " + p.Err.Error(), failed: p})
			}
			continue
		}
		m, ok := p.Body.(api.MessageDispatch)
		if !ok || listed[p.Command] {
			continue
		}
		e := entry{key: "m/" + p.Command.String(), kind: entryUser, text: m.Text, note: "Sending…"}
		if p.Err != nil {
			e.note, e.failed = "Not sent: "+p.Err.Error(), p
		}
		es = append(es, e)
	}
	return es
}

var messageNotes = map[client.MessageState]string{
	client.Queued:    "Queued",
	client.Held:      "Held",
	client.Cancelled: "Cancelled",
	client.Uncertain: "May not have reached the agent",
}

func userEntry(u *client.UserEntry) entry {
	note := messageNotes[u.State]
	if u.Reason != "" {
		note += ": " + u.Reason
	}
	return entry{key: "m/" + u.Message.String(), kind: entryUser, text: u.Text, note: note}
}

func workEntry(key string, w *client.WorkEntry) entry {
	e := entry{key: key, kind: entryWork, work: w.Items, working: w.Ended.IsZero(), took: "Working…"}
	if !e.working {
		e.took = "Worked for " + w.Ended.Sub(w.Started).Round(time.Second).String()
	}
	return e
}

// gather groups every env's threads into projects: this machine's first,
// then each server's, each machine's by name.
func (a *app) gather() []*project {
	var all []*project
	for _, e := range a.backend.Envs() {
		byKey := map[projectKey]*project{}
		var ps []*project
		for _, th := range e.Threads {
			k := keyOf(e, th.View.Workspace)
			p := byKey[k]
			if p == nil {
				p = &project{key: k, env: e}
				byKey[k] = p
				ps = append(ps, p)
			}
			p.threads = append(p.threads, thread{e, th})
		}
		slices.SortFunc(ps, func(x, y *project) int {
			return cmp.Or(cmp.Compare(x.name(), y.name()), cmp.Compare(x.key.workspace, y.key.workspace))
		})
		for _, p := range ps {
			slices.SortFunc(p.threads, newestFirst)
		}
		all = append(all, ps...)
	}
	return all
}

func newestFirst(x, y thread) int {
	return cmp.Or(y.View.UpdatedAt.Compare(x.View.UpdatedAt), cmp.Compare(y.id().String(), x.id().String()))
}

func (a *app) find(id api.ID) (thread, bool) {
	if id.IsZero() {
		return thread{}, false
	}
	for _, e := range a.backend.Envs() {
		if th := e.Threads[id]; th != nil {
			return thread{e, th}, true
		}
	}
	return thread{}, false
}

// current is the thread the chat shows: the one selected, or the draft
// when none is, or it's gone.
func (a *app) current() thread {
	if t, ok := a.find(a.selected); ok {
		return t
	}
	envs := a.backend.Envs()
	d := a.fresh
	if d.env != nil && d.env.Same != nil {
		d.env = d.env.Same // another env shows its engine now
	}
	if d.env != nil && !slices.Contains(envs, d.env) {
		d = draft{} // its server was removed
	}
	if d.env == nil && len(envs) > 0 {
		d.env = envs[0]
	}
	if d.workspace == "" {
		d.workspace = homeDir()
	}
	return thread{d.env, &client.Thread{View: api.Thread{Title: "New thread", Workspace: d.workspace}}}
}

func (a *app) projectOf(t thread) *project {
	k := keyOf(t.env, t.View.Workspace)
	for _, p := range a.projects {
		if p.key == k {
			return p
		}
	}
	return &project{key: k, env: t.env}
}

func (a *app) statusOf(t thread) status {
	v := t.View
	switch {
	case len(v.Requests) > 0 || v.Run == api.RunWaiting:
		return statusApproval
	case v.Working():
		return statusWorking
	case a.backend.Unread(t.Thread):
		return statusCompleted
	}
	return statusIdle
}

// disposition is the thread's inbox state, as the last change sent from
// here makes it once it lands.
func disposition(t thread) api.Disposition {
	d := t.View.Disposition
	for _, p := range t.Pending {
		if s, ok := p.Body.(api.InboxSet); ok && p.Err == nil {
			d = s.Disposition
		}
	}
	return d
}

// failedTo is what a command tried, for those the timeline shows as a row
// of their own when they fail; "" for messages and answers, whose failure
// shows on the message and on the request's card.
func failedTo(p client.Pending) string {
	switch b := p.Body.(type) {
	case api.ThreadCreate:
		return "create the thread"
	case api.TurnInterrupt:
		return "stop the agent"
	case api.InboxSet:
		switch b.Disposition.State {
		case api.Settled:
			return "settle the thread"
		case api.Snoozed:
			return "snooze the thread"
		}
		return "move the thread back to the inbox"
	}
	return ""
}

// failed is whether a command sent from here in the thread failed.
func failed(t thread) bool {
	return slices.ContainsFunc(t.Pending, func(p client.Pending) bool { return p.Err != nil })
}

// select_ shows the thread in the chat, read.
func (a *app) select_(t thread) {
	a.selected = t.id()
	a.follow()
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
}

// opened is the thread whose transcript the manager follows.
type opened struct {
	env *client.Env
	id  api.ID
}

// follow keeps the shown thread's transcript open, and no other.
func (a *app) follow() {
	var want opened
	if t, ok := a.find(a.selected); ok {
		want = opened{t.env, t.id()}
	}
	if want == a.opened {
		return
	}
	if a.opened.env != nil {
		a.backend.CloseThread(a.opened.env, a.opened.id)
	}
	if want.env != nil {
		a.backend.OpenThread(want.env, want.id)
	}
	a.opened = want
}

// changed runs after each change the manager posts: the shown thread's
// transcript stays open, and each turn that ended since is told of once.
func (a *app) changed() {
	a.follow()
	for _, e := range a.backend.Envs() {
		for id, th := range e.Threads {
			var ended time.Time
			if th.View.LastTurn != nil {
				ended = th.View.LastTurn.Ended
			}
			seen, known := a.ended[id]
			a.ended[id] = ended
			if !known || !ended.After(seen) {
				continue
			}
			a.notifyFinished(thread{e, th})
		}
	}
}

// stateDir is the local engine's state directory, the CLI's default:
// ~/.glitched, or GLITCHED_DIR.
func stateDir() string {
	if d := os.Getenv("GLITCHED_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".glitched")
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// ago is how long before now t was, as the sidebar shows it.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	}
	return fmt.Sprintf("%dmo", int(d.Hours()/24/30))
}

// snoozeLabel is when a snoozed thread comes back, as its row shows it.
func snoozeLabel(until, now time.Time) string {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch days := int(until.Sub(today).Hours() / 24); {
	case days < 1:
		return until.Format("15:04")
	case days < 2:
		return "Tomorrow"
	case days < 7:
		return until.Format("Mon")
	}
	return until.Format("Jan 2")
}
