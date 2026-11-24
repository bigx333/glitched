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
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// Tools are the tools glitched mcp serves, for Origin, on Engine.
type Tools struct {
	Engine api.Endpoint
	Origin Origin
	// Plugins reaches the tools of the plugins the engine runs; nil offers
	// none.
	Plugins PluginTools
	// Secrets asks the user for credentials; nil can't.
	Secrets Asker
	// Providers are the engine's providers, which a delegate that names a
	// provider, model or effort is checked against; nil checks nothing,
	// and refuses a provider other than the caller's.
	Providers Providers
	// Machines are the labels of the machines a child may be placed on,
	// as the engine listed them when the agent started.
	Machines []string
}

// Providers are the engine's providers, as provider.Mux serves them: each
// asked alone, so one that doesn't answer holds up no other.
type Providers interface {
	Describe(ctx context.Context, name string) (provider.Info, error)
	DefaultModel(ctx context.Context, name, workspace string) (string, error)
}

// providerAsk bounds what a delegate asks of a provider about itself.
const providerAsk = 20 * time.Second

// PluginTools are the engine's plugin tools, as transport.EndpointClient
// reaches them.
type PluginTools interface {
	PluginTools(ctx context.Context) (json.RawMessage, error)
	PluginTool(ctx context.Context, call json.RawMessage) (json.RawMessage, error)
}

const delegateTask = "delegate_task"

// delegateTool is delegate_task as an agent sees it, with machine offered
// when there are machines to place a child on, which it lists.
func delegateTool(machines []string) map[string]any {
	tool := delegateCatalog()
	if len(machines) == 0 {
		return tool
	}
	tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["machine"] = map[string]any{
		"type": "string", "enum": machines,
		"description": "Run the child on another machine, by its label: " + strings.Join(machines, ", ") + ". It works there in a worktree of its own, from this repository's branch as that machine has it, and its report comes here as a local child's does; what it asks is answered on that machine. Leave it out for a child on this machine.",
	}
	return tool
}

func delegateCatalog() map[string]any {
	return map[string]any{
		"name": delegateTask,
		"description": "Delegate a self-contained task to a child agent, in a new thread under this one. " +
			"The child starts at once and the call returns; each time its turn ends done or failed, its report comes to this thread as a new message, so do not wait or poll for it. Permissions inherit, and the folder too unless isolation is worktree, which gives the child a worktree and branch of its own from this folder's HEAD, without its uncommitted changes. Provider, model and effort inherit unless you pick them; a child on another provider starts at that provider's own default model and effort unless you pick them. " +
			"This thread's history is not copied, so the task must stand on its own. " +
			"The user sees the child and can open it. A child cannot gain broader permissions. " +
			"Reuse client_request_id to retry the same task safely; each new task needs a new one.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":              map[string]any{"type": "string", "minLength": 1, "maxLength": 120000, "description": "The child's first message: a complete brief."},
				"title":             map[string]any{"type": "string", "maxLength": 512, "description": "A short name for the child's thread."},
				"provider":          map[string]any{"type": "string", "description": "The child's provider, as claude or codex; else this thread's. An unknown one fails with the providers there are."},
				"model":             map[string]any{"type": "string", "description": "The child's model, by its provider's id for it; else this thread's on this thread's provider, and the provider's default on another. An unknown one fails with the ids there are."},
				"effort":            map[string]any{"type": "string", "description": "The child's reasoning effort; else this thread's on this thread's provider, and the model's default on another."},
				"isolation":         map[string]any{"type": "string", "enum": []string{"shared", "worktree"}, "default": "shared", "description": "shared: the child works in this thread's folder, uncommitted changes and all, so coordinate children that write. worktree: the child works in a git worktree of its own, on a new branch from this folder's HEAD; this folder's uncommitted changes are not copied. Its report names its branch: tell it to commit there, then merge the branch to use its work."},
				"client_request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
			"required":             []string{"task"},
			"additionalProperties": false,
		},
	}
}

