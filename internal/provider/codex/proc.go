package codex

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

	"glitched/internal/api"
	"glitched/internal/mcp"
	"glitched/internal/provider"
	"glitched/internal/provider/park"
	"glitched/internal/provider/sweep"
)

// procEnv names, in the environment of an app-server, its mark: the
// thread it serves and the launch. Every process Codex starts inherits
// it, so the engine can find them all after the engine that started them
// is gone.
const procEnv = "GLITCHED_CODEX_PROCESS"

var marked = sweep.Mark{Env: procEnv}

// wireEnv names a file every line to and from app-server is appended to,
// for a capture of the wire. Unset, nothing is written.
const wireEnv = "GLITCHED_CODEX_WIRE"

var (
	// closeGrace is how long Close lets app-server end on its own once
	// its stdin closes, before it stops it.
	closeGrace = 500 * time.Millisecond
	// interruptGrace is how long Codex has to end a turn it was asked to
	// stop, before it is stopped.
	interruptGrace = 10 * time.Second
)

const killGrace = sweep.KillGrace

// drainGrace is how long, once app-server has exited, its output may
// stay quiet before the adapter stops reading it: a process it left
// behind can hold the pipe open for good.
const drainGrace = 250 * time.Millisecond

// signIn is what a turn says when Codex needs its user to sign in.
const signIn = "Codex is signed out. Run codex login in Terminal to sign in."

// proc is one app-server and the Codex thread it runs for a Glitched
// thread, an Agent at a time, parked between them.
type proc struct {
	cmd     *exec.Cmd
	mark    string
	conn    *conn
	stdin   *os.File
	stderr  *tail
	heard   chan struct{} // closed once stderr is read to its end
	reaped  chan struct{} // closed once the process is reaped
	exitErr error         // set before reaped closes
	left    map[int]int64 // what ran in its process group as it was reaped; set before reaped closes
	halted  atomic.Bool   // it was stopped, and takes no more runs
	closing sync.Once

	version string
	// reviewer is who reviews the thread's approvals, as Codex said when
	// it started or resumed the thread: every turn asks for the same.
	reviewer string

	mu     sync.Mutex
	usage  *tokenUsage      // what Codex last said the thread spent
	peaks  map[string]int64 // the most each turn's readings said it had spent
	thread string           // Codex's id of the thread it runs
	over   map[string]bool  // Codex's ids of the turns its runs ended
	run    *Agent           // the run it serves, if any
	parked bool             // it waits for its thread's next run
	spoke  bool             // it spoke while parked
}

var _ park.Process = (*proc)(nil)

// launch starts app-server in dir under mark and initializes it. ctx
// bounds only the start; the process lives until Close.
func launch(ctx context.Context, prog provider.Program, dir, mark string) (*proc, error) {
	if err := guard(prog.Path); err != nil {
		return nil, err
	}
	cmd := exec.Command(prog.Path, append([]string{"app-server"}, prog.Args...)...)
	cmd.Dir = dir
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "BROWSER" || k == procEnv || mcp.Routing(k)
	})
	// A stale login must never open a browser tab in the middle of a turn.
	cmd.Env = prog.Environ(append(cmd.Env, "BROWSER=true", procEnv+"="+mark))
	cmd.SysProcAttr = sweep.SysProcAttr()
	p := &proc{cmd: cmd, mark: mark, stderr: &tail{}, heard: make(chan struct{}), reaped: make(chan struct{})}
	// The adapter's own pipes, unlike exec's, leave Wait nothing to wait
	// for once app-server exits, whoever else still holds them.
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
		return nil, fmt.Errorf("start Codex: %w", err)
	}
	p.stdin = inW
	go func() {
		p.exitErr = cmd.Wait()
		p.left = sweep.Group(cmd.Process.Pid)
		close(p.reaped)
	}()
	wire := openWire()
	p.conn = newConn(tap{inW, wire, ">> "}, p.handle)
	go p.watchStderr(errR, wire)
	go p.readStdout(outR, wire)
	go func() {
		<-p.heard
		<-p.conn.Done()
		wire.Close()
	}()

	// A call ends with ctx, but app-server would not: it is stopped.
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	var r struct {
		UserAgent string `json:"userAgent"`
	}
	err = p.conn.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "glitched", "title": "Glitched", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &r)
	if err == nil {
		err = p.conn.notify("initialized", nil)
	}
	if err != nil {
		err = p.failure("start", err)
		p.discard()
		return nil, err
	}
	p.version = versionOf(r.UserAgent)
	return p, nil
}

