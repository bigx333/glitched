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
	"time"
	"unicode/utf8"

	"glitched/internal/api"
)

const (
	listTasks  = "list_tasks"
	taskStatus = "task_status"
	taskCancel = "task_cancel"
	taskSend   = "task_send"
	taskAnswer = "task_answer"
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
}, {
	"name": taskAnswer,
	"description": "Answer a question one task of this thread's waits on, as the user would from its card. Its question reaches this thread on its own with its request_id; task_status lists the ones open. " +
		"Give one answer per question, in order: the labels picked, your own words in other, or both. Only a question: a task's approval or credentials are the user's, and this refuses them, as it does for a task the user took over. " +
		"The user sees the question too, and whichever answer lands first is the one the task gets.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_id":    taskID,
			"request_id": map[string]any{"type": "string", "description": "The question's request_id, from the task's question or task_status."},
			"answers": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"picked": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Labels of the options picked; more than one only where the question says pick any."},
					"other":  map[string]any{"type": "string", "description": "An answer in your own words."},
				},
				"additionalProperties": false,
			}},
			"client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required":             []string{"task_id", "request_id", "answers"},
		"additionalProperties": false,
	},
}}

// TaskState is a child as task_status and list_tasks show it.
type TaskState struct {
	TaskID string `json:"task_id"`
	Title  string `json:"title"`
	// Status is working, waiting_on_user, stopping, uncertain, or, with no
	// turn running, how its last ended: done, stopped, failed, abandoned,
	// or idle before any; or lost, for a task on another machine this
	// one no longer reaches.
	Status string `json:"status"`
	// On is where a task on another machine runs, and when this machine
	// last heard from it there: its status is as of then.
	On     string `json:"on,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Working counts the threads under it at work.
	Working int `json:"threads_under_it_working,omitempty"`
	// Waiting is what it waits on the user for: approval or question.
	Waiting []string `json:"waiting_on_user_for,omitempty"`
	// Questions are the questions it waits on, which task_answer answers.
	Questions []TaskQuestion `json:"questions,omitempty"`
	Queued    int            `json:"messages_waiting,omitempty"`
	// Resumable is whether a task_send would start what a stop held: the
	// task is idle and holds it.
	Resumable  bool   `json:"resumable,omitempty"`
	LastReport string `json:"last_report,omitempty"`
}

// TaskQuestion is a question request a task waits on.
type TaskQuestion struct {
	RequestID string         `json:"request_id"`
	Questions []api.Question `json:"questions"`
}

type taskArgs struct {
	TaskID    string       `json:"task_id"`
	Message   string       `json:"message"`
	Mode      string       `json:"mode"`
	Request   string       `json:"request_id"`
	Answers   []api.Answer `json:"answers"`
	RequestID string       `json:"client_request_id"`
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
	if utf8.RuneCountInString(in.RequestID) > 256 {
		return nil, fmt.Errorf("%s: client_request_id is over 256 characters", name)
	}
	// An acting call is the turn's that runs as it starts.
	var caller api.Caller
	if name == taskCancel || name == taskSend || name == taskAnswer {
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
	case taskAnswer:
		return t.answer(ctx, caller, task, in, cmp.Or(in.RequestID, toolUse, api.NewID().String()))
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

// answer answers a question task waits on, a command of the origin's live
// run, retried as the same command under key. The engine refuses any
// other request, and a task the user took over. A request answered already
// is read from the task's log, so a retry of the answer that took it gets
// that answer's receipt, and any other answer is refused.
func (t *Tools) answer(ctx context.Context, caller api.Caller, task api.Thread, in taskArgs, key string) (any, error) {
	if in.Message != "" || in.Mode != "" {
		return nil, errors.New("task_answer: message and mode are task_send's; give answers")
	}
	missing := fmt.Errorf("task_answer: the task waits on no request %q; task_status lists what it waits on", in.Request)
	id, err := api.ParseID(in.Request)
	if err != nil {
		return nil, missing
	}
	if len(in.Answers) == 0 {
		return nil, errors.New("task_answer: no answers; to leave the question to the user, don't call this")
	}
	generation, found := uint64(0), false
	if i := slices.IndexFunc(task.Requests, func(r api.Request) bool { return r.ID == id }); i >= 0 {
		generation, found = task.Requests[i].Generation, true
	} else if generation, found, err = t.opened(ctx, task.ID, id); err != nil {
		return nil, err
	}
	if !found {
		return nil, missing
	}
	answer, err := json.Marshal(api.Answers{Answers: in.Answers})
	if err != nil {
		return nil, err
	}
	_, err = t.Engine.Submit(ctx, api.Command{ID: derive(t.Origin.Thread, "answer", key), Target: task.ID, Caller: &caller, Body: api.RequestRespond{Request: id, Generation: generation, Answer: answer}})
	if errors.Is(err, api.ErrAlreadyAnswered) {
		return nil, errors.New("task_answer: the question was already answered, by the user or an earlier call")
	}
	if errors.Is(err, api.ErrConflict) && in.RequestID != "" {
		return nil, fmt.Errorf("task_answer: client_request_id %q already answered something else from this thread; use a new client_request_id for a new answer", in.RequestID)
	}
	if err != nil {
		return nil, fmt.Errorf("task_answer: %w", refusal(err))
	}
	return map[string]any{"task_id": task.ID.String(), "answered": true}, nil
}

// opened is the generation of request in thread's log, if it opened there.
func (t *Tools) opened(ctx context.Context, thread, request api.ID) (uint64, bool, error) {
	snap, err := t.Engine.Read(ctx, api.Query{Thread: thread})
	if err != nil {
		return 0, false, err
	}
	for _, e := range snap.Events {
		if b, err := e.Decode(); err == nil {
			if o, ok := b.(api.RequestOpened); ok && o.Request == request {
				return o.Generation, true, nil
			}
		}
	}
	return 0, false, nil
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
		if q, err := api.ParseQuestions(r.Body); r.Kind == api.KindQuestion && err == nil {
			s.Questions = append(s.Questions, TaskQuestion{RequestID: r.ID.String(), Questions: q.Questions})
		}
	}
	for _, u := range under(threads, th.ID) {
		if u.Working() {
			s.Working++
		}
	}
	if r := th.Remote; r != nil {
		machine := cmp.Or(r.Machine, "another machine")
		s.On = "on " + machine + ", not heard from since this machine's engine started"
		if !r.Heard.IsZero() {
			s.On = "on " + machine + ", last heard at " + r.Heard.UTC().Format(time.RFC3339)
		}
		if r.Lost != "" {
			s.Status, s.On = "lost", "on "+machine+": "+r.Lost
			return s
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
