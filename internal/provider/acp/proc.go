package acp

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
	"glitched/internal/provider/sweep"
)

// procEnv names, in the environment of an agent, its mark: the thread it
// serves and the launch. Every process the agent starts inherits it, so
// the engine can find them all after the engine that started them is gone.
const procEnv = "GLITCHED_ACP_PROCESS"

var marked = sweep.Mark{Env: procEnv}

// wireEnv names a file every line to and from an agent is appended to,
// for a capture of the wire. Unset, nothing is written.
const wireEnv = "GLITCHED_ACP_WIRE"

var (
	// closeGrace is how long Close lets an agent end on its own once its
	// stdin closes, before it stops it.
	closeGrace = 500 * time.Millisecond
	// interruptGrace is how long an agent has to end a turn it was asked
	// to cancel, before it is stopped.
	interruptGrace = 10 * time.Second
)

// killGrace is how long a process asked to stop has before it is killed.
const killGrace = sweep.KillGrace

// drainGrace is how long, once the agent has exited, its output may stay
// quiet before the adapter stops reading it: a process it left behind can
// hold the pipe open for good.
const drainGrace = 250 * time.Millisecond

// proc is one agent process and its ACP session. It runs one thread's
// turns, an Agent at a time, and waits parked between them.
type proc struct {
	profile Profile
	cmd     *exec.Cmd
	mark    string
	conn    *acpsdk.ClientSideConnection
	stdin   *os.File
	stderr  *tail
	heard   chan struct{} // closed once stderr is read to its end
	read    chan struct{} // closed once stdout is read to its end and let go
	// release has the bridge from stdout let go now, sending the SDK
	// nothing more, once the agent is stopped.
	release func()
	reaped  chan struct{} // closed once the process is reaped
	exitErr error         // set before reaped closes
	left    map[int]int64 // what ran in its process group as it was reaped; set before reaped closes
	halted  atomic.Bool   // it was stopped, and takes no more runs
	stale   atomic.Bool   // it printed that its login went stale
	closing sync.Once

	version string
	caps    caps
	briefed bool // the session's first prompt went out

	mu       sync.Mutex
	loading  acpsdk.SessionId // the session being loaded, whose replay is counted
	replayed int
	session  acpsdk.SessionId
	options  []acpsdk.SessionConfigOption // the session's, as the agent last said
	mode     string                       // the session's mode, as the agent last said
	moded    bool                         // the session has modes, as the agent said
	run      *Agent                       // the run the agent serves, if any
	prompt   chan struct{}                // closes once the run's prompt is written
	parked   bool                         // it waits for its thread's next run
	spoke    bool                         // it spoke while parked
}

// caps is what the agent said it can do, as the adapter reads it.
type caps struct {
	close, resume, load bool
}

var (
	_ park.Process  = (*proc)(nil)
	_ acpsdk.Client = (*proc)(nil)
)

