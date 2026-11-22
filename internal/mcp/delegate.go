package mcp

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"glitched/internal/api"
)

// Tools are the tools glitched mcp serves, for Origin, on Engine.
type Tools struct {
	Engine api.Endpoint
	Origin Origin
	// Plugins reaches the tools of the plugins the engine runs; nil offers
	// none.
	Plugins PluginTools
}

// PluginTools are the engine's plugin tools, as transport.EndpointClient
// reaches them.
type PluginTools interface {
	PluginTools(ctx context.Context) (json.RawMessage, error)
	PluginTool(ctx context.Context, call json.RawMessage) (json.RawMessage, error)
}

const delegateTask = "delegate_task"

var catalog = []map[string]any{{
	"name": delegateTask,
	"description": "Delegate a self-contained task to a child agent, in a new thread under this one. " +
		"The child starts at once and the call returns; each time its turn ends done or failed, its report comes to this thread as a new message, so do not wait or poll for it. Folder, permissions and model inherit unless you pick a model. " +
		"This thread's history is not copied, so the task must stand on its own. " +
		"The user sees the child and can open it. A child cannot gain broader permissions. " +
		"Reuse client_request_id to retry the same task safely; each new task needs a new one.",
	"inputSchema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task":              map[string]any{"type": "string", "minLength": 1, "maxLength": 120000, "description": "The child's first message: a complete brief."},
			"title":             map[string]any{"type": "string", "maxLength": 512, "description": "A short name for the child's thread."},
			"model":             map[string]any{"type": "string", "description": "The child's model, by the id this thread's agent knows it by; else this thread's."},
			"effort":            map[string]any{"type": "string", "description": "The child's reasoning effort; else this thread's."},
			"client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		},
		"required":             []string{"task"},
		"additionalProperties": false,
	},
}}

type delegateArgs struct {
	Task      string `json:"task"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	RequestID string `json:"client_request_id"`
}

// TaskView is a child as a tool call returns it.
type TaskView struct {
	TaskID string `json:"task_id"` // the child's thread
	Title  string `json:"title"`
	Status string `json:"status"`
}

// delegate starts one child of the origin's thread for one call. The
// child's thread and the create's command come from the call's id, the
// agent's or else Claude Code's own for the tool use, so a retry finds the
// child the first try made and starts nothing more.
func (t *Tools) delegate(ctx context.Context, raw json.RawMessage, toolUse string) (TaskView, error) {
	var in delegateArgs
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return TaskView{}, errors.New("delegate_task: " + err.Error())
	}
	if strings.TrimSpace(in.Task) == "" {
		return TaskView{}, errors.New("delegate_task: task is empty")
	}
	parent, err := t.thread(ctx)
	if err != nil {
		return TaskView{}, err
	}
	// The thread's live turn asks. The child inherits from it, at the model
	// and access it was admitted at: the thread's are its next turn's, and
	// they narrow the child only where the thread was narrowed since.
	turn := parent.Turn
	if turn == nil {
		return TaskView{}, errors.New("delegate_task: this thread has no turn running, so nothing can delegate for it")
	}
	// An agent's access is fixed when it starts. One started at another
	// access than the live turn's serves an earlier turn, as one still
	// closing does.
	if turn.Access != t.Origin.Access {
		return TaskView{}, errors.New("delegate_task: this agent was started for an earlier turn of the thread, so it can't delegate for the current one")
	}
	model, access := turn.Model, min(turn.Access, parent.Access)
	key := cmp.Or(in.RequestID, toolUse, api.NewID().String())
	child := derive(t.Origin.Thread, "thread", key)
	title := cmp.Or(in.Title, firstLine(in.Task))
	_, err = t.Engine.Submit(ctx, api.Command{
		ID:     derive(t.Origin.Thread, "call", key),
		Target: child,
		Body: api.ThreadCreate{
			Workspace: parent.Workspace,
			Title:     title,
			Model: api.ModelRef{
				Provider: model.Provider,
				Model:    cmp.Or(in.Model, model.Model),
				Effort:   cmp.Or(in.Effort, model.Effort),
				Fast:     model.Fast,
			},
			Access: access,
			Parent: t.Origin.Thread,
			Run:    turn.Run,
			Text:   in.Task,
		},
	})
	if errors.Is(err, api.ErrConflict) {
		// A retry of the same task finds the child the first try made,
		// though its command differs: a later turn's is another run's, and
		// a thread narrowed since narrows it.
		if th, task, ok := t.child(ctx, child); ok && th.Parent == t.Origin.Thread && task == in.Task {
			return TaskView{TaskID: child.String(), Title: th.Title, Status: "working"}, nil
		}
		if in.RequestID != "" {
			return TaskView{}, fmt.Errorf("delegate_task: client_request_id %q already started another task in this thread; use a new client_request_id for a new task", in.RequestID)
		}
	}
	if err != nil {
		return TaskView{}, refusal(err)
	}
	return TaskView{TaskID: child.String(), Title: title, Status: "working"}, nil
}

// thread is the origin's thread as the engine has it.
func (t *Tools) thread(ctx context.Context) (api.Thread, error) {
	snap, err := t.Engine.Read(ctx, api.Query{Thread: t.Origin.Thread})
	if err != nil {
		return api.Thread{}, err
	}
	for _, th := range snap.Threads {
		if th.ID == t.Origin.Thread {
			return th, nil
		}
	}
	return api.Thread{}, errors.New("this agent's thread is gone")
}

// child is the thread id as the engine has it, with its first message.
func (t *Tools) child(ctx context.Context, id api.ID) (api.Thread, string, bool) {
	snap, err := t.Engine.Read(ctx, api.Query{Thread: id})
	if err != nil {
		return api.Thread{}, "", false
	}
	i := slices.IndexFunc(snap.Threads, func(th api.Thread) bool { return th.ID == id })
	if i < 0 {
		return api.Thread{}, "", false
	}
	for _, e := range snap.Events {
		if b, err := e.Decode(); err == nil {
			if m, ok := b.(api.MessageQueued); ok {
				return snap.Threads[i], m.Text, true
			}
		}
	}
	return api.Thread{}, "", false
}

// refusal is the engine's reason, without the api's prefix, for the agent.
func refusal(err error) error {
	if r, ok := errors.AsType[*api.RejectedError](err); ok {
		return errors.New(r.Reason)
	}
	return err
}

// derive is the id of what the call with key makes under parent.
func derive(parent api.ID, what, key string) api.ID {
	sum := sha256.Sum256([]byte(parent.String() + "/delegate/" + what + "/" + key))
	return api.ID(sum[:16])
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60]) + "…"
	}
	return s
}
