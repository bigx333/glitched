package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// item is one of the turn's thread items, as Codex last reported it.
type item struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Status           string          `json:"status"`
	Command          string          `json:"command"`
	Cwd              string          `json:"cwd"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Changes          []change        `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Error            *struct {
		Message string `json:"message"`
	} `json:"error"`
	Query string `json:"query"`
	Path  string `json:"path"`
}

type change struct {
	Path string `json:"path"`
}

// kinds are the steps Glitched shows, by the type of item: Codex's
// thinking, plans, messages and compactions are not steps.
var kinds = map[string]string{
	"commandExecution":    "command",
	"fileChange":          "edit",
	"mcpToolCall":         "tool",
	"dynamicToolCall":     "tool",
	"collabAgentToolCall": "tool",
	"webSearch":           "search",
	"imageView":           "read",
}

func (it *item) shown() bool { _, ok := kinds[it.Type]; return ok }

func (it *item) open() bool { return it.shown() && it.Status == "inProgress" }

func (it *item) paths(workspace string) []string {
	var out []string
	for _, c := range it.Changes {
		out = append(out, provider.Rel(workspace, c.Path))
	}
	return out
}

func (it *item) work(workspace string) provider.Work {
	w := provider.Work{Item: it.ID, Kind: kinds[it.Type], Status: api.WorkStarted}
	switch it.Status {
	case "completed":
		w.Status = api.WorkCompleted
	case "failed", "declined":
		w.Status = api.WorkFailed
	}
	switch it.Type {
	case "commandExecution":
		w.Title = it.Command
		if w.Status != api.WorkStarted {
			w.Exit = it.ExitCode
			if it.AggregatedOutput != nil {
				w.Output = *it.AggregatedOutput
			}
		}
	case "fileChange":
		paths := it.paths(workspace)
		w.Title = "Edit " + strings.Join(paths, ", ")
		if len(paths) > 0 {
			w.Path = paths[0]
		}
	case "mcpToolCall":
		w.Title = it.Server + ": " + it.Tool
		if it.Error != nil {
			w.Output = it.Error.Message
		}
	case "webSearch":
		w.Title = "Search the web for " + it.Query
	case "imageView":
		w.Title, w.Path = "View "+filepath.Base(it.Path), provider.Rel(workspace, it.Path)
	default:
		w.Title = it.Tool
	}
	return w
}

// body is an approval card's body: a command to run where, or an
// operation on paths, or a tool's arguments, and why Codex asks.
type body struct {
	Operation string `json:"operation,omitempty"`
	Path      string `json:"path,omitempty"`
	Command   string `json:"command,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Args      string `json:"args,omitempty"`
}

func (b body) json() json.RawMessage {
	if b.Operation != "" && b.Path == "" && b.Args == "" && b.Reason == "" {
		b.Reason = "Codex gave no detail."
	}
	raw, _ := json.Marshal(b)
	return raw
}
