package engine

import (
	"fmt"
	"slices"
	"time"

	"glitched/internal/api"
)

type Event struct {
	Thread api.ID
	Seq    uint64 // its place in the log; zero while Decide drafts it
	At     time.Time
	Body   api.EventBody
}

// Apply folds one event into t. It panics on an edge the tables refuse,
// because the log only holds events Decide or Observe already folded.
func Apply(t Thread, e Event) Thread {
	t, err := apply(t, e)
	if err != nil {
		panic(err)
	}
	return t
}

// subagentRun is the run a native subagent's event is under.
func subagentRun(b api.EventBody) api.ID {
	switch b := b.(type) {
	case api.SubagentDelta:
		return b.Run
	case api.SubagentWork:
		return b.Run
	case api.SubagentEnded:
		return b.Run
	}
	return api.ID{}
}

// apply never writes through t's slices: callers keep the old state to diff.
func apply(t Thread, e Event) (Thread, error) {
	var err error
	var silent bool
	switch b := e.Body.(type) {
	case api.AssistantDelta:
		// Transcript only: no projection changes, so no revision either.
		if !t.silentRun(b.Run) {
			t.UpdatedAt = e.At
		}
		return t, nil
	case api.WorkItem:
		// Transcript only: no projection changes, so no revision either.
		if !t.silentRun(b.Run) {
			t.UpdatedAt = e.At
		}
		return t, nil
	case api.SubagentDelta, api.SubagentWork, api.SubagentEnded:
		// Transcript only, as a work item is, and never the run's own.
		if run := subagentRun(b); !t.silentRun(run) {
			t.UpdatedAt = e.At
		}
		return t, nil
	case api.PlanUpdated:
		// Transcript only, as a work item is.
		if !t.silentRun(b.Run) {
			t.UpdatedAt = e.At
		}
		return t, nil
	case api.CheckpointCaptured:
		// Transcript only, and the engine's own doing: nothing the user
		// sees changes, so neither revision nor UpdatedAt does.
		return t, nil
	case api.ThreadCreated:
		if t.Exists() {
			return t, &TransitionError{"thread", "created", "created"}
		}
		t = Thread{ID: e.Thread, Workspace: b.Workspace, Project: b.Project, Branch: b.Branch, Title: b.Title, Model: b.Model, Access: b.Access, OwnerEpoch: 1, Parent: b.Parent}
		if r := b.Remote; r != nil {
			t.Remote = &Remote{Engine: r.Engine, Link: r.Link, State: RemoteCreating}
		}
		if o := b.Origin; o != nil {
			t.Origin = &Origin{Origin: *o, Making: true}
		}
	case api.WorktreeReady, api.WorktreeFailed:
		if t.Origin == nil || !t.Origin.Making {
			return t, &TransitionError{"worktree", "made", api.EventType(b)}
		}
		o := *t.Origin
		o.Making = false
		t.Origin = &o
		if r, ok := b.(api.WorktreeReady); ok {
			t.Workspace, t.Branch = r.Workspace, r.Branch
		}
	case api.DispositionChanged:
		t.Disposition = b.Disposition
	case api.ThreadRenamed:
		t.Title = b.Title
	case api.SessionBound:
		silent = t.silentRun(b.Run)
		// Another conversation holds none of what the last one did.
		if t.Session != "" && b.Session != t.Session {
			t.Context = api.Context{}
		}
		t.Session = b.Session
	case api.ContextReported:
		silent = t.silentRun(b.Run)
		// A report that leaves the window out keeps the one known, if it
		// names the same model: another's, or an unnamed one's, is not
		// this one's.
		c := api.Context{Used: b.Used, Window: b.Window, Model: b.Model, CacheExpires: b.CacheExpires}
		switch {
		case b.Compacted:
			c, c.Stale, c.CacheExpires = t.Context, true, time.Time{}
		case b.Window == 0 && b.Model != "" && b.Model == t.Context.Model:
			c.Window = t.Context.Window
		}
		t.Context = c
	case api.AccessGranted:
		if i := t.runAt(b.Run); i >= 0 {
			t.Confirmed.Access = &api.ConfirmedAccess{Run: b.Run, Asked: t.Runs[i].Access, Access: b.Access, Mode: b.Mode, Why: b.Why}
		}
	case api.ModelConfirmed:
		if i := t.runAt(b.Run); i >= 0 {
			t.Confirmed.Model = &api.ConfirmedModel{Run: b.Run, Asked: t.Runs[i].Model, Model: b.Model, Effort: b.Effort, Why: b.Why}
		}
	case api.ThreadConfigured:
		// The window and prompt cache known were the last model's, for a
		// report that leaves its model out.
		if b.Model.Provider != t.Model.Provider || b.Model.Model != t.Model.Model {
			t.Context.Window = 0
			t.Context.CacheExpires = time.Time{}
		}
		t.Model, t.Access = b.Model, b.Access
	case api.ChildReported:
		t.Reported = e.At
		t.LastReport = &api.Report{Child: b.Child, Kind: b.Kind, Cursor: api.Cursor{Seq: e.Seq}}
	case api.ThreadDeleted:
		if t.Live() != nil {
			return t, &TransitionError{"thread", "running", "deleted"}
		}
		t.Deleted = true
	case api.MessageQueued:
		silent = b.Silent
		state := QueueReady
		if b.Held {
			state = QueueHeld
		}
		t.Queue = append(slices.Clip(t.Queue), Message{ID: b.Message, Text: b.Text, Mode: b.Mode, Pos: b.Pos, State: state})
	case api.MessageClaimed:
		if r := t.Live(); r == nil || r.ID != b.Run || !steerable[r.State] {
			return t, &TransitionError{"message", "unclaimed", "claimed by a run that cannot be steered"}
		}
		silent = t.silentRun(b.Run)
		if err = t.claim(b.Message, b.Run); err == nil && b.Steer {
			t.Queue[t.messageAt(b.Message)].Mode = api.SteerActive
		}
	case api.MessageDispatched:
		if m, ok := t.Message(b.Message); ok {
			silent = t.silentRun(m.Run)
		}
		err = t.setMessage(b.Message, QueueDispatched)
	case api.MessageCancelled:
		if m, ok := t.Message(b.Message); ok {
			silent = t.silentRun(m.Run)
		}
		err = t.setMessage(b.Message, QueueCancelled)
	case api.MessageHeld:
		if err = t.setMessage(b.Message, QueueHeld); err == nil {
			t.Queue[t.messageAt(b.Message)].Mode = api.Defer
		}
	case api.MessageReleased:
		// Only a client's held message is released: a wake is held in
		// AfterActive until a turn takes it.
		if m, ok := t.Message(b.Message); ok && m.Mode != api.Defer {
			return t, &TransitionError{"message", "a wake", "released"}
		}
		if err = t.setMessage(b.Message, QueueReady); err == nil {
			t.Queue[t.messageAt(b.Message)].Mode = api.AfterActive
		}
	case api.MessageEdited:
		var i int
		if i, err = t.waitingAt(b.Message, "edited"); err == nil {
			t.Queue = slices.Clone(t.Queue)
			t.Queue[i].Text = b.Text
		}
	case api.MessageMoved:
		var i int
		if i, err = t.waitingAt(b.Message, "moved"); err == nil {
			t.Queue = slices.Clone(t.Queue)
			t.Queue[i].Pos = b.Pos
		}
	case api.MessageUncertain:
		if m, ok := t.Message(b.Message); ok {
			silent = t.silentRun(m.Run)
		}
		err = t.setMessage(b.Message, QueueUncertain)
	case api.TurnAdmitted:
		if err = runEdges.check("run", t.slot(), RunStarting); err != nil {
			break
		}
		if err = t.claim(b.Run, b.Run); err != nil {
			break
		}
		silent = b.Silent
		t.Runs = append(slices.Clip(t.Runs), Run{ID: b.Run, Generation: b.Generation, Model: t.Model, Access: t.Access, Silent: b.Silent, State: RunStarting})
		t.Generation = b.Generation
	case api.TurnStarted:
		silent = t.silentRun(b.Run)
		if err = t.setRun(b.Run, RunRunning); err == nil {
			err = t.setMessage(b.Run, QueueDispatched)
		}
	case api.TurnRestored:
		// A run restored past starting is unchanged, and so is the revision
		// clients hold.
		if i := t.runAt(b.Run); i < 0 || t.Runs[i].State != RunStarting {
			return t, nil
		}
		silent = t.silentRun(b.Run)
		if err = t.setRun(b.Run, RunRunning); err == nil {
			err = t.setMessage(b.Run, QueueDispatched)
		}
	case api.TurnStopping:
		silent = t.silentRun(b.Run)
		if err = t.setRun(b.Run, RunStopping); err == nil {
			err = t.voidRequests(b.Run)
		}
	case api.TurnSignalled:
		if r := t.Live(); r == nil || r.ID != b.Run || r.State != RunStopping {
			return t, &TransitionError{"run", "not stopping", "signalled"}
		}
		silent = t.silentRun(b.Run)
		t.Runs = slices.Clone(t.Runs)
		t.Runs[len(t.Runs)-1].Signalled = true
	case api.TurnUncertain:
		silent = t.silentRun(b.Run)
		// A run uncertain already is only said to be so again, for a
		// rewind: nothing about the thread is newer.
		if i := t.runAt(b.Run); i >= 0 && t.Runs[i].State == RunUncertain {
			silent = true
			break
		}
		if err = t.setRun(b.Run, RunUncertain); err == nil {
			err = t.release(b.Run, QueueUncertain)
		}
		if err == nil {
			err = t.voidRequests(b.Run)
		}
	case api.TurnEnded:
		silent = t.silentRun(b.Run)
		if err = t.setRun(b.Run, RunFinished); err == nil {
			i := t.runAt(b.Run)
			t.Runs[i].Outcome, t.Runs[i].Error, t.Runs[i].Ended, t.Runs[i].Limit = b.Outcome, b.Error, e.At, b.Limit
			t.Tokens += b.Tokens
			err = t.release(b.Run, QueueCancelled)
		}
		if err == nil {
			err = t.voidRequests(b.Run)
		}
	case api.RequestOpened:
		if r := t.Live(); r == nil || r.ID != b.Run {
			return t, &TransitionError{"request", "none", "opened on a run that is not live"}
		} else if r.State != RunWaiting {
			if err = t.setRun(b.Run, RunWaiting); err != nil {
				break
			}
		}
		t.Requests = append(slices.Clip(t.Requests), Request{
			ID: b.Request, Run: b.Run, Generation: b.Generation, Dedup: b.Dedup, Kind: b.Kind, Body: b.Body, State: api.RequestPending,
		})
	case api.RequestAnswer:
		if err = t.setRequest(b.Request, api.RequestAnswered); err == nil {
			t.Requests[t.requestAt(b.Request)].Answer = b.Answer
			err = t.resume(b.Request)
		}
	case api.RequestDelivery:
		err = t.setRequest(b.Request, api.RequestDelivered)
	case api.ThreadReverting, api.ThreadReverted:
		// A revert of the files changes no field of the thread's; it moves
		// the revision, for clients to read it, and for the runtime's
		// commit hook to see the thread whose folder it marks. One that
		// rewound the conversation gives the thread another, which holds
		// none of what the last one did, nor the runs it dropped; one that
		// undid a rewind holds the runs that rewind dropped again. The lane
		// runs the revert before a run admitted since, which no agent has
		// begun yet, whether it is still starting or already stopping.
		if r, ok := b.(api.ThreadReverted); ok && r.Rewound() {
			if live := t.Live(); live != nil && live.State != RunStarting && live.State != RunStopping {
				return t, &TransitionError{"thread", "reverted", "rewound under a run at work"}
			}
			t.Session, t.Context = r.Session, api.Context{}
			t.Runs = slices.Clone(t.Runs)
			for i := range t.Runs {
				t.Runs[i].Dropped = (t.Runs[i].Dropped || slices.Contains(r.Dropped, t.Runs[i].ID)) && !slices.Contains(r.Restored, t.Runs[i].ID)
			}
		}
	case api.RequestInvalidation:
		if err = t.setRequest(b.Request, api.RequestInvalidated); err == nil {
			err = t.resume(b.Request)
		}
	case api.ResumeCancelled:
		r, ok := t.lastTurn()
		if !ok || r.ID != b.Run || r.Limit == nil {
			return t, &TransitionError{"resume", "none", "cancelled on a turn no usage limit ended"}
		}
		t.Runs = slices.Clone(t.Runs)
		t.Runs[t.runAt(b.Run)].ResumeCancelled = true
	default:
		return t, nil
	}
	if err != nil {
		return t, err
	}
	t.Revision++
	if !silent {
		t.UpdatedAt = e.At
	}
	return t, nil
}

