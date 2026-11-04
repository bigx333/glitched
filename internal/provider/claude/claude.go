// Package claude runs Claude Code turns: one `claude -p` process per run,
// speaking stream-json on stdin and stdout.
//
// Every claude glitched starts names glitched as its entrypoint, because
// Anthropic meters the SDK entrypoints, sdk-cli and sdk-ts, harder against
// the 5-hour usage window.
package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"glitched/internal/api"
	"glitched/internal/provider"
)

// ErrNotInstalled is Open's error when no claude binary can be found.
var ErrNotInstalled = errors.New("Claude Code is not installed.\nInstall it, or set claude_path in engine.json.")

// Adapter starts Claude Code with the user's own login and settings, as
// claude in a terminal would. Glitched holds no token.
type Adapter struct {
	// Path is the claude binary. Empty looks on PATH, then in ~/.local/bin,
	// where Claude Code's installer puts it.
	Path string
}

var _ provider.Adapter = (*Adapter)(nil)

func (a *Adapter) Describe(ctx context.Context) (provider.Info, error) {
	bin, err := a.binary()
	if err != nil {
		return provider.Info{}, err
	}
	out, err := Command(ctx, bin, "--version").Output()
	if err != nil {
		return provider.Info{}, fmt.Errorf("claude --version: %w", err)
	}
	version, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return provider.Info{Name: "claude", Version: version, Models: []string{"opus", "sonnet", "haiku"}}, nil
}

// Open starts the run's process on the thread's Claude session. The first
// run of a thread creates the session; later ones find it in use and resume
// it. Both answers come before any model call.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	bin, err := a.binary()
	if err != nil {
		return nil, err
	}
	session := sessionOf(spec.Thread)
	g, err := start(ctx, bin, spec, argv(spec, "--session-id", session))
	if errors.Is(err, errSessionInUse) {
		g, err = start(ctx, bin, spec, argv(spec, "--resume", session))
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// Reconcile stops what is left of the run: nothing can reattach to a
// claude whose pipes died with the engine that started it. The run is
// Lost, and the thread's next turn resumes its Claude session.
func (a *Adapter) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if stopRun(ctx, id.Run.String()) {
		return provider.Lost{Reason: "Claude Code was stopped: the engine that ran it is gone."}, nil
	}
	return provider.Lost{Reason: "Claude Code no longer runs."}, nil
}

// Command is how glitched runs claude: with the environment it has, less
// any entrypoint or run it inherited, plus its own entrypoint. Under a test
// without the claudelive tag, it panics for any claude but the test binary;
// in a program a test started, the command fails to start instead.
//
// Once ctx ends, claude gets SIGTERM, and SIGKILL killGrace later. Claude
// Code runs each tool call in a shell of its own session, which no signal
// to claude or its process group reaches; on SIGTERM claude stops it.
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Err = guard(bin)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(k, "CLAUDE_CODE_ENTRYPOINT") || k == runEnv
	}), "CLAUDE_CODE_ENTRYPOINT=glitched")
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = killGrace
	return cmd
}

// killGrace is how long claude has to stop, once asked, before it is
// killed.
const killGrace = 2 * time.Second

// runEnv names, in the environment of a run's claude, the run. Every process
// claude starts inherits it, so the engine can find them all, tool shells
// in sessions of their own included, after the engine that started them is
// gone.
const runEnv = "GLITCHED_CLAUDE_RUN"

func (a *Adapter) binary() (string, error) {
	if a.Path != "" {
		if isExecutable(a.Path) {
			return a.Path, nil
		}
		return "", ErrNotInstalled
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".local", "bin", "claude"); isExecutable(p) {
			return p, nil
		}
	}
	return "", ErrNotInstalled
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// argv is how a run starts Claude Code. A tool call that the user's own
// rules, hooks and the run's mode leave undecided comes to the run as a
// can_use_tool control request on stdout.
func argv(spec provider.SessionSpec, sessionFlag, session string) []string {
	a := []string{"-p", "--verbose",
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--include-partial-messages",
		"--permission-mode", modes[spec.Access], "--permission-prompt-tool", "stdio",
		"--disallowedTools", "AskUserQuestion",
	}
	if m := modelOf(spec.Model.Model); m != "" {
		a = append(a, "--model", m)
	}
	if e := spec.Model.Effort; e != "" {
		a = append(a, "--effort", e)
	}
	return append(a, sessionFlag, session)
}

var modes = map[api.Access]string{
	api.AccessAsk:       "default",
	api.AccessAutoEdits: "acceptEdits",
	api.AccessFull:      "bypassPermissions",
}

// modelOf is the CLI's name for a model the GUI lists, as "Claude Opus 5.5"
// is claude-opus-5-5. Any other name, such as an alias, goes as it is.
func modelOf(name string) string {
	if !strings.HasPrefix(name, "Claude ") {
		return name
	}
	return strings.NewReplacer(" ", "-", ".", "-").Replace(strings.ToLower(name))
}

// sessionOf is the Claude session of a thread: the thread's 16 bytes as a
// UUID, with the version and variant bits of a UUIDv8.
func sessionOf(thread api.ID) string {
	b := thread
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
