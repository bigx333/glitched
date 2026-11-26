package engine

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"reflect"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
)

// Decide checks a client command against the thread and returns what to
// commit. A command the tables refuse comes back as *api.RejectedError.
func Decide(t Thread, c api.Command, now time.Time) ([]Event, []Effect, error) {
	return DecideWith(t, c, Env{}, now)
}

// Places finds the machine this engine holds a link to under label: ok is
// whether there is one, and err that the list couldn't be read, its text
// the reason a remote child's create is refused.
type Places func(label string) (at api.Remote, ok bool, err error)

// Links finds a link this engine minted by its id, as tokens.json has it
// now, its access narrowed to this engine's setting for children from
// other machines. An error is that it may start or run nothing, as its
// machine was removed or the setting is off: its text is the reason.
type Links func(id api.ID) (api.Link, error)

// Env is what a decision reads besides its thread, as the engine has it
// at the commit.
type Env struct {
	// Places finds the machine a remote child is placed on; nil finds none.
	Places Places
	// Ceiling is the most the thread may run at now when it is a link's
	// thread or under one: the lowest of that thread's origin's access,
	// its link's and this engine's setting, as they are at the commit. An
	// error lets it run nothing. Nil caps a thread of this engine's own
	// at nothing, and lets a link's thread run nothing.
	Ceiling func() (api.Access, error)
	// Engine is this engine's id, and Line a child's create's parent and
	// those above it, as CheckChild had them: what a remote child's
	// machine is told of where it comes from.
	Engine api.ID
	Line   []Thread
}

// DecideWith is Decide in env.
func DecideWith(t Thread, c api.Command, env Env, now time.Time) ([]Event, []Effect, error) {
	now = now.UTC().Round(0)
	if c.ID.IsZero() || c.Target.IsZero() {
		return nil, nil, rejected("a command needs an id and a target")
	}
	if !c.Expires.IsZero() && now.After(c.Expires) {
		return nil, nil, api.ErrExpired
	}
	if t.Deleted {
		return nil, nil, api.ErrNotFound
	}
	if _, creating := c.Body.(api.ThreadCreate); !creating && !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	if c.Revision != 0 && c.Revision != t.Revision {
		return nil, nil, api.ErrRevision
	}
	d := &decision{t: t, thread: c.Target, at: now, env: env}
	if c.Caller != nil {
		switch b := c.Body.(type) {
		case api.MessageDispatch:
			if b.Mode != api.AfterActive && b.Mode != api.SteerActive {
				return nil, nil, rejected("an agent's message to a thread under its own goes after its run or into it")
			}
		case api.TurnInterrupt:
			if b.Drop {
				return nil, nil, rejected("an agent can't drop the messages waiting in a thread")
			}
			if r := t.Live(); r != nil && r.State == RunUncertain {
				return nil, nil, rejected("the thread's run didn't answer its stop, and only the user can end it")
			}
		case api.InboxSet:
			if !c.Caller.Run.IsZero() || b.Disposition.State != api.Settled {
				return nil, nil, rejected("an agent can only message, stop or answer the question of a thread under its own")
			}
		case api.RequestRespond:
			approval := rejected("an agent can answer only a question of a thread under its own: approvals and credentials are the user's")
			if r := t.Remote; r != nil {
				if !slices.Contains(r.Questions, b.Request) {
					if slices.Contains(r.Pending, b.Request) {
						return nil, nil, approval
					}
					return nil, nil, api.ErrNotFound
				}
			} else if i := t.requestAt(b.Request); i < 0 {
				return nil, nil, api.ErrNotFound
			} else if t.Requests[i].Kind != api.KindQuestion {
				return nil, nil, approval
			}
		default:
			return nil, nil, rejected("an agent can only message, stop or answer the question of a thread under its own")
		}
		d.by = c.Caller.Thread
	}
	var err error
	if t.Remote != nil {
		err = d.placeholder(c.ID, c.Body)
	} else {
		err = d.local(c.ID, c.Body)
	}
	if err == nil {
		err = d.settle()
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		err = rejected(te.Error())
	}
	if err != nil {
		return nil, nil, err
	}
	return d.events, d.effects, nil
}

// placeholder decides b, the intent of command id, on a remote child's
// placeholder, which never runs an agent here. What b asks of the child
// reaches its machine as an effect, which sends it there as a command
// frozen now: a settle or a delete settles the child there, where it
// keeps its transcript, a stop stops its run, a message is sent there and
// queues nothing here, its parent's agent's answer to a question it asks
// there is sent there, and a narrower access narrows it there. A
// placeholder whose create was never tried has nothing there to settle.
// The machine's person who took the child over there has it from then
// on: an agent's message or stop is refused, as for a local child the
// user took over.
func (d *decision) placeholder(id api.ID, b api.Intent) error {
	r := d.t.Remote
	theirs := !d.by.IsZero() && r.Taken
	switch b := b.(type) {
	case api.InboxSet:
		was := d.t.settled()
		if was && b.Disposition.State != api.Settled && r.State == RemoteCreating {
			return rejected("this child was settled before its machine started it, so it stays settled; start another")
		}
		if err := d.inbox(b); err != nil {
			return err
		}
		if !was && b.Disposition.State == api.Settled && r.tried() {
			d.stopThere(id, api.InboxSet{Disposition: api.Disposition{State: api.Settled}, Fence: d.t.Remote.Settles})
		}
		return nil
	case api.ThreadDelete:
		if err := d.delete(b); err != nil {
			return err
		}
		if r.tried() {
			d.stopThere(id, api.InboxSet{Disposition: api.Disposition{State: api.Settled}, Fence: r.Settles + 1})
		}
		return nil
	case api.ThreadRename:
		return d.rename(b)
	case api.ThreadConfigure:
		before := d.t.Access
		if err := d.configure(b); err != nil {
			return err
		}
		if _, access := b.Onto(d.t.Model, before); access < before && d.reaches() {
			d.effects = append(d.effects, RemoteConfigure{Engine: r.Engine, Link: r.Link, Command: d.there(id, "configure", api.ThreadConfigure{Access: &access})})
		}
		return nil
	case api.TurnInterrupt:
		if theirs {
			return rejected("the person on this child's machine took it over there: its last message or stop was theirs, so it's theirs to stop")
		}
		if r.State == RemoteCreating && d.t.settled() {
			return nil
		}
		d.effects = append(d.effects, RemoteStop{Engine: r.Engine, Link: r.Link, Command: d.there(id, "stop", api.TurnInterrupt{Tree: b.Tree})})
		return nil
	case api.MessageDispatch:
		switch {
		case r.State == RemoteLostState:
			return rejected("this child's machine no longer has it here: " + r.Lost)
		case r.State == RemoteCreating && d.t.settled():
			return rejected("this child was settled before its machine started it, so it takes no message; start another")
		case theirs:
			return rejected("the person on this child's machine took it over there: its last message or stop was theirs, so it's theirs to steer")
		case b.Auto || !b.Replaces.IsZero() || b.Mode != api.AfterActive && b.Mode != api.SteerActive && b.Mode != api.StartNow:
			return rejected("a child on another machine takes a message after its run, or into it")
		}
		if b.Mode == api.StartNow {
			b.Mode = api.AfterActive
		}
		if d.t.settled() {
			if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
				return err
			}
		}
		d.effects = append(d.effects, RemoteSend{Engine: r.Engine, Link: r.Link, Settles: d.t.Remote.Settles, Command: d.there(id, "send", api.MessageDispatch{Text: b.Text, Mode: b.Mode, Fence: d.t.Remote.Settles})})
		return nil
	case api.RequestRespond:
		switch {
		case d.by.IsZero():
			return rejected("this child runs on another machine, and its person answers what it asks there; here only its parent's agent answers its question")
		case !slices.Contains(r.Questions, b.Request):
			return rejected("this child's machine has no question of that id open; its approvals and credentials are answered there")
		case r.State == RemoteLostState:
			return rejected("this child's machine no longer has it here: " + r.Lost)
		case d.t.settled():
			return rejected("this child is settled, so it takes no answer")
		case theirs:
			return rejected("the person on this child's machine took it over there: its last message or stop was theirs, so it's theirs to answer")
		}
		d.effects = append(d.effects, RemoteSend{Engine: r.Engine, Link: r.Link, Settles: r.Settles, Command: d.there(id, "answer", api.RequestRespond{Request: b.Request, Answer: b.Answer})})
		return nil
	}
	return rejected("this child runs on another machine, and runs nothing here")
}

// reaches is whether what the placeholder is asked still reaches its
// child: its machine may have it, or will.
func (d *decision) reaches() bool {
	r := d.t.Remote
	return r.State != RemoteLostState && (r.State != RemoteCreating || !d.t.settled())
}

// there is the command the placeholder's child's machine is sent for
// b, of command id: its id is of id's, the placeholder's and what, so
// two placeholders a command takes on one machine send two commands.
func (d *decision) there(id api.ID, what string, b api.Intent) api.Command {
	return api.Command{ID: derive(id, d.t.ID.String()+"/"+what), Target: derive(d.t.ID, "remote"), Body: b}
}