func (t *Thread) setRun(id api.ID, to RunState) error {
	i := t.runAt(id)
	if i < 0 {
		return fmt.Errorf("engine: unknown run %s", id)
	}
	if err := runEdges.check("run", t.Runs[i].State, to); err != nil {
		return err
	}
	t.Runs = slices.Clone(t.Runs)
	t.Runs[i].State = to
	return nil
}

func (t *Thread) setMessage(id api.ID, to QueueState) error {
	i := t.messageAt(id)
	if i < 0 {
		return fmt.Errorf("engine: unknown message %s", id)
	}
	if err := queueEdges.check("message", t.Queue[i].State, to); err != nil {
		return err
	}
	t.Queue = slices.Clone(t.Queue)
	t.Queue[i].State = to
	return nil
}

// waitingAt is where the message of the user's that waits its turn is in
// the queue. The tables can't fence a change that leaves a message's state
// as it is, so this does: one claimed, sent or cancelled, or a wake, is
// neither edited nor moved.
func (t Thread) waitingAt(id api.ID, to string) (int, error) {
	i := t.messageAt(id)
	if i < 0 {
		return -1, fmt.Errorf("engine: unknown message %s", id)
	}
	if !t.Queue[i].waiting() {
		return -1, &TransitionError{"message", t.Queue[i].State, to}
	}
	return i, nil
}

