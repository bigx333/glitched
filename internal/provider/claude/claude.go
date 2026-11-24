// Package claude runs Claude Code turns on one `claude -p` process per
// thread, kept between its runs, speaking stream-json on stdin and stdout.
//
// Every claude glitched starts names glitched as its entrypoint, because
// Anthropic meters the SDK entrypoints, sdk-cli and sdk-ts, harder against
// the 5-hour usage window.
package claude

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
)

// ErrNotInstalled is Open's error when no claude binary can be found.
var ErrNotInstalled error = provider.Needs{Kind: provider.ErrNotInstalled, Help: "Claude Code is not installed.\nInstall it, or set its path in Settings › Providers."}

// Adapter starts Claude Code with the user's own login and settings, as
// claude in a terminal would. Glitched holds no token.
type Adapter struct {
	// Path, Home and Args are how claude starts, as provider.Launch has
	// them: set them before the first run, or with Place.
	Path, Home, Args string

	mu sync.Mutex // guards Path, Home and Args against Place

	once sync.Once
	pool *park.Pool[*proc]
	// noAuto is why Claude Code last refused its auto mode, "" since one
	// took it.
	noAuto atomic.Pointer[string]
	// limited is whether Claude Code last said the account nears its
	// usage limit, which every thread on the account shares.
	limited atomic.Bool
}

// keepIdle and keepMax are how long a thread's claude waits for its next
// run, and how many wait at once. A parked claude holds about 380 MB, and
// relaunching one costs about a second, so ten minutes keeps a thread the
// user is working in warm, and four bounds what idle threads hold.
var (
	keepIdle = 10 * time.Minute
	keepMax  = 4
)

func (a *Adapter) parked() *park.Pool[*proc] {
	a.once.Do(func() { a.pool = park.New[*proc](keepIdle, keepMax) })
	return a.pool
}

// Close stops every claude waiting for its thread's next run.
func (a *Adapter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.parked().Close(ctx)
}

var (
	_ provider.Adapter   = (*Adapter)(nil)
	_ provider.Commander = (*Adapter)(nil)
	_ provider.Defaulter = (*Adapter)(nil)
	_ io.Closer          = (*Adapter)(nil)
)

// Describe asks a claude for the models the user's account can run, as
// /model lists them. It starts in the home directory: the models are the
// account's, not a project's.
func (a *Adapter) Describe(ctx context.Context) (provider.Info, error) {
	prog, err := a.program()
	if err != nil {
		return provider.Info{}, err
	}
	home, _ := os.UserHomeDir()
	r, err := a.initialize(ctx, prog, home)
	if err != nil {
		return provider.Info{}, err
	}
	if r.Models == nil {
		// An answer without its models says nothing of them: what the
		// account ran before stands.
		return provider.Info{}, fmt.Errorf("Claude Code %s doesn't say what models the account runs", r.Version)
	}
	info := provider.Info{Name: "claude", Version: r.Version, Models: r.models(r.defaultModel(prog.Getenv, "")), Path: prog.Path}
	if s := a.noAuto.Load(); s != nil {
		info.NoAuto = *s
	}
	return info, nil
}

// DefaultModel is the model a claude in workspace runs where it's given no
// --model (defaultModel), by the aliases a claude there resolves, as its
// settings there may move them.
func (a *Adapter) DefaultModel(ctx context.Context, workspace string) (string, error) {
	prog, err := a.program()
	if err != nil {
		return "", err
	}
	r, err := a.initialize(ctx, prog, workspace)
	if err != nil {
		return "", err
	}
	return r.defaultModel(prog.Getenv, workspace), nil
}

