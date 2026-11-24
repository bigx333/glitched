package mcp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"glitched/internal/api"
)

const (
	listTasks  = "list_tasks"
	taskStatus = "task_status"
	taskCancel = "task_cancel"
	taskSend   = "task_send"
)

var taskID = map[string]any{"type": "string", "description": "The task_id delegate_task or list_tasks gave: a child thread of this one."}

var taskCatalog = []map[string]any{{
	"name":        listTasks,
	"description": "List the tasks delegated from this thread, each with its task_id, title and status. A task's report reaches this thread on its own each time its turn ends, so use this to find a task, not to wait for one.",
	"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
}, {
	"name": taskStatus,
	"description": "Read one task of this thread's: whether it is working, waiting on the user, or ended and how, how many threads under it are at work, what it waits on, and its last report here. " +
		"Its report reaches this thread on its own when its turn ends: do not poll this.",
	"inputSchema": map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"task_id": taskID},
		"required":             []string{"task_id"},
		"additionalProperties": false,
	},
}, {
	"name": taskCancel,
	"description": "Stop one task of this thread's and every thread under it, in one step; a thread under it the user took over stays theirs. Their waiting messages are held, not dropped, and a task_send to the task releases them; the threads and any worktree stay. A stopped turn sends no report. " +
		"A task the user took over (its last message or stop was the user's) is theirs to stop, and this refuses.",
	"inputSchema": map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"task_id": taskID, "client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
		"required":             []string{"task_id"},
		"additionalProperties": false,
	},
}, {
	"name": taskSend,
	"description": "Send a message to one task of this thread's, as its next brief or a correction. By default it waits behind the task's running turn; steer puts it into that turn. It also starts what a task_cancel held. The task's next report reaches this thread on its own. " +
		"A task the user took over (its last message or stop was the user's) is theirs to steer, and this refuses. Reuse client_request_id to retry the same message safely.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":           taskID,
			"message":           map[string]any{"type": "string", "minLength": 1, "maxLength": 120000},
			"mode":              map[string]any{"type": "string", "enum": []string{"queue", "steer"}, "default": "queue", "description": "queue: after the task's running turn, or at once when it has none. steer: into its running turn."},
			"client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required":             []string{"task_id", "message"},
		"additionalProperties": false,
	},
}}

// TaskState is a child as task_status and list_tasks show it.
type TaskState struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	// Status is working, waiting_on_user, stopping, uncertain, or, with no
	// turn running, how its last ended: done, stopped, failed, abandoned,
	// or idle before any.
	Status string `json:"status"`
	Branch string `json:"branch,omitempty"`
	// Working counts the threads under it at work.
	Working int `json:"threads_under_it_working,omitempty"`
	// Waiting is what it waits on the user for: approval or question.
	Waiting []string `json:"waiting_on_user_for,omitempty"`
	Queued  int      `json:"messages_waiting,omitempty"`
	// Resumable is whether a task_send would start what a stop held: the
	// task is idle and holds it.
	Resumable  bool   `json:"resumable,omitempty"`
	LastReport string `json:"last_report,omitempty"`
}

type taskArgs struct {
	TaskID    string `json:"task_id"`
	Message   string `json:"message"`
	Mode      string `json:"mode"`
	RequestID string `json:"client_request_id"`
}

// tasks runs one task tool for the origin's thread.
func (t *Tools) tasks(ctx context.Context, name string, raw json.RawMessage, toolUse string) (any, error) {
	var in taskArgs
	var err error
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	// An acting call is the turn's that runs as it starts.
	var caller api.Caller
	if name == taskCancel || name == taskSend {
		if caller, err = t.caller(ctx, name); err != nil {
			return nil, err
		}
	}
	snap, err := t.Engine.Read(ctx, api.Query{})
	if err != nil {
		return nil, err
	}
	reports, err := t.reports(ctx)
	if err != nil {
		return nil, err
	}
	if name == listTasks {
		out := []TaskState{}
		for _, th := range snap.Threads {
			if th.Parent == t.Origin.Thread {
				out = append(out, state(th, snap.Threads, reports[th.ID]))
			}
		}
		return map[string]any{"tasks": out}, nil
	}
	id, err := api.ParseID(in.TaskID)
	i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return th.ID == id })
	if err != nil || i < 0 || snap.Threads[i].Parent != t.Origin.Thread {
		return nil, fmt.Errorf("%s: no task %q under this thread; list_tasks lists them", name, in.TaskID)
	}
	task := snap.Threads[i]
	switch name {
	case taskStatus:
		return state(task, snap.Threads, reports[task.ID]), nil
	case taskCancel:
		return t.cancel(ctx, caller, task, reports, cmp.Or(in.RequestID, toolUse, api.NewID().String()))
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, errors.New("task_send: message is empty")
	}
	mode := api.AfterActive
	switch in.Mode {
	case "", "queue":
	case "steer":
		mode = api.SteerActive
	default:
		return nil, fmt.Errorf("task_send: mode is queue or steer, not %q", in.Mode)
	}
	key := cmp.Or(in.RequestID, toolUse, api.NewID().String())
	_, err = t.Engine.Submit(ctx, api.Command{ID: derive(t.Origin.Thread, "send", key), Target: task.ID, Caller: &caller, Body: api.MessageDispatch{Text: in.Message, Mode: mode}})
	if errors.Is(err, api.ErrConflict) && in.RequestID != "" {
		return nil, fmt.Errorf("task_send: client_request_id %q already sent another message from this thread; use a new client_request_id for a new message", in.RequestID)
	}
	if err != nil {
		return nil, fmt.Errorf("task_send: %w", refusal(err))
	}
	return map[string]any{"task_id": task.ID.String(), "sent": true}, nil
}