// stopThere has the placeholder's child settled or stopped there with
// b. While the machine never answered the create its first try sent,
// that create goes first, made settled under an id of its own: the
// machine either makes the child settled, so it never starts, or finds
// the child the first create made and takes b.
func (d *decision) stopThere(id api.ID, b api.Intent) {
	r := d.t.Remote
	s := RemoteStop{Engine: r.Engine, Link: r.Link, Command: d.there(id, "stop", b)}
	if r.Thread.IsZero() && r.Create != nil {
		c := *r.Create
		if create, ok := c.Body.(api.ThreadCreate); ok {
			if d.t.Access < create.Access {
				access := d.t.Access
				s.Narrow = &api.Command{ID: derive(id, d.t.ID.String()+"/narrow"), Target: c.Target, Body: api.ThreadConfigure{Access: &access}}
				create.Access = access
			}
			create.Settled = true
			c.ID, c.Body = derive(c.ID, "settled"), create
			s.Create = &c
		}
	}
	d.effects = append(d.effects, s)
}

// local decides b, the intent of command id, on a thread that runs here.
func (d *decision) local(id api.ID, b api.Intent) error {
	var err error
	switch b := b.(type) {
	case api.ThreadCreate:
		err = d.create(id, b)
	case api.MessageDispatch:
		err = d.dispatch(id, b)
	case api.TurnInterrupt:
		err = d.interrupt(b.Drop)
	case api.RequestRespond:
		err = d.respond(b)
	case api.InboxSet:
		err = d.inbox(b)
	case api.ThreadRename:
		err = d.rename(b)
	case api.ThreadDelete:
		err = d.delete(b)
	case api.ThreadConfigure:
		err = d.configure(b)
	case api.QueueResume:
		err = d.resume()
	case api.QueueEdit:
		err = d.edit(b)
	case api.QueueCancel:
		err = d.cancel(b)
	case api.QueueMove:
		err = d.move(b)
	case api.QueueSteer:
		err = d.steer(b)
	case api.ResumeCancel:
		err = d.cancelResume(b)
	case api.WakeDismiss:
		err = d.dismissWake(b)
	default:
		err = rejected("unknown intent")
	}
	return err
}

// TakesTree is whether b on a thread applies to every thread under it
// too, in the same commit, as the store commits it: settling a thread
// settles its tree, deleting it deletes its tree, and a configure that
// gives an access narrows its tree to it.
func TakesTree(b api.Intent) bool {
	switch b := b.(type) {
	case api.ThreadDelete:
		return true
	case api.InboxSet:
		return b.Disposition.State == api.Settled
	case api.ThreadConfigure:
		return b.Access != nil
	case api.TurnInterrupt:
		return b.Tree
	}
	return false
}

// Below is what b on a thread decides on t, a thread under it, when b
// takes the tree: a configure narrows t's access to the one it gives, and
// leaves t's model as it is. A stop of the tree stops a remote child's
// tree on its machine too.
func Below(b api.Intent, t Thread) api.Intent {
	if c, ok := b.(api.ThreadConfigure); ok {
		var below api.ThreadConfigure
		if c.Access != nil {
			below.Access = new(min(t.Access, *c.Access))
		}
		return below
	}
	if i, ok := b.(api.TurnInterrupt); ok {
		i.Tree = t.Remote != nil
		return i
	}
	return b
}

// CheckConfigure checks c on t, a child thread, against parent, the
// thread it's under: the access c leaves t at can't be wider than its
// parent's.
func CheckConfigure(parent, t Thread, c api.ThreadConfigure) error {
	if _, access := c.Onto(t.Model, t.Access); parent.Exists() && !parent.Deleted && access > parent.Access {
		return rejected("a child thread's access can't be wider than its parent's")
	}
	return nil
}

// ErrRefused is an observation the tables refuse: it arrived after the
// thread moved on, so it records nothing.
var ErrRefused = errors.New("engine: observation refused")

// Observe records what an agent or the adapter did. An observation the
// tables refuse returns ErrRefused.
func Observe(t Thread, o Observation, now time.Time) ([]Event, []Effect, error) {
	return ObserveWith(t, o, Env{}, now)
}

// ObserveWith is Observe in env.
func ObserveWith(t Thread, o Observation, env Env, now time.Time) ([]Event, []Effect, error) {
	if !t.Exists() {
		return nil, nil, api.ErrNotFound
	}
	if t.Deleted {
		// What the agent did after, as an effect's leftover, records
		// nothing on a thread no one can read.
		return nil, nil, fmt.Errorf("%w: %T: the thread was deleted", ErrRefused, o)
	}
	d := &decision{t: t, thread: t.ID, at: now.UTC().Round(0), env: env}
	err := d.observe(o)
	if err == nil {
		err = d.settle()
	}
	if te := (*TransitionError)(nil); errors.As(err, &te) {
		return nil, nil, fmt.Errorf("%w: %T: %w", ErrRefused, o, err)
	}
	if err != nil {
		return nil, nil, err
	}
	return d.events, d.effects, nil
}

type decision struct {
	t       Thread
	thread  api.ID
	at      time.Time
	events  []Event
	effects []Effect
	resumed bool   // the user resumed the queue: settle starts the wakes after a stop
	by      api.ID // the thread whose agent sent the command; zero for the user
	env     Env
	prior   time.Time
}

// ceiling is the most the thread may run at now: any access for a
// thread of this engine's own line, and for a link's thread or one under
// it, the env's ceiling. ok is false when it may run at none, as the
// link's machine was removed.
func (d *decision) ceiling() (c api.Access, ok bool) {
	if d.env.Ceiling == nil {
		return api.AccessFull, d.t.Origin == nil
	}
	c, err := d.env.Ceiling()
	if err != nil {
		return 0, false
	}
	if d.t.Origin != nil {
		c = min(c, d.t.Origin.Access)
	}
	return c, true
}

// under is whether access is within the thread's ceiling now.
func (d *decision) under(access api.Access) bool {
	c, ok := d.ceiling()
	return ok && access <= c
}

// runs is whether the thread may start a turn now: a link's thread once
// its worktree is made, and it or one under it while its access is
// within its ceiling, so a message over a ceiling lowered since waits
// for a narrower access.
func (d *decision) runs() bool {
	return (d.t.Origin == nil || d.t.Workspace != "") && d.under(d.t.Access)
}

func (d *decision) emit(b api.EventBody) error {
	e := Event{Thread: d.thread, At: d.at, Body: b}
	t, err := apply(d.t, e)
	if err != nil {
		return err
	}
	d.t, d.events = t, append(d.events, e)
	return nil
}

func rejected(reason string) error { return &api.RejectedError{Reason: reason} }

// create makes the thread, and with a text, queues it as the first
// message and starts its turn in the same commit, under the command's ID,
// so a crash leaves both or neither.
func (d *decision) create(id api.ID, c api.ThreadCreate) error {
	if c.Access > api.AccessFull {
		return rejected("unknown access level")
	}
	if c.Role != api.RoleDefault && c.Role != api.RoleManager {
		return rejected("unknown thread role")
	}
	if c.Role == api.RoleManager && (!c.Parent.IsZero() || c.Origin != nil || c.Remote != "") {
		return rejected("only a top-level thread can be a manager")
	}
	if c.Sweep != nil && c.Sweep.IntervalSeconds < 0 {
		return rejected("sweep interval must be non-negative")
	}
	if c.Sweep != nil && c.Sweep.IntervalSeconds > 0 && c.Role != api.RoleManager {
		return rejected("only a manager thread can schedule sweeps")
	}
	if !c.Run.IsZero() && c.Parent.IsZero() {
		return rejected("a run starts only a child thread")
	}
	if c.Origin != nil {
		return d.createFromLink(id, c)
	}
	if c.Worktree != nil {
		return rejected("this engine makes no worktrees")
	}
	if (c.Project == "") != (c.Branch == "") {
		return rejected("a thread in a worktree has both its project and its branch")
	}
	if c.Fork != nil && c.Forked == nil {
		return rejected("this engine forks no threads")
	}
	if c.Remote != "" {
		return d.placeRemote(id, c)
	}
	if err := d.emit(api.ThreadCreated{Workspace: c.Workspace, Project: c.Project, Branch: c.Branch, Title: c.Title, Model: c.Model, Access: c.Access, Parent: c.Parent, Fork: c.Forked, Role: c.Role, Sweep: normalizeSweep(c.Sweep)}); err != nil {
		return err
	}
	if c.Text == "" {
		return nil
	}
	if err := d.emit(api.MessageQueued{Message: id, Text: c.Text, Mode: api.StartNow, By: c.Parent}); err != nil {
		return err
	}
	return d.admit(id)
}