// launch starts the agent in dir under mark, with args after the
// profile's, and initializes it. ctx bounds only the start; the process
// lives until Close.
func launch(ctx context.Context, profile Profile, bin, dir, mark string, args []string) (*proc, error) {
	cmd := exec.Command(bin, slices.Concat(profile.Args, args)...)
	if err := guard(bin); err != nil {
		return nil, err
	}
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		_, ours := profile.Env[k]
		return ours || k == procEnv || mcp.Routing(k)
	})
	for k, v := range profile.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, procEnv+"="+mark)
	cmd.SysProcAttr = sweep.SysProcAttr()
	p := &proc{profile: profile, cmd: cmd, mark: mark, stderr: &tail{}, heard: make(chan struct{}), read: make(chan struct{}), reaped: make(chan struct{})}
	// The adapter's own pipes, unlike exec's, leave Wait nothing to wait
	// for once the agent exits, whoever else still holds them.
	inR, inW, err1 := os.Pipe()
	outR, outW, err2 := os.Pipe()
	errR, errW, err3 := os.Pipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			if f != nil {
				f.Close()
			}
		}
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	err := cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("start %s: %w", profile.Name, err)
	}
	p.stdin = inW
	go func() {
		p.exitErr = cmd.Wait()
		p.left = sweep.Group(cmd.Process.Pid)
		close(p.reaped)
	}()
	wire := openWire()
	go p.watchStderr(errR, wire)
	in := p.readStdout(outR, wire)
	p.conn = acpsdk.NewClientSideConnection(p, tap{inW, wire, ">> ", p.wrote}, in)
	p.conn.SetLogger(slog.New(redacted{slog.Default().Handler()}))
	// The SDK stops reading at a line it can't take, as one past its size
	// limit: the bridge to it lets go, so the agent's stdout closes.
	go func() {
		<-p.conn.Done()
		in.CloseWithError(io.ErrClosedPipe)
	}()

	// The SDK's own calls end with ctx, but a write to an agent that reads
	// nothing would not: the agent is stopped instead.
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	r, err := p.conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientInfo:      &acpsdk.Implementation{Name: "glitched", Version: "1"},
		// An agent that can ask with a form asks on Glitched's question
		// card. Without one, agy only prints its questions in the reply.
		ClientCapabilities: acpsdk.ClientCapabilities{Elicitation: &acpsdk.ElicitationCapabilities{Form: &acpsdk.ElicitationFormCapabilities{}}},
	})
	if err != nil {
		err = p.failure("start", err)
		p.discard()
		return nil, err
	}
	if r.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		p.discard()
		return nil, fmt.Errorf("%s speaks ACP version %d, and Glitched speaks %d.", profile.Name, r.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
	if r.AgentInfo != nil {
		p.version = r.AgentInfo.Version
	}
	p.caps.close = r.AgentCapabilities.SessionCapabilities.Close != nil
	p.caps.resume = r.AgentCapabilities.SessionCapabilities.Resume != nil
	p.caps.load = r.AgentCapabilities.LoadSession
	return p, nil
}

// open takes up the thread's session in dir with servers: it resumes the
// session the thread had, where the agent can, else loads it, and else
// starts one, which lost reports. agy answers a resume of a session it
// doesn't have with -32603 "Failed to read session", not -32002, so any
// refusal falls through to the next way; only a sign-in ends it.
func (p *proc) open(ctx context.Context, dir string, servers []provider.MCPServer, had string) (lost bool, err error) {
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	list := []acpsdk.McpServer{}
	for _, s := range servers {
		env := []acpsdk.EnvVariable{}
		for _, k := range sortedKeys(s.Env) {
			env = append(env, acpsdk.EnvVariable{Name: k, Value: s.Env[k]})
		}
		args := append([]string{}, s.Args...)
		list = append(list, acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{Name: s.Name, Command: s.Command, Args: args, Env: env}})
	}
	var options []acpsdk.SessionConfigOption
	var modes *acpsdk.SessionModeState
	id := acpsdk.SessionId(had)
	taken := false
	if had != "" && p.caps.resume {
		r, err := p.conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{SessionId: id, Cwd: dir, McpServers: list})
		if err == nil {
			taken, options, modes = true, r.ConfigOptions, r.Modes
		} else if err := p.fatal("resume the thread's session", err); err != nil {
			return false, err
		}
	}
	if had != "" && !taken && p.caps.load {
		// The agent replays the conversation before it answers, which no
		// run hears. agy loads a session it doesn't have as an empty one,
		// so a load that replays no message has lost the conversation.
		p.mu.Lock()
		p.loading, p.replayed = id, 0
		p.mu.Unlock()
		r, err := p.conn.LoadSession(ctx, acpsdk.LoadSessionRequest{SessionId: id, Cwd: dir, McpServers: list})
		p.mu.Lock()
		replayed := p.replayed
		p.loading = ""
		p.mu.Unlock()
		if err == nil {
			taken, options, modes = replayed > 0, r.ConfigOptions, r.Modes
		} else if err := p.fatal("load the thread's session", err); err != nil {
			return false, err
		}
	}
	// A session taken up again is briefed again: that it was bound says
	// only that a run opened it, not that its first prompt went out.
	if !taken {
		lost = had != ""
		r, err := p.conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: dir, McpServers: list})
		if err != nil {
			return false, p.failure("open a session", err)
		}
		id, options, modes = r.SessionId, r.ConfigOptions, r.Modes
	}
	p.mu.Lock()
	p.session, p.options, p.mode, p.moded = id, options, "", modes != nil
	if modes != nil {
		p.mode = string(modes.CurrentModeId)
	}
	p.mu.Unlock()
	return lost, nil
}