// versionOf is Codex's version in app-server's user agent, as
// "glitched/0.171.0 (Mac OS 27.2.1; arm64) …".
func versionOf(agent string) string {
	_, v, _ := strings.Cut(agent, "/")
	v, _, _ = strings.Cut(v, " ")
	return v
}

// signedIn is whether Codex has an account to run turns with. A
// signed-out app-server still lists models, from a catalog of its own.
func (p *proc) signedIn(ctx context.Context) error {
	var r struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenaiAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := p.conn.call(ctx, "account/read", map[string]any{"refreshToken": false}, &r); err != nil {
		return p.failure("read its account", err)
	}
	if r.RequiresOpenaiAuth && (len(r.Account) == 0 || string(r.Account) == "null") {
		return provider.Needs{Kind: provider.ErrSignedOut, Help: signIn}
	}
	return nil
}

// models are what Codex offers the account, its default first, each with
// the efforts it runs at, as Codex orders them, and the one it runs at
// where a thread names none. The first default is the Default.
func (p *proc) models(ctx context.Context) ([]provider.Model, error) {
	var out, rest []provider.Model
	cursor := ""
	for range 50 {
		params := map[string]any{"includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var r struct {
			Data []struct {
				ID, Model, DisplayName, Description string
				Hidden, IsDefault                   bool
				DefaultReasoningEffort              string `json:"defaultReasoningEffort"`
				SupportedReasoningEfforts           []struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := p.conn.call(ctx, "model/list", params, &r); err != nil {
			return nil, p.failure("list its models", err)
		}
		for _, m := range r.Data {
			id := cmp.Or(m.ID, m.Model)
			if m.Hidden || id == "" || slices.ContainsFunc(out, func(o provider.Model) bool { return o.ID == id }) ||
				slices.ContainsFunc(rest, func(o provider.Model) bool { return o.ID == id }) {
				continue
			}
			pm := provider.Model{ID: id, Name: cmp.Or(m.DisplayName, id), Description: m.Description, DefaultEffort: m.DefaultReasoningEffort}
			for _, e := range m.SupportedReasoningEfforts {
				if e.ReasoningEffort != "" {
					pm.Efforts = append(pm.Efforts, provider.Effort{ID: e.ReasoningEffort})
				}
			}
			if m.IsDefault {
				pm.Default = len(out) == 0
				out = append(out, pm)
			} else {
				rest = append(rest, pm)
			}
		}
		if r.NextCursor == nil || *r.NextCursor == "" || *r.NextCursor == cursor {
			break
		}
		cursor = *r.NextCursor
	}
	return append(out, rest...), nil
}

// startThread opens the Codex thread the Glitched thread's runs go to,
// in dir, with the run's MCP servers and their instructions: the one its
// earlier runs had, when Codex still has it, or else a new one, which
// lost reports. A trusted server's tools run without asking, as its
// engine decides each call.
func (p *proc) startThread(ctx context.Context, spec provider.SessionSpec) (lost bool, err error) {
	stop := context.AfterFunc(ctx, p.halt)
	defer stop()
	servers := map[string]any{}
	var notes []string
	for _, s := range spec.MCP {
		entry := map[string]any{"command": s.Command, "args": append([]string{}, s.Args...)}
		if len(s.Env) > 0 {
			entry["env"] = maps.Clone(s.Env)
		}
		if s.Trusted {
			entry["default_tools_approval_mode"] = "approve"
		}
		if s.ToolTimeout > 0 {
			entry["tool_timeout_sec"] = s.ToolTimeout.Seconds()
		}
		servers[s.Name] = entry
		if s.Instructions != "" {
			notes = append(notes, s.Instructions)
		}
	}
	params := map[string]any{
		"cwd":               spec.Workspace,
		"approvalPolicy":    levels[spec.Access].approval,
		"approvalsReviewer": levels[spec.Access].reviewer,
		"sandbox":           levels[spec.Access].sandbox,
		"config":            map[string]any{"mcp_servers": servers},
	}
	if spec.Model.Model != "" {
		params["model"] = spec.Model.Model
	}
	if len(notes) > 0 {
		params["developerInstructions"] = strings.Join(notes, "\n\n")
	}
	var r struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ApprovalsReviewer string `json:"approvalsReviewer"`
	}
	// The thread's reviewer is the one Codex says it took, the user for a
	// Codex too old to say.
	defer func() { p.reviewer = cmp.Or(r.ApprovalsReviewer, "user") }()
	if spec.Session != "" {
		resume := maps.Clone(params)
		resume["threadId"], resume["excludeTurns"] = spec.Session, true
		err := p.conn.call(ctx, "thread/resume", resume, &r)
		var re *rpcError
		switch {
		case err == nil && r.Thread.ID != spec.Session:
			return false, fmt.Errorf("Codex resumed thread %q for %q.", r.Thread.ID, spec.Session)
		case err == nil:
			return false, p.bind(r.Thread.ID)
		case !errors.As(err, &re):
			return false, p.failure("resume the thread", err)
		case strings.Contains(re.Message, "active writer"):
			return false, errors.New(inUse)
		case !strings.Contains(re.Message, "no rollout found"):
			return false, fmt.Errorf("Codex didn't resume the thread's conversation: %s", re.Message)
		}
		slog.Info("codex: Codex no longer has the thread", "thread", spec.Session, "err", re.Message)
		lost = true
	}
	if err := p.conn.call(ctx, "thread/start", params, &r); err != nil {
		return false, p.failure("start a thread", err)
	}
	return lost, p.bind(r.Thread.ID)
}

// inUse is why a run can't go on with a conversation another Codex
// client has open (T3's wording).
const inUse = "This conversation is open in another Codex client, such as the Codex app, the Codex CLI, or an IDE extension. Codex lets one client write to a conversation at a time."

// bind makes id the thread p runs.
func (p *proc) bind(id string) error {
	if id == "" {
		return errors.New("Codex opened a thread with no id.")
	}
	p.mu.Lock()
	p.thread = id
	p.mu.Unlock()
	return nil
}

// level is how one of Glitched's access levels reaches Codex: the
// approval policy, sandbox and reviewer of approvals a thread starts
// with, and the sandbox each turn sends. Approve for me is Codex's own
// auto-review, where a reviewer agent of Codex's decides what the sandbox
// asks, and escalates to the user what it won't. Full access is Codex's
// own bypass, as --dangerously-bypass-approvals-and-sandbox.
type level struct {
	approval, sandbox, reviewer string
	policy                      map[string]any
}

// autoReview is the reviewer of Codex's auto-review. A thread whose Codex
// doesn't offer it says its reviewer is the user instead.
const autoReview = "auto_review"

var levels = map[api.Access]level{
	api.AccessAsk:       {"on-request", "read-only", "user", map[string]any{"type": "readOnly", "networkAccess": false}},
	api.AccessAutoEdits: {"on-request", "workspace-write", autoReview, map[string]any{"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": false, "excludeTmpdirEnvVar": false, "excludeSlashTmp": false}},
	api.AccessFull:      {"never", "danger-full-access", "user", map[string]any{"type": "dangerFullAccess"}},
}

func (p *proc) threadID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.thread
}