// createFromLink creates a thread a link's create asked for, as the host
// resolved it: in the checkout Project, from the commit Worktree.Base,
// with its origin's link stamped. It holds its first message until its
// worktree is made, which its own effect does after the commit, so a
// crash leaves the thread to finish, and a settle in between leaves it
// nothing to start.
func (d *decision) createFromLink(id api.ID, c api.ThreadCreate) error {
	switch {
	case c.Origin.Link.IsZero():
		return rejected("a thread from another machine comes through a link")
	case !c.Parent.IsZero() || !c.Run.IsZero() || c.Fork != nil || c.Remote != "" || c.Workspace != "" || c.Branch != "":
		return rejected("a thread from another machine is a thread of its own here, in a worktree this engine makes")
	case !c.Settled && (c.Project == "" || c.Worktree == nil || c.Worktree.Base == ""):
		return rejected("a thread from another machine needs the checkout and the commit its worktree is made from")
	case strings.TrimSpace(c.Text) == "":
		return rejected("a thread from another machine needs its task")
	}
	origin := *c.Origin
	if err := d.emit(api.ThreadCreated{Project: c.Project, Title: c.Title, Model: c.Model, Access: c.Access, Origin: &origin}); err != nil {
		return err
	}
	if err := d.emit(api.MessageQueued{Message: id, Text: c.Text, Mode: api.StartNow, By: origin.Thread}); err != nil {
		return err
	}
	if c.Settled {
		return d.inbox(api.InboxSet{Disposition: api.Disposition{State: api.Settled}})
	}
	d.effects = append(d.effects, MakeWorktree{Project: c.Project, Base: c.Worktree.Base, Title: c.Title})
	return nil
}

// placeRemote creates a remote child's placeholder: a child here with
// the machine c names frozen on it, by its engine's and link's ids, and
// no run, ever. Its task is for the machine, so nothing queues it here,
// and it has no folder here: none of this machine's is its. The create
// the machine is sent, command id (id) included, is frozen in its effect.
func (d *decision) placeRemote(id api.ID, c api.ThreadCreate) error {
	switch {
	case c.Parent.IsZero():
		return rejected("only a child thread runs on another machine")
	case c.Project != "":
		return rejected("a child on another machine runs in a worktree that machine makes, not in this one's")
	case strings.TrimSpace(c.Text) == "":
		return rejected("a child on another machine needs its task")
	case len(d.env.Line) == 0 || d.env.Line[0].Live() == nil:
		return rejected("only the parent's live run can start a child thread, and not once it is stopping")
	}
	var at api.Remote
	var ok bool
	var err error
	if d.env.Places != nil {
		at, ok, err = d.env.Places(c.Remote)
	}
	switch {
	case err != nil:
		return rejected(err.Error())
	case !ok:
		return rejected(fmt.Sprintf("no machine called %q is in this machine's list; a child runs only on a machine the person added", c.Remote))
	case c.Repository == nil:
		return rejected("a child on another machine starts from its parent's branch of its repository, and the parent's folder is on no branch of a repository with a commit")
	}
	if err := d.emit(api.ThreadCreated{Title: c.Title, Model: c.Model, Access: c.Access, Parent: c.Parent, Remote: &at, Text: c.Text}); err != nil {
		return err
	}
	parent := d.env.Line[0]
	origin := api.Origin{Engine: d.env.Engine, Thread: parent.ID, Title: parent.Title, Depth: len(d.env.Line), Access: min(parent.Access, parent.Live().Access)}
	d.effects = append(d.effects, RemoteCreate{Engine: at.Engine, Link: at.Link, Command: api.Command{
		ID: derive(id, "remote"), Target: derive(d.thread, "remote"),
		Body: api.ThreadCreate{Title: c.Title, Model: c.Model, Access: c.Access, Text: c.Text, Origin: &origin, Repository: c.Repository},
	}})
	return nil
}

// derive is the id of what id makes on a remote child's machine: its
// command's, of its own command's, and its thread's there, of its
// placeholder's. The machine's thread is another than the placeholder,
// so no one id has events in two logs.
func derive(id api.ID, what string) api.ID {
	sum := sha256.Sum256([]byte(id.String() + "/" + what))
	return api.ID(sum[:16])
}

// Limits cap the children agents start: Working at work at once under one
// thread, All under it that aren't deleted, and Depth below a thread
// of the user's. A field of zero or less takes DefaultLimits', and Depth is
// at most api.MaxDepth.
type Limits struct {
	Working int `json:"working,omitempty"`
	All     int `json:"all,omitempty"`
	Depth   int `json:"depth,omitempty"`
}

var DefaultLimits = Limits{Working: 5, All: 20, Depth: api.MaxDepth}

// Effective is l as the engine applies it.
func (l Limits) Effective() Limits {
	or := func(v, def int) int {
		if v <= 0 {
			return def
		}
		return v
	}
	return Limits{
		Working: or(l.Working, DefaultLimits.Working),
		All:     or(l.All, DefaultLimits.All),
		Depth:   min(or(l.Depth, DefaultLimits.Depth), api.MaxDepth),
	}
}

// Kids counts a thread's children that aren't deleted: those whose agent
// works, with a live run that isn't uncertain, or on another machine, a
// placeholder not settled whose child is being created there, or whose
// child's run there works or stops as far as this engine read it; and
// all of them. An uncertain run has no agent at work; only the user ends
// it. A run the machine started that this engine hasn't read yet takes
// no place, so the count caps what this engine starts.
type Kids struct{ Working, All int }

// CheckChild checks a child's create against its parent's line, the
// parent first and its top last, as far up as MaxDepth+1 threads, and the
// parent's children: the parent is there, its live run asks and can still
// take work, the child is no deeper than lim's Depth, its access no wider
// than that run's, nor than its parent's as it is now, which is no wider
// than its own parent's, and the parent is under lim's counts. A line that
// runs past MaxDepth is too deep, cycle or not.
func CheckChild(line []Thread, kids Kids, lim Limits, c api.ThreadCreate) error {
	lim = lim.Effective()
	if len(line) == 0 || !line[0].Exists() || line[0].Deleted {
		return rejected("the parent thread is gone")
	}
	if line[0].settled() {
		return rejected("the parent thread is settled")
	}
	live := line[0].Live()
	if live == nil || live.ID != c.Run || !steerable[live.State] {
		return rejected("only the parent's live run can start a child thread, and not once it is stopping")
	}
	// A thread another machine's link started is as deep here as there,
	// so the depth counts across both, and a line from another machine
	// starts no children on a third: no A to B to A.
	depth := len(line)
	if o := line[len(line)-1].Origin; o != nil {
		depth += o.Depth
	}
	if depth > lim.Depth {
		return rejected(fmt.Sprintf("a child thread can be at most %d below a thread of yours", lim.Depth))
	}
	if c.Remote != "" && slices.ContainsFunc(line, func(t Thread) bool { return t.Origin != nil }) {
		return rejected("a thread another machine started here can't start children on other machines")
	}
	// The run keeps the access it was admitted at, whatever the thread was
	// given since for its next turn.
	if c.Access > live.Access {
		return rejected("a child thread's access can't be wider than the run's that starts it")
	}
	if c.Access > line[0].Access {
		return rejected("a child thread's access can't be wider than its parent's")
	}
	if kids.Working >= lim.Working {
		return rejected(fmt.Sprintf("%d child threads of this thread are at work, the most at once; start another once one of them stops", lim.Working))
	}
	if kids.All >= lim.All {
		return rejected(fmt.Sprintf("this thread has %d child threads, the most it may; start another once one is deleted", lim.All))
	}
	return nil
}

// CheckOrigin checks a link's create against link, as tokens.json has it
// now and narrowed to this engine's setting, and kids, the link's threads
// here: its origin is 1 to lim's Depth below a thread of its person's,
// its access no wider than its origin's nor the link's, and the link is
// under its counts, lim's where it gives none.
func CheckOrigin(link api.Link, kids Kids, lim Limits, c api.ThreadCreate) error {
	lim = lim.Effective()
	o := c.Origin
	working, all := cmp.Or(link.Working, lim.Working), cmp.Or(link.All, lim.All)
	switch {
	case o.Depth < 1 || o.Depth > lim.Depth:
		return rejected(fmt.Sprintf("a thread from another machine is 1 to %d below a thread of its person's there, not %d", lim.Depth, o.Depth))
	case c.Access > o.Access:
		return rejected("a thread from another machine can't run wider than its parent there")
	case c.Access > link.Access:
		return rejected("the link lets its threads run no wider than its ceiling and this machine's setting for children from other machines")
	case kids.Working >= working:
		return rejected(fmt.Sprintf("%d threads of this link are at work, the most at once; start another once one of them stops", working))
	case kids.All >= all:
		return rejected(fmt.Sprintf("this link has %d threads here, the most it may; start another once one is deleted", all))
	}
	return nil
}

// CheckFork checks c, a create with a Fork, against src, the thread it
// forks, and says where the fork goes on from: only the user forks, a
// thread that is there, from a finished turn that isn't a silent one
// and that src's conversation still holds, its last such when c names
// none.
func CheckFork(src Thread, c api.ThreadCreate) (*api.Forked, error) {
	if !c.Parent.IsZero() || !c.Run.IsZero() {
		return nil, rejected("only you fork a thread, not an agent")
	}
	if !src.Exists() || src.Deleted {
		return nil, rejected("the thread to fork is gone")
	}
	f := &api.Forked{Thread: src.ID, Run: c.Fork.Run, Title: src.Title, Provider: src.Model.Provider, Session: src.Session}
	finished := false
	for _, r := range src.Runs {
		if r.Dropped {
			continue
		}
		f.Runs = append(f.Runs, r.ID)
		turn := r.State == RunFinished && !r.Silent
		if c.Fork.Run.IsZero() && turn {
			f.Run = r.ID
		}
		if r.ID == f.Run {
			finished = turn
		}
	}
	if !finished {
		return nil, rejected("only a finished turn the thread's conversation still holds can be forked")
	}
	return f, nil
}