func (t *Thread) setRequest(id api.ID, to api.RequestState) error {
	i := t.requestAt(id)
	if i < 0 {
		return fmt.Errorf("engine: unknown request %s", id)
	}
	if err := requestEdges.check("request", t.Requests[i].State, to); err != nil {
		return err
	}
	t.Requests = slices.Clone(t.Requests)
	t.Requests[i].State = to
	return nil
}

func (t *Thread) claim(message, run api.ID) error {
	if err := t.setMessage(message, QueueClaimed); err != nil {
		return err
	}
	t.Queue[t.messageAt(message)].Run = run
	return nil
}

func (t *Thread) release(run api.ID, to QueueState) error {
	for _, m := range t.Queue {
		if m.Run == run && m.State == QueueClaimed {
			if err := t.setMessage(m.ID, to); err != nil {
				return err
			}
		}
	}
	return nil
}

// voidRequests invalidates the questions the run can no longer be answered
// on. An answer already given is left to its delivery.
func (t *Thread) voidRequests(run api.ID) error {
	for _, r := range t.Requests {
		if r.Run == run && r.State == api.RequestPending {
			if err := t.setRequest(r.ID, api.RequestInvalidated); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Thread) resume(request api.ID) error {
	run := t.Requests[t.requestAt(request)].Run
	if i := t.runAt(run); i >= 0 && t.Runs[i].State == RunWaiting && !t.openRequests(run) {
		return t.setRun(run, RunRunning)
	}
	return nil
}
