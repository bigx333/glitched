package engine

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
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
		err = d.settle(d.t.UpdatedAt)
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
	case api.SweepRun:
		err = d.runSweep(id, b)
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

type decision struct {
	t       Thread
	thread  api.ID
	at      time.Time
	events  []Event
	effects []Effect
	resumed bool   // the user resumed the queue: settle starts the wakes after a stop
	by      api.ID // the thread whose agent sent the command; zero for the user
	env     Env
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
	return d.admit(id, d.t.UpdatedAt)
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