// CheckCaller checks c, an agent's run, against line, the thread it acts
// on and those above it, and userLast, whether the user sent the thread's
// last message or stop: c's thread is above it and c's run is that
// thread's live run, still steerable and not a silent one, and the user
// hasn't taken the thread over.
func CheckCaller(line []Thread, c api.Caller, userLast bool) error {
	i := slices.IndexFunc(line, func(t Thread) bool { return t.ID == c.Thread })
	if i < 1 {
		return rejected("an agent can only act on a thread under its own")
	}
	if line[i].Deleted || line[i].settled() {
		return rejected("the agent's thread is settled or gone")
	}
	if live := line[i].Live(); live == nil || live.ID != c.Run || !steerable[live.State] || live.Silent {
		return rejected("only the live run of the thread above can act on it, and not once it is stopping")
	}
	if userLast {
		return rejected("the user took this thread over: its last message or stop was theirs, so it's theirs to steer")
	}
	return nil
}

func (d *decision) dispatch(id api.ID, m api.MessageDispatch) error {
	if m.Mode > api.Defer {
		return rejected("unknown dispatch mode")
	}
	if d.t.Access > api.AccessFull {
		return rejected("thread access is above the client's ceiling")
	}
	if o := d.t.Origin; o != nil && !o.Making && d.t.Workspace == "" {
		return rejected("this thread's worktree couldn't be made, so it runs nothing")
	}
	if o := d.t.Origin; o != nil && d.by == o.Thread && m.Fence < o.Fence {
		return rejected("the link's message was sent before a settle that came after it")
	}
	if live := d.t.Live(); !m.Replaces.IsZero() && (m.Mode != api.RestartActive || live == nil || live.ID != m.Replaces || live.State != RunUncertain) {
		return rejected("the run this was to replace has already ended")
	}
	// The engine's own continue is of a run it lost, or of a turn a usage
	// limit ended, once the limit reset, as api.Thread.LimitResume says.
	if m.Auto && m.Replaces.IsZero() {
		if at, ok := View(d.t).LimitResume(d.at); !ok || at.After(d.at) {
			return rejected("the engine continues a turn a usage limit ended only once it resets, on a thread of the user's that waits on nothing else")
		}
	}
	if m.Auto && (d.t.child() || d.t.settled() || d.t.Disposition.State == api.Snoozed && d.at.Before(d.t.Disposition.Until)) {
		return rejected("the engine continues only a run it lost, or a turn a usage limit ended, on a thread of the user's neither settled nor snoozed")
	}
	// A run left on a settled thread is the one the settle stopped, and a
	// silent maintenance run (such as idle compaction) cannot be steered.
	// A message sent while either is live queues right behind it.
	if live := d.t.Live(); live != nil && (d.t.settled() || live.Silent) && (m.Mode == api.SteerActive || m.Mode == api.StartNow) {
		m.Mode = api.AfterActive
	}
	// A steer never waits its turn: one the live run can't take now would
	// start a turn of its own once the run ends, after the user moved on.
	if live := d.t.Live(); m.Mode == api.SteerActive && live != nil && !steerable[live.State] {
		return rejected(fmt.Sprintf("the agent is %s and can't take a message; send it once the run ends", live.State))
	}
	if live := d.t.Live(); m.Mode == api.SteerActive && live != nil && !d.under(live.Access) {
		return rejected("the run was admitted wider than this thread may run at now, so it takes no message")
	}
	// A message is new work: a settled thread comes back to the inbox with
	// it, whichever client sent it. What waited before the settle stays
	// cancelled.
	if d.t.settled() {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
	}
	// The user sending again is their go-ahead for what their stop held.
	if m.Mode != api.Defer {
		if err := d.release(); err != nil {
			return err
		}
	}
	q := api.MessageQueued{Message: id, Text: m.Text, Mode: m.Mode, Pos: d.t.pos(false), Held: m.Mode == api.Defer, By: d.by, Replaces: m.Replaces, Auto: m.Auto}
	switch {
	case m.Mode == api.RestartActive:
		q.Pos = d.t.pos(true)
	case m.Mode == api.StartNow && d.t.Live() != nil:
		// A start now on a busy thread starts the moment the run ends, and
		// stops nothing: it goes ahead of what waits, behind the start
		// nows sent before it.
		pos, err := d.place(id, d.t.afterStarts())
		if err != nil {
			return err
		}
		q.Pos = pos
	}
	if err := d.emit(q); err != nil {
		return err
	}
	live := d.t.Live()
	switch {
	case m.Mode == api.StartNow && live == nil:
		return d.admit(id)
	case m.Mode == api.SteerActive && live != nil && steerable[live.State]:
		if err := d.raise(*live); err != nil {
			return err
		}
		if err := d.emit(api.MessageClaimed{Message: id, Run: live.ID}); err != nil {
			return err
		}
		d.effects = append(d.effects, SteerTurn{Run: live.ID, Message: id, Text: m.Text})
	case m.Mode == api.RestartActive && live != nil:
		return d.stop(*live)
	}
	// AfterActive, and steering a thread with nothing to steer, wait for
	// settle, which starts the latter at once; a Defer message is held
	// until something promotes it.
	return nil
}

// pos is a queue position before or after every message on the thread.
func (t Thread) pos(front bool) int64 {
	if len(t.Queue) == 0 {
		return 0
	}
	lo, hi := t.Queue[0].Pos, t.Queue[0].Pos
	for _, m := range t.Queue {
		lo, hi = min(lo, m.Pos), max(hi, m.Pos)
	}
	if front {
		return lo - 1
	}
	return hi + 1
}

// afterStarts is the first message waiting its turn that isn't a start
// now ready to go, or a restart's: where a start now sent while the thread
// is busy goes. Zero when there is none.
func (t Thread) afterStarts() api.ID {
	for _, m := range t.line() {
		if m.State != QueueReady || m.Mode != api.StartNow && m.Mode != api.RestartActive {
			return m.ID
		}
	}
	return api.ID{}
}

// place makes room in the line for message just before the message
// before, or last with before zero, and returns the position it takes
// there. Positions are whole numbers: where the two it goes between leave
// none free, those from before on move up one each, as far as they must.
func (d *decision) place(message, before api.ID) (int64, error) {
	if before.IsZero() {
		return d.t.pos(false), nil
	}
	line := slices.DeleteFunc(d.t.line(), func(m Message) bool { return m.ID == message })
	i := index(line, func(m Message) bool { return m.ID == before })
	switch {
	case i < 0:
		return 0, rejected("the message to put it before no longer waits in the queue")
	case i == 0:
		return line[0].Pos - 1, nil
	case line[i].Pos-line[i-1].Pos > 1:
		return line[i-1].Pos + (line[i].Pos-line[i-1].Pos)/2, nil
	}
	at := line[i-1].Pos + 1
	for next := at + 1; i < len(line) && line[i].Pos < next; i, next = i+1, next+1 {
		if err := d.emit(api.MessageMoved{Message: line[i].ID, Pos: next}); err != nil {
			return 0, err
		}
	}
	return at, nil
}