// fatal is err when it ends the run: a sign-in the agent needs, or an
// agent that is gone. An answer of the agent's that refuses is nil, and
// the caller tries the next way.
func (p *proc) fatal(doing string, err error) error {
	f := p.failure(doing, err)
	var re *acpsdk.RequestError
	select {
	case <-p.conn.Done():
		return f
	default:
	}
	if errors.Is(f, provider.ErrSignedOut) || p.halted.Load() || !errors.As(err, &re) {
		return f
	}
	slog.Info("acp: the agent didn't take up the thread's session", "agent", p.profile.Name, "doing", doing, "err", f)
	return nil
}

func (p *proc) sessionID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.session)
}

// ours is whether id is the agent's session. Before session/new answers
// there is none, and what comes goes nowhere.
func (p *proc) ours(id acpsdk.SessionId) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session != "" && id == p.session
}

func (p *proc) setOptions(o []acpsdk.SessionConfigOption) {
	p.mu.Lock()
	p.options = o
	p.mu.Unlock()
}

func (p *proc) currentOptions() []acpsdk.SessionConfigOption {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.options
}

// models are the session's models, by its model option, each with the
// efforts its thought-level option offers once it is the session's model.
// The Default is the model a new session starts on, the option's current
// value, and a model's DefaultEffort the level's once it is the model.
func (p *proc) models(ctx context.Context) ([]provider.Model, error) {
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	initial := p.currentOptions()
	model, ok := option(initial, acpsdk.SessionConfigOptionCategoryModel)
	if !ok {
		return []provider.Model{}, nil
	}
	var out []provider.Model
	for _, o := range choices(model) {
		m := provider.Model{ID: string(o.Value), Name: o.Name, Default: o.Value == model.CurrentValue}
		if o.Description != nil {
			m.Description = *o.Description
		}
		options := initial
		if model.CurrentValue != o.Value {
			r, err := p.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{ValueId: &acpsdk.SetSessionConfigOptionValueId{
				SessionId: p.session, ConfigId: model.Id, Value: o.Value,
			}})
			if err != nil {
				return nil, p.failure("list its models", err)
			}
			options = r.ConfigOptions
		}
		if level, ok := option(options, acpsdk.SessionConfigOptionCategoryThoughtLevel); ok {
			m.DefaultEffort = string(level.CurrentValue)
			for _, e := range choices(level) {
				m.Efforts = append(m.Efforts, provider.Effort{ID: string(e.Value), Label: e.Name})
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// configure sets the session's model and effort to ref's, and its mode
// to mode, where they differ. A ref without either, or no mode, leaves
// the agent's own.
func (p *proc) configure(ctx context.Context, ref api.ModelRef, mode string) error {
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	// ACP picks a mode of those the session lists: one that lists none
	// has none to pick, and runs as it starts, which asks the most. The
	// mode is the one the agent last said, never the one asked, so a mode
	// it moved to since is set again.
	p.mu.Lock()
	current, moded := p.mode, p.moded
	p.mu.Unlock()
	if mode != "" && mode != current && moded {
		if _, err := p.conn.SetSessionMode(ctx, acpsdk.SetSessionModeRequest{SessionId: p.session, ModeId: acpsdk.SessionModeId(mode)}); err != nil {
			return p.failure("run at this access level, as its mode "+mode, err)
		}
	}
	for _, want := range []struct {
		category acpsdk.SessionConfigOptionCategory
		value    string
	}{{acpsdk.SessionConfigOptionCategoryModel, ref.Model}, {acpsdk.SessionConfigOptionCategoryThoughtLevel, ref.Effort}} {
		o, ok := option(p.currentOptions(), want.category)
		if want.value == "" || ok && string(o.CurrentValue) == want.value {
			continue
		}
		if !ok || !slices.ContainsFunc(choices(o), func(c acpsdk.SessionConfigSelectOption) bool { return string(c.Value) == want.value }) {
			if want.category == acpsdk.SessionConfigOptionCategoryThoughtLevel {
				// A model with no such level runs at its own.
				slog.Info("acp: the model runs at no such effort; it keeps its own", "agent", p.profile.Name, "effort", want.value)
				continue
			}
			return fmt.Errorf("%s doesn't offer the model %s.", p.profile.Name, want.value)
		}
		r, err := p.conn.SetSessionConfigOption(ctx, acpsdk.SetSessionConfigOptionRequest{ValueId: &acpsdk.SetSessionConfigOptionValueId{
			SessionId: p.session, ConfigId: o.Id, Value: acpsdk.SessionConfigValueId(want.value),
		}})
		if err != nil {
			return p.failure("set the "+string(want.category), err)
		}
		p.setOptions(r.ConfigOptions)
	}
	return nil
}

func option(options []acpsdk.SessionConfigOption, category acpsdk.SessionConfigOptionCategory) (acpsdk.SessionConfigOptionSelect, bool) {
	for _, o := range options {
		if s := o.Select; s != nil && s.Category != nil && *s.Category == category {
			return *s, true
		}
	}
	return acpsdk.SessionConfigOptionSelect{}, false
}

func choices(o acpsdk.SessionConfigOptionSelect) []acpsdk.SessionConfigSelectOption {
	if u := o.Options.Ungrouped; u != nil {
		return *u
	}
	var out []acpsdk.SessionConfigSelectOption
	if g := o.Options.Grouped; g != nil {
		for _, group := range *g {
			out = append(out, group.Options...)
		}
	}
	return out
}

// failure is why doing went wrong: the user's login when the agent says
// it needs one, else what the agent said, or what it printed as it exited.
func (p *proc) failure(doing string, err error) error {
	// A connection that ended went with the process, whose exit, and what
	// it printed last, say why.
	select {
	case <-p.conn.Done():
		select {
		case <-p.reaped:
		case <-time.After(killGrace):
		}
	default:
	}
	gone := false
	select {
	case <-p.reaped:
		<-p.heard
		gone = true
	default:
	}
	var re *acpsdk.RequestError
	if p.stale.Load() || errors.As(err, &re) && re.Code == -32000 {
		return provider.Needs{Kind: provider.ErrSignedOut, Help: p.profile.SignIn}
	}
	if gone {
		return p.exited()
	}
	if re != nil {
		return fmt.Errorf("%s couldn't %s: %s", p.profile.Name, doing, said(re))
	}
	return fmt.Errorf("%s couldn't %s: %w", p.profile.Name, doing, err)
}

// said is what the agent said of an error: its data, where that is words,
// else its message.
func said(re *acpsdk.RequestError) string {
	var data any
	json.Unmarshal(re.Data, &data)
	if m, ok := data.(map[string]any); ok {
		data = cmp.Or(m["message"], m["error"], m["details"])
	}
	if s, ok := data.(string); ok && s != "" {
		return s
	}
	return re.Message
}

// exited is why the process is gone, from what it printed last.
func (p *proc) exited() error {
	said := strings.TrimSpace(p.stderr.String())
	if said == "" && p.exitErr != nil {
		said = p.exitErr.Error()
	}
	return fmt.Errorf("%s exited: %s", p.profile.Name, cmp.Or(said, "no reason given"))
}

// readStdout hands the SDK the agent's JSON-RPC lines and keeps the rest,
// which ACP has no place for, to itself: a line that says the login went
// stale marks the agent signed out.
func (p *proc) readStdout(stdout *os.File, wire io.Writer) *io.PipeReader {
	r, w := io.Pipe()
	p.release = sync.OnceFunc(func() {
		r.CloseWithError(io.ErrClosedPipe)
		stdout.Close()
	})
	go func() {
		<-p.reaped
		stdout.SetReadDeadline(time.Now().Add(drainGrace))
	}()
	go func() {
		defer close(p.read)
		defer stdout.Close()
		lines := bufio.NewReader(stdout)
		for {
			line, err := lines.ReadBytes('\n')
			if len(line) > 0 {
				wire.Write(append([]byte("<< "), line...))
				if t := bytes.TrimSpace(line); len(t) > 0 && t[0] != '{' {
					p.notice(string(t))
				} else if _, werr := w.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				w.CloseWithError(io.EOF)
				return
			}
		}
	}()
	return r
}

func (p *proc) watchStderr(stderr *os.File, wire io.Writer) {
	go func() {
		<-p.reaped
		stderr.SetReadDeadline(time.Now().Add(drainGrace))
	}()
	defer close(p.heard)
	defer stderr.Close()
	lines := bufio.NewReader(stderr)
	for {
		line, err := lines.ReadBytes('\n')
		if len(line) > 0 {
			wire.Write(append([]byte("!! "), line...))
			p.stderr.Write(line)
			p.notice(string(bytes.TrimSpace(line)))
		}
		if err != nil {
			return
		}
	}
}

func (p *proc) notice(line string) {
	for _, s := range p.profile.SignedOut {
		if strings.Contains(strings.ToLower(line), strings.ToLower(s)) {
			p.stale.Store(true)
			return
		}
	}
}

// halt stops the agent and what it started, and keeps it from taking
// another run. It returns at once.
func (p *proc) halt() {
	if p.halted.Swap(true) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*killGrace)
		defer cancel()
		p.stop(ctx, nil)
	}()
}

// stop ends the agent and every process under its mark, and returns once
// they are gone.
func (p *proc) stop(ctx context.Context, seen map[int]int64) error {
	// A tool that outlived a crashed agent, with no mark to show, is
	// found by the group the agent led.
	select {
	case <-p.reaped:
		if seen == nil {
			seen = map[int]int64{}
		}
		maps.Copy(seen, p.left)
	default:
	}
	if _, err := marked.Stop(ctx, p.mark, seen); err != nil {
		return err
	}
	select {
	case <-p.reaped:
		return nil
	default:
	}
	// Where the engine can't find the agent by its mark, it has only the
	// process it started.
	p.cmd.Process.Kill()
	select {
	case <-p.reaped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// discard closes a process that takes no run, as one that failed to
// start, in the background of the caller's own deadline.
func (p *proc) discard() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*killGrace)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		slog.Warn("acp: stopping an agent", "agent", p.profile.Name, "err", err)
	}
}

