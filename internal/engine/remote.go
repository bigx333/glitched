package engine

import (
	"slices"

	"glitched/internal/api"
)

// Follow folds events of a remote child's thread, as its machine's log
// has them, into r: from nothing when fresh, as a snapshot holds every
// event of the thread, and on from r's last run otherwise. It returns r
// as of the last event, its Cursor left to the caller, the reports the
// events hold that r hasn't delivered, and whether the thread was
// deleted there. A report follows the local rules: a run's end reports
// done or failed unless it was stopping, silent or uncertain, and a
// request reports the run blocked unless the run is silent. A worktree
// that wasn't made there reports failed: the child never runs. Settled
// there before its first run, it starts none. Its last message or stop
// there with no agent's thread behind it is its person's, which takes it
// over there, as a local child's user does.
func Follow(r Remote, events []api.Event, fresh bool) (Remote, []RemoteReport, bool, error) {
	if fresh {
		r.Run, r.Silent, r.Slot, r.Pending, r.Taken, r.Ended = api.ID{}, false, api.RunIdle, nil, false, nil
		r.Model, r.Access, r.Turn, r.Hold = api.ModelRef{}, 0, nil, api.Hold{}
	}
	// unheld is r's hold without message m, which no longer waits.
	unheld := func(m api.ID) {
		drop := func(ids []api.ID) []api.ID {
			return slices.DeleteFunc(slices.Clone(ids), func(x api.ID) bool { return x == m })
		}
		r.Hold.Messages, r.Hold.Wakes = drop(r.Hold.Messages), drop(r.Hold.Wakes)
	}
	var out []RemoteReport
	add := func(p RemoteReport) {
		if !slices.Contains(r.Reported, p.Key) && !slices.ContainsFunc(out, func(o RemoteReport) bool { return o.Key == p.Key }) {
			out = append(out, p)
		}
	}
	gone, first := false, false
	for _, e := range events {
		b, err := e.Decode()
		if err != nil {
			return r, nil, false, err
		}
		switch b := b.(type) {
		case api.ThreadCreated:
			r.Model, r.Access = b.Model, b.Access
			if b.Origin != nil {
				r.Slot, first = api.RunStarting, true
			}
		case api.DispositionChanged:
			if b.Disposition.State == api.Settled && r.Run.IsZero() && r.Slot == api.RunStarting {
				r.Slot = api.RunIdle
			}
		case api.WorktreeReady:
			// What starts there next is admitted after this, if anything.
			if r.Run.IsZero() && r.Slot == api.RunStarting {
				r.Slot = api.RunIdle
			}
		case api.WorktreeFailed:
			key := derive(r.Thread, "worktree")
			add(RemoteReport{Key: key, Run: key, Kind: api.ReportFailed, Error: "its worktree there wasn't made: " + b.Error})
			r.Slot = api.RunIdle
		case api.ThreadConfigured:
			r.Model, r.Access = b.Model, b.Access
		case api.TurnAdmitted:
			r.Run, r.Silent, r.Slot, r.Pending = b.Run, b.Silent, api.RunStarting, nil
			if !b.Silent {
				r.Hold.Stopped = false
			}
			r.Turn = &api.TurnResult{Run: b.Run, Model: r.Model, Access: r.Access}
		case api.TurnStarted:
			if b.Run == r.Run && r.Slot == api.RunStarting {
				r.Slot = api.RunRunning
			}
		case api.TurnRestored:
			if b.Run == r.Run && r.Slot == api.RunStarting {
				r.Slot = api.RunRunning
			}
		case api.RequestOpened:
			if b.Run != r.Run {
				break
			}
			r.Slot = api.RunWaiting
			r.Pending = append(slices.Clip(r.Pending), b.Request)
			if !r.Silent {
				p := RemoteReport{Key: b.Request, Run: b.Run, Kind: api.ReportBlocked, Ask: b.Body}
				if b.Kind == api.KindQuestion {
					p.Question = b.Request
				}
				add(p)
			}
		case api.RequestAnswer:
			r = r.settle(b.Request)
		case api.RequestInvalidation:
			r = r.settle(b.Request)
		case api.TurnStopping:
			if b.Run == r.Run {
				r.Slot = api.RunStopping
			}
		case api.TurnUncertain:
			if b.Run == r.Run {
				r.Slot = api.RunUncertain
			}
		case api.TurnSignalled:
			if b.Run == r.Run && !r.Silent {
				r.Hold.Stopped = true
			}
		case api.TurnEnded:
			if b.Run != r.Run {
				break
			}
			if !r.Silent && (b.Outcome == api.OutcomeInterrupted || b.Outcome == api.OutcomeAbandoned) {
				r.Hold.Stopped = true
			}
			if !r.Silent {
				end := api.TurnResult{Run: b.Run}
				if r.Turn != nil && r.Turn.Run == b.Run {
					end = *r.Turn
				}
				end.Outcome, end.Ended, end.Limit = b.Outcome, e.At, b.Limit
				r.Ended = &end
			}
			if r.Slot != api.RunStopping && r.Slot != api.RunUncertain && !r.Silent {
				switch b.Outcome {
				case api.OutcomeCompleted:
					add(RemoteReport{Key: b.Run, Run: b.Run, Kind: api.ReportDone})
				case api.OutcomeFailed:
					add(RemoteReport{Key: b.Run, Run: b.Run, Kind: api.ReportFailed, Error: b.Error})
				}
			}
			r.Slot, r.Pending = api.RunIdle, nil
		case api.ResumeCancelled:
			if r.Ended != nil && r.Ended.Run == b.Run {
				end := *r.Ended
				end.ResumeCancelled = true
				r.Ended = &end
			}
		case api.MessageQueued:
			// The create's first message is the origin's, though a log
			// before it was so named has it unnamed.
			if b.Report.IsZero() && !b.Silent && !first {
				r.Taken = b.By.IsZero()
			}
			first = false
			switch {
			case b.Held && b.Mode == api.Defer:
				r.Hold.Messages = append(slices.Clip(r.Hold.Messages), b.Message)
			case b.Held:
				r.Hold.Wakes = append(slices.Clip(r.Hold.Wakes), b.Message)
			}
		case api.MessageHeld:
			unheld(b.Message)
			r.Hold.Messages = append(slices.Clip(r.Hold.Messages), b.Message)
		case api.MessageReleased:
			unheld(b.Message)
		case api.MessageClaimed:
			unheld(b.Message)
		case api.MessageDispatched:
			unheld(b.Message)
		case api.MessageCancelled:
			unheld(b.Message)
		case api.StopAsked:
			r.Taken = b.By.IsZero()
			// A stop asked of a run that had ended takes at once.
			if b.Run == r.Run && r.Slot == api.RunIdle && !r.Silent {
				r.Hold.Stopped = true
			}
		case api.ThreadDeleted:
			gone = true
		}
	}
	return r, out, gone, nil
}

// settle is r with request no longer pending: it runs again once none is.
func (r Remote) settle(request api.ID) Remote {
	r.Pending = slices.DeleteFunc(slices.Clone(r.Pending), func(p api.ID) bool { return p == request })
	if r.Slot == api.RunWaiting && len(r.Pending) == 0 {
		r.Slot = api.RunRunning
	}
	return r
}