// admit starts message's turn. The wakes that wait go in with it, claimed
// by its run: the reports first, so the user's words come last.
func (d *decision) admit(message api.ID) error {
	if !d.runs() {
		return nil
	}
	m := d.t.Queue[d.t.messageAt(message)]
	var texts []string
	var with []api.ID
	for _, w := range d.t.Queue {
		if w.waking() && w.ID != message {
			texts, with = append(texts, w.Text), append(with, w.ID)
		}
	}
	if m.waking() {
		texts = append([]string{m.Text}, texts...)
	} else {
		texts = append(texts, m.Text)
	}
	gen := d.t.Generation + 1
	admitted := api.TurnAdmitted{Run: message, Generation: gen}
	if m.waking() {
		admitted.Wake, admitted.Prior = api.WakeReport, cmp.Or(d.prior, d.t.UpdatedAt)
	}
	if err := d.emit(admitted); err != nil {
		return err
	}
	for _, id := range with {
		if err := d.emit(api.MessageClaimed{Message: id, Run: message}); err != nil {
			return err
		}
	}
	d.effects = append(d.effects, StartTurn{
		Run: message, Generation: gen, Text: strings.Join(texts, "\n\n"), Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

func (d *decision) sent(run api.ID) error {
	var ids []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueClaimed && m.Run == run && m.ID != run && m.Mode == api.AfterActive {
			ids = append(ids, m.ID)
		}
	}
	for _, id := range ids {
		if err := d.emit(api.MessageDispatched{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// settle starts the next ready message whenever the thread is idle, so
// admission happens in the same commit as whatever freed the thread. A
// settled or deleted thread starts nothing, nor does a remote child's
// placeholder, ever. With no ready message, a wake
// starts a turn of its own, unless the last run was stopped: a stop stays
// a stop, and the wakes go in with the next message the user sends, or
// start once they resume the queue.
func (d *decision) settle() error {
	if d.t.Live() != nil || d.t.settled() || d.t.Deleted || d.t.Remote != nil {
		return nil
	}
	if m := d.t.next(); m != nil {
		return d.admit(m.ID)
	}
	if d.t.stopped() && !d.resumed {
		return nil
	}
	if i := index(d.t.Queue, Message.waking); i >= 0 {
		return d.admit(d.t.Queue[i].ID)
	}
	return nil
}

// interrupt is the user's stop. Asked again once the agent took the first
// interrupt, the user gives up waiting on a run that ignores it; nobody can
// say what that run is doing. Asked again before, it changes nothing.
//
// A stop goes quiet: what waits its turn is held until the user sends again
// or resumes the queue. One of a silent run holds nothing, as the user's
// messages wait behind it, not on it.
//
// A halt, with drop, cancels what waits its turn and stops the run. It is
// not the user giving up on a run: on one already stopping it waits for
// the run's end, as the first stop does. Settling a thread halts it.
func (d *decision) interrupt(drop bool) error {
	if drop {
		if err := d.drop("the thread was settled or deleted"); err != nil {
			return err
		}
	}
	r := d.t.Live()
	if r != nil && !r.Silent {
		if err := d.emit(api.StopAsked{Run: r.ID, By: d.by}); err != nil {
			return err
		}
	}
	if r != nil && !r.Silent && !drop {
		if err := d.hold(); err != nil {
			return err
		}
	}
	switch {
	case r == nil && !drop && d.t.Origin != nil:
		// A stop of a link's thread with nothing running holds what
		// waits, as its first message before its worktree is made or
		// while its access is over the cap, and stops its last run, so
		// its wakes wait as a stopped run's do. With no run yet the stop
		// is still asked, so whose it was is kept.
		var last api.ID
		for i := len(d.t.Runs) - 1; i >= 0; i-- {
			if r := d.t.Runs[i]; !r.Silent {
				last = r.ID
				break
			}
		}
		if err := d.emit(api.StopAsked{Run: last, By: d.by}); err != nil {
			return err
		}
		return d.hold()
	case r == nil:
		return nil
	case drop || r.State != RunStopping:
		return d.stop(*r)
	case !d.by.IsZero():
		// An agent stops a run that runs: one already stopping is the
		// user's to give up on.
		return nil
	case r.Signalled:
		return d.lost(*r)
	}
	return nil
}

// lost is that r's agent no longer answers. The user decides what an
// uncertain run did, but a silent one asked them nothing and resumed the
// conversation: it ends, and its agent is closed.
func (d *decision) lost(r Run) error {
	if !r.Silent {
		return d.uncertain(api.TurnUncertain{Run: r.ID, Reason: api.UncertainStopped})
	}
	d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	return d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeFailed, Error: "the compaction could not be stopped"})
}

func (d *decision) stop(r Run) error {
	switch r.State {
	case RunStopping:
		return nil
	case RunUncertain:
		// Ending an uncertain run is the user's explicit decision to move on.
		if err := d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned}); err != nil {
			return err
		}
		return nil
	}
	if err := d.emit(api.TurnStopping{Run: r.ID}); err != nil {
		return err
	}
	d.effects = append(d.effects, InterruptTurn{Run: r.ID})
	return nil
}

// hold holds the ready messages, so settle starts none of them.
func (d *decision) hold() error {
	var ready []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueReady {
			ready = append(ready, m.ID)
		}
	}
	for _, id := range ready {
		if err := d.emit(api.MessageHeld{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// release readies the messages of the user's that are held: those a stop
// held, and those sent as a Defer.
func (d *decision) release() error {
	var held []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueHeld && m.Mode == api.Defer {
			held = append(held, m.ID)
		}
	}
	for _, id := range held {
		if err := d.emit(api.MessageReleased{Message: id}); err != nil {
			return err
		}
	}
	return nil
}

// resume is the user's go-ahead after a stop. A live run holds the
// wakes until it ends, and a resume can't wait for that: with nothing of
// the user's held, it's refused, for them to resume once the run ends.
// cancelResume stops the engine continuing the thread's last turn once the
// usage limit that ended it resets.
func (d *decision) cancelResume(c api.ResumeCancel) error {
	r, ok := d.t.lastTurn()
	if !ok || r.ID != c.Run || r.Limit == nil || r.ResumeCancelled {
		return rejected("no usage limit's reset is waited on")
	}
	return d.emit(api.ResumeCancelled{Run: r.ID})
}

func (d *decision) resume() error {
	if d.t.Live() != nil && !d.t.holding() {
		return rejected("the agent is still at work; resume once its run ends")
	}
	d.resumed = true
	return d.release()
}

// queued is the message of the user's id names, while it waits its turn:
// what the queue's intents act on.
func (d *decision) queued(id api.ID) (Message, error) {
	if m, ok := d.t.Message(id); ok && m.waiting() {
		return m, nil
	}
	return Message{}, rejected("the message no longer waits in the queue")
}

func (d *decision) edit(b api.QueueEdit) error {
	m, err := d.queued(b.Message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(b.Text) == "" {
		return rejected("a message needs words; take it out of the queue instead")
	}
	if b.Text == m.Text {
		return nil
	}
	return d.emit(api.MessageEdited{Message: m.ID, Text: b.Text})
}

func (d *decision) cancel(b api.QueueCancel) error {
	if _, err := d.queued(b.Message); err != nil {
		return err
	}
	return d.emit(api.MessageCancelled{Message: b.Message, Reason: "taken out of the queue"})
}

// move puts a message in the line just before another, or last. One
// already there stays, and nothing is recorded.
func (d *decision) move(b api.QueueMove) error {
	if _, err := d.queued(b.Message); err != nil {
		return err
	}
	if b.Before == b.Message {
		return rejected("a message can't go before itself")
	}
	line := d.t.line()
	i := index(line, func(m Message) bool { return m.ID == b.Message })
	if b.Before.IsZero() && i == len(line)-1 || i+1 < len(line) && line[i+1].ID == b.Before {
		return nil
	}
	pos, err := d.place(b.Message, b.Before)
	if err != nil {
		return err
	}
	return d.emit(api.MessageMoved{Message: b.Message, Pos: pos})
}

// steer sends a message that waits its turn now, as a send does: into the
// live run, as a SteerActive, or with none, as the thread's next turn. It
// stops nothing, so a run that can't take a message refuses it. Like a
// send, it's the user's go-ahead for what their stop held, as a row's
// Steer now is in Zeron.
func (d *decision) steer(b api.QueueSteer) error {
	m, err := d.queued(b.Message)
	if err != nil {
		return err
	}
	live := d.t.Live()
	switch {
	case live != nil && live.Silent:
		return rejected("the agent is compacting its context and can't take a message; it goes once that ends")
	case live != nil && !steerable[live.State]:
		return rejected(fmt.Sprintf("the agent is %s and can't take a message; send it once the run ends", live.State))
	case live != nil && !d.under(live.Access):
		return rejected("the run was admitted wider than this thread may run at now, so it takes no message")
	}
	if err := d.release(); err != nil {
		return err
	}
	if live == nil {
		return d.admit(m.ID)
	}
	if err := d.raise(*live); err != nil {
		return err
	}
	if err := d.emit(api.MessageClaimed{Message: m.ID, Run: live.ID, Steer: true}); err != nil {
		return err
	}
	d.effects = append(d.effects, SteerTurn{Run: live.ID, Message: m.ID, Text: m.Text})
	return nil
}

// drop cancels the messages waiting their turn, held or ready, so settle
// starts none of them. One the live run claimed, a steer, goes with it.
func (d *decision) drop(reason string) error {
	var waiting []api.ID
	for _, m := range d.t.Queue {
		if m.State == QueueReady || m.State == QueueHeld {
			waiting = append(waiting, m.ID)
		}
	}
	for _, id := range waiting {
		if err := d.emit(api.MessageCancelled{Message: id, Reason: reason}); err != nil {
			return err
		}
	}
	return nil
}

func (d *decision) respond(b api.RequestRespond) error {
	i := d.t.requestAt(b.Request)
	if i < 0 {
		return api.ErrNotFound
	}
	r := d.t.Requests[i]
	if r.Generation != b.Generation {
		return rejected("the request belongs to another run")
	}
	if !json.Valid(b.Answer) {
		return rejected("the answer is not JSON")
	}
	if err := fits(r, b.Answer); err != nil {
		return rejected(err.Error())
	}
	if r.Kind == api.KindCredentials {
		p, _ := api.ParseProvided(b.Answer)
		b.Answer, _ = json.Marshal(p)
	}
	if err := d.emit(api.RequestAnswer{Request: r.ID, Answer: b.Answer}); err != nil {
		if r.State == api.RequestAnswered || r.State == api.RequestDelivered {
			return api.ErrAlreadyAnswered
		}
		return err
	}
	d.effects = append(d.effects, DeliverAnswer{Run: r.Run, Request: r.ID, Dedup: r.Dedup, Answer: b.Answer})
	return nil
}

// fits is nil when answer answers r. A question takes its answers or a
// dismissal, and refuses a misfit before the race for the request, which
// it would win; credentials take the names provided, never values, or
// none; an approval keeps its grant always or for the session only where
// its body offers that, and takes any other JSON.
func fits(r Request, answer json.RawMessage) error {
	if r.Kind == api.KindApproval {
		var a, offered struct{ Always, Session bool }
		json.Unmarshal(answer, &a)
		json.Unmarshal(r.Body, &offered)
		switch {
		case a.Always && a.Session:
			return errors.New("an answer keeps its grant always or for the session, not both")
		case a.Always && !offered.Always:
			return errors.New("the agent offers no way to always allow this")
		case a.Session && !offered.Session:
			return errors.New("the agent offers no way to allow this for the session")
		}
		return nil
	}
	if r.Kind == api.KindCredentials {
		p, err := api.ParseProvided(answer)
		if err != nil {
			return err
		}
		c, err := api.ParseCredentials(r.Body)
		if err != nil {
			return err
		}
		return p.Fit(c)
	}
	if r.Kind != api.KindQuestion {
		return nil
	}
	a, err := api.ParseAnswers(answer)
	if err != nil {
		return err
	}
	q, _ := api.ParseQuestions(r.Body) // a body no card can show takes only a dismissal
	return a.Fit(q)
}

// inbox sets the user's disposition. Settled is done with, as archived
// was: in the same commit, what waits its turn is cancelled and the live
// run is stopped, and settle starts nothing on the thread until it's back
// in the inbox.
func (d *decision) inbox(b api.InboxSet) error {
	v := b.Disposition
	v.Until = v.Until.UTC().Round(0)
	if v.State > api.Settled || (v.State == api.Snoozed) == v.Until.IsZero() {
		return rejected("only a snoozed thread has an until, and it must")
	}
	if v.State == d.t.Disposition.State && v.Until.Equal(d.t.Disposition.Until) {
		// A link's settle of a thread settled already still fences what
		// the link sent before it.
		if o := d.t.Origin; o != nil && b.Fence > o.Fence {
			return d.emit(api.DispositionChanged{Disposition: v, Fence: b.Fence})
		}
		return nil
	}
	if err := d.emit(api.DispositionChanged{Disposition: v, Fence: b.Fence}); err != nil {
		return err
	}
	if v.State != api.Settled {
		return nil
	}
	return d.interrupt(true)
}

// delete tombstones the thread. A live run ends with it, abandoned, in the
// same commit, with an effect that closes its agent, which a crash or a
// shutdown leaves for the next host: whatever a client last saw of the
// thread, nothing of it is left running. An idle thread's last run gets the
// close too, for an agent its provider kept for the next run.
//
// One that removes the worktree waits for the tree to be still: an agent
// closing may still write there, after the host looked for changes. It
// removes the worktree of each thread of the tree in one, after its agent's
// close, as an effect a crash leaves for the next host.
func (d *decision) delete(b api.ThreadDelete) error {
	if b.RemoveWorktree && d.t.Live() != nil {
		return rejected("stop the thread, and every thread under it, before its worktree goes")
	}
	if r := d.t.Live(); r != nil {
		if err := d.emit(api.TurnEnded{Run: r.ID, Outcome: api.OutcomeAbandoned}); err != nil {
			return err
		}
		d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	} else if n := len(d.t.Runs); n > 0 {
		r := d.t.Runs[n-1]
		d.effects = append(d.effects, CloseAgent{Run: r.ID, Generation: r.Generation})
	}
	if b.RemoveWorktree && d.t.Branch != "" {
		d.effects = append(d.effects, RemoveWorktree{Workspace: d.t.Workspace, Project: d.t.Project, Branch: d.t.Branch})
	}
	return d.emit(api.ThreadDeleted{})
}

func (d *decision) rename(b api.ThreadRename) error {
	title := strings.TrimSpace(b.Title)
	if title == "" {
		return rejected("a thread needs a title")
	}
	if title == d.t.Title {
		return nil
	}
	return d.emit(api.ThreadRenamed{Title: title})
}

// configure sets what b gives of the thread's model and access, on the
// ones the thread has now: what b leaves out stays, whatever its sender
// last saw of it. The provider is none of b's to give.
func (d *decision) configure(b api.ThreadConfigure) error {
	m, access := b.Onto(d.t.Model, d.t.Access)
	if access > api.AccessFull {
		return rejected("unknown access level")
	}
	if access > d.t.Access && !d.under(access) {
		return rejected("this thread came from another machine, and may run no wider than its parent there, its link and this machine's setting allow")
	}
	if m.Model == "" && d.t.Model.Model != "" {
		return rejected("a thread needs a model")
	}
	var newSweep *api.SweepSchedule
	sweepChanged := false
	if b.Sweep != nil {
		if b.Sweep.IntervalSeconds < 0 {
			return rejected("sweep interval must be non-negative")
		}
		if b.Sweep.IntervalSeconds > 0 && d.t.Role != api.RoleManager {
			return rejected("only a manager thread can schedule sweeps")
		}
		newSweep = normalizeSweep(b.Sweep)
		sweepChanged = !sweepEqual(d.t.Sweep, newSweep)
	}
	if m == d.t.Model && access == d.t.Access && !sweepChanged {
		return nil
	}
	return d.emit(api.ThreadConfigured{Model: m, Access: access, Sweep: newSweep, SweepSet: sweepChanged})
}

func (d *decision) dismissWake(b api.WakeDismiss) error {
	if d.t.Role != api.RoleManager || d.t.child() {
		return rejected("only a Manager thread dismisses its wakes")
	}
	live := d.t.Live()
	if live == nil {
		return rejected("no live run to dismiss")
	}
	if !b.Run.IsZero() && b.Run != live.ID {
		return rejected("that run is no longer live")
	}
	if live.Wake == api.NotWake {
		return rejected("only a wake a worker's report or a sweep started can be dismissed, not a turn the user asked for or joined")
	}
	if live.Dismissed {
		return nil
	}
	e := api.WakeDismissed{Run: live.ID, Reason: strings.TrimSpace(b.Reason), UpdatedAt: live.Prior}
	if live.Wake == api.WakeReport && !d.t.Reported.After(live.Woke) {
		e.Reports, e.Reported = true, d.t.Alerted
	}
	return d.emit(e)
}

func (d *decision) raise(r Run) error {
	if r.Wake == api.NotWake {
		return nil
	}
	return d.emit(api.WakeRaised{Run: r.ID})
}

func (d *decision) snoozing() bool {
	return d.t.Disposition.State == api.Snoozed && d.at.Before(d.t.Disposition.Until)
}

func (d *decision) unsnooze(r Run) error {
	if r.Wake == api.NotWake || d.t.Role != api.RoleManager || !d.snoozing() {
		return nil
	}
	return d.emit(api.DispositionChanged{Disposition: api.Disposition{}})
}

func holds[T comparable](held *T, v T) bool { return held != nil && *held == v }

func (d *decision) observe(o Observation) error {
	live := d.t.Live()
	isLive := func(run api.ID) bool { return live != nil && live.ID == run }
	switch o := o.(type) {
	case Bound:
		// A run that is no longer the thread's binds nothing.
		if isLive(o.Run) && (o.Fork != "" || o.Session != "" && o.Session != d.t.Session) {
			return d.emit(api.SessionBound{Run: o.Run, Session: o.Session, Fork: o.Fork})
		}
	case Started:
		if !isLive(o.Run) {
			return nil
		}
		if live.State != RunStarting {
			// Interrupted while starting, but the agent did get the message.
			if err := d.emit(api.MessageDispatched{Message: o.Run}); err != nil {
				return err
			}
			return d.sent(o.Run)
		}
		if err := d.emit(api.TurnStarted{Run: o.Run}); err != nil {
			return err
		}
		return d.sent(o.Run)
	case Ended:
		// Only the user ends an uncertain run.
		if isLive(o.Run) && live.State != RunUncertain {
			stopped := live.State == RunStopping
			silent := live.Silent
			// A run that ended while it stopped was stopped, whichever
			// came first, its end or its interrupt's word: a stop stays a
			// stop.
			if stopped && !live.Signalled {
				if err := d.emit(api.TurnSignalled{Run: o.Run}); err != nil {
					return err
				}
			}
			// A run the user stopped holds the thread for no usage limit,
			// whatever ended it first.
			limit := o.Limit
			if stopped {
				limit = nil
			}
			if err := d.emit(api.TurnEnded{Run: o.Run, Outcome: o.Outcome, Error: o.Error, Tokens: o.Tokens, Limit: limit, Head: o.Head}); err != nil {
				return err
			}
			if !live.Dismissed {
				if err := d.unsnooze(*live); err != nil {
					return err
				}
			}
			// A run being stopped or a silent maintenance run reports nothing.
			switch {
			case stopped || silent:
			case o.Outcome == api.OutcomeCompleted:
				d.report(o.Run, api.ReportDone, "", nil)
			case o.Outcome == api.OutcomeFailed:
				d.report(o.Run, api.ReportFailed, o.Error, nil)
			}
		}
	case Delta:
		if isLive(o.Run) {
			return d.emit(api.AssistantDelta{Run: o.Run, Text: o.Text})
		}
	case Context:
		if isLive(o.Run) {
			return d.emit(api.ContextReported{
				Run: o.Run, Used: o.Used, Window: o.Window, Model: o.Model, Compacted: o.Compacted,
				CacheExpires: o.CacheExpires, PreTokens: o.PreTokens, PostTokens: o.PostTokens,
			})
		}
	case Granted:
		// A confirmation the thread holds already says nothing new.
		if isLive(o.Run) && !holds(d.t.Confirmed.Access, api.ConfirmedAccess{Run: o.Run, Asked: live.Access, Access: o.Access, Mode: o.Mode, Why: o.Why}) {
			return d.emit(api.AccessGranted{Run: o.Run, Access: o.Access, Mode: o.Mode, Why: o.Why})
		}
	case Ran:
		if isLive(o.Run) && !holds(d.t.Confirmed.Model, api.ConfirmedModel{Run: o.Run, Asked: live.Model, Model: o.Model, Effort: o.Effort, Why: o.Why}) {
			return d.emit(api.ModelConfirmed{Run: o.Run, Model: o.Model, Effort: o.Effort, Why: o.Why})
		}
	case Work:
		if isLive(o.Item.Run) {
			return d.emit(o.Item)
		}
	case Subagent:
		if isLive(o.Run) {
			return d.emit(o.Event)
		}
	case Plan:
		if isLive(o.Plan.Run) {
			return d.emit(o.Plan)
		}
	case Surfaced:
		dup := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup }) >= 0
		if isLive(o.Run) && !dup && live.Silent {
			// Nobody watches a maintenance run to answer it, and the
			// user's next message waits behind it: it stops instead.
			return d.stop(*live)
		}
		if isLive(o.Run) && !dup {
			if err := d.unsnooze(*live); err != nil {
				return err
			}
			if err := d.raise(*live); err != nil {
				return err
			}
			if err := d.emit(api.RequestOpened{
				Request: o.Request, Run: o.Run, Generation: live.Generation, Dedup: o.Dedup, Kind: o.Kind, Body: o.Body,
			}); err != nil {
				return err
			}
			d.report(o.Run, api.ReportBlocked, "", &api.Request{ID: o.Request, Kind: o.Kind, Body: o.Body})
		}
	case Withdrawn:
		// An answer already given is left to its delivery.
		i := index(d.t.Requests, func(r Request) bool { return r.Run == o.Run && r.Dedup == o.Dedup })
		if i >= 0 && d.t.Requests[i].State == api.RequestPending {
			return d.emit(api.RequestInvalidation{Request: d.t.Requests[i].ID, Reason: o.Reason})
		}
	case Delivered:
		return d.emit(api.RequestDelivery{Request: o.Request})
	case Unheard:
		return d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Reason})
	case DeliveryFailed:
		if err := d.emit(api.RequestInvalidation{Request: o.Request, Reason: o.Error, Unconfirmed: o.Unconfirmed}); err != nil {
			return err
		}
		// An answer that can't be delivered is never assumed; the run that
		// waits for it is stopped instead. Only the user ends an uncertain
		// run.
		if r := d.t.Requests[d.t.requestAt(o.Request)]; isLive(r.Run) && live.State != RunUncertain {
			return d.stop(*d.t.Live())
		}
	case Steered:
		return d.emit(api.MessageDispatched{Message: o.Message})
	case SteerFailed:
		return d.emit(api.MessageCancelled{Message: o.Message, Reason: o.Error})
	case SteerLost:
		return d.emit(api.MessageUncertain{Message: o.Message})
	case InterruptSent:
		if isLive(o.Run) && live.State == RunStopping {
			return d.emit(api.TurnSignalled{Run: o.Run})
		}
	case InterruptLost:
		if isLive(o.Run) && live.State == RunStopping {
			return d.lost(*live)
		}
	case Reconciled:
		if !isLive(o.Run) {
			return nil
		}
		if o.Restored {
			if err := d.emit(api.TurnRestored{Run: o.Run}); err != nil {
				return err
			}
			return d.sent(o.Run)
		}
		if o.Failed {
			d.effects = append(d.effects, CloseAgent{Run: o.Run, Generation: live.Generation})
		}
		// A maintenance run resumed the conversation and asked the user
		// nothing, so there is nothing for them to decide: it ends.
		if live.Silent {
			return d.emit(api.TurnEnded{Run: o.Run, Outcome: api.OutcomeFailed, Error: "the engine restarted during the compaction"})
		}
		switch {
		case o.Asked:
			return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainStopped})
		case o.Failed:
			return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainUnconfirmed})
		}
		worked := live.State == RunRunning || live.State == RunStarting
		return d.uncertain(api.TurnUncertain{Run: o.Run, Reason: api.UncertainRestarted, Continue: o.Continue && worked && !d.t.child()})
	case Rewound:
		return d.rewound()
	case Reported:
		return d.reported(o)
	case IdleCompact:
		return d.idleCompact(o)
	case SweepDue:
		return d.sweepDue(o)
	case WorktreeReady:
		return d.emit(api.WorktreeReady{Workspace: o.Workspace, Branch: o.Branch})
	case WorktreeFailed:
		if err := d.emit(api.WorktreeFailed{Error: o.Error}); err != nil {
			return err
		}
		return d.drop("Can't make a worktree: " + o.Error)
	case RemoteAttempted, RemoteBound, RemoteSeen, RemoteLost:
		return d.remote(o)
	}
	return nil
}

