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
	"glitched/internal/plugins/bundled"
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

// startPlugins runs the plugins' services, the built-ins' with them,
// whose effects act on the engine itself.
func (d *Daemon) startPlugins(cfg Config) *plugins.Services {
	return plugins.StartServices(cfg.Dir, plugins.ServiceOptions{
		Logf:        func(format string, args ...any) { slog.Info(fmt.Sprintf(format, args...)) },
		Do:          d.pluginEffect,
		Run:         cfg.PluginRun,
		TimerScale:  cfg.PluginTimerScale,
		Bundled:     bundled.FS,
		ReleaseKeys: []string{bundled.ReleaseKey},
	})
}

// pluginEffect is what plugin's service may do, with no window: read,
// start and message threads. The host checked its grant. It answers
// later, off the services' queue, so a slow create holds up no other
// service, call or timer.
func (d *Daemon) pluginEffect(plugin string, e plugins.Effect) (any, error) {
	return plugins.Pending(func(done func(any, error)) {
		go func() { done(d.effect(plugin, e)) }()
	}), nil
}

func (d *Daemon) effect(plugin string, e plugins.Effect) (any, error) {
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
		c, err := plugins.ParseCreate(e.Args)
		if err != nil {
			return nil, err
		}
		ws := c.Workspace
		if ws == "" {
			ws, _ = os.UserHomeDir()
		}
		if !filepath.IsAbs(ws) {
			return nil, fmt.Errorf("workspace %q is not an absolute path", ws)
		}
		model := c.Model
		if model == nil {
			// The model of the thread the user worked in last: a service
			// has no composer to read it from.
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
			model = &last.Model
		}
		if _, ok := d.providers[model.Provider]; !ok {
			return nil, fmt.Errorf("threads.create: this engine runs no provider %q", model.Provider)
		}
		thread, cmd := api.NewID(), api.NewID()
		if c.Key != nil {
			if thread, cmd, err = c.Key.IDs(plugin); err != nil {
				return nil, fmt.Errorf("threads.create: %v", err)
			}
		}
		if _, err := ep.Submit(ctx, api.Command{ID: cmd, Target: thread, Body: api.ThreadCreate{Workspace: ws, Title: c.Title, Model: *model, Access: c.Access, Text: c.Prompt, Worktree: c.Worktree}}); err != nil {
			return nil, err
		}
		return map[string]string{"thread": thread.String()}, nil
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
