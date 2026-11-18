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

// commandsTimeout bounds a listing: claude answers initialize before it
// would call any model.
const commandsTimeout = 30 * time.Second

// Commands asks a claude in workspace for its slash commands, which
// include the user's and the project's skills and those of the user's
// plugins, then stops it. It never sends a prompt, so no model is called
// and, with persistence off, no session is left behind.
func (a *Adapter) Commands(ctx context.Context, workspace string) ([]provider.Command, error) {
	bin, err := a.binary()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandsTimeout)
	defer cancel()
	cmd := Command(ctx, bin, "-p", "--verbose", "--no-session-persistence",
		"--input-format", "stream-json", "--output-format", "stream-json")
	cmd.Dir = workspace
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// The adapter's own pipe, as a run's: a process claude's hooks leave
	// holding it can't keep the read, or Wait, from ending with ctx.
	outW, out, err := pipe(false)
	if err != nil {
		in.Close()
		return nil, err
	}
	defer out.Close()
	cmd.Stdout = outW
	err = cmd.Start()
	outW.Close()
	if err != nil {
		in.Close()
		return nil, err
	}
	defer cmd.Wait()
	defer in.Close()
	fmt.Fprintf(in, `{"type":"control_request","request_id":%q,"request":{"subtype":"initialize"}}`+"\n", initRequest)
	return readCommands(ctx, out)
}

// readCommands finds the initialize reply among what claude prints, giving
// up when ctx ends.
func readCommands(ctx context.Context, out *os.File) ([]provider.Command, error) {
	stop := context.AfterFunc(ctx, func() { out.SetReadDeadline(time.Now()) })
	defer stop()
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			Response *struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					Commands []provider.Command `json:"commands"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || m.Response == nil || m.Response.RequestID != initRequest {
			continue
		}
		if m.Response.Subtype != "success" {
			return nil, fmt.Errorf("Claude Code refused to list its commands: %s", m.Response.Error)
		}
		// A name starting with _ is Claude Code's own, not one to type.
		return slices.DeleteFunc(m.Response.Response.Commands, func(c provider.Command) bool {
			return strings.HasPrefix(c.Name, "_")
		}), nil
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			err = cmp.Or(ctx.Err(), err)
		}
		return nil, err
	}
	return nil, fmt.Errorf("Claude Code exited before listing its commands")
}