// Wake is whether the agent can take its thread's next run: it kept quiet
// while parked and still runs.
func (p *proc) Wake() bool {
	p.mu.Lock()
	spoke := p.spoke
	p.mu.Unlock()
	if spoke || p.halted.Load() {
		return false
	}
	select {
	case <-p.reaped:
		return false
	case <-p.conn.Done():
		return false
	default:
	}
	p.stderr.reset()
	return true
}

func (p *proc) Done() <-chan struct{} { return p.reaped }

// Close ends stdin, which ends an idle agent, and stops one still there
// after closeGrace, with whatever it left running. What it started is
// read first: once it exits, a tool it left that shows no mark, as a
// platform binary on macOS, has no marked ancestor to be found by. It
// returns once the bridge from the agent's stdout has let go of it, and
// has it let go at once: the SDK may have stopped reading, and a process
// the agent left may hold stdout open.
func (p *proc) Close(ctx context.Context) error {
	seen := map[int]int64{}
	marked.Scan(p.mark, seen)
	p.closing.Do(func() { p.stdin.Close() })
	select {
	case <-p.reaped:
	case <-time.After(closeGrace):
	case <-ctx.Done():
	}
	p.halted.Store(true)
	if err := p.stop(ctx, seen); err != nil {
		return err
	}
	p.release()
	select {
	case <-p.read:
		return nil
	default:
	}
	select {
	case <-p.read:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// expectPrompt has prompt close once the next prompt is written to the
// agent.
func (p *proc) expectPrompt(prompt chan struct{}) {
	p.mu.Lock()
	p.prompt = prompt
	p.mu.Unlock()
}

func (p *proc) wrote(line []byte) {
	if !bytes.Contains(line, []byte(`"method":"session/prompt"`)) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prompt != nil {
		close(p.prompt)
		p.prompt = nil
	}
}

// retire keeps the agent from its thread's next run: it spoke when no
// turn of its was open.
func (p *proc) retire(what string) {
	slog.Warn("acp: the agent spoke between turns, so it takes no other run", "agent", p.profile.Name, "update", what)
	p.mu.Lock()
	p.spoke = true
	p.mu.Unlock()
}

func (p *proc) retired() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.spoke
}

// attach has run take the agent's updates and requests, or none while run
// is nil.
func (p *proc) attach(run *Agent) {
	p.mu.Lock()
	p.run, p.parked = run, false
	p.mu.Unlock()
}

// park has the agent wait for its thread's next run.
func (p *proc) park() {
	p.mu.Lock()
	p.run, p.parked = nil, true
	p.mu.Unlock()
}

// current is the run the agent serves. An agent that speaks while parked
// would have its words land in the thread's next run: it is stopped
// instead, unless what it said is only news of the session. What it says
// with no run before then, as a session's history, goes nowhere.
func (p *proc) current(speaking bool, what string) *Agent {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.run != nil || !speaking:
	case p.parked:
		slog.Warn("acp: a parked agent spoke, so it is stopped", "agent", p.profile.Name, "update", what)
		p.spoke = true
		p.halt()
	default:
		slog.Info("acp: the agent spoke with no run to hear it", "agent", p.profile.Name, "update", what)
	}
	return p.run
}

