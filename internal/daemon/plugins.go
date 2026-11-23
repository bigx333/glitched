package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/plugins"
)

// pluginServer serves the plugins' services and tools over the engine's
// unix socket, as JSON.
type pluginServer struct{ s *plugins.Services }

func (p pluginServer) PluginTools(ctx context.Context) (json.RawMessage, error) {
	tools, err := p.s.Tools(ctx)
	if err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []plugins.ToolInfo{}
	}
	return json.Marshal(tools)
}

func (p pluginServer) PluginTool(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var c plugins.ToolCall
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return p.s.CallTool(ctx, c)
}

func (p pluginServer) PluginCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var c plugins.ServiceCall
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return p.s.Call(ctx, c)
}

// startPlugins runs the plugins' services, whose effects act on the
// engine itself.
func (d *Daemon) startPlugins(dir string, run func(ctx context.Context, id string, allowed []string, args json.RawMessage) (any, error)) *plugins.Services {
	return plugins.StartServices(dir, plugins.ServiceOptions{
		Logf: func(format string, args ...any) { slog.Info(fmt.Sprintf(format, args...)) },
		Do:   d.pluginEffect,
		Run:  run,
	})
}

// pluginEffect is what a service may do, with no window: read, start and
// message threads. The host checked its grant.
func (d *Daemon) pluginEffect(_ string, e plugins.Effect) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ep := d.Endpoint()
	switch e.Type {
	case "threads.list":
		snap, err := ep.Read(ctx, api.Query{})
		if err != nil {
			return nil, err
		}
		out := []plugins.ThreadRecord{}
		for _, t := range snap.Threads {
			out = append(out, plugins.ThreadRecord{ID: t.ID.String(), Title: t.Title, Workspace: t.Workspace, Status: status(t), UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339), Local: true})
		}
		slices.SortFunc(out, func(x, y plugins.ThreadRecord) int { return strings.Compare(y.UpdatedAt, x.UpdatedAt) })
		return out, nil
	case "threads.create":
		var a struct{ Prompt, Title, Workspace string }
		if err := json.Unmarshal(e.Args, &a); err != nil || strings.TrimSpace(a.Prompt) == "" {
			return nil, errors.New("threads.create needs a prompt")
		}
		ws := a.Workspace
		if ws == "" {
			ws, _ = os.UserHomeDir()
		}
		if !filepath.IsAbs(ws) {
			return nil, fmt.Errorf("workspace %q is not an absolute path", ws)
		}
		// The model and access of the thread the user worked in last: a
		// service has no composer to read them from.
		snap, err := ep.Read(ctx, api.Query{})
		if err != nil {
			return nil, err
		}
		var last api.Thread
		for _, t := range snap.Threads {
			if t.Parent.IsZero() && t.UpdatedAt.After(last.UpdatedAt) {
				last = t
			}
		}
		if last.Model.Provider == "" {
			return nil, errors.New("no thread yet to take a model from: start one first")
		}
		id := api.NewID()
		title := a.Title
		if title == "" {
			title = a.Prompt
		}
		if r := []rune(title); len(r) > 48 {
			title = string(r[:48]) + "…"
		}
		for _, body := range []api.Intent{api.ThreadCreate{Workspace: ws, Title: title, Model: last.Model, Access: last.Access}, api.MessageDispatch{Text: a.Prompt, Mode: api.SteerActive}} {
			if _, err := ep.Submit(ctx, api.Command{ID: api.NewID(), Target: id, Body: body}); err != nil {
				return nil, err
			}
		}
		return map[string]string{"thread": id.String()}, nil
	case "threads.send":
		var a struct{ Thread, Text string }
		if err := json.Unmarshal(e.Args, &a); err != nil || strings.TrimSpace(a.Text) == "" {
			return nil, errors.New("threads.send needs a thread and text")
		}
		id, err := api.ParseID(a.Thread)
		if err != nil {
			return nil, fmt.Errorf("no thread %q", a.Thread)
		}
		cmd := api.NewID()
		if _, err := ep.Submit(ctx, api.Command{ID: cmd, Target: id, Body: api.MessageDispatch{Text: a.Text, Mode: api.SteerActive}}); err != nil {
			return nil, err
		}
		return map[string]string{"message": cmd.String()}, nil
	}
	return nil, fmt.Errorf("a service can't %s: it has no window", e.Type)
}

// status is a thread's status, in the words plugins read.
func status(t api.Thread) string {
	switch {
	case len(t.Requests) > 0 || t.Run == api.RunWaiting:
		return "waiting"
	case t.Working():
		return "working"
	case t.LastTurn != nil:
		return "completed"
	}
	return "idle"
}
