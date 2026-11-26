package main

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// project is the folder of the project the thread is under: the one a
// thread in a worktree was started from, else where it runs.
func (t thread) project() string { return cmp.Or(t.View.Project, t.View.Workspace) }

// A project is a workspace on one machine: the threads the sidebar shows
// under its tile.
type project struct {
	key     projectKey
	env     *client.Env
	threads []thread // the most recently updated first
	// noProject is whether it holds the threads started with no project,
	// each in a folder Glitched made for it under the scratch root.
	noProject   bool
	icon, color string // its mark, from marks.json; "" for one never settled
}

type projectKey struct {
	server    string // the server's label; "" on this machine
	workspace string
}

func keyOf(e *client.Env, workspace string) projectKey {
	if e != nil && e.Kind == client.Remote {
		return projectKey{e.Name, workspace}
	}
	return projectKey{workspace: workspace}
}

// machineKey is what a file keeps e's machine by: "" for this machine, and
// a server's engine ID, not its label, which may come to name another
// engine. A server whose engine hasn't answered yet has none, nor does
// a machine gone, nil.
func machineKey(e *client.Env) (string, bool) {
	if e == nil {
		return "", false
	}
	if e.Kind != client.Remote {
		return "", true
	}
	return e.ID.String(), !e.ID.IsZero()
}

// newProject is e's project at k, with no thread yet, and the mark drawn
// for its name: gather's projects and projectOf's alike.
func (a *app) newProject(e *client.Env, k projectKey) *project {
	p := &project{key: k, env: e, noProject: k == keyOf(nil, a.scratchRoot())}
	a.markProject(p)
	return p
}

// threadKey is the project t is in: its workspace's, but for a thread with
// no project, whose workspace is a folder of its own under the scratch
// root, where all of them are one project.
func (a *app) threadKey(t thread) projectKey {
	ws := t.project()
	added := slices.ContainsFunc(a.added, func(p addedProject) bool { return p.Server == "" && p.Workspace == ws })
	if t.local() && filepath.Dir(ws) == a.scratchRoot() && !added {
		ws = a.scratchRoot()
	}
	return keyOf(t.env, ws)
}

func (p *project) name() string {
	if p.noProject {
		return "No project"
	}
	return filepath.Base(p.key.workspace)
}

var (
	projectIcons  = []string{"terminal", "wind", "phone", "code", "global", "magic-stick-3", "fast-tier", "folder"}
	projectColors = []string{"#8b5cf6", "#0ea5e9", "#22c55e", "#f97316", "#ec4899", "#14b8a6", "#eab308"}
)

// mark is the project's icon and color, which marks.json keeps by its
// name, so that no two projects shown share an icon while icons go round,
// and the same workspace on two machines looks the same.
func (p *project) mark() (*ui.SVG, ui.Color) {
	icon, color := p.markNames()
	return appIconNamed(icon), ui.Hex(color)
}

// markNames are the project's icon, by name, and color, as #rrggbb: its
// mark, or for a name never settled its hash pick.
func (p *project) markNames() (string, string) {
	if p.noProject {
		return "chat-round-line", "#8e8e93"
	}
	if p.icon != "" {
		return p.icon, p.color
	}
	icon, color := hashMark(p.name())
	return projectIcons[icon], projectColors[color]
}

// draft is where New thread starts the next thread: a machine, and a
// workspace on it.
type draft struct {
	env       *client.Env // nil: this machine's
	workspace string      // "": no project, on this machine
}

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryWork
	entryRequest
	entryFailed
	entryReport
	entryError // why a run failed
	entryCompact
	entryPlan
	entryFork
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
	// at is when a message was sent or a reply's words came; zero for a
	// message not sent yet. ran is how long the turn ran, on its last
	// reply.
	at  time.Time
	ran time.Duration
	// retry is a turn's last reply, or its message when it said nothing,
	// when the turn failed or was stopped: there is something to ask
	// again.
	retry bool
	// A run's work: whether some of it is still going, and when it started
	// and ended, zero while it goes.
	working        bool
	started, ended time.Time
	// paused is what a run's last work says while the run waits on a
	// request.
	paused  string
	took    string
	work    []client.WorkStep
	plan    []api.PlanStep
	request *client.RequestEntry
	run     api.ID // the run a message, a reply or a work entry is of
	// reports are a run of children's reports, one card's.
	reports []client.ReportEntry
	// fork is where a fork goes on from, a fork entry's.
	fork *client.ForkEntry
	// turn is the run a turn's last reply is of, once the run ended: a
	// fork can go on from there.
	turn api.ID
}

