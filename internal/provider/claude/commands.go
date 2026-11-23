package claude

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"glitched/internal/provider"
)

// commandsTimeout bounds an initialize: claude answers initialize before it
// would call any model.
const commandsTimeout = 30 * time.Second

// Commands asks a claude in workspace for its slash commands, which
// include the user's and the project's skills and those of the user's
// plugins.
func (a *Adapter) Commands(ctx context.Context, workspace string) ([]provider.Command, error) {
	prog, err := a.program()
	if err != nil {
		return nil, err
	}
	r, err := a.initialize(ctx, prog, workspace)
	if err != nil {
		return nil, err
	}
	return r.commands(), nil
}

// initialized is what claude answers initialize with, as far as glitched
// reads it.
type initialized struct {
	Commands []provider.Command `json:"commands"`
	Models   []modelEntry       `json:"models"`
	Version  string             `json:"claude_code_version"`
}

// modelEntry is one of /model's entries: an alias, as Value names it, of
// the model it resolves to.
type modelEntry struct {
	Value         string   `json:"value"`
	ResolvedModel string   `json:"resolvedModel"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Efforts       []string `json:"supportedEffortLevels"`
}

// commands are the ones to type: a name starting with _ is Claude Code's
// own.
func (r initialized) commands() []provider.Command {
	return slices.DeleteFunc(r.Commands, func(c provider.Command) bool {
		return strings.HasPrefix(c.Name, "_")
	})
}

// models are the ones the account can run, each by its full id, as the
// session pins it, with Claude Code's words for it less its price, and the
// efforts --effort takes for it. The default's entry is left out: it is
// one of the others, which it names. def is the Default, a session's
// model where it names none (defaultModel), listed where no entry is it:
// by the default's words for it, a long-context "[1m]" one as its base,
// so named, else by its id.
func (r initialized) models(def string) []provider.Model {
	out := []provider.Model{}
	for _, m := range r.Models {
		if m.Value == "default" {
			continue
		}
		// "Fable 5.2 · Most capable for … · $4/$20 per Mtok"
		parts := slices.DeleteFunc(strings.Split(m.Description, " · "), func(p string) bool { return strings.Contains(p, "$") })
		name := m.DisplayName
		if len(parts) > 0 && strings.HasPrefix(parts[0], m.DisplayName) {
			name, parts = parts[0], parts[1:]
		}
		var efforts []provider.Effort
		for _, e := range m.Efforts {
			efforts = append(efforts, provider.Effort{ID: e})
		}
		out = append(out, provider.Model{ID: cmp.Or(m.ResolvedModel, m.Value), Name: name, Description: strings.Join(parts, " · "), Efforts: efforts})
	}
	if def == "" {
		return out
	}
	if i := slices.IndexFunc(out, func(m provider.Model) bool { return m.ID == def }); i >= 0 {
		out[i].Default = true
		return out
	}
	row := provider.Model{ID: def, Name: def, Default: true}
	for _, m := range r.Models {
		// "Use the default model (currently Opus 5.5) · $4/$20 per Mtok"
		if _, rest, ok := strings.Cut(m.Description, "(currently "); ok && m.Value == "default" && m.ResolvedModel == def {
			row.Name, _, _ = strings.Cut(rest, ")")
		}
	}
	if base, long := strings.CutSuffix(def, "[1m]"); long {
		if i := slices.IndexFunc(out, func(m provider.Model) bool { return m.ID == base }); i >= 0 {
			row = out[i]
			row.ID, row.Name, row.Default = def, out[i].Name+" (1M context)", true
		}
	}
	return append(out, row)
}

// defaultModel is the model a claude in workspace runs where it's given
// no --model, as Claude Code takes it: ANTHROPIC_MODEL, as its settings'
// env sets it over its own environment, then its settings' model, a
// workspace's local settings over its project's over the user's; else
// /model's default. An alias, as sonnet, is the model /model resolves it
// to, and one /model doesn't list is its own name, which claude resolves
// as it runs. "" where nothing names one.
func (r initialized) defaultModel(getenv func(string) string, workspace string) string {
	name := cmp.Or(configuredModel(getenv, workspace), "default")
	for _, alias := range []string{name, strings.TrimSuffix(name, "[1m]")} {
		i := slices.IndexFunc(r.Models, func(m modelEntry) bool { return m.Value == alias && m.ResolvedModel != "" })
		if i < 0 {
			continue
		}
		if alias != name && !strings.HasSuffix(r.Models[i].ResolvedModel, "[1m]") {
			return r.Models[i].ResolvedModel + "[1m]"
		}
		return r.Models[i].ResolvedModel
	}
	if name == "default" {
		return ""
	}
	return name
}

// configuredModel is the model Claude Code's environment, as getenv reads
// it, or settings name for a claude in workspace, by defaultModel's
// order; "" for none. The user's settings are in CLAUDE_CONFIG_DIR, as
// the provider's launch setting may set it, else in ~/.claude.
func configuredModel(getenv func(string) string, workspace string) string {
	dir := getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	files := []string{filepath.Join(dir, "settings.json")}
	if workspace != "" {
		files = append(files, filepath.Join(workspace, ".claude", "settings.json"), filepath.Join(workspace, ".claude", "settings.local.json"))
	}
	env, model := getenv("ANTHROPIC_MODEL"), ""
	for _, f := range files {
		var s struct {
			Model any            `json:"model"`
			Env   map[string]any `json:"env"`
		}
		if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &s) != nil {
			continue
		}
		// Claude Code sets its settings' env over its own, an empty
		// value too.
		if m, ok := s.Env["ANTHROPIC_MODEL"].(string); ok {
			env = m
		}
		if m, ok := s.Model.(string); ok && strings.TrimSpace(m) != "" {
			model = strings.TrimSpace(m)
		}
	}
	return cmp.Or(strings.TrimSpace(env), model)
}

// initialize starts prog's claude in dir, has it answer initialize, then stops
// it. It never sends a prompt, so no model is called and, with persistence
// off, no session is left behind.
func (a *Adapter) initialize(ctx context.Context, prog provider.Program, dir string) (initialized, error) {
	ctx, cancel := context.WithTimeout(ctx, commandsTimeout)
	defer cancel()
	cmd := command(ctx, prog, "-p", "--verbose", "--no-session-persistence",
		"--input-format", "stream-json", "--output-format", "stream-json")
	cmd.Dir = dir
	in, err := cmd.StdinPipe()
	if err != nil {
		return initialized{}, err
	}
	// The adapter's own pipe, as a run's: a process claude's hooks leave
	// holding it can't keep the read, or Wait, from ending with ctx.
	outW, out, err := pipe(false)
	if err != nil {
		in.Close()
		return initialized{}, err
	}
	defer out.Close()
	cmd.Stdout = outW
	err = cmd.Start()
	outW.Close()
	if err != nil {
		in.Close()
		return initialized{}, err
	}
	defer cmd.Wait()
	defer in.Close()
	fmt.Fprintf(in, `{"type":"control_request","request_id":%q,"request":{"subtype":"initialize"}}`+"\n", initRequest)
	return readInitialized(ctx, out)
}

// readInitialized finds the initialize reply among what claude prints,
// giving up when ctx ends.
func readInitialized(ctx context.Context, out *os.File) (initialized, error) {
	stop := context.AfterFunc(ctx, func() { out.SetReadDeadline(time.Now()) })
	defer stop()
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			Response *struct {
				Subtype   string      `json:"subtype"`
				RequestID string      `json:"request_id"`
				Error     string      `json:"error"`
				Response  initialized `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || m.Response == nil || m.Response.RequestID != initRequest {
			continue
		}
		if m.Response.Subtype != "success" {
			return initialized{}, fmt.Errorf("Claude Code refused to initialize: %s", m.Response.Error)
		}
		return m.Response.Response, nil
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			err = cmp.Or(ctx.Err(), err)
		}
		return initialized{}, err
	}
	return initialized{}, fmt.Errorf("Claude Code exited before it initialized")
}