// cancel stops task's run and every run under it in one commit, a
// command of the origin's live run, retried as the same command under key.
// What the user took over stays theirs.
func (t *Tools) cancel(ctx context.Context, caller api.Caller, task api.Thread, reports map[api.ID]string, key string) (TaskState, error) {
	if _, err := t.Engine.Submit(ctx, api.Command{ID: derive(t.Origin.Thread, "cancel", key), Target: task.ID, Caller: &caller, Body: api.TurnInterrupt{Tree: true}}); err != nil {
		return TaskState{}, fmt.Errorf("task_cancel: %w", refusal(err))
	}
	snap, err := t.Engine.Read(ctx, api.Query{})
	if err != nil {
		return TaskState{}, err
	}
	if i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return th.ID == task.ID }); i >= 0 {
		return state(snap.Threads[i], snap.Threads, reports[task.ID]), nil
	}
	return TaskState{}, errors.New("task_cancel: the task is gone")
}

// caller is the origin's live run, which a command to a task comes from.
// An agent started at another access than the live turn's serves an
// earlier turn, and acts for none.
func (t *Tools) caller(ctx context.Context, name string) (api.Caller, error) {
	th, err := t.thread(ctx)
	if err != nil {
		return api.Caller{}, err
	}
	if th.Turn == nil {
		return api.Caller{}, fmt.Errorf("%s: this thread has no turn running, so nothing can act for it", name)
	}
	if th.Turn.Access != t.Origin.Access {
		return api.Caller{}, fmt.Errorf("%s: this agent was started for an earlier turn of the thread, so it can't act for the current one", name)
	}
	return api.Caller{Thread: t.Origin.Thread, Run: th.Turn.Run}, nil
}

// reports is each child's last report in the origin's log, in a line.
func (t *Tools) reports(ctx context.Context) (map[api.ID]string, error) {
	snap, err := t.Engine.Read(ctx, api.Query{Thread: t.Origin.Thread})
	if err != nil {
		return nil, err
	}
	out := map[api.ID]string{}
	for _, e := range snap.Events {
		b, err := e.Decode()
		r, ok := b.(api.ChildReported)
		if err != nil || !ok {
			continue
		}
		kind := map[api.ReportKind]string{api.ReportDone: "done", api.ReportFailed: "failed", api.ReportBlocked: "waiting on the user"}[r.Kind]
		out[r.Child] = strings.TrimSpace(kind + ": " + r.Outcome)
	}
	return out, nil
}

// state is th as a task, among threads, with report its last.
func state(th api.Thread, threads []api.Thread, report string) TaskState {
	s := TaskState{TaskID: th.ID.String(), Title: th.Title, Branch: th.Branch, Queued: th.Queued, Resumable: th.Resumable, LastReport: report}
	for _, r := range th.Requests {
		s.Waiting = append(s.Waiting, r.Kind)
	}
	for _, u := range under(threads, th.ID) {
		if u.Working() {
			s.Working++
		}
	}
	switch {
	case th.Run == api.RunWaiting || len(th.Requests) > 0:
		s.Status = "waiting_on_user"
	case th.Run == api.RunStopping:
		s.Status = "stopping"
	case th.Run == api.RunUncertain:
		s.Status = "uncertain"
	case th.Working():
		s.Status = "working"
	case th.LastTurn == nil:
		s.Status = "idle"
	default:
		s.Status = map[api.Outcome]string{api.OutcomeCompleted: "done", api.OutcomeInterrupted: "stopped", api.OutcomeFailed: "failed", api.OutcomeAbandoned: "abandoned"}[th.LastTurn.Outcome]
	}
	return s
}

// under is every thread below top among threads, each before those below
// it, as far down as api.MaxDepth.
func under(threads []api.Thread, top api.ID) []api.Thread {
	var out []api.Thread
	level := []api.ID{top}
	for range api.MaxDepth {
		var next []api.ID
		for _, th := range threads {
			if slices.Contains(level, th.Parent) {
				out = append(out, th)
				next = append(next, th.ID)
			}
		}
		if len(next) == 0 {
			break
		}
		level = next
	}
	return out
}