// remote records what the runtime saw of a placeholder's child on its
// machine. A first try at its create is refused once the placeholder is
// settled, so nothing reaches the machine after that; one tried before
// goes on. What the child reports reaches its parent as a local child's
// report does, once per key, whatever the machine's log replays. A lost
// child reports it failed, once: lost is where it ends.
func (d *decision) remote(o Observation) error {
	r := d.t.Remote
	if r == nil {
		return fmt.Errorf("%w: %T: the thread runs here", ErrRefused, o)
	}
	switch o := o.(type) {
	case RemoteAttempted:
		switch {
		case r.State != RemoteCreating || d.t.settled() && !r.Attempted:
			return fmt.Errorf("%w: the placeholder is settled, or its child no longer being created", ErrRefused)
		case r.Attempted:
			return nil
		}
		return d.emit(api.RemoteAttempted{Create: &o.Create})
	case RemoteBound:
		return d.emit(api.RemoteBound{Thread: o.Thread})
	case RemoteLost:
		if err := d.emit(api.RemoteLost{Reason: o.Reason}); err != nil {
			return err
		}
		d.remoteReport(o.On, RemoteReport{Run: lostRun(d.t.ID), Kind: api.ReportFailed, Error: o.Reason})
		return nil
	case RemoteSeen:
		if r.State != RemoteBoundState {
			return fmt.Errorf("%w: the placeholder isn't following its child", ErrRefused)
		}
		var keys []api.ID
		var reports []RemoteReport
		for _, p := range o.Reports {
			if !slices.Contains(r.Reported, p.Key) && !slices.Contains(keys, p.Key) {
				keys, reports = append(keys, p.Key), append(reports, p)
			}
		}
		if o.Cursor == r.Cursor && o.Run == r.Run && o.Silent == r.Silent && o.Slot == r.Slot && slices.Equal(o.Pending, r.Pending) && slices.Equal(o.Questions, r.Questions) && o.Taken == r.Taken && reflect.DeepEqual(o.Ended, r.Ended) && o.Model == r.Model && o.Access == r.Access && reflect.DeepEqual(o.Turn, r.Turn) && o.Held == r.Held && len(keys) == 0 {
			return nil
		}
		if err := d.emit(api.RemoteSeen{Cursor: o.Cursor, Run: o.Run, Silent: o.Silent, State: o.Slot, Pending: o.Pending, Questions: o.Questions, Taken: o.Taken, Ended: o.Ended, Model: o.Model, Access: o.Access, Turn: o.Turn, Held: o.Held, Keys: keys}); err != nil {
			return err
		}
		for _, p := range reports {
			d.remoteReport(o.On, p)
		}
	}
	return nil
}