type delegateArgs struct {
	Task      string `json:"task"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Isolation string `json:"isolation"`
	Machine   string `json:"machine"`
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
	if in.Isolation != "" && in.Isolation != "shared" && in.Isolation != "worktree" {
		return TaskView{}, fmt.Errorf("delegate_task: isolation is shared or worktree, not %q", in.Isolation)
	}
	if in.Machine != "" && in.Isolation == "shared" {
		return TaskView{}, errors.New("delegate_task: a child on another machine works in a worktree of its own there, so it can't share this folder")
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
	key := cmp.Or(in.RequestID, toolUse, api.NewID().String())
	child := derive(t.Origin.Thread, "thread", key)
	// An agent's access is fixed when it starts. One started at another
	// access than the live turn's serves an earlier turn, as one still
	// closing does: it may only find a child it already started.
	if turn.Access != t.Origin.Access {
		if th, task, ok := t.child(ctx, child); ok && th.Parent == t.Origin.Thread && task == in.Task {
			return TaskView{TaskID: child.String(), Title: th.Title, Status: "working"}, nil
		}
		return TaskView{}, errors.New("delegate_task: this agent was started for an earlier turn of the thread, so it can't delegate for the current one")
	}
	// A retry finds the child its first try made before anything about the
	// providers is asked again: what they say may have changed since.
	if th, task, ok := t.child(ctx, child); ok && th.Parent == t.Origin.Thread && task == in.Task {
		return TaskView{TaskID: child.String(), Title: th.Title, Status: "working"}, nil
	}
	model, err := t.childModel(ctx, turn.Model, parent.Workspace, in)
	if err != nil {
		return TaskView{}, err
	}
	title := cmp.Or(in.Title, firstLine(in.Task))
	create := api.ThreadCreate{
		Workspace: parent.Workspace,
		Project:   parent.Project,
		Branch:    parent.Branch,
		Title:     title,
		Model:     model,
		Access:    min(turn.Access, parent.Access),
		Parent:    t.Origin.Thread,
		Run:       turn.Run,
		Text:      in.Task,
	}
	switch {
	case in.Machine != "":
		create.Workspace, create.Project, create.Branch, create.Remote = "", "", "", in.Machine
	case in.Isolation == "worktree":
		create.Project, create.Branch, create.Worktree = "", "", &api.NewWorktree{}
	}
	_, err = t.Engine.Submit(ctx, api.Command{ID: derive(t.Origin.Thread, "call", key), Target: child, Body: create})
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

// childModel is the model a child of a turn at turn, in workspace, runs
// at, as in asks. On the turn's provider, what in leaves out is the
// turn's; on another, the turn's model, effort and Fast are that
// provider's to choose, so they are left out too and its program runs its
// own default, as a thread that follows it does. What in names is checked
// against what the provider offers, and an effort only where the provider
// says the model's efforts.
func (t *Tools) childModel(ctx context.Context, turn api.ModelRef, workspace string, in delegateArgs) (api.ModelRef, error) {
	m := turn
	if in.Provider != "" && in.Provider != turn.Provider {
		m = api.ModelRef{Provider: in.Provider}
	}
	m.Model, m.Effort = cmp.Or(in.Model, m.Model), cmp.Or(in.Effort, m.Effort)
	if in.Provider == "" && in.Model == "" && in.Effort == "" {
		return m, nil
	}
	if t.Providers == nil {
		if m.Provider != turn.Provider {
			return api.ModelRef{}, errors.New("delegate_task: this server can't start a child on another provider")
		}
		return m, nil
	}
	ctx, cancel := context.WithTimeout(ctx, providerAsk)
	defer cancel()
	info, err := t.Providers.Describe(ctx, m.Provider)
	if err != nil {
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s can't run a child: %w", m.Provider, err)
	}
	ids := func(n int, id func(int) string) string {
		out := make([]string, n)
		for i := range out {
			out[i] = id(i)
		}
		return strings.Join(out, ", ")
	}
	name := m.Model
	if name == "" {
		// The program's default where the child runs, as its settings
		// there may name one, else the one the provider marks.
		if name, err = t.Providers.DefaultModel(ctx, m.Provider, workspace); err != nil || name == "" {
			if i := slices.IndexFunc(info.Models, func(x provider.Model) bool { return x.Default }); i >= 0 {
				name = info.Models[i].ID
			}
		}
	}
	j := slices.IndexFunc(info.Models, func(x provider.Model) bool { return x.ID == name })
	switch {
	case in.Model != "" && len(info.Models) == 0:
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s lists no models, so it runs only its own default: leave model out", m.Provider)
	case in.Model != "" && j < 0:
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s has no model %q; its models are %s", m.Provider, in.Model, ids(len(info.Models), func(i int) string { return info.Models[i].ID }))
	case in.Effort == "":
		return m, nil
	case j < 0:
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s can't say which model it runs by default, so the effort %q can't be checked: name a model, or leave effort out", m.Provider, in.Effort)
	case len(info.Models[j].Efforts) == 0:
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s offers no efforts for %s, so it would run at its own: leave effort out", m.Provider, name)
	}
	efforts := info.Models[j].Efforts
	if !slices.ContainsFunc(efforts, func(e provider.Effort) bool { return e.ID == in.Effort }) {
		return api.ModelRef{}, fmt.Errorf("delegate_task: %s runs at no effort %q; its efforts are %s", name, in.Effort, ids(len(efforts), func(i int) string { return efforts[i].ID }))
	}
	return m, nil
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
		switch b, _ := e.Decode(); b := b.(type) {
		case api.ThreadCreated:
			if b.Remote != nil {
				return snap.Threads[i], b.Text, true
			}
		case api.MessageQueued:
			return snap.Threads[i], b.Text, true
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