// handle hands a notification or request of the thread's to the run
// it's for. One with no run, while parked, stops app-server, unless it is
// only news of the thread's state. A request with no run to answer it is
// refused, so Codex never waits on it.
func (p *proc) handle(m message) {
	var scope struct {
		ThreadID string `json:"threadId"`
	}
	json.Unmarshal(m.Params, &scope)
	request := len(m.ID) > 0
	p.mu.Lock()
	run := p.run
	ours := scope.ThreadID == "" || scope.ThreadID == p.thread
	p.mu.Unlock()
	switch {
	case !ours:
		if request {
			p.conn.refuse(m.ID, -32601, "Glitched runs no such thread here")
		}
	case run != nil && request:
		run.request(m)
	case run != nil:
		run.notify(m)
	case m.Method == "thread/tokenUsage/updated":
		// What Codex spent between runs is no run's, and a reading it
		// sends again in the next run is no news.
		var u struct {
			TurnID     string     `json:"turnId"`
			TokenUsage tokenUsage `json:"tokenUsage"`
		}
		json.Unmarshal(m.Params, &u)
		p.reading(u.TurnID, u.TokenUsage, false)
	case request:
		p.quiet(m.Method)
		p.conn.refuse(m.ID, -32601, "no turn is running")
	case speaks(m.Method):
		p.quiet(m.Method)
	}
}

// speaks is whether a notification is about a turn's work, which no
// parked app-server may send, rather than news of the thread's state.
func speaks(method string) bool {
	return strings.HasPrefix(method, "item/") || strings.HasPrefix(method, "turn/") || method == "error"
}