// remoteReport has the placeholder report p to its parent, as report
// does for a local child, with what its machine, labelled on, said.
func (d *decision) remoteReport(on string, p RemoteReport) {
	r := ReportToParent{Parent: d.t.Parent, Child: d.t.ID, Run: p.Run, Kind: p.Kind, Title: d.t.Title, Error: p.Error, Ask: p.Ask, Question: p.Question, On: on}
	if p.Kind == api.ReportDone {
		r.Reply = &p.Reply
	}
	d.effects = append(d.effects, r)
}

// lostRun is the run a lost placeholder reports failed under: there may
// be no run of its child's to name, and it is lost once.
func lostRun(placeholder api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched remote lost "), placeholder[:]...))
	var id api.ID
	copy(id[:6], placeholder[:6])
	copy(id[6:], sum[:])
	return id
}

// idleCompact starts a silent /compact run when the thread is still idle,
// unchanged since AfterRun, and inside its idle compaction window.
func (d *decision) idleCompact(o IdleCompact) error {
	afterRun, start, end, ok := IdleCompactWindow(d.t, d.at)
	if !ok || afterRun != o.AfterRun || d.at.Before(start) || !d.at.Before(end) {
		return fmt.Errorf("%w: thread is not eligible for idle compaction", ErrRefused)
	}
	if o.Run.IsZero() {
		return fmt.Errorf("%w: idle compaction needs a run id", ErrRefused)
	}
	if !d.runs() {
		return fmt.Errorf("%w: the thread may run nothing now", ErrRefused)
	}
	if err := d.emit(api.MessageQueued{Message: o.Run, Text: "/compact", Mode: api.StartNow, Pos: d.t.pos(false), Silent: true}); err != nil {
		return err
	}
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: o.Run, Generation: gen, Silent: true}); err != nil {
		return err
	}
	d.effects = append(d.effects, StartTurn{
		Run: o.Run, Generation: gen, Text: "/compact", Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access, ResumeOnly: true,
	})
	return nil
}