// Open runs the run on the thread's claude: the one its last run left,
// when that claude was started as this run would start one, or else a new
// one. The thread's session is spec.Session, else the thread's uuid. A
// thread's first run creates its Claude session, and a later one resumes
// it; a session that turns out to exist, or not to, takes the other flag.
// Both answers come before any model call. A run that takes up a rewind
// never runs on the claude an earlier run left, which holds the
// conversation before it.
func (a *Adapter) Open(ctx context.Context, spec provider.SessionSpec) (provider.Agent, error) {
	prog, err := a.program()
	if err != nil {
		return nil, err
	}
	args := a.argv(spec)
	session := cmp.Or(spec.Session, uuidOf(spec.Thread))
	// What a claude is started with is fixed for its life: a run that
	// would start it otherwise needs another one.
	id, _ := json.Marshal([]any{prog.Path, spec.Workspace, prog.Env, prog.Args, args, session})
	key := string(id)
	if spec.ResumeOnly && spec.Rewound != nil {
		return nil, provider.ErrDeclined
	}
	// Claude Code's own gates, read now: the user may have turned idle
	// compaction off since the thread's last run, and the account may near
	// its limit on another thread's word.
	if spec.ResumeOnly {
		if s := idleSettings(prog, spec.Workspace); !s.idle || a.limited.Load() {
			return nil, provider.ErrDeclined
		}
	}
	keep := func(p *proc) { a.parked().Park(spec.Thread, key, p) }
	var p *proc
	var ok bool
	if spec.Rewound != nil {
		err = a.parked().Drop(ctx, spec.Thread)
	} else {
		p, ok, err = a.parked().Take(ctx, spec.Thread, key)
	}
	switch {
	case err != nil:
		return nil, fmt.Errorf("claude: the thread's earlier claude may still run: %w", err)
	case ok:
		a.told(spec, p)
		return newAgent(spec, p, keep), nil
	}
	var native bool
	var took string
	switch {
	case spec.Rewound != nil:
		p, native, took, err = a.rewound(ctx, prog, spec, args, session)
		if took != session {
			id, _ = json.Marshal([]any{prog.Path, spec.Workspace, prog.Env, prog.Args, args, took})
			key = string(id)
		}
	case spec.ResumeOnly:
		p, err = launch(ctx, prog, spec, slices.Concat(args, []string{"--resume", session}))
	default:
		flags := []string{"--session-id", "--resume"}
		if spec.Generation > 1 {
			slices.Reverse(flags)
		}
		p, err = launch(ctx, prog, spec, slices.Concat(args, []string{flags[0], session}))
		if errors.Is(err, errSessionInUse) || errors.Is(err, errNoSession) {
			p, err = launch(ctx, prog, spec, slices.Concat(args, []string{flags[1], session}))
		}
	}
	if err != nil {
		return nil, err
	}
	p.cache = idleSettings(prog, spec.Workspace)
	p.cache.limited = &a.limited
	// A resumed claude knows nothing of the cache its last one wrote: only
	// a one-hour cache is reported, so one still warm is that.
	if spec.CacheExpires.After(time.Now()) {
		p.cache.ttl = time.Hour
	}
	if g := grants[spec.Access]; g.Else != "" {
		if p.noAuto, err = p.switchMode(ctx, g); err != nil {
			p.Close(context.Background())
			return nil, err
		}
	}
	a.told(spec, p)
	g := newAgent(spec, p, keep)
	g.native, g.session = native, took
	return g, nil
}

// told keeps what the run's claude said of its auto mode as Claude
// Code's latest word on it, for a level that asks for it.
func (a *Adapter) told(spec provider.SessionSpec, p *proc) {
	if grants[spec.Access].Else != "" {
		a.noAuto.Store(&p.noAuto)
	}
}

// Reconcile stops what is left of the run: nothing can reattach to a
// claude whose pipes died with the engine that started it. The run is
// Lost, and the thread's next turn resumes its Claude session. The claude
// that ran it may have served the thread's earlier runs too, so every
// claude of the thread is stopped: the host reconciles a run only while
// no other run of its thread is live here.
func (a *Adapter) Reconcile(ctx context.Context, id provider.RuntimeIdentity) (provider.Recovery, error) {
	if err := a.parked().Drop(ctx, id.Thread); err != nil {
		return nil, err
	}
	found, err := stopRun(ctx, threadMark(id.Thread))
	if err == nil {
		var legacy bool
		legacy, err = stopRun(ctx, legacyMark(id.Run))
		found = found || legacy
	}
	switch {
	case err != nil:
		return nil, err
	case found:
		return provider.Lost{Reason: "Claude Code was stopped: the engine that ran it is gone."}, nil
	}
	return provider.Lost{Reason: "Claude Code no longer runs."}, nil
}

// Command is how glitched runs claude: with the environment it has, less
// any entrypoint it inherited, plus its own. It drops the variables that
// name a glitched mcp caller too, which only a run's own --mcp-config
// sets, so claude never speaks for a thread an outer engine runs. Under a
// test without the claudelive tag, it panics for any claude but the test
// binary; in a program a test started, the command fails to start instead.
//
// Once ctx ends, claude gets SIGTERM, and SIGKILL killGrace later. Claude
// Code runs each tool call in a shell of its own session, which no signal
// to claude or its process group reaches; on SIGTERM claude stops it.
// Windows has no SIGTERM: there claude is killed at once, and for a run,
// whose claude start puts in a job, the job ends what it started.
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Err = guard(bin)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return strings.EqualFold(k, "CLAUDE_CODE_ENTRYPOINT") || mcp.Routing(k)
	}), "CLAUDE_CODE_ENTRYPOINT=glitched")
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = killGrace
	return cmd
}

// killGrace is how long claude has to stop, once asked, before it is
// killed.
const killGrace = 2 * time.Second

