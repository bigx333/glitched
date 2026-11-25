// Package mcp is glitched mcp: the MCP server each agent the engine runs
// starts for itself, speaking newline-delimited JSON-RPC 2.0 on stdin and
// stdout. It forwards each tool call to the engine over its socket. The
// engine decides, and the server only shapes the replies.
//
// Only the environment names the calling thread, never a tool's arguments,
// so an agent acts for its own thread alone. An agent's process can outlive
// a run and serve its thread's next one, so the server names no run: each
// call acts for the thread's live turn.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
	"glitched/internal/transport"
)

// The environment of glitched mcp names its caller: the thread of the agent
// that started it, the access that agent was started at, and the engine's
// socket.
const (
	EnvThread = "GLITCHED_THREAD"
	EnvAccess = "GLITCHED_ACCESS"
	EnvEngine = "GLITCHED_ENGINE"
	EnvRole   = "GLITCHED_ROLE"
	// EnvMachines lists, as a JSON array, the labels of the machines a
	// child may be placed on: labels only, never a link's token.
	EnvMachines = "GLITCHED_MACHINES"
)

// Routing reports whether the environment variable k names a caller. A
// process the engine starts drops any it inherited, so an engine inside a
// run never speaks for that run's thread.
func Routing(k string) bool {
	for _, e := range []string{EnvThread, EnvAccess, EnvEngine, EnvRole, EnvMachines} {
		if strings.EqualFold(k, e) {
			return true
		}
	}
	return false
}

// Origin is the thread whose agent calls the tools, and the access the
// agent was started at.
type Origin struct {
	Thread api.ID
	Access api.Access
	Role   api.ThreadRole
}

// Launcher is how an engine's agents start glitched mcp: Command is a
// glitched binary, the CLI's or the GUI's, and Engine the socket it dials.
type Launcher struct {
	Command string
	Engine  string
	// Machines lists the machines a child may be placed on as an agent
	// starts; nil lists none.
	Machines func() []string
}

// For is the server for an agent of thread started at access, whichever of
// the thread's runs at that access the agent serves.
func (l Launcher) For(thread api.ID, access api.Access) provider.MCPServer {
	return l.ForRole(thread, access, api.RoleDefault)
}

// ForRole returns the glitched MCP server configuration for thread at access
// and role.
func (l Launcher) ForRole(thread api.ID, access api.Access, role api.ThreadRole) provider.MCPServer {
	s := l.server(thread, access, role)
	if l.Machines == nil {
		return s
	}
	if ms := l.Machines(); len(ms) > 0 {
		b, _ := json.Marshal(ms)
		s.Env[EnvMachines] = string(b)
	}
	return s
}

func (l Launcher) server(thread api.ID, access api.Access, role api.ThreadRole) provider.MCPServer {
	env := map[string]string{
		EnvThread: thread.String(),
		EnvAccess: strconv.Itoa(int(access)),
		EnvEngine: l.Engine,
	}
	if role != api.RoleDefault {
		env[EnvRole] = string(role)
	}
	return provider.MCPServer{
		Name:         "glitched",
		Command:      l.Command,
		Args:         []string{"mcp"},
		Env:          env,
		Instructions: instructionsFor(role),
		Trusted:      true,
		// request_credentials waits for the user up to the engine's 30
		// minutes, past which the engine answers it itself.
		ToolTimeout: 35 * time.Minute,
	}
}

const instructions = `Glitched runs you in a thread. Its MCP server glitched gives you the tool delegate_task, which starts a child agent in a thread of its own, under this one.

- Follow the user's delegation preferences and the repository's instructions. That the tool is there does not by itself call for delegating work.
- The child starts at once, with this thread's permissions, its folder unless you ask for isolation worktree (a worktree and branch of its own from this folder's HEAD, without its uncommitted changes), and its provider and model unless you pick others: another provider (claude for codex, say) starts at its own default model unless you pick one. It does not see this conversation, so give it a self-contained brief.
- Each time a child's turn ends done or failed, its report reaches this thread as a new message, in your next turn. Do not wait or poll for it: carry on, or end your turn.
- list_tasks and task_status show your children; task_send messages one (a new brief or a correction), task_cancel stops one and everything under it, and task_answer answers a question one asks, which reaches you on its own. An approval or credentials a child asks for are the user's. Use them to act on a child, never to wait for one: its report comes on its own. A child the user took over (the user sent its last message or stop) is theirs, and these refuse it.
- A child that delegates in turn reports each turn it ends done or failed, so it can report before its own children finish and again after. Its latest report is the current one. If you delegate and end your turn while children still work, say so in your reply: that reply is your report to your parent.
- The user sees each child and can open it. A child cannot get wider permissions than this thread.
- Use a new client_request_id for each new task, and the same one to retry a call, so a retry does not start a second child.

When you need a secret from the user (an API key, a token, a password), ask with request_credentials, never in chat. The user types the values into a private card; you get back where they are, never the values, and must never print them.`

// Main serves stdin and stdout for the caller its environment names, until
// stdin closes.
func Main(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	o, engine, err := fromEnv()
	if err != nil {
		return err
	}
	ep := transport.NewEndpointClient(transport.Loopback{Network: "unix", Address: engine}, api.ID{})
	defer ep.Close()
	var machines []string
	json.Unmarshal([]byte(os.Getenv(EnvMachines)), &machines)
	return Serve(ctx, stdin, stdout, &Tools{Engine: ep, Origin: o, Plugins: ep, Secrets: ep, Providers: engineProviders{ep}, Machines: machines})
}

// engineProviders are the providers of the engine ep reaches.
type engineProviders struct{ ep *transport.EndpointClient }

func (p engineProviders) Describe(ctx context.Context, name string) (provider.Info, error) {
	return p.ep.ProviderDescribe(ctx, name)
}

func (p engineProviders) DefaultModel(ctx context.Context, name, workspace string) (string, error) {
	return p.ep.ProviderDefault(ctx, name, workspace)
}

func fromEnv() (Origin, string, error) {
	thread, err := api.ParseID(os.Getenv(EnvThread))
	access, aerr := strconv.ParseUint(os.Getenv(EnvAccess), 10, 8)
	engine := os.Getenv(EnvEngine)
	if err != nil || aerr != nil || engine == "" {
		return Origin{}, "", fmt.Errorf("mcp: %s, %s and %s name the agent's thread and access, and glitched sets them for each agent it starts", EnvThread, EnvAccess, EnvEngine)
	}
	return Origin{Thread: thread, Access: api.Access(access), Role: api.ThreadRole(os.Getenv(EnvRole))}, engine, nil
}