func (d *decision) sweepDue(o SweepDue) error {
	at, ok := NextSweep(d.t)
	if !ok || d.at.Before(at) || d.snoozing() {
		return fmt.Errorf("%w: thread is not due for a sweep", ErrRefused)
	}
	if _, ok := View(d.t).LimitResume(d.at); ok {
		return fmt.Errorf("%w: the thread waits to continue a turn a usage limit ended", ErrRefused)
	}
	if o.Run.IsZero() {
		return fmt.Errorf("%w: sweep needs a run id", ErrRefused)
	}
	if !d.runs() {
		return fmt.Errorf("%w: the thread may run nothing now", ErrRefused)
	}
	text := strings.TrimSpace(o.Text)
	if text == "" && d.t.Sweep != nil {
		text = strings.TrimSpace(d.t.Sweep.Prompt)
	}
	if text == "" {
		text = api.DefaultSweepPrompt
	}
	prior := d.t.UpdatedAt
	if err := d.emit(api.MessageQueued{Message: o.Run, Text: text, Mode: api.StartNow, Pos: d.t.pos(false), Auto: true, Sweep: true}); err != nil {
		return err
	}
	gen := d.t.Generation + 1
	if err := d.emit(api.TurnAdmitted{Run: o.Run, Generation: gen, Wake: api.WakeSweep, Prior: prior}); err != nil {
		return err
	}
	if err := d.emit(api.SweepStarted{Run: o.Run}); err != nil {
		return err
	}
	d.effects = append(d.effects, StartTurn{
		Run: o.Run, Generation: gen, Text: text, Workspace: d.t.Workspace, Model: d.t.Model, Access: d.t.Access,
	})
	return nil
}

// reported logs a child's report. It brings a snoozed parent, and only its
// parent, back to the inbox. A done or failed one queues a wake, which
// gives the parent's agent the child's result with its next turn (see
// settle), and so does a question the child waits on, with a wake of its
// own, for the parent's agent to answer or leave to the user. An approval
// or credentials are the user's alone, and wake nothing. A settled parent stays settled and takes no wake: settling
// halted the tree, so this is a run that ended before its stop landed, and
// the user is done with the thread. The report is logged either way. A
// report the parent already took a wake for is a replay, and changes
// nothing.
func (d *decision) reported(o Reported) error {
	id, text := wakeID(o.Report.Run), wakeText(o, d.t.Branch)
	if !o.Question.IsZero() {
		id, text = askWakeID(o.Question), askText(o)
	}
	if d.t.messageAt(id) >= 0 {
		return nil
	}
	d.prior = d.t.UpdatedAt
	if err := d.emit(o.Report); err != nil {
		return err
	}
	wakes := !(o.Report.Kind == api.ReportBlocked && o.Question.IsZero() || d.t.settled())
	quiet := wakes && d.t.Role == api.RoleManager && d.snoozing()
	if d.t.Disposition.State == api.Snoozed && !quiet {
		if err := d.emit(api.DispositionChanged{Disposition: api.Disposition{}}); err != nil {
			return err
		}
	}
	if !wakes {
		return nil
	}
	if err := d.emit(api.MessageQueued{
		Message: id, Text: text, Mode: api.AfterActive, Pos: d.t.pos(false), Held: true, Report: o.Report.Child,
	}); err != nil || !quiet {
		return err
	}
	if err := d.settle(); err != nil {
		return err
	}
	if live := d.t.Live(); live != nil && live.Wake == api.WakeReport {
		if m, _ := d.t.Message(id); m.Run == live.ID {
			return nil
		}
	}
	return d.emit(api.DispositionChanged{Disposition: api.Disposition{}})
}

// wakeID is the wake for a child's run, the same each time it reports, so
// a report replayed finds the wake it took. It keeps the run's time; the
// rest is a hash of the run.
func wakeID(run api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched wake "), run[:]...))
	var id api.ID
	copy(id[:6], run[:6])
	copy(id[6:], sum[:])
	return id
}

// askWakeID is the wake for a child's question, one per question request,
// apart from its run's: the run's own report still wakes the parent when
// it ends.
func askWakeID(request api.ID) api.ID {
	sum := sha256.Sum256(append([]byte("glitched ask wake "), request[:]...))
	var id api.ID
	copy(id[:6], request[:6])
	copy(id[6:], sum[:])
	return id
}

// askText gives the parent's agent a child's question, in the task tags
// wakeText uses, with the request task_answer names. A body no card can
// show is the user's to dismiss.
func askText(o Reported) string {
	var b strings.Builder
	q, err := api.ParseQuestions(o.Ask)
	if err != nil {
		b.WriteString(o.Report.Outcome + "\n")
	}
	for i, x := range q.Questions {
		fmt.Fprintf(&b, "%d. %s", i+1, x.Text)
		if x.Multi {
			b.WriteString(" (pick any)")
		}
		b.WriteString("\n")
		for _, opt := range x.Options {
			fmt.Fprintf(&b, "   - %s", opt.Label)
			if opt.About != "" {
				fmt.Fprintf(&b, ": %s", opt.About)
			}
			b.WriteString("\n")
		}
	}
	answer := "Answer it with task_answer, or leave it to the user, who sees it too."
	if o.On != "" {
		answer = "It runs on " + o.On + ". Answer it with task_answer, or leave it to the user there, who sees it too."
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"waiting_on_question\" request=\"%s\">\n<task_question>\n%s</task_question>\n</task>\n%s",
		o.Report.Child, html.EscapeString(o.Report.Title), o.Question, b.String(), answer)
}

// wakeText gives the parent's agent a child's result, in the tags Zeron
// gives a task's: which child, how it ended, and what it said last. A
// child on a branch other than the parent's, in a worktree of its own,
// names it, for the parent to merge.
func wakeText(o Reported, parentBranch string) string {
	state := "completed"
	if o.Report.Kind == api.ReportFailed {
		state = "failed"
	}
	reply := cmp.Or(strings.TrimSpace(o.Reply), o.Report.Outcome)
	branch := ""
	if o.Branch != "" && o.Branch != parentBranch {
		branch = fmt.Sprintf(" branch=\"%s\"", html.EscapeString(o.Branch))
	}
	return fmt.Sprintf("<task id=\"%s\" title=\"%s\" state=\"%s\"%s>\n<task_result>\n%s\n</task_result>\n</task>",
		o.Report.Child, html.EscapeString(o.Report.Title), state, branch, reply)
}

// report has a child's run report to its parent, after the commit, as how
// it went, with the request a blocked run waits on. A run the user stopped
// reports nothing: they asked for it.
func (d *decision) report(run api.ID, kind api.ReportKind, failure string, ask *api.Request) {
	if d.t.Parent.IsZero() {
		return
	}
	r := ReportToParent{Parent: d.t.Parent, Child: d.t.ID, Run: run, Kind: kind, Title: d.t.Title, Error: failure, Branch: d.t.Branch}
	if ask != nil {
		r.Ask = ask.Body
		if ask.Kind == api.KindQuestion {
			r.Question = ask.ID
		}
	}
	d.effects = append(d.effects, r)
}

// uncertain is that nobody can prove what u's run's agent is doing. A
// child's parent hears it waits on the user, as it does of an approval,
// unless it was stopped: that was asked for.
func (d *decision) uncertain(u api.TurnUncertain) error {
	if err := d.emit(u); err != nil {
		return err
	}
	if u.Reason != api.UncertainStopped {
		d.report(u.Run, api.ReportBlocked, "", nil)
	}
	return nil
}

func (d *decision) rewound() error {
	// A run already uncertain is so again for the rewind, which no restart
	// continues: the history it was to go on from may be lost.
	switch live := d.t.Live(); {
	case live == nil:
	case live.State == RunUncertain:
		if err := d.emit(api.TurnUncertain{Run: live.ID, Reason: api.UncertainRewound}); err != nil {
			return err
		}
	default:
		if err := d.uncertain(api.TurnUncertain{Run: live.ID, Reason: api.UncertainRewound}); err != nil {
			return err
		}
	}
	var queued []api.ID
	for _, m := range d.t.Queue {
		if m.waiting() {
			queued = append(queued, m.ID)
		}
	}
	for _, id := range queued {
		if err := d.emit(api.MessageCancelled{Message: id, Reason: "the database was restored; send again if it never ran"}); err != nil {
			return err
		}
	}
	return nil
}
