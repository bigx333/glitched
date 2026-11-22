// Package mcp is glitched mcp: the MCP server each agent the engine runs
// starts for itself, speaking newline-delimited JSON-RPC 2.0 on stdin and
// stdout. It forwards each tool call to the engine over its socket. The
// engine decides, and the server only shapes the replies.
//
// Only the environment names the calling thread and run, never a tool's
// arguments, so an agent acts for its own thread alone.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// The environment of glitched mcp names its caller: the thread and run of
// the agent that started it, and the engine's socket.
const (
	EnvThread = "GLITCHED_THREAD"
	EnvRun    = "GLITCHED_RUN"
	EnvEngine = "GLITCHED_ENGINE"
)

// Routing reports whether the environment variable k names a caller. A
// process the engine starts drops any it inherited, so an engine inside a
// run never speaks for that run's thread.
func Routing(k string) bool {
	for _, e := range []string{EnvThread, EnvRun, EnvEngine} {
		if strings.EqualFold(k, e) {
			return true
		}
	}
	return false
}

// Origin is the run whose agent calls the tools.
type Origin struct {
	Thread, Run api.ID
}

// Launcher is how an engine's agents start glitched mcp: Command is a
// glitched binary, the CLI's or the GUI's, and Engine the socket it dials.
type Launcher struct {
	Command string
	Engine  string
}

// For is the server for run's agent on thread.
func (l Launcher) For(thread, run api.ID) provider.MCPServer {
	return provider.MCPServer{
		Name:    "glitched",
		Command: l.Command,
		Args:    []string{"mcp"},
		Env: map[string]string{
			EnvThread: thread.String(),
			EnvRun:    run.String(),
			EnvEngine: l.Engine,
		},
		Instructions: instructions,
		Trusted:      true,
	}
}

const instructions = `Glitched runs you in a thread. Its MCP server glitched gives you the tool delegate_task, which starts a child agent in a thread of its own, under this one.

- Follow the user's delegation preferences and the repository's instructions. That the tool is there does not by itself call for delegating work.
- The child starts at once, with this thread's folder, permissions and model unless you pick another model. It does not see this conversation, so give it a self-contained brief.
- Each time a child's turn ends done or failed, its report reaches this thread as a new message, in your next turn. Do not wait or poll for it: carry on, or end your turn.
- A child that delegates in turn reports each turn it ends done or failed, so it can report before its own children finish and again after. Its latest report is the current one. If you delegate and end your turn while children still work, say so in your reply: that reply is your report to your parent.
- The user sees each child and can open it. A child cannot get wider permissions than this thread.
- Use a new client_request_id for each new task, and the same one to retry a call, so a retry does not start a second child.`

// Main serves stdin and stdout for the caller its environment names, until
// stdin closes.
func Main(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	o, engine, err := fromEnv()
	if err != nil {
		return err
	}
	ep := transport.NewEndpointClient(transport.Loopback{Network: "unix", Address: engine}, api.ID{})
	defer ep.Close()
	return Serve(ctx, stdin, stdout, &Tools{Engine: ep, Origin: o, Plugins: ep})
}

func fromEnv() (Origin, string, error) {
	thread, err1 := api.ParseID(os.Getenv(EnvThread))
	run, err2 := api.ParseID(os.Getenv(EnvRun))
	engine := os.Getenv(EnvEngine)
	if err := errors.Join(err1, err2); err != nil || engine == "" {
		return Origin{}, "", fmt.Errorf("mcp: %s, %s and %s name the agent's run, and glitched sets them for each agent it starts", EnvThread, EnvRun, EnvEngine)
	}
	return Origin{Thread: thread, Run: run}, engine, nil
}