// fileChange is a file the agent's edits name, when they last moved, and
// how many of them the agent says it made: one under way, failed or
// declined isn't, and a later word on one made doesn't count it again.
type fileChange struct {
	path  string
	stamp time.Time
	made  int
}

// entriesOf is what the timeline lists of a thread: its transcript, the
// messages on their way into the run, then those sent from here that it
// doesn't show yet, and the other commands sent from here that failed.
// Those that wait their turn are the queue's, over the composer.
func entriesOf(t thread) []entry {
	d := newDeriving(nil, nil, nil)
	if tr := t.Transcript; tr != nil {
		for _, e := range tr.Entries {
			d.add(e)
		}
		markPaused(d.es)
		for _, u := range tr.Queue {
			d.listed[u.Message] = true
			if !waits(u) {
				d.es = append(d.es, userEntry(u))
			}
		}
	}
	return append(d.es, pendingEntries(t.Pending, d.isListed)...)
}

// deriving derives a transcript's rows in order: the rows so far, and
// what the next entry's row depends on, over what was derived before it
// (base), which it leaves as it was.
type deriving struct {
	es         []entry
	n, baseN   map[string]int // entries so far by kind and run, for keys
	listed     map[api.ID]bool
	baseListed map[api.ID]bool
}

func newDeriving(es []entry, n map[string]int, listed map[api.ID]bool) *deriving {
	return &deriving{es: es, n: map[string]int{}, baseN: n, listed: map[api.ID]bool{}, baseListed: listed}
}

// key keys a run's entries of a kind by their order in it.
func (d *deriving) key(kind string, run api.ID) string {
	k := kind + "/" + run.String()
	d.n[k]++
	return k + "/" + strconv.Itoa(d.baseN[k]+d.n[k])
}

func (d *deriving) isListed(id api.ID) bool { return d.listed[id] || d.baseListed[id] }

func (d *deriving) add(e client.Entry) {
	switch e := e.(type) {
	case *client.UserEntry:
		d.listed[e.Message] = true
		d.es = append(d.es, userEntry(e))
	case *client.AssistantEntry:
		ae := entry{key: d.key("a", e.Run), kind: entryAssistant, text: e.Text, streaming: e.Streaming, at: e.At, ran: ranFor(e.End), retry: unfinished(e.End), run: e.Run}
		if e.End != nil {
			ae.turn = e.Run
		}
		d.es = append(d.es, ae)
	case *client.WorkEntry:
		d.es = append(d.es, workEntry(d.key("w", e.Run), e))
	case *client.PlanEntry:
		d.es = append(d.es, entry{key: d.key("p", e.Run), kind: entryPlan, plan: e.Steps, run: e.Run})
	case *client.RequestEntry:
		d.es = append(d.es, entry{key: "r/" + e.Request.String(), kind: entryRequest, request: e})
	case *client.ErrorEntry:
		d.es = append(d.es, entry{key: d.key("e", e.Run), kind: entryError, text: e.Error})
	case *client.CompactionEntry:
		// Only what it did shows: a compaction at work shows nothing.
		if !e.Compacted {
			return
		}
		d.es = append(d.es, entry{key: "k/" + e.Run.String(), kind: entryCompact, text: compactLabel(e), at: e.At})
	case *client.ForkEntry:
		d.es = append(d.es, entry{key: "fork", kind: entryFork, at: e.At, fork: e})
	case *client.ReportEntry:
		// One after another, they're one card, keyed by its first and
		// its order among the cards a run's report starts: a run that
		// asked twice can start two, the parent's text between them.
		if n := len(d.es); n > 0 && d.es[n-1].kind == entryReport {
			d.es[n-1].reports = addReport(d.es[n-1].reports, *e)
			break
		}
		d.es = append(d.es, entry{key: d.key("c/"+e.Child.String(), e.Run), kind: entryReport, reports: []client.ReportEntry{*e}})
	}
}