// quiet stops a parked app-server that spoke: what it said would land in
// the thread's next run, or it is still at work on the last one.
func (p *proc) quiet(what string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.parked {
		slog.Info("codex: app-server spoke with no run to hear it", "method", what)
		return
	}
	slog.Warn("codex: a parked app-server spoke, so it is stopped", "method", what)
	p.spoke = true
	p.halt()
}

// ended records that turn is over: what Codex says of it after reaches
// no later run.
func (p *proc) ended(turn string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.over == nil {
		p.over = map[string]bool{}
	}
	p.over[turn] = true
}

// isOver is whether turn is one a run of p's ended.
func (p *proc) isOver(turn string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.over[turn]
}

// retire keeps app-server from its thread's next run: it spoke of a turn
// that is over.
func (p *proc) retire(what string) {
	slog.Warn("codex: app-server spoke of a turn that is over, so it takes no other run", "method", what)
	p.mu.Lock()
	p.spoke = true
	p.mu.Unlock()
}

// failure is why doing went wrong: what Codex said, or what it printed as
// it exited.
func (p *proc) failure(doing string, err error) error {
	select {
	case <-p.conn.Done():
		select {
		case <-p.reaped:
		case <-time.After(killGrace):
		}
	default:
	}
	select {
	case <-p.reaped:
		<-p.heard
		return p.exited()
	default:
	}
	var re *rpcError
	if errors.As(err, &re) {
		return fmt.Errorf("Codex couldn't %s: %s", doing, re.Message)
	}
	return fmt.Errorf("Codex couldn't %s: %w", doing, err)
}

// exited is why app-server is gone, from what it printed last.
func (p *proc) exited() error {
	said := p.stderr.last()
	if said == "" && p.exitErr != nil {
		said = p.exitErr.Error()
	}
	return fmt.Errorf("Codex exited: %s", cmp.Or(said, "no reason given"))
}

func (p *proc) readStdout(stdout *os.File, wire io.Writer) {
	go func() {
		<-p.reaped
		stdout.SetReadDeadline(time.Now().Add(drainGrace))
	}()
	defer stdout.Close()
	lines := bufio.NewReader(stdout)
	for {
		line, err := lines.ReadBytes('\n')
		if len(line) > 0 {
			wire.Write(append([]byte("<< "), line...))
			if t := bytes.TrimSpace(line); len(t) > 0 && t[0] == '{' {
				p.conn.line(t)
			}
		}
		if err != nil {
			p.conn.end()
			return
		}
	}
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
		}
		if err != nil {
			return
		}
	}
}

// halt stops app-server and what it started, and keeps it from taking
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

// stop ends app-server and every process under its mark, and returns
// once they are gone.
func (p *proc) stop(ctx context.Context, seen map[int]int64) error {
	// A tool that outlived a crashed app-server, with no mark to show, is
	// found by the group app-server led.
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
	// Where the engine can't find app-server by its mark, it has only the
	// process it started.
	p.cmd.Process.Kill()
	select {
	case <-p.reaped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// discard closes an app-server that takes no run, as one that failed to
// start, in the background of the caller's own deadline.
func (p *proc) discard() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*killGrace)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		slog.Warn("codex: stopping app-server", "err", err)
	}
}

// Wake is whether app-server can take its thread's next run: it kept
// quiet while parked and still runs.
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

// Close ends stdin, which ends an idle app-server, and stops one still
// there after closeGrace, with whatever it left running. What it started
// is read first: once it exits, a tool it left that shows no mark, as a
// platform binary on macOS, has no marked ancestor to be found by.
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
	return p.stop(ctx, seen)
}

// attach has run take the thread's notifications and requests, or none
// while run is nil.
func (p *proc) attach(run *Agent) {
	p.mu.Lock()
	p.run, p.parked = run, false
	p.mu.Unlock()
}

// park has app-server wait for its thread's next run.
func (p *proc) park() {
	p.mu.Lock()
	p.run, p.parked = nil, true
	p.mu.Unlock()
}

// reading keeps u, turn's, as the thread's latest reading of what it
// spent, and returns the one before, nil if none. A reading of a turn not
// live that says no more than that turn's did before is one Codex sent
// again: it is kept for nothing.
func (p *proc) reading(turn string, u tokenUsage, live bool) *tokenUsage {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := p.usage
	if peak, seen := p.peaks[turn]; !live && seen && u.Total.Total <= peak {
		return prev
	}
	if p.peaks == nil {
		p.peaks = map[string]int64{}
	}
	p.peaks[turn] = max(p.peaks[turn], u.Total.Total)
	p.usage = &u
	return prev
}