func (p *proc) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	p.mu.Lock()
	replay := p.loading != "" && n.SessionId == p.loading
	if replay && (n.Update.UserMessageChunk != nil || n.Update.AgentMessageChunk != nil) {
		p.replayed++
	}
	p.mu.Unlock()
	if replay {
		return nil
	}
	if !p.ours(n.SessionId) {
		slog.Info("acp: an update for another session, which the agent's run drops", "agent", p.profile.Name, "update", kindOf(n.Update))
		return nil
	}
	u := n.Update
	quiet := u.AvailableCommandsUpdate != nil || u.CurrentModeUpdate != nil || u.ConfigOptionUpdate != nil ||
		u.SessionInfoUpdate != nil || u.UsageUpdate != nil || u.UserMessageChunk != nil
	if u.ConfigOptionUpdate != nil {
		p.setOptions(u.ConfigOptionUpdate.ConfigOptions)
	}
	if u.CurrentModeUpdate != nil {
		p.mu.Lock()
		p.mode = string(u.CurrentModeUpdate.CurrentModeId)
		p.mu.Unlock()
	}
	run := p.current(!quiet, kindOf(u))
	switch {
	case run == nil:
	case u.UsageUpdate != nil:
		run.usage(*u.UsageUpdate)
	case !quiet:
		run.update(u)
	}
	return nil
}