// pendingEntries are the rows of the commands sent from here: messages the
// transcript doesn't list yet, and other commands that failed.
func pendingEntries(pending []client.Pending, listed func(api.ID) bool) []entry {
	var es []entry
	for _, p := range pending {
		if failedTo(p) != "" {
			if p.Err != nil {
				es = append(es, entry{key: "f/" + p.Command.String(), kind: entryFailed, text: failure(p), failed: p})
			}
			continue
		}
		m, ok := p.Body.(api.MessageDispatch)
		if !ok || listed(p.Command) {
			continue
		}
		e := entry{key: "m/" + p.Command.String(), kind: entryUser, text: m.Text, note: "Sending…"}
		if p.Err != nil {
			e.note, e.failed = "Not sent: "+why(p.Err), p
		}
		es = append(es, e)
	}
	return es
}

// rows is a thread's timeline as the frame reads it: entriesOf's entries,
// the files their edits name, its turns, and the entries of the requests
// waiting on the user.
type rows struct {
	es    []entry
	files []fileChange
	turns []turnInfo
	asks  []int
	// fixed is how many of the rows are the transcript's, which keep
	// their places as the thread grows, while epoch stays the same.
	fixed int
	epoch uint64
	// stable is how many of the first rows change only as epoch does,
	// but for the children's reports a row holds.
	stable int
	// change is which of any thread's rows these are: it changes with them.
	change uint64
}

// rowsEpochs counts the rows derived from the start, by any thread's
// rowsCache, each an epoch of its own, and rowsChanges every change to
// them. Only the window's loop derives.
var rowsEpochs, rowsChanges uint64

// rowsLive is how many of a transcript's last entries are derived again
// on each change: a reply streams, and a run's work and requests change,
// at the end of the thread.
const rowsLive = 64

// A rowsCache is a thread's rows, derived in two parts so that a long
// thread costs a change, and a frame, what changes rather than all it
// holds. The settled part is the rows of the transcript's entries before
// its last rowsLive, each derived once, and all again when the transcript
// says one of them changed (Transcript.Edits) or its entries were
// replaced. The live part, the last entries and the queue, is derived
// again after each change, over the settled part. Every change to a
// transcript is posted to the window, whose changed counts it
// (app.posts); a command sent from here changes Pending at once, so its
// rows are derived each time and compared.
type rowsCache struct {
	tr    *client.Transcript
	posts uint64
	edits client.EditMark // the transcript's edits read
	// liveFiles are the files the live part's edits name, as files was
	// last derived with them over the settled part's.
	liveFiles []fileChange
	settled   settledRows
	paused    []int // the rows marked paused
	live      *deriving
	head      []entry // the transcript's rows, the queue's last
	headTs    int     // the head's turns
	tail      []entry // the commands'
	joined    bool    // rows is head and tail
	rows
}

// settledRows is the settled part of a rowsCache: how many entries it
// derived, the first and the last of them, to tell the entries replaced,
// and the first rows of rowsCache.es, with what the rows after them
// depend on. The turns and asks are the first of the rows'.
type settledRows struct {
	entries     int
	first, last client.Entry
	es          int
	n           map[string]int
	listed      map[api.ID]bool
	files       []fileChange
	at          map[string]int // files by path
	turns       int
	steps       int // the last turn's steps in the settled part
	asks        []int
}

// holds is whether src starts with the settled entries.
func (s *settledRows) holds(src []client.Entry) bool {
	return s.entries == 0 || len(src) >= s.entries && src[0] == s.first && src[s.entries-1] == s.last
}

// rowsOf is the thread's rows: entriesOf's, derived again only as the
// thread changes.
func (a *app) rowsOf(t thread) rows {
	c := a.rows[t.id()]
	if c == nil {
		c = &rowsCache{}
		a.rows[t.id()] = c
	}
	if c.live == nil || c.tr != t.Transcript || c.posts != a.posts {
		a.notePlace(t.id())
		c.derive(t.Transcript)
		c.posts, c.joined = a.posts, false
	}
	tail := pendingEntries(t.Pending, c.live.isListed)
	if !c.joined || !sameRows(tail, c.tail) {
		a.notePlace(t.id())
		c.tail, c.joined = tail, true
		c.es = append(c.head, tail...)
		rowsChanges++
		c.change = rowsChanges
		c.turns = c.turns[:c.headTs]
		for i, e := range tail {
			if e.kind == entryUser {
				c.turns = append(c.turns, turnInfo{entry: len(c.head) + i})
			}
		}
	}
	return c.rows
}

