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
// request reports the run blocked unless the run is silent.
func Follow(r Remote, events []api.Event, fresh bool) (Remote, []RemoteReport, bool, error) {
	if fresh {
		r.Run, r.Silent, r.Slot = api.ID{}, false, api.RunIdle
	}
	var out []RemoteReport
	add := func(p RemoteReport) {
		if !slices.Contains(r.Reported, p.Key) && !slices.ContainsFunc(out, func(o RemoteReport) bool { return o.Key == p.Key }) {
			out = append(out, p)
		}
	}
	gone := false
	for _, e := range events {
		b, err := e.Decode()
		if err != nil {
			return r, nil, false, err
		}
		switch b := b.(type) {
		case api.TurnAdmitted:
			r.Run, r.Silent, r.Slot = b.Run, b.Silent, api.RunStarting
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
			if !r.Silent {
				p := RemoteReport{Key: b.Request, Run: b.Run, Kind: api.ReportBlocked, Ask: b.Body}
				if b.Kind == api.KindQuestion {
					p.Question = b.Request
				}
				add(p)
			}
		case api.RequestAnswer, api.RequestInvalidation:
			if r.Slot == api.RunWaiting {
				r.Slot = api.RunRunning
			}
		case api.TurnStopping:
			if b.Run == r.Run {
				r.Slot = api.RunStopping
			}
		case api.TurnUncertain:
			if b.Run == r.Run {
				r.Slot = api.RunUncertain
			}
		case api.TurnEnded:
			if b.Run != r.Run {
				break
			}
			if r.Slot != api.RunStopping && r.Slot != api.RunUncertain && !r.Silent {
				switch b.Outcome {
				case api.OutcomeCompleted:
					add(RemoteReport{Key: b.Run, Run: b.Run, Kind: api.ReportDone})
				case api.OutcomeFailed:
					add(RemoteReport{Key: b.Run, Run: b.Run, Kind: api.ReportFailed, Error: b.Error})
				}
			}
			r.Slot = api.RunIdle
		case api.ThreadDeleted:
			gone = true
		}
	}
	return r, out, gone, nil
}
