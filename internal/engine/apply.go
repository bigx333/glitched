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

// apply never writes through t's slices: callers keep the old state to diff.
func apply(t Thread, e Event) (Thread, error) {
	var err error
	switch b := e.Body.(type) {
	case api.AssistantDelta, api.WorkItem:
		// Transcript only: no projection changes, so no revision either.
		t.UpdatedAt = e.At
		return t, nil
	case api.ThreadCreated:
		if t.Exists() {
			return t, &TransitionError{"thread", "created", "created"}
		}
		t = Thread{ID: e.Thread, Workspace: b.Workspace, Title: b.Title, Model: b.Model, Access: b.Access, OwnerEpoch: 1, Parent: b.Parent}
	case api.DispositionChanged:
		t.Disposition = b.Disposition
	case api.ThreadRenamed:
		t.Title = b.Title
	case api.ThreadConfigured:
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
		state := QueueReady
		if b.Held {
			state = QueueHeld
		}
		t.Queue = append(slices.Clip(t.Queue), Message{ID: b.Message, Text: b.Text, Mode: b.Mode, Pos: b.Pos, State: state})
	case api.MessageClaimed:
		if r := t.Live(); r == nil || r.ID != b.Run || !steerable[r.State] {
			return t, &TransitionError{"message", "unclaimed", "claimed by a run that cannot be steered"}
		}
		err = t.claim(b.Message, b.Run)
	case api.MessageDispatched:
		err = t.setMessage(b.Message, QueueDispatched)
	case api.MessageCancelled:
		err = t.setMessage(b.Message, QueueCancelled)
	case api.MessageUncertain:
		err = t.setMessage(b.Message, QueueUncertain)
	case api.TurnAdmitted:
		if err = runEdges.check("run", t.slot(), RunStarting); err != nil {
			break
		}
		if err = t.claim(b.Run, b.Run); err != nil {
			break
		}
		t.Runs = append(slices.Clip(t.Runs), Run{ID: b.Run, Generation: b.Generation, Model: t.Model, Access: t.Access, State: RunStarting})
		t.Generation = b.Generation
	case api.TurnStarted:
		if err = t.setRun(b.Run, RunRunning); err == nil {
			err = t.setMessage(b.Run, QueueDispatched)
		}
	case api.TurnRestored:
		// A run restored past starting is unchanged, and so is the revision
		// clients hold.
		if i := t.runAt(b.Run); i < 0 || t.Runs[i].State != RunStarting {
			return t, nil
		}
		if err = t.setRun(b.Run, RunRunning); err == nil {
			err = t.setMessage(b.Run, QueueDispatched)
		}
	case api.TurnStopping:
		if err = t.setRun(b.Run, RunStopping); err == nil {
			err = t.voidRequests(b.Run)
		}
	case api.TurnSignalled:
		if r := t.Live(); r == nil || r.ID != b.Run || r.State != RunStopping {
			return t, &TransitionError{"run", "not stopping", "signalled"}
		}
		t.Runs = slices.Clone(t.Runs)
		t.Runs[len(t.Runs)-1].Signalled = true
	case api.TurnUncertain:
		if err = t.setRun(b.Run, RunUncertain); err == nil {
			err = t.release(b.Run, QueueUncertain)
		}
		if err == nil {
			err = t.voidRequests(b.Run)
		}
	case api.TurnEnded:
		if err = t.setRun(b.Run, RunFinished); err == nil {
			i := t.runAt(b.Run)
			t.Runs[i].Outcome, t.Runs[i].Error, t.Runs[i].Ended = b.Outcome, b.Error, e.At
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
	case api.RequestInvalidation:
		if err = t.setRequest(b.Request, api.RequestInvalidated); err == nil {
			err = t.resume(b.Request)
		}
	default:
		return t, nil
	}
	if err != nil {
		return t, err
	}
	t.Revision++
	t.UpdatedAt = e.At
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