// derive derives the head of tr's rows: the settled part again if one of
// its entries changed, more of it as the thread grows, and the live part.
// The rows, turns and asks of both parts share their arrays: deriving the
// live part again writes past the settled part's.
func (c *rowsCache) derive(tr *client.Transcript) {
	var src []client.Entry
	var edited []int
	all, mark := false, client.EditMark{}
	if tr != nil {
		src = tr.Entries
		edited, all, mark = tr.Edits(c.edits)
	}
	s := &c.settled
	if c.tr != tr || !s.holds(src) || all || slices.ContainsFunc(edited, func(i int) bool { return i < s.entries }) {
		rowsEpochs++
		*c = rowsCache{rows: rows{epoch: rowsEpochs}}
		*s = settledRows{n: map[string]int{}, listed: map[api.ID]bool{}, at: map[string]int{}}
	}
	c.tr, c.edits = tr, mark
	for _, i := range c.paused {
		c.es[i].paused = ""
	}
	c.paused = c.paused[:0]
	turns := c.turns[:s.turns]
	// A report that follows the settled part's last, a report too, joins
	// its row, again each time: addReport keeps one report of a child.
	// Whether rows naming files moved into the settled part, whose files
	// they change.
	filesMoved := false
	if to := len(src) - rowsLive; to > s.entries {
		d := &deriving{es: c.es[:s.es], n: s.n, listed: s.listed}
		for _, e := range src[s.entries:to] {
			d.add(e)
		}
		filesMoved = len(addFiles(nil, map[string]int{}, d.es[s.es:])) > 0
		s.files = addFiles(s.files, s.at, d.es[s.es:])
		turns, s.steps = addTurns(turns, s.steps, d.es, s.es)
		s.asks = addAsks(s.asks, d.es, s.es)
		c.es, s.es, s.turns = d.es, len(d.es), len(turns)
		s.entries, s.first, s.last = to, src[0], src[to-1]
	}

	c.stable = s.es
	live := newDeriving(c.es[:s.es], s.n, s.listed)
	for _, e := range src[s.entries:] {
		live.add(e)
	}
	c.asks = addAsks(slices.Clip(s.asks), live.es, s.es)
	c.paused = pauseAsked(live.es, c.asks, c.paused)
	c.fixed = len(live.es)
	if tr != nil {
		for _, u := range tr.Queue {
			live.listed[u.Message] = true
			if !waits(u) {
				live.es = append(live.es, userEntry(u))
			}
		}
	}
	c.live, c.head = live, live.es
	// The files are the settled part's with the live part's over them,
	// derived again only as those change: a reply streaming in, a
	// command run or a report leaves them as they were.
	liveFiles := addFiles(nil, map[string]int{}, live.es[s.es:])
	if filesMoved || c.files == nil || !slices.Equal(liveFiles, c.liveFiles) {
		c.files, c.liveFiles = addFiles(slices.Clone(s.files), maps.Clone(s.at), live.es[s.es:]), liveFiles
	}
	c.turns, _ = addTurns(turns, s.steps, live.es, s.es)
	c.headTs = len(c.turns)
}

// pauseAsked marks each run's last work paused while a request of the run
// waits, as markPaused does, from the rows of those requests, asks, in
// order: each marks the run's work nearest before it, unless a request
// before it did, looking back no further than the run's message or a
// request of the run waiting too, whose work it is. It returns marked
// with the rows it marked.
func pauseAsked(es []entry, asks, marked []int) []int {
	for _, k := range asks {
		r := es[k].request
		message := "m/" + r.Run.String()
		for i := k - 1; i >= 0; i-- {
			e := &es[i]
			if e.kind == entryUser && e.key == message ||
				e.kind == entryRequest && e.request.Run == r.Run && e.request.State == api.RequestPending {
				break
			}
			if e.kind != entryWork || e.run != r.Run {
				continue
			}
			if e.paused == "" {
				e.paused = pausedFor[r.Kind != api.KindApproval]
				marked = append(marked, i)
			}
			break
		}
	}
	return marked
}

