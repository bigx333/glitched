package claude

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	r, err := a.initialize(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return r.commands(), nil
}

// initialized is what claude answers initialize with, as far as glitched
// reads it.
type initialized struct {
	Commands []provider.Command `json:"commands"`
	Models   []struct {
		Value         string   `json:"value"`
		ResolvedModel string   `json:"resolvedModel"`
		DisplayName   string   `json:"displayName"`
		Description   string   `json:"description"`
		Efforts       []string `json:"supportedEffortLevels"`
	} `json:"models"`
	Version string `json:"claude_code_version"`
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
// efforts --effort takes for it. The default is left out: it is one of
// the others, which it names.
func (r initialized) models() []provider.Model {
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
	return out
}

// initialize starts a claude in dir, has it answer initialize, then stops
// it. It never sends a prompt, so no model is called and, with persistence
// off, no session is left behind.
func (a *Adapter) initialize(ctx context.Context, dir string) (initialized, error) {
	bin, err := a.binary()
	if err != nil {
		return initialized{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandsTimeout)
	defer cancel()
	cmd := Command(ctx, bin, "-p", "--verbose", "--no-session-persistence",
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