func (p *proc) RequestPermission(ctx context.Context, r acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	if !p.ours(r.SessionId) {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	run := p.current(true, "permission")
	if run == nil {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return acpsdk.RequestPermissionResponse{Outcome: run.permission(ctx, r)}, nil
}

// Glitched lets the agent use its own file and terminal tools, so it
// offers none of its own: the agent never calls these.

func (*proc) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, acpsdk.NewMethodNotFound("fs/read_text_file")
}

func (*proc) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, acpsdk.NewMethodNotFound("fs/write_text_file")
}

func (*proc) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/create")
}

func (*proc) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, acpsdk.NewMethodNotFound("terminal/output")
}

func (*proc) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/release")
}

func (*proc) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, acpsdk.NewMethodNotFound("terminal/wait_for_exit")
}

func (*proc) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, acpsdk.NewMethodNotFound("terminal/kill")
}

// CreateElicitation asks the run's user the form's questions. Glitched
// advertises forms only, each for a session's run.
func (p *proc) CreateElicitation(ctx context.Context, r acpsdk.CreateElicitationRequest) (acpsdk.CreateElicitationResponse, error) {
	cancelled := acpsdk.CreateElicitationResponse{Cancel: &acpsdk.CreateElicitationCancel{Action: "cancel"}}
	if r.Form == nil || r.Form.Session == nil || !p.ours(r.Form.Session.SessionId) {
		return cancelled, nil
	}
	run := p.current(true, "question")
	if run == nil {
		return cancelled, nil
	}
	return run.question(ctx, *r.Form), nil
}

func (*proc) CompleteElicitation(context.Context, acpsdk.CompleteElicitationNotification) error {
	return nil
}