// addTurns adds the turns of es[from:] to turns, whose last has steps so
// far, and returns them and the last's steps.
func addTurns(turns []turnInfo, steps int, es []entry, from int) ([]turnInfo, int) {
	for i := from; i < len(es); i++ {
		switch e := es[i]; {
		case e.kind == entryUser:
			turns = append(turns, turnInfo{entry: i})
			steps = 0
		case len(turns) == 0:
		case e.kind == entryWork:
			steps += len(e.work)
			turns[len(turns)-1].steps = steps
		}
	}
	return turns, steps
}

// addAsks adds the rows of es[from:] of requests waiting on the user.
func addAsks(asks []int, es []entry, from int) []int {
	for i := from; i < len(es); i++ {
		if r := es[i].request; r != nil && r.State == api.RequestPending {
			asks = append(asks, i)
		}
	}
	return asks
}

// sameRows is whether two runs of the commands' rows show the same.
func sameRows(x, y []entry) bool {
	return slices.EqualFunc(x, y, func(x, y entry) bool {
		return x.key == y.key && x.text == y.text && x.note == y.note && x.failed.Terminal() == y.failed.Terminal()
	})
}

// pausedFor is what paused work says, by whether its run waits on the
// user's answer, to questions or for credentials, rather than an approval.
var pausedFor = map[bool]string{false: "Paused for approval", true: "Paused for your answer"}

// markPaused marks each run's last work entry paused while a request of
// the run waits.
func markPaused(es []entry) {
	asking := map[api.ID]string{}
	for i := len(es) - 1; i >= 0; i-- {
		e := &es[i]
		switch {
		case e.request != nil && e.request.State == api.RequestPending:
			asking[e.request.Run] = pausedFor[e.request.Kind != api.KindApproval]
		case e.kind == entryWork && asking[e.run] != "":
			e.paused = asking[e.run]
			delete(asking, e.run)
		}
	}
}

var messageNotes = map[client.MessageState]string{
	client.Queued:    "Queued",
	client.Held:      "Held",
	client.Cancelled: "Cancelled",
	client.Uncertain: "May not have reached the agent",
}

// waits is whether u waits its turn, queued or held: the queue over the
// composer lists it, not the timeline, until it goes in.
func waits(u *client.UserEntry) bool { return u.State == client.Queued || u.State == client.Held }

func userEntry(u *client.UserEntry) entry {
	note := messageNotes[u.State]
	if u.Reason != "" {
		note += ": " + u.Reason
	}
	return entry{key: "m/" + u.Message.String(), kind: entryUser, text: u.Text, note: note, at: u.At, retry: unfinished(u.End), run: u.Run()}
}

// ranFor is how long a turn that ended so ran, zero while it runs.
func ranFor(end *client.TurnEnd) time.Duration {
	if end == nil {
		return 0
	}
	return end.Took
}

// unfinished is whether a turn that ended so left something to ask again:
// a message of the user's it failed, was stopped, or was given up on.
func unfinished(end *client.TurnEnd) bool {
	return end != nil && end.Asked && end.Outcome != api.OutcomeCompleted
}

func workEntry(key string, w *client.WorkEntry) entry {
	e := entry{key: key, kind: entryWork, work: w.Items, working: w.Ended.IsZero(), took: "Working…", run: w.Run}
	if !e.working {
		e.started, e.ended = w.Started, w.Ended
		e.took = "Worked for " + w.Ended.Sub(w.Started).Round(time.Second).String()
	}
	return e
}

// changedFiles is every file the agent's edits in the thread name, in the
// order first named.
func changedFiles(es []entry) []fileChange { return addFiles(nil, map[string]int{}, es) }

// addFiles adds the files the edits of es name to files, which at indexes
// by path, and moves those named again.
func addFiles(files []fileChange, at map[string]int, es []entry) []fileChange {
	for _, e := range es {
		for _, w := range e.work {
			if w.Kind != "edit" || w.Path == "" {
				continue
			}
			i, ok := at[w.Path]
			if !ok {
				i, at[w.Path] = len(files), len(files)
				files = append(files, fileChange{path: w.Path})
			}
			for _, t := range []time.Time{w.Started, w.Ended} {
				if t.After(files[i].stamp) {
					files[i].stamp = t
				}
			}
			if w.Status == api.WorkCompleted {
				files[i].made++
			}
		}
	}
	return files
}