// program is the claude a launch starts now, or what stops one starting.
func (a *Adapter) program() (provider.Program, error) {
	a.mu.Lock()
	l := provider.Launch{Path: a.Path, Home: a.Home, Args: a.Args}
	a.mu.Unlock()
	prog, err := placement.Program(l)
	if err != nil {
		return provider.Program{}, provider.Unplaced(err, "Claude Code", ErrNotInstalled.Error())
	}
	return prog, nil
}

// command is Command for prog: in its environment, with its user's
// arguments ahead of args, so that a flag Glitched sets wins over a
// repeat of it.
func command(ctx context.Context, prog provider.Program, args ...string) *exec.Cmd {
	cmd := Command(ctx, prog.Path, slices.Concat(prog.Args, args)...)
	cmd.Env = prog.Environ(cmd.Env)
	return cmd
}

// exeSuffix is what a program's name ends in: Claude Code's native
// installer puts claude.exe in ~/.local/bin on Windows.
var exeSuffix = map[bool]string{true: ".exe"}[runtime.GOOS == "windows"]

// argv is how a run starts Claude Code. A tool call that the user's own
// rules, hooks and the run's mode leave undecided comes to the run as a
// can_use_tool control request on stdout. Glitched's skills are a plugin
// for the session alone, which a claude the user starts never loads.
//
// The run's MCP servers go beside the user's own, which --mcp-config
// without --strict-mcp-config merges with. One --mcp-config takes them
// all, a value each in the run's order, as it takes every value up to the
// next flag, which always follows. Only trusted servers' tools never ask.
func (ad *Adapter) argv(spec provider.SessionSpec) []string {
	a := []string{"-p", "--verbose",
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--include-partial-messages",
		"--permission-mode", cmp.Or(grants[spec.Access].Else, grants[spec.Access].Mode), "--permission-prompt-tool", "stdio",
	}
	var trusted, instructions []string
	for i, s := range spec.MCP {
		if i == 0 {
			a = append(a, "--mcp-config")
		}
		a = append(a, mcpConfig(s))
		if s.Trusted {
			trusted = append(trusted, "mcp__"+s.Name+"__*")
		}
		if s.Instructions != "" {
			instructions = append(instructions, s.Instructions)
		}
	}
	if len(trusted) > 0 {
		a = append(append(a, "--allowedTools"), trusted...)
	}
	if len(instructions) > 0 {
		a = append(a, "--append-system-prompt", strings.Join(instructions, "\n\n"))
	}
	if m := modelOf(spec.Model.Model); m != "" {
		a = append(a, "--model", m)
	}
	if e := spec.Model.Effort; e != "" {
		a = append(a, "--effort", e)
	}
	if r := spec.Skills.Root; r != "" {
		a = append(a, "--plugin-dir", r)
	}
	return a
}

// SkillDelivery is how a claude gets Glitched's skills: as the plugin
// glitched, whose skills Claude Code names glitched:<name>.
func (a *Adapter) SkillDelivery() string { return "Claude Code: as /glitched:<name>" }

// mcpConfig is s as Claude Code reads an --mcp-config, with no env where
// s adds none.
func mcpConfig(s provider.MCPServer) string {
	server := map[string]any{"type": "stdio", "command": s.Command, "args": s.Args}
	if s.Env != nil {
		server["env"] = s.Env
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{s.Name: server}})
	return string(b)
}

// grants are Claude Code's permission modes for Glitched's levels.
// Approve for me is its auto mode, where its own classifier approves what
// is safe and asks for the rest. Its claude starts in acceptEdits, which
// every Claude Code takes, and is switched to auto before its first turn:
// one that can't run auto, for its version, settings or model, refuses
// the switch and runs in acceptEdits, as before auto.
var grants = provider.Grants{
	api.AccessAsk:       {Mode: "default"},
	api.AccessAutoEdits: {Mode: "auto", Else: "acceptEdits"},
	api.AccessFull:      {Mode: "bypassPermissions"},
}

// levels are the Glitched levels Claude Code's permission modes make,
// grants read backwards. plan and dontAsk let the agent do less than Ask
// does, which a run in either says.
var levels = map[string]api.Access{
	"default":           api.AccessAsk,
	"plan":              api.AccessAsk,
	"dontAsk":           api.AccessAsk,
	"acceptEdits":       api.AccessAutoEdits,
	"auto":              api.AccessAutoEdits,
	"bypassPermissions": api.AccessFull,
}

// modelOf is the CLI's name for a model the GUI lists, as "Claude Opus 5.5"
// is claude-opus-5-5. Any other name, such as an alias, goes as it is.
func modelOf(name string) string {
	if !strings.HasPrefix(name, "Claude ") {
		return name
	}
	return strings.NewReplacer(" ", "-", ".", "-").Replace(strings.ToLower(name))
}

// uuidOf is id's 16 bytes as a UUID, with the version and variant bits of
// a UUIDv8. A thread's is its Claude session.
func uuidOf(id api.ID) string {
	b := id
	b[6] = b[6]&0x0f | 0x80
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