// gather groups every env's threads into projects, with the folders added
// on this machine, which may have none: this machine's first, then each
// server's, each machine's by name.
func (a *app) gather() []*project {
	byKey := map[envProject]*project{}
	at := func(e *client.Env, key projectKey) *project {
		k := envProject{e, key}
		p := byKey[k]
		if p == nil {
			p = a.newProject(e, key)
			byKey[k] = p
		}
		return p
	}
	for _, t := range a.recent() {
		if !deleting(t) {
			p := at(t.env, a.threadKey(t))
			p.threads = append(p.threads, t)
		}
	}
	envs := a.backend.Envs()
	for _, ap := range a.added {
		if i := slices.IndexFunc(envs, func(e *client.Env) bool { return ap.on(e) && a.backend.ShownBy(e) == e }); i >= 0 {
			at(envs[i], keyOf(envs[i], ap.Workspace))
		}
	}
	var all []*project
	for _, e := range envs {
		var ps []*project
		for k, p := range byKey {
			if k.env == e {
				ps = append(ps, p)
			}
		}
		slices.SortFunc(ps, func(x, y *project) int {
			return cmp.Or(cmp.Compare(x.name(), y.name()), cmp.Compare(x.key.workspace, y.key.workspace))
		})
		all = append(all, ps...)
	}
	return all
}

// envProject is a project of one env's.
type envProject struct {
	env *client.Env
	key projectKey
}

// recentThreads are every env's threads, the most recently updated first,
// as recent last sorted them.
type recentThreads struct {
	threads []thread
	// posts, envs and membership are what they were sorted after: the
	// changes posted to the window, the envs, and the threads that came
	// and went in them.
	posts      uint64
	envs       []*client.Env
	membership uint64
}

// recent is every env's threads, the most recently updated first. They are
// sorted again only after a change the manager posts, the only way a
// thread's recency changes, or as threads or envs come or go, not each
// frame: with 10,000 threads, sorting them took most of a frame.
func (a *app) recent() []thread {
	envs := a.backend.Envs()
	var membership uint64
	for _, e := range envs {
		membership += e.Membership
	}
	r := &a.recentThreads
	if r.threads != nil && r.posts == a.posts && r.membership == membership && slices.Equal(r.envs, envs) {
		return r.threads
	}
	var ts []thread
	for _, e := range envs {
		for _, th := range e.Threads {
			ts = append(ts, thread{e, th})
		}
	}
	slices.SortFunc(ts, newestFirst)
	*r = recentThreads{ts, a.posts, slices.Clone(envs), membership}
	return ts
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
	if d.env != nil && (!slices.Contains(envs, d.env) || a.duplicate(d.env)) {
		d = draft{} // its server was removed, or shows another's engine
	}
	if d.env == nil && len(envs) > 0 {
		d.env = envs[0]
	}
	if d.workspace == "" {
		last, _ := a.lastDraft(d.env)
		d.workspace = last.workspace
	}
	if d.workspace == "" {
		d.workspace = a.scratchRoot()
		if d.env != nil && d.env.Kind == client.Remote {
			d.workspace = homeDir()
		}
	}
	return thread{d.env, &client.Thread{View: api.Thread{Title: "New thread", Workspace: d.workspace}}}
}

// duplicate is whether e reaches the engine an env listed before it shows,
// so nothing starts on it.
func (a *app) duplicate(e *client.Env) bool { return a.backend.ShownBy(e) != e }

func (a *app) projectOf(t thread) *project {
	k := a.threadKey(t)
	for _, p := range a.projects {
		if p.key == k {
			return p
		}
	}
	return a.newProject(t.env, k)
}

func compactLabel(e *client.CompactionEntry) string {
	if e.PreTokens > 0 && e.PostTokens > 0 {
		return fmt.Sprintf("Auto-compacted before cache expiry · %s → %s tokens",
			contextTokens(float64(e.PreTokens)), contextTokens(float64(e.PostTokens)))
	}
	return "Auto-compacted before cache expiry"
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

// title is the thread's title, as the last rename sent from here makes
// it once it lands.
func title(t thread) string {
	s := t.View.Title
	for _, p := range t.Pending {
		if r, ok := p.Body.(api.ThreadRename); ok && p.Err == nil {
			s = r.Title
		}
	}
	return s
}

// deleting is whether a delete sent from here is on its way, of the
// thread or of one above it, as the engine deletes a thread's tree with
// it: the thread shows nowhere meanwhile, and comes back if the delete
// fails.
func deleting(t thread) bool {
	for range maxDepth + 1 {
		if slices.ContainsFunc(t.Pending, func(p client.Pending) bool {
			_, del := p.Body.(api.ThreadDelete)
			return del && p.Err == nil
		}) {
			return true
		}
		if t.env == nil || t.env.Threads[t.View.Parent] == nil {
			return false
		}
		t.Thread = t.env.Threads[t.View.Parent]
	}
	return false
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
	case api.QueueResume:
		return "resume the queue"
	case api.QueueEdit:
		return "edit the queued message"
	case api.QueueCancel:
		return "take the message out of the queue"
	case api.QueueMove:
		return "move the queued message"
	case api.QueueSteer:
		return "send the queued message now"
	case api.InboxSet:
		switch b.Disposition.State {
		case api.Settled:
			return "settle the thread"
		case api.Snoozed:
			return "snooze the thread"
		}
		return "move the thread back to the inbox"
	case api.ThreadRename:
		return "rename the thread"
	case api.ThreadDelete:
		return "delete the thread"
	case api.ThreadConfigure:
		return "change the thread's " + given(b)
	}
	return ""
}

// given names what a configure gives, the choice it was: one that failed
// has a row of its own, beside those of the thread's other choices that
// failed, and its Retry sets what it names and no more.
func given(c api.ThreadConfigure) string {
	var names []string
	if c.Model != nil {
		names = append(names, "model")
	}
	if c.Effort != nil {
		names = append(names, "effort")
	}
	if c.Fast != nil || c.Ultrafast != nil {
		name := "fast mode"
		if c.Ultrafast != nil && *c.Ultrafast {
			name = "ultrafast mode"
		}
		names = append(names, name)
	}
	if c.Access != nil {
		names = append(names, "access")
	}
	if c.Sweep != nil {
		names = append(names, "sweep schedule")
	}
	switch n := len(names); n {
	case 0:
		return "model and access"
	case 1:
		return names[0]
	default:
		return strings.Join(names[:n-1], ", ") + " and " + names[n-1]
	}
}

// failure says what a failed command of failedTo's tried, and why it
// failed.
func failure(p client.Pending) string { return "Couldn't " + failedTo(p) + ": " + why(p.Err) }

// why is what a command's error tells the user: a refusal's reason, without
// the engine's prefix.
func why(err error) string {
	if rej := (*api.RejectedError)(nil); errors.As(err, &rej) {
		return rej.Reason
	}
	return err.Error()
}

// failed is whether a command sent from here in the thread failed.
func failed(t thread) bool {
	return slices.ContainsFunc(t.Pending, func(p client.Pending) bool { return p.Err != nil })
}

// select_ shows the thread in the chat, read.
func (a *app) select_(t thread) {
	a.selected, a.dockJump, a.chosen, a.asked, a.revealing = t.id(), false, api.ID{}, nil, api.ID{}
	a.follow()
	if a.router.Path() != "/" {
		a.router.Push("/")
	}
}

// revealRow says, once, that the sidebar's row of the thread id is to
// scroll into view: while it is the thread shown, opened to be revealed.
func (a *app) revealRow(id api.ID) bool {
	if id != a.revealing || id != a.selected {
		return false
	}
	a.revealing = api.ID{}
	return true
}

// choose shows the thread a person chose, its composer taking the focus
// (composerInput).
func (a *app) choose(t thread) {
	if b, ok := a.remoteThread(t); ok {
		t = b
	}
	a.select_(t)
	a.chosen = t.id()
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
	// Open before close: the close may make room among the cached
	// transcripts by the one about to show.
	if want.env != nil {
		a.backend.OpenThread(want.env, want.id)
	}
	if a.opened.env != nil {
		a.backend.CloseThread(a.opened.env, a.opened.id)
	}
	a.opened = want
}

// changed runs after each change the manager posts: the shown thread's
// transcript stays open, and each turn that ended since is told of once.
func (a *app) changed() {
	a.posts++
	a.follow()
	a.emitThreadChanges()
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

// expandHome is a path written from the home directory, as "~/Projects/x",
// in full.
func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(homeDir(), rest)
	}
	return path
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
